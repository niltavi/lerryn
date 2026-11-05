// Package agentevals runs real agent clients against a temporary Lerryn
// instance and scores what they did (tech design 11.8).
//
// Each scenario prepares a fresh all-in-one instance (the lerryn binary
// built from this checkout, SQLite, local storage and the Docker daemon
// from the environment), gives an agent a task in the user's words, and
// then checks the outcome against the server: versions, runs, triggers and
// the MCP tool calls recorded by a proxy in front of /mcp. The agent's own
// account of what it did is kept in the transcript but never trusted.
//
// The agent is Claude Code in print mode with a throwaway MCP config and
// the skills from skills/ installed into its working directory. The user's
// own Claude Code configuration is never written to. Evaluations spend
// model tokens, so they only run when asked:
//
//	LERRYN_AGENT_EVALS=claude go test -run TestAgentEvals -v -timeout 60m ./tests/agent-evals/
//
// Other settings:
//
//	LERRYN_AGENT_EVAL_MODEL          model alias or ID (default sonnet)
//	LERRYN_AGENT_EVAL_SCENARIOS      comma-separated scenario names (default all)
//	LERRYN_AGENT_EVAL_RUNS           runs per scenario (default 1)
//	LERRYN_AGENT_EVAL_MIN_PASS_RATE  fail a scenario below this rate (default 1)
//	LERRYN_AGENT_EVAL_MAX_BUDGET_USD spending cap per agent run (default 2)
//	LERRYN_AGENT_EVAL_OUT            where transcripts and reports go (default a new temp dir)
//	LERRYN_AGENT_EVAL_BINARY         evaluate this lerryn binary instead of building the checkout
//	LERRYN_TEST_MOUNT_ROOT           parent of the instance's data directory, for daemons that only mount some paths
//
// A scenario whose platform features are not implemented yet is skipped
// with the reason: before the agent starts, each scenario probes the
// endpoints it depends on.
package agentevals
