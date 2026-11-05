package runnerhttp_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestClaimBudgetDoesNotStrandRuns(t *testing.T) {
	for _, tt := range []struct {
		name string
		max  int
		want int
	}{
		{name: "small batch", max: 3, want: 3},
		{name: "large batch request", max: 64, want: 8},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			dir := t.TempDir()
			client := e.client(dir, staticToken)
			if err := client.Register(e.ctx, runproto.Registration{ID: "remote", Slots: 10}); err != nil {
				t.Fatal(err)
			}
			m := service.Manifest{Runtime: service.RuntimeSpec{Language: "python", Version: "3.12"},
				Entrypoint: []string{"python", "main.py"}, Concurrency: service.ConcurrencyAllow}
			task, err := e.tasks.Apply(e.ctx, e.user, service.ApplyTaskInput{ProjectID: e.user.PersonalProjectID, Name: "batch", Manifest: m,
				Files: []service.FileInput{{Path: "main.py", Content: []byte("print('hello')")}}})
			if err != nil {
				t.Fatal(err)
			}
			for range 10 {
				if _, err := e.runs.Start(e.ctx, e.user, task.Task.Task.ID, service.StartRunInput{}); err != nil {
					t.Fatal(err)
				}
			}
			token, err := os.ReadFile(filepath.Join(dir, "runner-credential")) // #nosec G304 -- a temp dir of this test
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]int{"max": tt.max})
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, e.srv.URL+"/runner/v1/claim", bytes.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
			req.Header.Set("Lerryn-Runner-Protocol", "2")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			var first struct {
				Assignments []struct {
					RunID uuid.UUID `json:"run_id"`
				} `json:"assignments"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&first); err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("claim response: status %d, %v", resp.StatusCode, err)
			}
			if len(first.Assignments) != tt.want {
				t.Fatalf("HTTP claimed %d runs, want %d", len(first.Assignments), tt.want)
			}
			seen := map[uuid.UUID]bool{}
			for _, a := range first.Assignments {
				seen[a.RunID] = true
			}
			next, err := client.Claim(e.ctx, "remote", 64)
			if err != nil || len(next) != 10-tt.want {
				t.Fatalf("remaining assignments = %d, %v", len(next), err)
			}
			for _, a := range next {
				if seen[a.RunID] {
					t.Fatalf("run %s delivered twice", a.RunID)
				}
				seen[a.RunID] = true
			}
			var runs []store.Run
			if err := e.db.NewSelect().Model(&runs).Where("task_id = ?", task.Task.Task.ID).Scan(e.ctx); err != nil {
				t.Fatal(err)
			}
			for _, run := range runs {
				if run.Status != store.RunPreparing || !seen[run.ID] {
					t.Fatalf("committed assignment was not delivered: %+v", run)
				}
			}
		})
	}
}

func TestClaimBudgetPreservesEmbeddedBatch(t *testing.T) {
	e := newEnv(t)
	if err := e.coord.Register(e.ctx, runproto.Registration{ID: "embedded", Slots: 10}); err != nil {
		t.Fatal(err)
	}
	first := e.startRun()
	var run store.Run
	if err := e.db.NewSelect().Model(&run).Where("id = ?", first).Scan(e.ctx); err != nil {
		t.Fatal(err)
	}
	for range 9 {
		if _, err := e.runs.Start(e.ctx, e.user, run.TaskID, service.StartRunInput{}); err != nil {
			t.Fatal(err)
		}
	}
	assignments, err := e.coord.Claim(e.ctx, "embedded", 64)
	if err != nil || len(assignments) != 10 {
		t.Fatalf("embedded assignments = %d, %v; HTTP cap must not limit the in-process coordinator", len(assignments), err)
	}
}
