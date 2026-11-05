package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

func TestParseEvent(t *testing.T) {
	now := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		line    string
		want    runproto.Event
		mark    string
		warning string
		err     string
	}{
		{
			name: "stage start",
			line: `{"v":1,"ts":"2026-11-02T07:59:59.5Z","kind":"stage.start","stage":"fetch"}`,
			want: runproto.Event{TS: now.Add(-500 * time.Millisecond), Kind: "stage.start", Stage: "fetch"},
		},
		{
			name: "stage done with message, no version, unknown field",
			line: `{"kind":"stage.done","stage":"fetch_2","message":"got 3","extra":true}`,
			want: runproto.Event{TS: now, Kind: "stage.done", Stage: "fetch_2", Message: "got 3"},
		},
		{
			name: "stage start with mark",
			line: `{"v":1,"kind":"stage.start","stage":"fetch","mark":"0a1b2c3d4e5f6789"}`,
			want: runproto.Event{TS: now, Kind: "stage.start", Stage: "fetch"},
			mark: "0a1b2c3d4e5f6789",
		},
		{
			name: "malformed mark ignored",
			line: `{"v":1,"kind":"stage.start","stage":"fetch","mark":"not hex"}`,
			want: runproto.Event{TS: now, Kind: "stage.start", Stage: "fetch"},
		},
		{
			name: "mark on a result ignored",
			line: `{"kind":"result","status":"ok","summary":"s","mark":"ab"}`,
			want: runproto.Event{TS: now, Kind: "result", Result: &runproto.Result{Status: "ok", Summary: "s"}},
		},
		{
			name:    "result data not UTF-8",
			line:    "{\"kind\":\"result\",\"status\":\"ok\",\"summary\":\"s\",\"data\":\"\xff\"}",
			want:    runproto.Event{TS: now, Kind: "result", Result: &runproto.Result{Status: "ok", Summary: "s"}},
			warning: "not valid UTF-8",
		},
		{
			name: "far-off timestamp replaced",
			line: `{"v":1,"ts":"1999-01-01T00:00:00Z","kind":"stage.fail","stage":"save","message":"HTTP 503"}`,
			want: runproto.Event{TS: now, Kind: "stage.fail", Stage: "save", Message: "HTTP 503"},
		},
		{
			name: "long message cut",
			line: `{"kind":"stage.done","stage":"a","message":"` + strings.Repeat("é", 600) + `"}`,
			want: runproto.Event{TS: now, Kind: "stage.done", Stage: "a", Message: strings.Repeat("é", 510) + "…"},
		},
		{
			name: "result with data",
			line: `{"v":1,"kind":"result","status":"partial","summary":"2 of 3","data":{ "saved" : [1, 2] }}`,
			want: runproto.Event{TS: now, Kind: "result", Result: &runproto.Result{Status: "partial", Summary: "2 of 3", Data: []byte(`{"saved":[1,2]}`)}},
		},
		{
			name: "result with null data",
			line: `{"kind":"result","status":"ok","summary":"fine","data":null}`,
			want: runproto.Event{TS: now, Kind: "result", Result: &runproto.Result{Status: "ok", Summary: "fine"}},
		},
		{
			name:    "result data too large",
			line:    `{"kind":"result","status":"ok","summary":"big","data":"` + strings.Repeat("x", 70_000) + `"}`,
			want:    runproto.Event{TS: now, Kind: "result", Result: &runproto.Result{Status: "ok", Summary: "big"}},
			warning: "data was dropped",
		},
		{
			name: "long summary cut",
			line: `{"kind":"result","status":"failed","summary":"` + strings.Repeat("s", 3000) + `"}`,
			want: runproto.Event{TS: now, Kind: "result", Result: &runproto.Result{Status: "failed", Summary: strings.Repeat("s", 2045) + "…"}},
		},
		{
			name: "tool call",
			line: `{"v":1,"kind":"tool.call","tool":"crm-mcp","method":"search_accounts","ok":true,"duration_ms":42,"args_summary":"{\"q\":\"acme\"}"}`,
			want: runproto.Event{TS: now, Kind: "tool.call", ToolCall: &runproto.ToolCall{
				Tool: "crm-mcp", Method: "search_accounts", OK: true, DurationMillis: 42, ArgsSummary: `{"q":"acme"}`}},
		},
		{
			name: "failed tool call, long summary cut, error kept",
			line: `{"kind":"tool.call","tool":"crm","method":"m","ok":false,"duration_ms":-5,"args_summary":"` + strings.Repeat("参", 300) + `","error":"HTTP 503"}`,
			want: runproto.Event{TS: now, Kind: "tool.call", ToolCall: &runproto.ToolCall{
				Tool: "crm", Method: "m", ArgsSummary: strings.Repeat("参", 199) + "…", Error: "HTTP 503"}},
		},
		{
			name: "error of a successful call dropped",
			line: `{"kind":"tool.call","tool":"crm","method":"m","ok":true,"error":"x"}`,
			want: runproto.Event{TS: now, Kind: "tool.call", ToolCall: &runproto.ToolCall{Tool: "crm", Method: "m", OK: true}},
		},
		{name: "tool call bad name", line: `{"kind":"tool.call","tool":"../crm","method":"m","ok":true}`, err: "tool name"},
		{name: "tool call without ok", line: `{"kind":"tool.call","tool":"crm","method":"m"}`, err: "needs method and ok"},
		{name: "tool call without method", line: `{"kind":"tool.call","tool":"crm","ok":true}`, err: "needs method and ok"},
		{name: "not json", line: `stage start fetch`, err: "invalid JSON"},
		{name: "two values", line: `{"kind":"result"} {}`, err: "more than one"},
		{name: "array", line: `[1]`, err: "invalid JSON"},
		{name: "future version", line: `{"v":2,"kind":"stage.start","stage":"a"}`, err: "version 2"},
		{name: "missing kind", line: `{"stage":"a"}`, err: "kind is missing"},
		{name: "unknown kind", line: `{"kind":"artifact"}`, err: `unknown kind "artifact"`},
		{name: "bad stage id", line: `{"kind":"stage.start","stage":"Fetch Data"}`, err: "stage id"},
		{name: "missing stage", line: `{"kind":"stage.done"}`, err: "stage id"},
		{name: "bad status", line: `{"kind":"result","status":"success","summary":"x"}`, err: "result status"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, mark, warnings, err := parseEvent([]byte(tc.line), now)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("parseEvent() error = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got = limitEvent(got)
			if !eventEqual(got, tc.want) {
				t.Fatalf("parseEvent() = %+v (result %+v)\nwant %+v (result %+v)", got, got.Result, tc.want, tc.want.Result)
			}
			if mark != tc.mark {
				t.Fatalf("mark = %q, want %q", mark, tc.mark)
			}
			if tc.warning == "" && len(warnings) > 0 || tc.warning != "" && !slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, tc.warning) }) {
				t.Fatalf("warnings = %q, want %q", warnings, tc.warning)
			}
		})
	}
}

