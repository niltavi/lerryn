package httpapi

import (
	"net/http"
	"testing"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestRunArchiveEndpoint(t *testing.T) {
	c := newTriggerClient(t)
	agent, project := c.agent("alice")
	task := decode[ApplyTaskResult](t, c.apply(agent, project, "digest", digestRequest(), "", "")).Task
	base := "/api/v1/tasks/" + task.ID.String()
	run := decode[Run](t, c.as(agent, http.MethodPost, base+"/runs", nil))
	path := "/api/v1/runs/" + run.ID.String()
	body := map[string]any{"archived": true}
	c.want(c.as(agent, http.MethodPut, path+"/archive", map[string]any{}), http.StatusBadRequest, ErrorCodeValidationFailed)
	c.want(c.as(agent, http.MethodPut, path+"/archive", body), http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	reader, _ := c.agent("reader", "read")
	c.want(c.as(reader, http.MethodPut, path+"/archive", body), http.StatusForbidden, ErrorCodeInsufficientScope)
	viewer, _ := c.agent("viewer")
	c.want(c.as(viewer, http.MethodPut, path+"/archive", body), http.StatusNotFound, ErrorCodeNotFound)
	var user store.User
	if err := c.db.NewSelect().Model(&user).Where("username = ?", "viewer").Scan(t.Context()); err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	if _, err := c.db.NewInsert().Model(&store.RoleBinding{ID: store.NewID(), ProjectID: project, UserID: user.ID, Role: service.RoleViewer, CreatedAt: now, UpdatedAt: now}).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	c.want(c.as(viewer, http.MethodPut, path+"/archive", body), http.StatusForbidden, ErrorCodePermissionDenied)
	if _, err := c.db.NewUpdate().Model((*store.Run)(nil)).Set("status = ?", store.RunCompleted).Set("finished_at = ?", now).Where("id = ?", run.ID).Exec(t.Context()); err != nil {
		t.Fatal(err)
	}
	rec := c.as(agent, http.MethodPut, path+"/archive", body)
	c.want(rec, http.StatusOK, "")
	archived := decode[Run](t, rec)
	if archived.ArchivedAt == nil || archived.Status != RunStatusCompleted {
		t.Fatalf("archived run: %+v", archived)
	}
	for _, archivedOnly := range []bool{false, true} {
		query := "/api/v1/runs?task_id=" + task.ID.String()
		if archivedOnly {
			query += "&archived=true"
		}
		page := decode[RunPage](t, c.as(agent, http.MethodGet, query, nil))
		want := 0
		if archivedOnly {
			want = 1
		}
		if len(page.Items) != want {
			t.Fatalf("archive filter %v returned %d records", archivedOnly, len(page.Items))
		}
	}
	// Deleting the task hides it without removing its archived run or log.
	c.want(c.as(agent, http.MethodDelete, base, nil), http.StatusOK, "")
	c.want(c.as(agent, http.MethodGet, path, nil), http.StatusOK, "")
	c.want(c.as(agent, http.MethodGet, path+"/logs", nil), http.StatusOK, "")
	rec = c.as(agent, http.MethodPut, path+"/archive", map[string]any{"archived": false})
	c.want(rec, http.StatusOK, "")
	if decode[Run](t, rec).ArchivedAt != nil {
		t.Fatal("run was not restored")
	}
}
