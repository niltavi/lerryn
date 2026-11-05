package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestLogCallFailureLevel(t *testing.T) {
	gone, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name  string
		ctx   context.Context
		err   error
		level string
	}{
		{"failure", context.Background(), errors.New("boom"), "level=ERROR"},
		{"client disconnected", gone, fmt.Errorf("call getRun: %w", context.Canceled), "level=DEBUG"},
		{"cancelled with the client still there", context.Background(), context.Canceled, "level=ERROR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
			logCallFailure(tt.ctx, log, tt.err, "tool", "run_get")
			if out := buf.String(); !strings.Contains(out, tt.level) || !strings.Contains(out, "tool=run_get") {
				t.Errorf("log = %q, want %s", out, tt.level)
			}
		})
	}
}