func eventEqual(a, b runproto.Event) bool {
	if (a.Result == nil) != (b.Result == nil) {
		return false
	}
	if a.Result != nil && (a.Result.Status != b.Result.Status || a.Result.Summary != b.Result.Summary || string(a.Result.Data) != string(b.Result.Data)) {
		return false
	}
	if (a.ToolCall == nil) != (b.ToolCall == nil) || a.ToolCall != nil && *a.ToolCall != *b.ToolCall {
		return false
	}
	a.Result, b.Result = nil, nil
	return a.Seq == b.Seq && a.TS.Equal(b.TS) && a.Kind == b.Kind && a.Stage == b.Stage && a.Message == b.Message
}

func newTestEventReader(t *testing.T) (*eventReader, *logPipeline, *stageTracker, string) {
	t.Helper()
	dir := t.TempDir()
	stages := &stageTracker{}
	logs := newLogPipeline(defaultLogLimits(), stages, nil, time.Now)
	return newEventReader(dir, stages, logs, time.Now), logs, stages, filepath.Join(dir, eventsFileName)
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600) // #nosec G304 -- a test's own temp file
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestEventReader(t *testing.T) {
	r, logs, stages, path := newTestEventReader(t)
	defer r.close()

	r.poll(false) // no file yet
	appendFile(t, path, `{"kind":"stage.start","stage":"fetch"}`+"\n"+`{"kind":"stage.do`)
	r.poll(false)
	if len(r.pending) != 1 || stages.current() != "fetch" {
		t.Fatalf("after the first poll: pending %+v, stage %q", r.pending, stages.current())
	}
	appendFile(t, path, `ne","stage":"fetch"}`+"\nnot json\n\n"+`{"kind":"result","status":"ok","summary":"s"}`+"\n"+`{"kind":"stage.start","stage":"x"`)
	r.poll(false)
	if stages.current() != "" {
		t.Fatalf("stage after done = %q", stages.current())
	}
	r.poll(true)

	var kinds []string
	for i, e := range r.pending {
		if e.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d", i, e.Seq)
		}
		kinds = append(kinds, e.Kind)
	}
	if want := []string{"stage.start", "stage.done", "result"}; !slices.Equal(kinds, want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	warnings := texts(mustDrain(t, logs))
	if len(warnings) != 2 || !strings.Contains(warnings[0], "line 3 ignored: invalid JSON") ||
		!strings.Contains(warnings[1], "line 6 ignored: incomplete line") {
		t.Fatalf("warnings = %q", warnings)
	}

	var sent []runproto.Event
	if err := r.send(context.Background(), func(_ context.Context, ev []runproto.Event) error {
		sent = append(sent, ev...)
		return nil
	}); err != nil || len(sent) != 3 || len(r.pending) != 0 {
		t.Fatalf("send: %v, sent %d, pending %d", err, len(sent), len(r.pending))
	}
}

