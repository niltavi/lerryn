//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// webhookTask reports the webhook body it finds at /sbx/in/body, and its
// media type, as its result.
const webhookTask = `import json, os
import sbx

with open("/sbx/in/body", "rb") as f:
    body = json.load(f)
try:
    open("/sbx/in/body", "a").close()
    writable = True
except OSError:
    writable = False
sbx.result("ok", "got lead %d" % body["lead"], {
    "lead": body["lead"],
    "content_type": os.environ.get("LERRYN_INPUT_CONTENT_TYPE"),
    "writable": writable,
})
`

// A webhook-triggered run reads the request body from /sbx/in/body, read
// only, with the media type in LERRYN_INPUT_CONTENT_TYPE (tech design 7.4,
// 8.3), through the embedded runner of all-in-one.
func TestWebhookBodyReachesTheTask(t *testing.T) {
	cfg := acceptanceConfig(t)
	cfg.Server.RequireTestBeforeActivate = false
	a := startAllInOne(t, cfg)
	project := a.signUp("hook")

	var applied struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	a.call(http.MethodPut, "/api/v1/projects/"+project+"/tasks/hook-body", map[string]any{
		"manifest": map[string]any{
			"runtime":    map[string]any{"language": "python", "version": "3.12"},
			"entrypoint": []string{"python", "main.py"},
			"network":    "none",
			"limits":     map[string]any{"timeout": "2m", "memory": "256Mi"},
		},
		"files": []map[string]any{{"path": "main.py", "content": webhookTask}},
	}, http.StatusOK, &applied)
	taskID := applied.Task.ID

	var set struct {
		Triggers []struct {
			ID      string `json:"id"`
			Webhook *struct {
				Token *string `json:"token"`
			} `json:"webhook"`
		} `json:"triggers"`
	}
	a.call(http.MethodPut, "/api/v1/tasks/"+taskID+"/triggers", map[string]any{"triggers": []any{
		map[string]any{"name": "push", "type": "webhook"},
	}}, http.StatusOK, &set)
	hookID, token := set.Triggers[0].ID, *set.Triggers[0].Webhook.Token
	a.call(http.MethodPost, "/api/v1/tasks/"+taskID+"/activation", map[string]any{"active": true}, http.StatusOK, nil)

	req, _ := http.NewRequest(http.MethodPost, a.base+"/hooks/"+hookID, bytes.NewReader([]byte(`{"lead": 42}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var accepted struct {
		RunID string `json:"run_id"`
	}
	err = json.NewDecoder(resp.Body).Decode(&accepted)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusAccepted {
		t.Fatalf("webhook = %d: %v", resp.StatusCode, err)
	}

	run := a.waitRun(accepted.RunID, 3*time.Minute)
	if run.Status != "completed" || run.Result == nil {
		t.Fatalf("run = %+v", run)
	}
	var data struct {
		Lead        int    `json:"lead"`
		ContentType string `json:"content_type"`
		Writable    bool   `json:"writable"`
	}
	if err := json.Unmarshal(run.Result.Data, &data); err != nil {
		t.Fatalf("result data %s: %v", run.Result.Data, err)
	}
	if data.Lead != 42 || data.ContentType != "application/json; charset=utf-8" || data.Writable {
		t.Fatalf("task saw %+v", data)
	}
}
