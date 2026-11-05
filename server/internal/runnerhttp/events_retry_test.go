package runnerhttp_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runnerhttp"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestEventBatchRetryAfterPartialSuccess(t *testing.T) {
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed batch committed=%t", committed), func(t *testing.T) {
			e := newEnv(t)
			var calls, firstCount atomic.Int64
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/events") {
					e.handler.ServeHTTP(w, r)
					return
				}
				data, err := io.ReadAll(io.LimitReader(r.Body, (16<<20)+1))
				if err != nil || len(data) > 16<<20 {
					t.Errorf("event request=%d bytes, %v", len(data), err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var body struct {
					Events []runproto.Event `json:"events"`
				}
				if err := json.Unmarshal(data, &body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(data))
				call := calls.Add(1)
				if call == 1 {
					firstCount.Store(int64(len(body.Events)))
				}
				if call == 2 {
					if committed {
						rec := httptest.NewRecorder()
						e.handler.ServeHTTP(rec, r)
						if rec.Code != http.StatusNoContent {
							t.Errorf("committing lost response=%d, %s", rec.Code, rec.Body.String())
						}
					}
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
				e.handler.ServeHTTP(w, r)
			}))
			t.Cleanup(proxy.Close)
			peer, err := runnerhttp.NewClient(runnerhttp.ClientOptions{ServerURL: proxy.URL, RegistrationToken: staticToken, CredentialFile: filepath.Join(t.TempDir(), "credential")})
			if err != nil {
				t.Fatal(err)
			}
			if err := peer.Register(e.ctx, runproto.Registration{ID: resultRunnerID, Slots: 1}); err != nil {
				t.Fatal(err)
			}
			id := e.startRun()
			aa, err := peer.Claim(e.ctx, resultRunnerID, 1)
			if err != nil || len(aa) != 1 {
				t.Fatalf("claim=%+v, %v", aa, err)
			}
			if err := peer.Started(e.ctx, resultRunnerID, id, "", time.Now()); err != nil {
				t.Fatal(err)
			}
			raw := json.RawMessage(`"` + strings.Repeat("a", (64<<10)-2) + `"`)
			events := make([]runproto.Event, 300)
			for i := range events {
				events[i] = runproto.Event{Seq: int64(i + 1), Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Summary: fmt.Sprint(i), Data: raw}}
			}
			if err := peer.AppendEvents(e.ctx, resultRunnerID, id, events); err == nil || errors.Is(err, runproto.ErrRejected) {
				t.Fatalf("partial upload error=%v; want transient failure", err)
			}
			var run store.Run
			if err := e.db.NewSelect().Model(&run).Where("id = ?", id).Scan(e.ctx); err != nil {
				t.Fatal(err)
			}
			wantSeq := firstCount.Load()
			if committed {
				wantSeq = int64(len(events))
			}
			if firstCount.Load() <= 0 || firstCount.Load() >= int64(len(events)) || run.RunnerEventSeq != wantSeq {
				t.Fatalf("after partial upload: seq=%d first batch=%d committed=%t", run.RunnerEventSeq, firstCount.Load(), committed)
			}
			if err := peer.AppendEvents(e.ctx, resultRunnerID, id, events); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 4 {
				t.Fatalf("event requests=%d; want both original batches retried", calls.Load())
			}
			zero := 0
			if err := peer.PutState(e.ctx, resultRunnerID, id, runproto.StateSnapshot{Version: runproto.StateFormatVersion}); err != nil {
				t.Fatal(err)
			}
			if err := peer.Finish(e.ctx, resultRunnerID, id, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero, FinishedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			detail, err := e.runs.Get(e.ctx, e.user, id)
			if err != nil || detail.Run.Status != store.RunCompleted || detail.Run.RunnerEventSeq != 300 || detail.Result == nil || detail.Result.Summary != "299" {
				t.Fatalf("recovered run=%+v, %v", detail, err)
			}
			var rows []store.RunEvent
			if err := e.db.NewSelect().Model(&rows).Where("run_id = ? AND kind = ?", id, store.EventResult).Order("seq").Scan(e.ctx); err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(events) {
				t.Fatalf("stored %d result events, want %d without duplicates", len(rows), len(events))
			}
			for i, row := range rows {
				var result struct {
					Summary string `json:"summary"`
				}
				if err := json.Unmarshal([]byte(row.Payload), &result); err != nil || result.Summary != fmt.Sprint(i) {
					t.Fatalf("result %d is out of order: %+v, %v", i, result, err)
				}
			}
		})
	}
}
