package agentevals

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/niltavi/lerryn/skills"
)

// Agent runs one prompt against an MCP endpoint in a working directory and
// returns what happened.
type Agent interface {
	Name() string
	Run(ctx context.Context, job Job) (Transcript, error)
}

// Job is one agent run.
type Job struct {
	Prompt string
	// MCPURL is the endpoint the agent connects to; Token its bearer token.
	MCPURL, Token string
	// Dir is an empty working directory; the skills are installed into it.
	Dir string
	// TranscriptPath receives the raw transcript.
	TranscriptPath string
}

// Transcript is what an agent run produced.
type Transcript struct {
	// ToolUses are the tools the model called, in order.
	ToolUses []ToolUse `json:"tool_uses"`
	// Texts are the model's text blocks, in order; Final is its last answer.
	Texts []string `json:"texts"`
	Final string   `json:"final"`
	// Skills are the skills the model loaded.
	Skills   []string      `json:"skills"`
	CostUSD  float64       `json:"cost_usd"`
	Turns    int           `json:"turns"`
	Duration time.Duration `json:"duration"`
	// Outcome is the client's end state, for example success or
	// error_max_budget_usd.
	Outcome string `json:"outcome"`
	IsError bool   `json:"is_error"`
}

// ToolUse is one tool call in the transcript.
type ToolUse struct {
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

// Claude is Claude Code in print mode.
type Claude struct {
	// Model is passed to --model.
	Model string
	// MaxBudgetUSD caps the spend of one run (--max-budget-usd).
	MaxBudgetUSD float64
	// Binary defaults to claude on PATH.
	Binary string
}

func (c Claude) Name() string { return "claude" }

// Tools the agent may use: the Lerryn MCP server, and loading and reading
// the installed skills. Anything else (shell, edits, web) is denied, which
// print mode does for every tool that is not allowed.
var claudeAllowedTools = []string{"mcp__lerryn", "Skill", "Read(./.claude/skills/**)", "Glob(./.claude/skills/**)"}

// Run writes a throwaway MCP config into the job directory, installs the
// skills as project skills there and runs claude -p. Only project settings
// are loaded (there are none besides the skills), --strict-mcp-config
// ignores every other MCP server, and no session is saved; the user's own
// configuration is read for login only and never written.
func (c Claude) Run(ctx context.Context, job Job) (Transcript, error) {
	if _, err := skills.Install(filepath.Join(job.Dir, ".claude", "skills"), skills.InstallOptions{Version: "eval"}); err != nil {
		return Transcript{}, err
	}
	cfg := map[string]any{"mcpServers": map[string]any{"lerryn": map[string]any{
		"type": "http", "url": job.MCPURL, "headers": map[string]string{"Authorization": "Bearer " + job.Token},
	}}}
	data, _ := json.Marshal(cfg)
	// The config holds the token; it lives outside the agent's working
	// directory so the agent cannot read it with its file tools.
	mcpConfig := job.TranscriptPath + ".mcp.json"
	if err := os.WriteFile(mcpConfig, data, 0o600); err != nil {
		return Transcript{}, err
	}
	defer func() { _ = os.Remove(mcpConfig) }()

	bin := c.Binary
	if bin == "" {
		bin = "claude"
	}
	args := []string{"-p", job.Prompt,
		"--mcp-config", mcpConfig, "--strict-mcp-config",
		"--allowedTools", strings.Join(claudeAllowedTools, ","),
		"--setting-sources", "project",
		"--no-session-persistence",
		"--output-format", "stream-json", "--verbose",
		"--model", c.Model,
	}
	if c.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(c.MaxBudgetUSD, 'f', 2, 64))
	}
	cmd := exec.CommandContext(ctx, bin, args...) // #nosec G204 -- fixed flags; gated behind an env var
	cmd.Dir = job.Dir
	cmd.Env = instanceEnv(os.Environ()) // the agent has no Lerryn CLI credentials either
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	start := time.Now()
	out, err := cmd.Output()
	if werr := os.WriteFile(job.TranscriptPath, out, 0o600); werr != nil {
		return Transcript{}, werr
	}
	tr := ParseClaudeTranscript(out)
	if tr.Duration == 0 {
		tr.Duration = time.Since(start)
	}
	if err != nil && tr.Outcome == "" {
		return tr, fmt.Errorf("claude: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return tr, nil
}

// ParseClaudeTranscript reads Claude Code's stream-json output.
func ParseClaudeTranscript(data []byte) Transcript {
	var tr Transcript
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		var ev struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
			Message struct {
				Content []struct {
					Type  string          `json:"type"`
					Text  string          `json:"text"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				} `json:"content"`
			} `json:"message"`
			Result     string  `json:"result"`
			IsError    bool    `json:"is_error"`
			CostUSD    float64 `json:"total_cost_usd"`
			NumTurns   int     `json:"num_turns"`
			DurationMS int64   `json:"duration_ms"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, b := range ev.Message.Content {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						tr.Texts = append(tr.Texts, b.Text)
					}
				case "tool_use":
					tr.ToolUses = append(tr.ToolUses, ToolUse{Name: b.Name, Input: b.Input})
					if b.Name == "Skill" {
						var in struct {
							Skill string `json:"skill"`
						}
						_ = json.Unmarshal(b.Input, &in)
						tr.Skills = append(tr.Skills, in.Skill)
					}
				}
			}
		case "result":
			tr.Outcome, tr.IsError = ev.Subtype, ev.IsError
			tr.CostUSD, tr.Turns = ev.CostUSD, ev.NumTurns
			tr.Duration = time.Duration(ev.DurationMS) * time.Millisecond
			tr.Final = ev.Result
		}
	}
	if tr.Final == "" && len(tr.Texts) > 0 {
		tr.Final = tr.Texts[len(tr.Texts)-1]
	}
	return tr
}
