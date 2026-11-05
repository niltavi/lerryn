package agentevals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Scenario is one evaluation: a situation, a request in the user's words,
// and checks against the server afterwards (tech design 11.8).
type Scenario struct {
	Name        string
	Description string
	// Requires are probes for the platform features the scenario needs.
	// A failing probe skips the scenario with its reason.
	Requires []Probe
	// Setup prepares the instance and returns the scenario's state.
	Setup func(ctx context.Context, env *Env) error
	// Prompt is what the user says to the agent.
	Prompt func(env *Env) string
	// Hook lets the scenario act while the agent works.
	Hook func(ctx context.Context, env *Env) Hook
	// Checks score the outcome.
	Checks []Check
}

// Probe checks that a platform feature is implemented.
type Probe struct {
	Name string
	// Check returns ErrUnavailable (wrapped with the reason) when the
	// feature is missing, or another error when the probe itself failed.
	Check func(ctx context.Context, in *Instance) error
}

// ErrUnavailable marks a platform feature that is not implemented yet.
var ErrUnavailable = errors.New("not available on this server")

// Check is one scored expectation.
type Check struct {
	Name string
	// Run returns the verdict and a short explanation.
	Run func(ctx context.Context, env *Env) (Verdict, string)
}

// Verdict is a check's outcome.
type Verdict string

const (
	Pass Verdict = "pass"
	Fail Verdict = "fail"
	// Skip means the check could not be decided on this server, because a
	// feature it reads is not implemented yet. It does not count.
	Skip Verdict = "skip"
)

// Env is what setup, hooks and checks share during one scenario run.
type Env struct {
	Instance *Instance
	Proxy    *Proxy
	// Transcript is filled in after the agent ran.
	Transcript Transcript
	// Values carry setup results (task and run IDs) to prompts and checks.
	Values map[string]string
	// Notes are things the hook or setup observed, kept in the report.
	Notes []string
	// cleanup runs when the scenario run ends.
	cleanup []func()
}

// Cleanup registers f to run when the scenario run ends.
func (e *Env) Cleanup(f func()) { e.cleanup = append(e.cleanup, f) }

// Note records an observation for the report.
func (e *Env) Note(format string, args ...any) {
	e.Notes = append(e.Notes, fmt.Sprintf(format, args...))
}

// RunResult is one scored scenario run.
type RunResult struct {
	Scenario string        `json:"scenario"`
	Agent    string        `json:"agent"`
	Model    string        `json:"model"`
	Attempt  int           `json:"attempt"`
	Passed   bool          `json:"passed"`
	Skipped  string        `json:"skipped,omitempty"`
	Error    string        `json:"error,omitempty"`
	Checks   []CheckResult `json:"checks"`
	CostUSD  float64       `json:"cost_usd"`
	Turns    int           `json:"turns"`
	Duration time.Duration `json:"duration_ns"`
	Outcome  string        `json:"outcome"`
	Skills   []string      `json:"skills"`
	Tools    []string      `json:"tools"`
	Final    string        `json:"final"`
	Notes    []string      `json:"notes,omitempty"`
	Dir      string        `json:"dir"`
}

// CheckResult is one check's verdict.
type CheckResult struct {
	Name    string  `json:"name"`
	Verdict Verdict `json:"verdict"`
	Detail  string  `json:"detail"`
}

// RunOptions configure one scenario run.
type RunOptions struct {
	Agent   Agent
	Model   string
	Attempt int
	// Dir receives the transcript, tool calls, server log and result.
	Dir string
	// MountRoot is the parent of the instance's data directory.
	MountRoot string
	Timeout   time.Duration
}

// RunScenario starts a fresh instance, runs the scenario once and scores
// it. Errors in the harness itself (not in the agent's work) are returned
// in RunResult.Error.
func RunScenario(ctx context.Context, s Scenario, o RunOptions) RunResult {
	res := RunResult{Scenario: s.Name, Agent: o.Agent.Name(), Model: o.Model, Attempt: o.Attempt, Dir: o.Dir}
	fail := func(err error) RunResult {
		res.Error = err.Error()
		_ = writeJSON(filepath.Join(o.Dir, "result.json"), res)
		return res
	}
	if err := os.MkdirAll(o.Dir, 0o750); err != nil {
		return fail(err)
	}
	data, err := os.MkdirTemp(o.MountRoot, "lerryn-eval-")
	if err != nil {
		return fail(err)
	}
	defer func() { _ = os.RemoveAll(data) }() // #nosec G703 -- a temp dir created above

	in, err := StartInstance(ctx, data, filepath.Join(o.Dir, "server.log"))
	if err != nil {
		return fail(err)
	}
	defer in.Stop()
	env := &Env{Instance: in, Values: map[string]string{}}
	defer func() {
		for i := len(env.cleanup) - 1; i >= 0; i-- {
			env.cleanup[i]()
		}
	}()

	for _, p := range s.Requires {
		if err := p.Check(ctx, in); err != nil {
			if errors.Is(err, ErrUnavailable) {
				res.Skipped = fmt.Sprintf("%s: %v", p.Name, err)
				_ = writeJSON(filepath.Join(o.Dir, "result.json"), res)
				return res
			}
			return fail(fmt.Errorf("probe %s: %w", p.Name, err))
		}
	}
	if s.Setup != nil {
		if err := s.Setup(ctx, env); err != nil {
			return fail(fmt.Errorf("setup: %w", err))
		}
	}
	var hook Hook
	if s.Hook != nil {
		hook = s.Hook(ctx, env)
	}
	proxy, err := NewProxy(in.Base, hook)
	if err != nil {
		return fail(err)
	}
	defer proxy.Close()
	env.Proxy = proxy

	work := filepath.Join(o.Dir, "workdir")
	if err := os.MkdirAll(work, 0o750); err != nil {
		return fail(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	tr, agentErr := o.Agent.Run(runCtx, Job{
		Prompt: s.Prompt(env), MCPURL: proxy.URL, Token: in.Token, Dir: work,
		TranscriptPath: filepath.Join(o.Dir, "transcript.jsonl"),
	})
	cancel()
	env.Transcript = tr
	res.CostUSD, res.Turns, res.Duration, res.Outcome = tr.CostUSD, tr.Turns, tr.Duration, tr.Outcome
	res.Skills, res.Final = tr.Skills, tr.Final
	calls := proxy.Calls()
	for _, c := range calls {
		res.Tools = append(res.Tools, c.Operation())
	}
	_ = writeJSON(filepath.Join(o.Dir, "tool-calls.json"), calls)
	if agentErr != nil {
		res.Notes = env.Notes
		return fail(agentErr)
	}

	res.Passed = true
	for _, c := range s.Checks {
		v, detail := c.Run(ctx, env)
		res.Checks = append(res.Checks, CheckResult{Name: c.Name, Verdict: v, Detail: detail})
		if v == Fail {
			res.Passed = false
		}
	}
	res.Notes = env.Notes
	_ = writeJSON(filepath.Join(o.Dir, "result.json"), res)
	return res
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600) // #nosec G703 -- under the eval output directory
}
