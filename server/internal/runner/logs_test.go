package runner

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func testPipeline(limits logLimits) (*logPipeline, *stageTracker) {
	stages := &stageTracker{}
	return newLogPipeline(limits, stages, nil, time.Now), stages
}

// drain returns everything pending and acknowledges it.
func drain(t *testing.T, p *logPipeline) ([]runproto.LogLine, runproto.Dropped) {
	t.Helper()
	var all []runproto.LogLine
	var dropped runproto.Dropped
	err := p.send(context.Background(), func(_ context.Context, lines []runproto.LogLine, d runproto.Dropped) error {
		all = append(all, lines...)
		dropped = d
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return all, dropped
}

func texts(lines []runproto.LogLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Text
	}
	return out
}

func TestLineSplitting(t *testing.T) {
	tests := []struct {
		name   string
		writes []string
		want   []string
	}{
		{"one write, two lines", []string{"a\nb\n"}, []string{"a", "b"}},
		{"line split across writes", []string{"hel", "lo", "\nworld\n"}, []string{"hello", "world"}},
		{"crlf", []string{"a\r\nb\r", "\n"}, []string{"a", "b"}},
		{"lone cr kept", []string{"50%\r100%\n"}, []string{"50%\r100%"}},
		{"empty lines kept", []string{"\n\nx\n"}, []string{"", "", "x"}},
		{"final line without newline", []string{"a\nb"}, []string{"a", "b"}},
		{"invalid utf-8 and nul", []string{"a\xffb\x00c\n"}, []string{"a�b�c"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := testPipeline(defaultLogLimits())
			out, flush := p.output()
			for _, w := range tc.writes {
				if n, err := out.Stdout.Write([]byte(w)); n != len(w) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			flush()
			lines, _ := drain(t, p)
			if got := texts(lines); !slices.Equal(got, tc.want) {
				t.Fatalf("lines = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLineTruncation(t *testing.T) {
	limits := defaultLogLimits()
	limits.lineBytes = 64
	p, _ := testPipeline(limits)
	out, flush := p.output()

	exact := strings.Repeat("e", 64)
	long := strings.Repeat("x", 100)
	// A multi-byte rune straddles the cut.
	runes := strings.Repeat("é", 40)
	for _, chunk := range []string{exact + "\r\n", long[:30], long[30:] + "\n", runes + "\n", "next\n", strings.Repeat("y", 200)} {
		_, _ = out.Stdout.Write([]byte(chunk))
	}
	flush()
	lines, _ := drain(t, p)
	got := texts(lines)
	if len(got) != 5 {
		t.Fatalf("lines = %q", got)
	}
	if got[0] != exact {
		t.Errorf("a line of exactly the limit was changed: %q", got[0])
	}
	for _, i := range []int{1, 2, 4} {
		if len(got[i]) > 64 || !strings.HasSuffix(got[i], truncatedMarker) || !utf8.ValidString(got[i]) {
			t.Errorf("line %d = %q (%d bytes), want cut at 64 bytes with the marker", i, got[i], len(got[i]))
		}
	}
	if got[3] != "next" {
		t.Errorf("the rest of a cut line leaked into the next: %q", got[3])
	}
	for i, l := range lines {
		if l.Seq != int64(i+1) {
			t.Errorf("line %d has seq %d", i, l.Seq)
		}
	}
}

func TestLogLimits(t *testing.T) {
	limits := logLimits{lineBytes: 100, runLines: 10, runBytes: 1000, reserveLines: 3, reserveBytes: 200, batchBytes: 1 << 20}
	p, _ := testPipeline(limits)
	out, flush := p.output()
	for range 20 {
		_, _ = out.Stdout.Write([]byte("0123456789\n"))
	}
	flush()
	p.system("closing line")
	lines, dropped := drain(t, p)

	// 7 task lines fit below the reserve; the notice and the closing line
	// use the reserve.
	if len(lines) != 9 {
		t.Fatalf("got %d lines: %q", len(lines), texts(lines))
	}
	notice := lines[7]
	if notice.Stream != runproto.StreamSystem || !strings.Contains(notice.Text, "log limit reached (10 lines or 1000 bytes per run)") {
		t.Fatalf("notice = %+v", notice)
	}
	if lines[8].Text != "closing line" {
		t.Fatalf("system line after the limit = %+v", lines[8])
	}
	if dropped != (runproto.Dropped{Lines: 13, Bytes: 130}) {
		t.Fatalf("dropped = %+v", dropped)
	}

	// Bytes limit, with the running total sent even when no line is left.
	limits = logLimits{lineBytes: 100, runLines: 1000, runBytes: 200, reserveLines: 1, reserveBytes: 150, batchBytes: 1 << 20}
	p, _ = testPipeline(limits)
	out, flush = p.output()
	_, _ = out.Stdout.Write([]byte(strings.Repeat("a", 30) + "\n" + strings.Repeat("b", 30) + "\n"))
	lines, dropped = drain(t, p)
	if len(lines) != 2 || lines[0].Text != strings.Repeat("a", 30) || lines[1].Stream != runproto.StreamSystem || dropped.Lines != 1 {
		t.Fatalf("lines = %q, dropped = %+v", texts(lines), dropped)
	}
	_, _ = out.Stderr.Write([]byte("more\n"))
	flush()
	lines, dropped = drain(t, p)
	if len(lines) != 0 || dropped != (runproto.Dropped{Lines: 2, Bytes: 34}) {
		t.Fatalf("after the limit: lines = %q, dropped = %+v", texts(lines), dropped)
	}
}

func TestLogBatchingAndRetry(t *testing.T) {
	limits := defaultLogLimits()
	limits.batchBytes = 25
	p, stages := testPipeline(limits)
	out, _ := p.output()

	_, _ = out.Stdout.Write([]byte("0123456789\n"))
	stages.apply(runproto.Event{Kind: runproto.EventStageStart, Stage: "fetch"})
	_, _ = out.Stderr.Write([]byte("abcdefghij\n"))
	p.system("system")
	_, _ = out.Stdout.Write([]byte("klmnopqrst\n"))
	select {
	case <-p.kick:
	default:
		t.Fatal("no early upload requested after batchBytes of text")
	}

	var calls [][]int64
	fail := true
	send := func(_ context.Context, lines []runproto.LogLine, _ runproto.Dropped) error {
		var seqs []int64
		for _, l := range lines {
			seqs = append(seqs, l.Seq)
		}
		calls = append(calls, seqs)
		if fail && len(calls) == 2 {
			return errors.New("unavailable")
		}
		return nil
	}
	if err := p.send(context.Background(), send); err == nil {
		t.Fatal("send succeeded despite an upload error")
	}
	fail = false
	if err := p.send(context.Background(), send); err != nil {
		t.Fatal(err)
	}
	// Batches of at most 25 bytes; the failed batch is resent unchanged.
	want := [][]int64{{1, 2}, {3, 4}, {3, 4}}
	if !slices.EqualFunc(calls, want, slices.Equal) {
		t.Fatalf("batches = %v, want %v", calls, want)
	}
	if !p.idle() {
		t.Fatal("lines left after a successful send")
	}

	// Stage labels follow the tracker; streams share the sequence.
	p2, stages2 := testPipeline(defaultLogLimits())
	out2, _ := p2.output()
	_, _ = out2.Stdout.Write([]byte("before\n"))
	stages2.apply(runproto.Event{Kind: runproto.EventStageStart, Stage: "fetch"})
	_, _ = out2.Stderr.Write([]byte("during\n"))
	stages2.apply(runproto.Event{Kind: runproto.EventStageDone, Stage: "fetch"})
	_, _ = out2.Stdout.Write([]byte("after\n"))
	lines, _ := drain(t, p2)
	var labels []string
	for _, l := range lines {
		labels = append(labels, l.Stream+":"+l.Stage)
	}
	if want := []string{"stdout:", "stderr:fetch", "stdout:"}; !slices.Equal(labels, want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
}

func TestRedactHook(t *testing.T) {
	stages := &stageTracker{}
	p := newLogPipeline(defaultLogLimits(), stages, func(s string) string {
		return strings.ReplaceAll(s, "s3cret", "******")
	}, time.Now)
	out, _ := p.output()
	_, _ = out.Stdout.Write([]byte("token=s3cret\n"))
	lines, _ := drain(t, p)
	if len(lines) != 1 || lines[0].Text != "token=******" {
		t.Fatalf("lines = %q", texts(lines))
	}
}
