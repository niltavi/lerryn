package runner

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

const testToken = "TESTTOKEN234567ABCDEFGHIJK"

func markerLine(kind, stage, mark string) string {
	return fmt.Sprintf("\x1elerryn %s %s %s %s\n", testToken, mark, kind, stage)
}

func start(stage, mark string) string { return markerLine(runproto.EventStageStart, stage, mark) }
func done(stage, mark string) string  { return markerLine(runproto.EventStageDone, stage, mark) }

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// step is one thing that happens to a run's output: stdout or stderr
// written, an event read from the events file, time passing, or the
// container's output ending.
type step struct {
	stdout, stderr string
	event          string // "kind stage mark"
	wait           time.Duration
	end            bool
}

func ev(kind, stage, mark string) step { return step{event: kind + " " + stage + " " + mark} }

func TestStageMarkers(t *testing.T) {
	tests := []struct {
		name  string
		steps []step
		// want is "stream:text@stage" per log line.
		want []string
	}{
		{
			name: "event read before its marker arrives",
			steps: []step{
				{stdout: "a\n"},
				ev("stage.start", "fetch", "1"),
				{stdout: "b\n" + start("fetch", "1") + "c\n"},
				ev("stage.done", "fetch", "2"),
				{stdout: "d\n" + done("fetch", "2") + "e\n"},
			},
			want: []string{"stdout:a@", "stdout:b@", "stdout:c@fetch", "stdout:d@fetch", "stdout:e@"},
		},
		{
			name: "marker arrives before its event is read",
			steps: []step{
				{stdout: start("fetch", "1") + "a\n" + done("fetch", "2") + "b\n"},
				ev("stage.start", "fetch", "1"),
				{stdout: "c\n"},
				ev("stage.done", "fetch", "2"),
				{stdout: "d\n"},
			},
			want: []string{"stdout:a@fetch", "stdout:b@", "stdout:c@", "stdout:d@"},
		},
		{
			name: "text left on the line before a marker",
			steps: []step{
				{stdout: "progress 50%"},
				{stdout: start("fetch", "1") + "x\n"},
			},
			want: []string{"stdout:progress 50%@", "stdout:x@fetch"},
		},
		{
			name: "marker with another token is output",
			steps: []step{
				{stdout: "\x1elerryn OTHER 1 stage.start fetch\n" + "a\n"},
			},
			want: []string{"stdout:\x1elerryn OTHER 1 stage.start fetch@", "stdout:a@"},
		},
		{
			name: "malformed marker is output",
			steps: []step{
				{stdout: markerLine("stage.skip", "fetch", "1") + markerLine("stage.start", "Bad", "2") + markerLine("stage.start", "a", "XYZ")},
			},
			want: []string{
				"stdout:" + strings.TrimSuffix(markerLine("stage.skip", "fetch", "1"), "\n") + "@",
				"stdout:" + strings.TrimSuffix(markerLine("stage.start", "Bad", "2"), "\n") + "@",
				"stdout:" + strings.TrimSuffix(markerLine("stage.start", "a", "XYZ"), "\n") + "@",
			},
		},
		{
			name: "markers on stderr are output",
			steps: []step{
				{stderr: start("fetch", "1")},
				{stdout: "a\n"},
			},
			want: []string{"stderr:" + strings.TrimSuffix(start("fetch", "1"), "\n") + "@", "stdout:a@"},
		},
		{
			name: "event without a mark applies when read",
			steps: []step{
				{stdout: "a\n"},
				ev("stage.start", "fetch", ""),
				{stdout: "b\n"},
			},
			want: []string{"stdout:a@", "stdout:b@fetch"},
		},
		{
			name: "missing marker: quiet stdout ends the wait",
			steps: []step{
				ev("stage.start", "fetch", "1"),
				{stdout: "a\n"},
				{wait: 500 * time.Millisecond},
				{stdout: "b\n"},
				{wait: 1100 * time.Millisecond},
				{stderr: "c\n"},
				// The marker comes after all; it is only removed.
				{stdout: start("fetch", "1") + "d\n"},
			},
			want: []string{"stdout:a@", "stdout:b@", "stderr:c@fetch", "stdout:d@fetch"},
		},
		{
			name: "missing marker: a later marker applies it first",
			steps: []step{
				ev("stage.start", "outer", "1"),
				ev("stage.start", "inner", "2"),
				{stdout: "a\n" + start("inner", "2") + "b\n" + done("inner", "3") + "c\n"},
			},
			want: []string{"stdout:a@", "stdout:b@inner", "stdout:c@outer"},
		},
		{
			name: "missing marker: enough stdout ends the wait",
			steps: []step{
				ev("stage.start", "fetch", "1"),
				{stdout: strings.Repeat("x", markerWaitBytes) + "\n"},
				{stdout: "a\n"},
			},
			want: []string{"stdout:" + strings.Repeat("x", 16) + "@fetch", "stdout:a@fetch"},
		},
		{
			name: "end of output applies what is awaited",
			steps: []step{
				{stdout: "a\n"},
				ev("stage.start", "fetch", "1"),
				{end: true},
				ev("stage.done", "fetch", "2"),
			},
			want: []string{"stdout:a@", "system:end1@fetch", "system:end2@"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fakeClock{t: time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)}
			limits := defaultLogLimits()
			limits.lineBytes = 200
			p := newLogPipeline(limits, &stageTracker{}, nil, clock.now)
			p.useMarkers(testToken)
			out, flush := p.taskOutput()
			ended := false
			for _, s := range tc.steps {
				switch {
				case s.stdout != "":
					_, _ = out.Stdout.Write([]byte(s.stdout))
				case s.stderr != "":
					_, _ = out.Stderr.Write([]byte(s.stderr))
				case s.event != "":
					f := strings.Split(s.event, " ")
					p.fileEvent(runproto.Event{Kind: f[0], Stage: f[1]}, f[2])
					if ended {
						p.system("end2")
					}
				case s.wait > 0:
					clock.advance(s.wait)
					p.expireMarks()
				case s.end:
					flush()
					ended = true
					p.system("end1")
				}
			}
			var got []string
			for _, l := range mustDrain(t, p) {
				text := l.Text
				if strings.HasSuffix(text, truncatedMarker) {
					text = strings.Repeat("x", 16)
				}
				got = append(got, l.Stream+":"+text+"@"+l.Stage)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("lines:\n%q\nwant:\n%q", got, tc.want)
			}
		})
	}
}