func mustDrain(t *testing.T, p *logPipeline) []runproto.LogLine {
	lines, _ := drain(t, p)
	return lines
}

func TestEventReaderSkipsLongLines(t *testing.T) {
	r, logs, _, path := newTestEventReader(t)
	defer r.close()
	appendFile(t, path, strings.Repeat("x", maxEventLineBytes-10))
	r.poll(false)
	appendFile(t, path, strings.Repeat("x", 20)+"\n"+`{"kind":"stage.start","stage":"ok"}`+"\n")
	r.poll(false)
	if len(r.pending) != 1 || r.pending[0].Stage != "ok" {
		t.Fatalf("pending = %+v", r.pending)
	}
	if w := texts(mustDrain(t, logs)); len(w) != 1 || !strings.Contains(w[0], "line 1 ignored: longer than 128 KiB") {
		t.Fatalf("warnings = %q", w)
	}
}

func TestStageTracker(t *testing.T) {
	var s stageTracker
	ev := func(kind, stage string) { s.apply(runproto.Event{Kind: kind, Stage: stage}) }
	check := func(current, errStage string) {
		t.Helper()
		if s.current() != current || s.errorStage() != errStage {
			t.Fatalf("current %q, error stage %q; want %q, %q", s.current(), s.errorStage(), current, errStage)
		}
	}
	check("", "")
	ev(runproto.EventStageStart, "outer")
	ev(runproto.EventStageStart, "inner")
	check("inner", "inner")
	ev(runproto.EventStageDone, "inner")
	check("outer", "outer")
	ev(runproto.EventStageFail, "outer")
	check("", "outer")
	ev(runproto.EventStageStart, "next")
	ev(runproto.EventStageDone, "next")
	check("", "")
	// Done for a stage that is not current leaves the current one.
	ev(runproto.EventStageStart, "a")
	ev(runproto.EventStageDone, "zzz")
	check("a", "a")
}
