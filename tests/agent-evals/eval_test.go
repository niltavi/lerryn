package agentevals

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// evalsEnv lists the agents to evaluate. See the package documentation.
const evalsEnv = "LERRYN_AGENT_EVALS"

func envFloat(t *testing.T, name string, def float64) float64 {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		t.Fatalf("%s=%q: %v", name, v, err)
	}
	return f
}

func TestAgentEvals(t *testing.T) {
	want := os.Getenv(evalsEnv)
	if want == "" {
		t.Skipf("set %s=claude to run the agent evaluations; they spend model tokens", evalsEnv)
	}
	if !strings.Contains(want, "claude") {
		t.Skipf("%s=%q: only claude is supported", evalsEnv, want)
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude is not installed")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker is not available:", err)
	}
	model := os.Getenv("LERRYN_AGENT_EVAL_MODEL")
	if model == "" {
		model = "sonnet"
	}
	runs := int(envFloat(t, "LERRYN_AGENT_EVAL_RUNS", 1))
	minRate := envFloat(t, "LERRYN_AGENT_EVAL_MIN_PASS_RATE", 1)
	agent := Claude{Model: model, MaxBudgetUSD: envFloat(t, "LERRYN_AGENT_EVAL_MAX_BUDGET_USD", 2)}

	out := os.Getenv("LERRYN_AGENT_EVAL_OUT")
	if out == "" {
		var err error
		if out, err = os.MkdirTemp("", "lerryn-agent-evals-"+time.Now().UTC().Format("20060102T150405Z")+"-"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(out, 0o750); err != nil { // #nosec G703 -- the developer chose the output directory
		t.Fatal(err)
	}
	t.Logf("transcripts and reports: %s", out)

	var only []string
	if s := os.Getenv("LERRYN_AGENT_EVAL_SCENARIOS"); s != "" {
		only = strings.Split(s, ",")
	}
	ctx := context.Background()
	if _, err := Binary(ctx); err != nil {
		t.Fatal(err)
	}

	var results []RunResult
	defer func() {
		path, err := WriteReport(out, agent.Name(), model, results)
		if err != nil {
			t.Errorf("write report: %v", err)
			return
		}
		data, _ := os.ReadFile(path) // #nosec G304 G703 -- the report just written
		t.Logf("report %s:\n%s", path, data)
	}()
	for _, s := range Scenarios() {
		if len(only) > 0 && !slices.Contains(only, s.Name) {
			continue
		}
		t.Run(s.Name, func(t *testing.T) {
			var mine []RunResult
			for i := 1; i <= runs; i++ {
				r := RunScenario(ctx, s, RunOptions{
					Agent: agent, Model: model, Attempt: i,
					Dir:       filepath.Join(out, s.Name, fmt.Sprint(i)),
					MountRoot: os.Getenv("LERRYN_TEST_MOUNT_ROOT"),
					Timeout:   15 * time.Minute,
				})
				results = append(results, r)
				mine = append(mine, r)
				if r.Skipped != "" {
					t.Skip(r.Skipped)
				}
				t.Logf("run %d: passed=%v cost=%.2f USD turns=%d error=%q", i, r.Passed, r.CostUSD, r.Turns, r.Error)
				for _, c := range r.Checks {
					t.Logf("  %s %s: %s", c.Verdict, c.Name, c.Detail)
				}
			}
			sum := Summarize(mine)[0]
			if sum.PassRate < minRate {
				t.Errorf("pass rate %.2f below %.2f (%d of %d runs)", sum.PassRate, minRate, sum.Passed, sum.Runs)
			}
		})
	}
}
