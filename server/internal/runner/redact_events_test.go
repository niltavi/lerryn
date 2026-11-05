package runner

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Event texts are cut to their limits only after redaction, and texts the
// SDKs already cut are redacted as cut, so a value straddling either cut
// leaves nothing of itself behind.
func TestRedactEventTextsAtTheirLimits(t *testing.T) {
	// The value starts a few bytes before the limit, so cutting first
	// would keep its beginning.
	straddle := func(limit int) string { return strings.Repeat("x", limit-8) + secretValue + strings.Repeat("y", 50) }
	sdkCut := `args {"key":"` + secretValue[:9] + sdkCutMark
	tests := []struct {
		name  string
		event map[string]any
	}{
		{"message", map[string]any{"kind": "stage.done", "stage": "a", "message": straddle(maxEventMessageBytes)}},
		{"summary", map[string]any{"kind": "result", "status": "ok", "summary": straddle(maxEventSummaryBytes)}},
		{"method", map[string]any{"kind": "tool.call", "tool": "crm", "method": straddle(runproto.MaxToolMethodBytes), "ok": true}},
		{"args summary", map[string]any{"kind": "tool.call", "tool": "crm", "method": "m", "ok": true, "args_summary": straddle(runproto.MaxToolArgsRunes)}},
		{"error", map[string]any{"kind": "tool.call", "tool": "crm", "method": "m", "ok": false, "error": straddle(runproto.MaxToolErrorBytes)}},
		{"message cut by the SDK", map[string]any{"kind": "stage.done", "stage": "a", "message": sdkCut}},
		{"summary cut by the SDK", map[string]any{"kind": "result", "status": "ok", "summary": sdkCut}},
		{"args summary cut by the SDK", map[string]any{"kind": "tool.call", "tool": "crm", "method": "m", "ok": true, "args_summary": sdkCut}},
		{"error cut by the SDK", map[string]any{"kind": "tool.call", "tool": "crm", "method": "m", "ok": false, "error": sdkCut}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, logs, _, _ := newTestEventReader(t)
			logs.useSecrets(newRedactor([]string{secretValue}))
			line, err := json.Marshal(tc.event)
			if err != nil {
				t.Fatal(err)
			}
			r.handle(line)
			if len(r.pending) != 1 {
				t.Fatalf("events = %d, want 1", len(r.pending))
			}
			ev := r.pending[0]
			b, _ := json.Marshal(ev)
			for n := 4; n <= len(secretValue); n++ {
				if strings.Contains(string(b), secretValue[:n]) {
					t.Fatalf("event leaks %q: %s", secretValue[:n], b)
				}
			}
			if !strings.Contains(string(b), "[RED") {
				t.Fatalf("event has no redaction mark: %s", b)
			}
			if limited := limitEvent(ev); !eventEqual(limited, ev) {
				t.Fatalf("event exceeds its limits: %s", b)
			}
		})
	}
}

// A text that ends in "…" with no value before it is left alone.
func TestRedactEventKeepsPlainEllipsis(t *testing.T) {
	p, _ := testPipeline(defaultLogLimits())
	p.useSecrets(newRedactor([]string{secretValue}))
	ev := p.redactEvent(runproto.Event{Kind: runproto.EventStageDone, Message: "loading" + sdkCutMark})
	if ev.Message != "loading"+sdkCutMark {
		t.Fatalf("message = %q", ev.Message)
	}
}
