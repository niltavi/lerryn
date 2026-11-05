package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// A run whose log and artifacts retention removed still reads, says when
// they went, and answers 410 expired where their content was.
func TestRetentionExpiredContent(t *testing.T) {
	c := newM2Client(t)
	alice, project := c.agent("alice")
	task := appliedTask(t, c.apply(alice, project, "digest", digestRequest(), "", ""))
	rec := c.as(alice, http.MethodPost, "/api/v1/tasks/"+task.ID.String()+"/runs", nil)
	c.want(rec, http.StatusAccepted, "")
	run := decode[Run](t, rec)
	if err := c.coord.Register(context.Background(), runproto.Registration{ID: "runner-1", Name: "builder-1", Slots: 8}); err != nil {
		t.Fatal(err)
	}
	c.playRunWithArtifact(run.ID, "out/report.json", `{"leads": 3}`)
	runPath := "/api/v1/runs/" + run.ID.String()

	rec = c.as(alice, http.MethodGet, runPath, nil)
	c.want(rec, http.StatusOK, "")
	if got := decode[Run](t, rec); got.LogsExpiredAt != nil {
		t.Fatalf("logs_expired_at before cleanup = %v", got.LogsExpiredAt)
	}
	rec = c.as(alice, http.MethodGet, runPath+"/artifacts", nil)
	c.want(rec, http.StatusOK, "")
	download := strings.TrimPrefix(decode[ArtifactPage](t, rec).Items[0].Links.Download, testOrigin)

	ctx := context.Background()
	for _, q := range []string{
		"UPDATE runs SET finished_at = ? WHERE id = ?",
		"UPDATE artifacts SET created_at = ? WHERE run_id = ?",
	} {
		if _, err := c.db.ExecContext(ctx, q, store.Now().Add(-10*24*time.Hour), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	res, err := service.NewRetention(c.db, nil, service.RetentionPolicy{Logs: 7 * 24 * time.Hour, Artifacts: 7 * 24 * time.Hour}, nil).Cleanup(ctx)
	if err != nil || res.Logs != 1 || res.Artifacts != 1 {
		t.Fatalf("cleanup = %+v, %v", res, err)
	}

	rec = c.as(alice, http.MethodGet, runPath, nil)
	c.want(rec, http.StatusOK, "")
	if got := decode[Run](t, rec); got.LogsExpiredAt == nil || got.Status != RunStatusCompleted {
		t.Fatalf("run after cleanup = %+v", got)
	}
	for _, path := range []string{runPath + "/logs?tail=10", runPath + "/logs/stream", runPath + "/logs/download", download} {
		rec := c.as(alice, http.MethodGet, path, nil)
		c.want(rec, http.StatusGone, ErrorCodeExpired)
		p := decode[Problem](t, rec)
		if p.NextActions == nil || (*p.NextActions)[0].Action != "run_rerun" {
			t.Fatalf("%s: problem = %+v", path, p)
		}
	}
	rec = c.as(alice, http.MethodGet, runPath+"/artifacts", nil)
	c.want(rec, http.StatusOK, "")
	if arts := decode[ArtifactPage](t, rec); len(arts.Items) != 1 || arts.Items[0].ExpiredAt == nil {
		t.Fatalf("artifacts after cleanup = %+v", arts)
	}
	// Someone without access still learns nothing.
	bob, _ := c.agent("bob")
	c.want(c.as(bob, http.MethodGet, runPath+"/logs", nil), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(bob, http.MethodGet, download, nil), http.StatusNotFound, ErrorCodeNotFound)
}
