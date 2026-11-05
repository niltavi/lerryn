//go:build integration

package acceptance

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// retentionTask prints enough to be worth archiving and leaves the same
// report every time, so that its runs share one artifact object.
const retentionTask = `for i in range(300):
    print(f"row {i}", flush=True)
with open("/sbx/out/report.txt", "w") as f:
    f.write("the same report every day\n")
`

// sqliteTime is how the server writes times to SQLite.
func sqliteTime(t time.Time) string {
	return t.UTC().Format("2006-01-02 15:04:05.999999") + "+00:00"
}

type retentionRun struct {
	ID            string     `json:"id"`
	Status        string     `json:"status"`
	LogLines      int64      `json:"log_lines"`
	LogsExpiredAt *time.Time `json:"logs_expired_at"`
}

// P0 保留策略 (PRD 日志、产物和保留策略): run records, logs and artifacts
// stay until the deployer's retention policy removes them. Removed logs
// and artifacts read as removed, not as empty; a removed run is gone;
// the newest run of the task stays; objects another run still uses stay
// on disk, the others are deleted; the cleanup is audited.
func TestRetentionPolicy(t *testing.T) {
	t.Parallel()
	s := prepareServer(t)
	s.env = append(s.env,
		"LERRYN_RETENTION_RUNS_DAYS=30",
		"LERRYN_RETENTION_LOGS_DAYS=7",
		"LERRYN_RETENTION_ARTIFACTS_DAYS=7",
	)
	s.start()
	project := s.signUp("alice")
	taskID := s.applyTask(project, "daily-report", retentionTask, map[string]any{"concurrency": "allow"})
	s.activate(taskID)

	// Three runs: the oldest will be past the run period, the middle one
	// past the log and artifact periods, the newest within all of them.
	ids := make([]string, 3)
	for i := range ids {
		var run retentionRun
		s.call(http.MethodPost, "/api/v1/tasks/"+taskID+"/runs", map[string]any{}, http.StatusAccepted, &run)
		ids[i] = run.ID
		s.waitRetentionRun(run.ID, func(r retentionRun) bool { return r.Status == "completed" })
	}
	oldest, middle, newest := ids[0], ids[1], ids[2]

	// Wait until the leader has archived all three logs, and note the
	// objects they use.
	db := s.db()
	logBlob := map[string]string{}
	deadline := time.Now().Add(time.Minute)
	for len(logBlob) < 3 {
		if time.Now().After(deadline) {
			t.Fatalf("logs archived: %v", logBlob)
		}
		for _, id := range ids {
			var h sql.NullString
			if err := db.QueryRowContext(context.Background(), `SELECT log_blob FROM runs WHERE id = ?`, id).Scan(&h); err != nil {
				t.Fatal(err)
			}
			if h.Valid {
				logBlob[id] = h.String
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	var report string
	if err := db.QueryRowContext(context.Background(), `SELECT blob_key FROM artifacts WHERE run_id = ?`, newest).Scan(&report); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	object := func(hash string) string {
		return filepath.Join(s.dir, "storage", "blobs", "sha256", hash[:2], hash)
	}
	for _, id := range ids {
		if _, err := os.Stat(object(logBlob[id])); err != nil {
			t.Fatalf("log archive of %s: %v", id, err)
		}
	}

	// Ten and forty days pass.
	s.stop()
	db = s.db()
	now := time.Now()
	for id, age := range map[string]time.Duration{oldest: 40 * 24 * time.Hour, middle: 10 * 24 * time.Hour} {
		at := sqliteTime(now.Add(-age))
		for _, q := range []string{`UPDATE runs SET finished_at = ? WHERE id = ?`, `UPDATE artifacts SET created_at = ? WHERE run_id = ?`} {
			if _, err := db.ExecContext(context.Background(), q, at, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = db.Close()
	s.start()

	// The leader's first cleanup comes shortly after it starts.
	expired := s.waitRetentionRun(middle, func(r retentionRun) bool { return r.LogsExpiredAt != nil })
	if expired.Status != "completed" || expired.LogLines < 300 {
		t.Fatalf("middle run = %+v", expired)
	}
	if status, _ := s.request(http.MethodGet, "/api/v1/runs/"+oldest, nil); status != http.StatusNotFound {
		t.Fatalf("oldest run = %d, want 404 after the run period", status)
	}

	// The middle run's log and artifact read as removed.
	for _, path := range []string{"/logs?tail=20", "/logs/download"} {
		status, body := s.request(http.MethodGet, "/api/v1/runs/"+middle+path, nil)
		var p struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal(body, &p)
		if status != http.StatusGone || p.Code != "expired" {
			t.Fatalf("%s of the middle run = %d %s", path, status, body)
		}
	}
	var arts struct {
		Items []struct {
			ExpiredAt *time.Time `json:"expired_at"`
			Links     struct {
				Download string `json:"download"`
			} `json:"links"`
		} `json:"items"`
	}
	s.call(http.MethodGet, "/api/v1/runs/"+middle+"/artifacts", nil, http.StatusOK, &arts)
	if len(arts.Items) != 1 || arts.Items[0].ExpiredAt == nil {
		t.Fatalf("middle artifacts = %+v", arts)
	}
	if status, body := s.request(http.MethodGet, strings.TrimPrefix(arts.Items[0].Links.Download, s.base), nil); status != http.StatusGone {
		t.Fatalf("download of an expired artifact = %d %s", status, body)
	}

	// The newest run is untouched.
	var tail struct {
		Lines    []any `json:"lines"`
		Complete bool  `json:"complete"`
	}
	s.call(http.MethodGet, "/api/v1/runs/"+newest+"/logs?tail=20", nil, http.StatusOK, &tail)
	if len(tail.Lines) != 20 || !tail.Complete {
		t.Fatalf("newest log tail = %+v", tail)
	}
	s.call(http.MethodGet, "/api/v1/runs/"+newest+"/artifacts", nil, http.StatusOK, &arts)
	if len(arts.Items) != 1 || arts.Items[0].ExpiredAt != nil {
		t.Fatalf("newest artifacts = %+v", arts)
	}
	if status, body := s.request(http.MethodGet, strings.TrimPrefix(arts.Items[0].Links.Download, s.base), nil); status != http.StatusOK ||
		string(body) != "the same report every day\n" {
		t.Fatalf("newest artifact = %d %q", status, body)
	}

	// On disk: the removed logs are gone, the newest log and the shared
	// report stay.
	for id, want := range map[string]bool{oldest: false, middle: false, newest: true} {
		_, err := os.Stat(object(logBlob[id]))
		if (err == nil) != want {
			t.Errorf("log archive of %s on disk: %v, want present = %v", id, err, want)
		}
	}
	if _, err := os.Stat(object(report)); err != nil {
		t.Fatalf("the report the newest run still uses was deleted: %v", err)
	}

	// The cleanup left an audit record with its totals.
	db = s.db()
	defer func() { _ = db.Close() }()
	var details string
	if err := db.QueryRowContext(context.Background(),
		`SELECT details FROM audit_events WHERE action = 'retention.cleanup' ORDER BY ts LIMIT 1`).Scan(&details); err != nil {
		t.Fatalf("audit record: %v", err)
	}
	var totals map[string]any
	if err := json.Unmarshal([]byte(details), &totals); err != nil || totals["runs"] != float64(1) || totals["logs"] != float64(2) || totals["artifacts"] != float64(2) {
		t.Fatalf("audit details = %s", details)
	}
}

// waitRetentionRun polls getRun until done accepts the run.
func (s *server) waitRetentionRun(id string, done func(retentionRun) bool) retentionRun {
	s.t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		var run retentionRun
		s.call(http.MethodGet, "/api/v1/runs/"+id, nil, http.StatusOK, &run)
		if done(run) {
			return run
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("run %s = %+v", id, run)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
