package agentevals

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

// Probes send well-formed requests on a scratch task. A stub handler
// answers 500 internal_error (see Response.Pending); anything else that is
// not the documented behaviour is reported as unavailable too, with what
// the server did, so a half-implemented feature does not produce
// misleading scores.

const probeTask = "eval-probe"

func probeRequest() map[string]any {
	return map[string]any{
		"manifest": map[string]any{
			"runtime":    map[string]any{"language": "shell", "version": "5"},
			"entrypoint": []string{"bash", "main.sh"},
			"workflow":   []map[string]any{{"title": "记录探测结果", "description": "输出探测标记，供调用方确认脚本已运行。"}},
			"network":    "none",
		},
		"files": []map[string]any{{"path": "main.sh", "content": "echo probe\n"}},
	}
}

// scratchTask returns the probe task, creating it on first use.
func scratchTask(ctx context.Context, in *Instance) (Task, error) {
	r, err := in.API(ctx, http.MethodGet, "/api/v1/tasks?name="+probeTask+"&project_id="+in.ProjectID, nil)
	if err == nil && r.Status == http.StatusOK {
		// List items carry latest_version as a number, not the version
		// object of getTask, so only the ID is read here.
		var page struct {
			Items []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if r.Decode(&page) == nil && len(page.Items) == 1 {
			return in.GetTask(ctx, page.Items[0].ID)
		}
	}
	return in.ApplyTask(ctx, probeTask, probeRequest(), 0)
}

func unavailable(what string, r Response) error {
	if r.Pending() {
		return fmt.Errorf("%w: %s is not implemented yet (500 internal_error)", ErrUnavailable, what)
	}
	return fmt.Errorf("%w: %s answered %d %s: %.200s", ErrUnavailable, what, r.Status, r.Code(), r.Body)
}

// ProbeIfMatch: applyTask on an existing task rejects a stale If-Match
// with 412 version_conflict.
var ProbeIfMatch = Probe{Name: "applyTask If-Match", Check: func(ctx context.Context, in *Instance) error {
	t, err := scratchTask(ctx, in)
	if err != nil {
		return err
	}
	r, err := in.API(ctx, http.MethodPut, "/api/v1/projects/"+in.ProjectID+"/tasks/"+probeTask, probeRequest(),
		"If-Match", fmt.Sprint(t.LatestVersion.Seq+100))
	if err != nil {
		return err
	}
	if r.Status == http.StatusPreconditionFailed && r.Code() == "version_conflict" {
		return nil
	}
	return unavailable("If-Match on applyTask", r)
}}

// ProbeTaskFile: getTaskFile returns a file of a version. Without it an
// agent cannot change an existing task: task_apply replaces all files.
var ProbeTaskFile = Probe{Name: "getTaskFile", Check: func(ctx context.Context, in *Instance) error {
	t, err := scratchTask(ctx, in)
	if err != nil {
		return err
	}
	r, err := in.API(ctx, http.MethodGet, fmt.Sprintf("/api/v1/tasks/%s/versions/%d/file?path=main.sh", t.ID, t.LatestVersion.Seq), nil)
	if err != nil {
		return err
	}
	if r.Status == http.StatusOK {
		return nil
	}
	return unavailable("getTaskFile", r)
}}

// ProbeDryRun: applyTask with dry_run=true previews without saving.
var ProbeDryRun = Probe{Name: "applyTask dry_run", Check: func(ctx context.Context, in *Instance) error {
	t, err := scratchTask(ctx, in)
	if err != nil {
		return err
	}
	r, err := in.API(ctx, http.MethodPut, "/api/v1/projects/"+in.ProjectID+"/tasks/"+probeTask+"?dry_run=true", probeRequest(),
		"If-Match", fmt.Sprint(t.LatestVersion.Seq))
	if err != nil {
		return err
	}
	if r.Status == http.StatusOK {
		return nil
	}
	return unavailable("dry_run on applyTask", r)
}}

// ProbeActivation: setTaskActivation answers. Pausing a task that was
// never activated is always allowed.
var ProbeActivation = Probe{Name: "setTaskActivation", Check: func(ctx context.Context, in *Instance) error {
	t, err := scratchTask(ctx, in)
	if err != nil {
		return err
	}
	r, err := in.API(ctx, http.MethodPost, "/api/v1/tasks/"+t.ID+"/activation", map[string]any{"active": false})
	if err != nil {
		return err
	}
	if r.Status == http.StatusOK {
		return nil
	}
	return unavailable("setTaskActivation", r)
}}

// ProbeTriggers: setTriggers previews a cron trigger.
var ProbeTriggers = Probe{Name: "setTriggers", Check: func(ctx context.Context, in *Instance) error {
	t, err := scratchTask(ctx, in)
	if err != nil {
		return err
	}
	r, err := in.API(ctx, http.MethodPut, "/api/v1/tasks/"+t.ID+"/triggers?dry_run=true", map[string]any{"triggers": []any{
		map[string]any{"name": "probe", "type": "cron", "schedule": "0 8 * * 1-5", "timezone": "Asia/Shanghai"},
	}})
	if err != nil {
		return err
	}
	if r.Status == http.StatusOK {
		return nil
	}
	return unavailable("setTriggers", r)
}}

// ProbeRerun: rerunRun answers not_found for an unknown run.
var ProbeRerun = Probe{Name: "rerunRun", Check: func(ctx context.Context, in *Instance) error {
	r, err := in.API(ctx, http.MethodPost, "/api/v1/runs/"+uuid.NewString()+"/rerun", map[string]any{})
	if err != nil {
		return err
	}
	if r.Status == http.StatusNotFound && r.Code() == "not_found" {
		return nil
	}
	return unavailable("rerunRun", r)
}}

// ProbeCredentialRequests: createCredentialRequest returns a fill URL that
// the user's session can open. The probe request is cancelled again.
var ProbeCredentialRequests = Probe{Name: "credential requests and fill page", Check: func(ctx context.Context, in *Instance) error {
	t, err := scratchTask(ctx, in)
	if err != nil {
		return err
	}
	var cr struct {
		ID  string `json:"id"`
		URL string `json:"url"`
	}
	r, err := in.API(ctx, http.MethodPost, "/api/v1/credential-requests", map[string]any{
		"task_id": t.ID, "name": "PROBE_KEY", "purpose": "探测", "fields": []any{map[string]any{"name": "PROBE_KEY"}},
	})
	if err != nil {
		return err
	}
	if r.Status != http.StatusCreated {
		return unavailable("createCredentialRequest", r)
	}
	if err := r.Decode(&cr); err != nil {
		return err
	}
	if _, err := fetchForm(ctx, in, cr.URL); err != nil {
		return fmt.Errorf("%w: credential fill page: %w", ErrUnavailable, err)
	}
	_, _ = in.API(ctx, http.MethodDelete, "/api/v1/credential-requests/"+url.PathEscape(cr.ID), nil)
	return nil
}}

// ProbeNotifications: notification channels can be listed.
var ProbeNotifications = Probe{Name: "notification channels", Check: func(ctx context.Context, in *Instance) error {
	r, err := in.API(ctx, http.MethodGet, "/api/v1/notification-channels", nil)
	if err != nil {
		return err
	}
	if r.Status == http.StatusOK {
		return nil
	}
	return unavailable("listNotificationChannels", r)
}}
