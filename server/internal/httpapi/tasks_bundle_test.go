package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// TestTaskExportImport exports a task with its history and triggers and
// imports it into another user's project, every response checked against
// the spec.
func TestTaskExportImport(t *testing.T) {
	c := newTriggerClient(t)
	alice, aliceProject := c.agent("alice")
	bob, bobProject := c.agent("bob")
	req := digestRequest()
	req["manifest"].(map[string]any)["credentials"] = []map[string]any{{"name": "LLM_API_KEY"}}
	first := decode[ApplyTaskResult](t, c.apply(alice, aliceProject, "digest", req, "", "")).Task
	taskPath := "/api/v1/tasks/" + first.ID.String()
	req["files"] = []map[string]any{{"path": "main.py", "content": "print('v2')\n"}, {"path": "run.sh", "content": "#!/bin/sh\n", "executable": true}}
	c.want(c.apply(alice, aliceProject, "digest", req, "1", ""), http.StatusOK, "")
	rec := c.as(alice, http.MethodPut, taskPath+"/triggers", map[string]any{"triggers": []any{
		map[string]any{"name": "morning", "type": "cron", "schedule": "0 8 * * 1-5", "timezone": "Asia/Shanghai", "params": map[string]any{"source": "erp"}},
		map[string]any{"name": "push", "type": "webhook"},
	}})
	c.want(rec, http.StatusOK, "")
	var sourceToken string
	for _, tr := range decode[TriggerSet](t, rec).Triggers {
		if tr.Webhook != nil {
			sourceToken = *tr.Webhook.Token
		}
	}

	rec = c.as(alice, http.MethodGet, taskPath+"/export", nil)
	c.want(rec, http.StatusOK, "")
	latest := decode[TaskBundle](t, rec)
	if latest.Format != TaskBundleFormatLerrynTaskBundle || latest.FormatVersion != 1 || latest.ServerVersion != "dev" ||
		len(latest.Versions) != 1 || latest.Versions[0].Seq != 2 || latest.Triggers == nil || len(*latest.Triggers) != 2 || latest.ActiveVersion != nil {
		t.Fatalf("bundle = %+v", latest)
	}
	// Nothing in the bundle ties it to this deployment.
	raw := rec.Body.String()
	for _, leak := range []string{first.ID.String(), aliceProject.String(), first.CreatedBy.String(), sourceToken, "wl_trg_"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the bundle contains %q: %s", leak, raw)
		}
	}
	rec = c.as(alice, http.MethodGet, taskPath+"/export?history=true&triggers=false", nil)
	c.want(rec, http.StatusOK, "")
	bundle := decode[TaskBundle](t, rec)
	if len(bundle.Versions) != 2 || bundle.Triggers != nil {
		t.Fatalf("history bundle = %+v", bundle)
	}
	c.want(c.as(alice, http.MethodGet, taskPath+"/export?version=9", nil), http.StatusBadRequest, ErrorCodeValidationFailed)
	c.want(c.as(bob, http.MethodGet, taskPath+"/export", nil), http.StatusNotFound, ErrorCodeNotFound)

	// Bob imports the bundle with history and triggers into his project.
	var full map[string]any
	if err := json.Unmarshal([]byte(raw), &full); err != nil {
		t.Fatal(err)
	}
	full["versions"] = mustMap(t, bundle)["versions"]
	importPath := "/api/v1/projects/" + bobProject.String() + "/task-imports"
	rec = c.as(bob, http.MethodPost, importPath+"?dry_run=true", map[string]any{"bundle": full})
	c.want(rec, http.StatusOK, "")
	if p := decode[ImportTaskResult](t, rec); !p.DryRun || p.Task != nil || len(p.Versions) != 2 || p.Triggers[1].Webhook.Token != nil {
		t.Fatalf("preview = %+v", p)
	}
	rec = c.as(bob, http.MethodPost, importPath, map[string]any{"bundle": full})
	c.want(rec, http.StatusOK, "")
	res := decode[ImportTaskResult](t, rec)
	if res.DryRun || res.Task == nil || res.Task.ProjectID != bobProject || res.Task.LatestVersion != 2 || res.Task.Name != "digest" ||
		res.Task.Links.Console != testOrigin+"/tasks/"+res.Task.ID.String() {
		t.Fatalf("imported = %+v", res.Task)
	}
	imported := decode[Task](t, c.as(bob, http.MethodGet, "/api/v1/tasks/"+res.Task.ID.String(), nil))
	if imported.ActiveVersion != nil || imported.State != TaskStateInactive || imported.LatestVersion.TestedAt != nil ||
		len(imported.LatestVersion.Files) != 2 || imported.CreatedBy == first.CreatedBy {
		t.Fatalf("imported task = %+v", imported)
	}
	if res.Versions[0] != (ImportedVersion{SourceSeq: 1, Seq: 1}) || res.Versions[1] != (ImportedVersion{SourceSeq: 2, Seq: 2}) {
		t.Errorf("versions = %+v", res.Versions)
	}
	if len(res.Credentials) != 1 || res.Credentials[0].Name != "LLM_API_KEY" || res.Credentials[0].Status != TaskCredentialStatusMissing || res.Credentials[0].Credential != nil {
		t.Errorf("credentials = %+v", res.Credentials)
	}
	var hook *Trigger
	for i := range res.Triggers {
		if res.Triggers[i].Webhook != nil {
			hook = &res.Triggers[i]
		}
	}
	if hook == nil || hook.Webhook.Token == nil || *hook.Webhook.Token == sourceToken || !strings.HasSuffix(hook.Webhook.URL, hook.ID.String()) {
		t.Fatalf("webhook = %+v", hook)
	}
	// The task does not fire until it is activated; then the new token
	// works and the source's does not.
	hookPath := hooksPath + hook.ID.String()
	c.want(c.hook(hookPath, *hook.Webhook.Token, "", "x"), http.StatusNotFound, ErrorCodeNotFound)
	c.activate(bob, res.Task.ID) // this client does not require a test run
	c.want(c.hook(hookPath, sourceToken, "", "x"), http.StatusUnauthorized, ErrorCodeUnauthenticated)
	c.want(c.hook(hookPath, *hook.Webhook.Token, "", "x"), http.StatusAccepted, "")

	// The name is taken in bob's project now.
	rec = c.as(bob, http.MethodPost, importPath, map[string]any{"bundle": full})
	c.want(rec, http.StatusPreconditionFailed, ErrorCodeVersionConflict)
	c.want(c.as(bob, http.MethodPost, importPath, map[string]any{"bundle": full, "name": "digest-copy"}), http.StatusOK, "")

	// Content errors point into the bundle; a wrong shape fails the contract.
	versions := full["versions"].([]any)
	broken := map[string]any{}
	for k, v := range full {
		broken[k] = v
	}
	v0 := mustMap(t, versions[0])
	v0["files"] = []any{map[string]any{"path": "a/../b", "content": "x"}}
	broken["versions"] = []any{v0}
	rec = c.as(bob, http.MethodPost, importPath, map[string]any{"bundle": broken, "name": "broken"})
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	if p := decode[Problem](t, rec); p.Errors == nil || (*p.Errors)[0].Pointer == nil || *(*p.Errors)[0].Pointer != "#/bundle/versions/0/files/0/path" {
		t.Fatalf("problem = %+v", p)
	}
	broken["format_version"] = 2
	c.want(c.as(bob, http.MethodPost, importPath, map[string]any{"bundle": broken, "name": "broken"}), http.StatusBadRequest, ErrorCodeValidationFailed)

	// A read-only token can export but not import.
	reader, _ := c.agent("carol", "read")
	c.want(c.as(reader, http.MethodPost, "/api/v1/projects/"+aliceProject.String()+"/task-imports", map[string]any{"bundle": full}),
		http.StatusForbidden, ErrorCodeInsufficientScope)

	var actions []string
	if err := c.db.NewSelect().Model((*store.AuditEvent)(nil)).Column("action").
		Where("action IN (?, ?)", "exportTask", "importTask").Order("id").Scan(context.Background(), &actions); err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, " ") != "exportTask exportTask importTask importTask" {
		t.Errorf("audit actions = %v", actions)
	}
}

// TestTaskExportViewer: a viewer of the project may export its tasks but
// not import into it.
func TestTaskExportViewer(t *testing.T) {
	c := newTriggerClient(t)
	alice, project := c.agent("alice")
	viewer, _ := c.agent("vera")
	task := decode[ApplyTaskResult](t, c.apply(alice, project, "digest", digestRequest(), "", "")).Task
	var vera store.User
	if err := c.db.NewSelect().Model(&vera).Where("username = ?", "vera").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	if _, err := c.db.NewInsert().Model(&store.RoleBinding{
		ID: store.NewID(), ProjectID: project, UserID: vera.ID, Role: service.RoleViewer, CreatedAt: now, UpdatedAt: now,
	}).Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := c.as(viewer, http.MethodGet, "/api/v1/tasks/"+task.ID.String()+"/export", nil)
	c.want(rec, http.StatusOK, "")
	rec = c.as(viewer, http.MethodPost, "/api/v1/projects/"+project.String()+"/task-imports",
		map[string]any{"bundle": mustMap(t, decode[TaskBundle](t, rec)), "name": "copy"})
	c.want(rec, http.StatusForbidden, ErrorCodePermissionDenied)
}

func mustMap(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
