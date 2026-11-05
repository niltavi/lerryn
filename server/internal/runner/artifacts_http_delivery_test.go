package runner

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runnerhttp"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestArtifactHTTPDelivery(t *testing.T) {
	for _, mode := range []string{"four failures then recovery", "committed response lost", "report timeout", "protocol rejection"} {
		t.Run(mode, func(t *testing.T) {
			ctx := t.Context()
			db := storetest.Open(t)
			blobs, err := blob.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			authz := service.NewAuthorizer(db)
			runs := service.NewRuns(db, authz, nil)
			coord := service.NewCoordinator(db, blobs, runs, nil)
			runners := service.NewRunners(db, coord, nil).WithStaticRegistrationToken("registration")
			accounts := service.NewAccounts(db, nil)
			_, session, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"})
			if err != nil {
				t.Fatal(err)
			}
			p, err := accounts.Authenticate(ctx, session.Token)
			if err != nil {
				t.Fatal(err)
			}
			tasks := service.NewTasks(db, authz, blobs, nil)
			task, err := tasks.Apply(ctx, p, service.ApplyTaskInput{
				ProjectID: p.PersonalProjectID, Name: "artifact",
				Manifest: service.Manifest{Runtime: service.RuntimeSpec{Language: "python", Version: "3.12"}, Entrypoint: []string{"python", "main.py"}, Concurrency: service.ConcurrencyAllow},
				Files:    []service.FileInput{{Path: "main.py", Content: []byte("print('hi')\n")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			started, err := runs.Start(ctx, p, task.Task.Task.ID, service.StartRunInput{})
			if err != nil {
				t.Fatal(err)
			}
			realHandler := runnerhttp.NewHandler(runnerhttp.Options{Coordinator: coord, Runners: runners})
			expected := bytes.Repeat([]byte{0, 1, 2, 255, 'a'}, 1<<14)
			var calls atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/artifacts") {
					realHandler.ServeHTTP(w, r)
					return
				}
				n := calls.Add(1)
				data, err := io.ReadAll(r.Body)
				if err != nil || !bytes.Equal(data, expected) {
					t.Errorf("attempt %d sent %d/%d bytes: %v", n, len(data), len(expected), err)
				}
				r.Body = io.NopCloser(bytes.NewReader(data))
				switch {
				case mode == "four failures then recovery" && n <= 4, mode == "report timeout":
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				case mode == "protocol rejection":
					// The real handler verifies declared size against the body.
					r.Body = io.NopCloser(io.LimitReader(r.Body, int64(len(data)-1)))
					realHandler.ServeHTTP(w, r)
					return
				case mode == "committed response lost" && n == 1:
					recorder := httptest.NewRecorder()
					realHandler.ServeHTTP(recorder, r)
					if recorder.Code != http.StatusNoContent {
						t.Errorf("committed response = %d %s", recorder.Code, recorder.Body.String())
					}
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				default:
					realHandler.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			peer, err := runnerhttp.NewClient(runnerhttp.ClientOptions{ServerURL: srv.URL, RegistrationToken: "registration", CredentialFile: filepath.Join(t.TempDir(), "credential")})
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, nil)
			h.r.coord = peer
			h.r.reportTimeout = time.Minute
			if mode == "report timeout" {
				h.r.reportTimeout = time.Second
			}
			h.r.logFlushInterval = time.Hour
			h.r.eventPollInterval = time.Hour
			if err := peer.Register(ctx, runproto.Registration{ID: h.r.opts.ID, Slots: 1}); err != nil {
				t.Fatal(err)
			}
			assignments, err := peer.Claim(ctx, h.r.opts.ID, 1)
			if err != nil || len(assignments) != 1 {
				t.Fatalf("Claim = %v, %v", assignments, err)
			}
			h.ex.behave = func(c *fakeContainer) {
				out := c.mountSource(outMount)
				if err := os.Mkdir(filepath.Join(out, "nested"), 0o700); err != nil {
					t.Error(err)
				}
				if err := os.WriteFile(filepath.Join(out, "nested", "report 1.bin"), expected, 0o600); err != nil {
					t.Error(err)
				}
				c.exit(0, false)
			}
			if err := h.r.ensureLayout(); err != nil {
				t.Fatal(err)
			}
			active := h.r.track(assignments[0])
			h.r.execute(ctx, active)
			h.r.untrack(active)
			if calls.Load() == 0 {
				t.Fatal("the artifact delivery path was not exercised")
			}
			detail, err := runs.Get(ctx, p, started.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			artifacts, err := runs.Artifacts(ctx, p, started.Run.ID, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			count, err := db.NewSelect().Model((*store.RunEvent)(nil)).Where("run_id = ? AND kind = ?", started.Run.ID, store.EventArtifact).Count(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "protocol rejection" || mode == "report timeout" {
				if detail.Run.Status != store.RunRunning || len(artifacts.Artifacts) != 0 || count != 0 {
					t.Fatalf("undelivered file: state = %s, artifacts = %v, artifact events = %d", detail.Run.Status, artifacts.Artifacts, count)
				}
				if mode == "protocol rejection" && calls.Load() != 1 {
					t.Fatalf("permanent rejection retried %d times", calls.Load())
				}
				// Failed reporting leaves the active server run to its lease,
				// so exercise the existing reconciliation without waiting for TTL.
				if _, err := db.NewUpdate().Model((*store.Run)(nil)).
					Set("lease_expires_at = ?", store.Now().Add(-time.Minute)).Where("id = ?", started.Run.ID).Exec(ctx); err != nil {
					t.Fatal(err)
				}
				ids, err := runs.Reconcile(ctx)
				if err != nil || len(ids) != 1 || ids[0] != started.Run.ID {
					t.Fatalf("Reconcile = %v, %v", ids, err)
				}
				detail, err = runs.Get(ctx, p, started.Run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if detail.Run.Status != store.RunInterrupted || detail.Run.ErrorCode != "run_interrupted" || detail.Run.ErrorMessage == "" || detail.Run.FinishedAt.IsZero() {
					t.Fatalf("undelivered artifact after lease expiry: %+v", detail.Run)
				}
			} else {
				wantCalls := int64(5)
				if mode == "committed response lost" {
					wantCalls = 2
				}
				if detail.Run.Status != store.RunCompleted || calls.Load() != wantCalls || len(artifacts.Artifacts) != 1 || count != 1 {
					t.Fatalf("state = %s, attempts = %d, artifacts = %v, artifact events = %d", detail.Run.Status, calls.Load(), artifacts.Artifacts, count)
				}
				got, err := blob.ReadAll(ctx, blobs, artifacts.Artifacts[0].BlobKey)
				if err != nil || !bytes.Equal(got, expected) {
					t.Fatalf("stored artifact = %d/%d bytes: %v", len(got), len(expected), err)
				}
			}
			if len(h.ex.startedSpecs()) != 1 || !h.ex.wasRemoved("c1") {
				t.Fatal("report retry reran the task or failed to clean up the stopped container")
			}
		})
	}
}
