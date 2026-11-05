package metrics

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestNilMetricsRecordNothing(t *testing.T) {
	var m *Metrics
	m.AddLeaseExpirations(3)
	m.AddRetention(Retention{Runs: 1})
	m.RegisterStore(nil, nil)
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	if got := m.InstrumentHTTP(next, func(*http.Request) string { return "x" }); got == nil {
		t.Fatal("InstrumentHTTP on nil returned nil")
	}
}

func TestInstrumentHTTPLabelsByOperationAndCode(t *testing.T) {
	m := New()
	h := m.InstrumentHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/missing":
			http.NotFound(w, r)
		case "/stream":
			// Streaming handlers rely on Flush reaching the real writer.
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "data")
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}), func(r *http.Request) string { return strings.TrimPrefix(r.URL.Path, "/") })

	for _, path := range []string{"/getTask", "/getTask", "/missing", "/stream"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	m.AddLeaseExpirations(2)
	m.AddRetention(Retention{Logs: 3, Artifacts: 2, Objects: 4, LogBytes: 1024, ArtifactBytes: 512})

	body := scrape(t, m.Handler(""), "")
	for _, want := range []string{
		`lerryn_http_requests_total{code="200",operation="getTask"} 2`,
		`lerryn_http_requests_total{code="404",operation="missing"} 1`,
		`lerryn_http_requests_total{code="200",operation="stream"} 1`,
		`lerryn_http_request_duration_seconds_count{operation="getTask"} 2`,
		`lerryn_http_requests_in_flight 0`,
		`lerryn_runs_lease_expirations_total 2`,
		`lerryn_retention_removed_total{kind="runs"} 0`,
		`lerryn_retention_removed_total{kind="logs"} 3`,
		`lerryn_retention_removed_total{kind="artifacts"} 2`,
		`lerryn_retention_removed_total{kind="objects"} 4`,
		`lerryn_retention_removed_bytes_total{kind="logs"} 1024`,
		`lerryn_retention_removed_bytes_total{kind="artifacts"} 512`,
		`go_goroutines`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape lacks %q", want)
		}
	}
}

func TestHandlerRequiresToken(t *testing.T) {
	h := New().Handler("s3cret-token")
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong token", "Bearer nope", http.StatusUnauthorized},
		{"not bearer", "Basic s3cret-token", http.StatusUnauthorized},
		{"right token", "Bearer s3cret-token", http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}
}

func TestStoreMetrics(t *testing.T) {
	db := storetest.Open(t)
	ctx := context.Background()
	f := newFixture(t, db)

	now := store.Now()
	// One runner seen just now and one long gone: only the first counts.
	for _, r := range []store.Runner{
		{ID: "runner-a", Name: "a", Slots: 4, LastSeenAt: now, CreatedAt: now},
		{ID: "runner-b", Name: "b", Slots: 8, LastSeenAt: now.Add(-time.Hour), CreatedAt: now},
	} {
		if _, err := db.NewInsert().Model(&r).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	f.run(store.RunQueued, now.Add(-90*time.Second))
	f.run(store.RunQueued, now.Add(-10*time.Second))
	f.run(store.RunRunning, now)
	f.run(store.RunPreparing, now)
	f.run(store.RunCompleted, now)

	m := New()
	m.RegisterStore(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	body := scrape(t, m.Handler(""), "")
	for _, want := range []string{
		`lerryn_store_up 1`,
		`lerryn_runs{status="queued"} 2`,
		`lerryn_runs{status="running"} 1`,
		`lerryn_runs{status="completed"} 1`,
		`lerryn_runs{status="failed"} 0`,
		`lerryn_run_queue_depth 2`,
		`lerryn_runners_online 1`,
		`lerryn_runner_slots 4`,
		`lerryn_runner_slots_in_use 2`,
		`go_sql_open_connections{db_name="` + db.Native().Name() + `"}`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("scrape lacks %q\n%s", want, grepLerryn(body))
		}
	}
	age := metricValue(t, body, "lerryn_run_queue_oldest_seconds")
	if age < 89 || age > 600 {
		t.Errorf("oldest queued age = %v, want about 90s", age)
	}
}

func TestStoreMetricsReportDatabaseErrors(t *testing.T) {
	db := storetest.Open(t)
	m := New()
	m.RegisterStore(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = db.Close()
	body := scrape(t, m.Handler(""), "")
	if !strings.Contains(body, "lerryn_store_up 0") {
		t.Fatalf("closed database not reported:\n%s", grepLerryn(body))
	}
}

type fixture struct {
	t       *testing.T
	db      *store.DB
	project store.Project
	task    store.Task
	version store.TaskVersion
}

func newFixture(t *testing.T, db *store.DB) *fixture {
	t.Helper()
	ctx := context.Background()
	now := store.Now()
	user := store.User{ID: store.NewID(), Username: "metrics", CreatedAt: now, UpdatedAt: now}
	f := &fixture{t: t, db: db}
	f.project = store.Project{ID: store.NewID(), Name: "p", OwnerUserID: user.ID, IsPersonal: true, CreatedAt: now, UpdatedAt: now}
	f.task = store.Task{ID: store.NewID(), ProjectID: f.project.ID, Name: "t", LatestSeq: 1, CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now}
	f.version = store.TaskVersion{ID: store.NewID(), TaskID: f.task.ID, Seq: 1, Manifest: "{}", TreeHash: "x", CreatedBy: user.ID, CreatedAt: now}
	for _, m := range []any{&user, &f.project, &f.task, &f.version} {
		if _, err := db.NewInsert().Model(m).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *fixture) run(status string, queuedAt time.Time) {
	f.t.Helper()
	r := store.Run{
		ID: store.NewID(), ProjectID: f.project.ID, TaskID: f.task.ID, VersionID: f.version.ID, VersionSeq: 1,
		Status: status, TriggerKind: "manual", Params: "{}", QueuedAt: queuedAt,
	}
	if _, err := f.db.NewInsert().Model(&r).Exec(context.Background()); err != nil {
		f.t.Fatal(err)
	}
}

func scrape(t *testing.T, h http.Handler, token string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d: %s", rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func metricValue(t *testing.T, body, name string) float64 {
	t.Helper()
	for line := range strings.Lines(body) {
		if v, ok := strings.CutPrefix(line, name+" "); ok {
			var f float64
			if _, err := fmt.Sscan(v, &f); err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			return f
		}
	}
	t.Fatalf("metric %s not found", name)
	return 0
}

func grepLerryn(body string) string {
	var b strings.Builder
	for line := range strings.Lines(body) {
		if strings.HasPrefix(line, "lerryn_") {
			b.WriteString(line)
		}
	}
	return b.String()
}
