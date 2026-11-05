package cli

import (
	"context"
	"testing"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/mcp"
)

// The friendly commands name a few operations and arguments of the real
// contract. Everything else is reached through `lerryn api`, which needs no
// such list. A rename in api/ shows up here instead of as a broken command.
func TestContractHasCLIOperations(t *testing.T) {
	reg, err := mcp.Load(context.Background(), api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	uses := map[string][]string{
		"discover":    nil,
		"login":       {"username", "password"},
		"logout":      nil,
		"createToken": {"name", "scopes", "expires_in_days"},
		"revokeToken": {"token_id"},
		"applyTask":   {"project_id", "task_name", "manifest", "files", "if_match", "dry_run"},
		"listTasks":   {"name", "project_id", "cursor"},
		"getTask":     {"task_id"},
		"startRun":    {"task_id", "version", "params", "test"},
		"getRun":      {"run_id", "wait"},
		"cancelRun":   {"run_id"},
		"readRunLogs": {"run_id", "cursor", "limit", "tail"},
	}
	for id, args := range uses {
		op, ok := reg.Operation(id)
		if !ok {
			t.Errorf("operation %s is missing from the contract", id)
			continue
		}
		if r := op.NotCallableReason(); r != "" && !op.SessionOnly {
			t.Errorf("%s is not callable: %s", id, r)
		}
		for _, a := range args {
			if !op.HasArg(a) {
				t.Errorf("%s has no argument %s; it has %v", id, a, op.Args())
			}
		}
	}
	// Every operation gets a CLI subcommand.
	root := apiCmdForTest(t)
	for _, op := range reg.Operations() {
		if c, _, err := root.Find([]string{op.OperationID}); err != nil || c.Name() != op.OperationID {
			t.Errorf("lerryn api has no %s subcommand", op.OperationID)
		}
	}
}

func apiCmdForTest(t *testing.T) *cobra.Command {
	t.Helper()
	a := &app{opts: Options{Spec: api.Spec, Getenv: envMap(nil), IsTerminal: func(any) bool { return false }}}
	return a.apiCmd()
}
