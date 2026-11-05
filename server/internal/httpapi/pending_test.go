package httpapi

import (
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Requests that break the new contract are rejected before any stub runs,
// so implementers can rely on the validated shapes.
func TestPendingOperationsValidateRequests(t *testing.T) {
	c := newTaskClient(t)
	admin, _ := c.agent("alice")
	id := uuid.NewString()
	tests := []struct {
		name, method, path string
		body               any
		headers            []string
	}{
		{"unknown run status", http.MethodGet, "/api/v1/runs?status=done", nil, nil},
		{"trigger name", http.MethodPut, "/api/v1/tasks/" + id + "/triggers", map[string]any{"triggers": []any{
			map[string]any{"name": "Bad Name", "type": "cron"},
		}}, nil},
		{"credential field name", http.MethodPost, "/api/v1/credential-requests", map[string]any{
			"purpose": "调用模型", "fields": []any{map[string]any{"name": "lower"}},
		}, nil},
		{"If-Match format", http.MethodPut, "/api/v1/tasks/" + id + "/triggers", map[string]any{"triggers": []any{}},
			[]string{"If-Match", "W/\"abc\""}},
		{"Idempotency-Key length", http.MethodPost, "/api/v1/runs/" + id + "/rerun", nil,
			[]string{"Idempotency-Key", strings.Repeat("k", 129)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := append([]string{"Authorization", admin}, tt.headers...)
			c.want(c.call(tt.method, tt.path, tt.body, headers...), http.StatusBadRequest, ErrorCodeValidationFailed)
		})
	}
}
