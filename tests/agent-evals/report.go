package agentevals

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Summary aggregates the runs of one scenario.
type Summary struct {
	Scenario string  `json:"scenario"`
	Runs     int     `json:"runs"`
	Passed   int     `json:"passed"`
	Errors   int     `json:"errors"`
	Skipped  string  `json:"skipped,omitempty"`
	PassRate float64 `json:"pass_rate"`
	CostUSD  float64 `json:"cost_usd"`
	// CheckPasses counts passes per check over the scored runs.
	CheckPasses map[string]int `json:"check_passes"`
}

// Summarize groups results by scenario, in first-seen order. A run that
// errored in the harness counts against the pass rate; skipped runs do
// not count at all.
func Summarize(results []RunResult) []Summary {
	var order []string
	by := map[string]*Summary{}
	for _, r := range results {
		s, ok := by[r.Scenario]
		if !ok {
			s = &Summary{Scenario: r.Scenario, CheckPasses: map[string]int{}}
			by[r.Scenario] = s
			order = append(order, r.Scenario)
		}
		if r.Skipped != "" {
			s.Skipped = r.Skipped
			continue
		}
		s.Runs++
		s.CostUSD += r.CostUSD
		if r.Error != "" {
			s.Errors++
		}
		if r.Passed {
			s.Passed++
		}
		for _, c := range r.Checks {
			if c.Verdict == Pass {
				s.CheckPasses[c.Name]++
			} else if _, seen := s.CheckPasses[c.Name]; !seen {
				s.CheckPasses[c.Name] = 0
			}
		}
	}
	out := make([]Summary, 0, len(order))
	for _, name := range order {
		s := by[name]
		if s.Runs > 0 {
			s.PassRate = float64(s.Passed) / float64(s.Runs)
			s.Skipped = ""
		}
		out = append(out, *s)
	}
	return out
}

// Markdown renders the results as a report for people.
func Markdown(agent, model string, at time.Time, results []RunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent evals %s\n\nAgent `%s`, model `%s`.\n\n", at.UTC().Format(time.RFC3339), agent, model)
	b.WriteString("| Scenario | Runs | Passed | Pass rate | Cost (USD) | Note |\n| --- | --- | --- | --- | --- | --- |\n")
	var total float64
	for _, s := range Summarize(results) {
		total += s.CostUSD
		note := s.Skipped
		if s.Errors > 0 {
			note = fmt.Sprintf("%d harness error(s)", s.Errors)
		}
		rate := "-"
		if s.Runs > 0 {
			rate = fmt.Sprintf("%.0f%%", 100*s.PassRate)
		}
		fmt.Fprintf(&b, "| %s | %d | %d | %s | %.2f | %s |\n", s.Scenario, s.Runs, s.Passed, rate, s.CostUSD, strings.ReplaceAll(note, "|", `\|`))
	}
	fmt.Fprintf(&b, "\nTotal cost: %.2f USD.\n", total)
	for _, r := range results {
		if r.Skipped != "" {
			continue
		}
		status := "FAIL"
		if r.Passed {
			status = "PASS"
		}
		fmt.Fprintf(&b, "\n## %s #%d: %s\n\n", r.Scenario, r.Attempt, status)
		if r.Error != "" {
			fmt.Fprintf(&b, "Harness error: %s\n\n", r.Error)
		}
		fmt.Fprintf(&b, "Cost %.2f USD, %d turns, %s, outcome `%s`. Skills: %s. Directory: `%s`.\n\n",
			r.CostUSD, r.Turns, r.Duration.Round(time.Second), r.Outcome, strings.Join(r.Skills, ", "), r.Dir)
		for _, c := range r.Checks {
			fmt.Fprintf(&b, "- %s `%s`: %s\n", c.Verdict, c.Name, c.Detail)
		}
		if len(r.Tools) > 0 {
			fmt.Fprintf(&b, "\nTool calls: %s\n", strings.Join(r.Tools, " → "))
		}
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "\nNote: %s\n", n)
		}
	}
	return b.String()
}

// WriteReport writes results.json and report.md into dir.
func WriteReport(dir, agent, model string, results []RunResult) (string, error) {
	if err := writeJSON(filepath.Join(dir, "results.json"), map[string]any{
		"agent": agent, "model": model, "summary": Summarize(results), "runs": results,
	}); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "report.md")
	return path, os.WriteFile(path, // #nosec G703 -- under the eval output directory
		[]byte(Markdown(agent, model, time.Now(), results)), 0o600)
}
