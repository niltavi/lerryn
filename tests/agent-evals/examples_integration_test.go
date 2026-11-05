//go:build integration

package agentevals

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/niltavi/lerryn/skills"
)

// The examples in the lerryn-automation skill are real tasks (tech design
// 11.5): each one is submitted to an all-in-one exactly as written and must
// pass a test run with an ok result.
//
//	go test -tags integration -run TestSkillExamples -v ./tests/agent-evals/
var exampleBlock = regexp.MustCompile("(?s)<!-- example: ([a-z0-9-]+) -->\\s*```json\\n(.*?)\\n```")

func TestSkillExamples(t *testing.T) {
	doc, err := skills.ReadFile("lerryn-automation", "references/examples.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := exampleBlock.FindAllStringSubmatch(string(doc), -1)
	if len(blocks) < 3 {
		t.Fatalf("found %d examples, want one per runtime", len(blocks))
	}
	ctx := context.Background()
	dir, err := os.MkdirTemp(os.Getenv("LERRYN_TEST_MOUNT_ROOT"), "lerryn-examples-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) }) // #nosec G703 -- created above
	in, err := StartInstance(ctx, dir, filepath.Join(t.TempDir(), "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Stop)

	for _, b := range blocks {
		t.Run(b[1], func(t *testing.T) {
			var req map[string]any
			if err := json.Unmarshal([]byte(b[2]), &req); err != nil {
				t.Fatalf("example is not valid JSON: %v", err)
			}
			name, _ := req["task_name"].(string)
			task, err := in.ApplyTask(ctx, name, map[string]any{"manifest": req["manifest"], "files": req["files"]}, 0)
			if err != nil {
				t.Fatal(err)
			}
			run, err := in.StartRun(ctx, task.ID, map[string]any{"test": true})
			if err != nil {
				t.Fatal(err)
			}
			run, err = in.WaitRun(ctx, run.ID, 5*time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			if run.Status != "completed" || run.Result == nil || run.Result.Status != "ok" {
				r, _ := in.API(ctx, "GET", "/api/v1/runs/"+run.ID+"/logs?tail=50", nil)
				t.Fatalf("run = %s %+v %+v\nlogs: %s", run.Status, run.Error, run.Result, r.Body)
			}
			t.Logf("%s: %s %s", name, run.Result.Summary, run.Result.Data)
		})
	}
}