// TestInstallOutputKeepsMarkers checks that only the run container's stdout
// is read for markers.
func TestInstallOutputKeepsMarkers(t *testing.T) {
	p := newLogPipeline(defaultLogLimits(), &stageTracker{}, nil, time.Now)
	p.useMarkers(testToken)
	out, flush := p.output()
	_, _ = out.Stdout.Write([]byte(start("fetch", "1") + "a\n"))
	flush()
	lines := mustDrain(t, p)
	if len(lines) != 2 || lines[1].Stage != "" || !strings.Contains(lines[0].Text, "lerryn") {
		t.Fatalf("lines = %+v", lines)
	}
}

// TestMarkerLabelsUnderLoad runs a large burst across a stage change with
// the events file read at the worst moment: before the burst is processed.
// Every line carries the right stage.
func TestMarkerLabelsUnderLoad(t *testing.T) {
	p := newLogPipeline(defaultLogLimits(), &stageTracker{}, nil, time.Now)
	p.useMarkers(testToken)
	out, flush := p.taskOutput()
	const n = 20000
	var b strings.Builder
	b.WriteString(start("one", "1"))
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	b.WriteString(done("one", "2") + start("two", "3"))
	for i := range n {
		fmt.Fprintf(&b, "line %d\n", n+i)
	}
	p.fileEvent(runproto.Event{Kind: runproto.EventStageStart, Stage: "one"}, "1")
	p.fileEvent(runproto.Event{Kind: runproto.EventStageDone, Stage: "one"}, "2")
	p.fileEvent(runproto.Event{Kind: runproto.EventStageStart, Stage: "two"}, "3")
	data := []byte(b.String())
	for len(data) > 0 {
		k := min(len(data), 4093)
		_, _ = out.Stdout.Write(data[:k])
		data = data[k:]
	}
	flush()
	lines := mustDrain(t, p)
	if len(lines) != 2*n {
		t.Fatalf("%d lines, want %d", len(lines), 2*n)
	}
	for i, l := range lines {
		want := "one"
		if i >= n {
			want = "two"
		}
		if l.Text != fmt.Sprintf("line %d", i) || l.Stage != want {
			t.Fatalf("line %d = %q in stage %q, want stage %q", i, l.Text, l.Stage, want)
		}
	}
}
