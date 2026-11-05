package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/mcp"
)

// reservedFlags are flags of the api commands themselves; an operation
// argument with one of these names is set with --arg instead.
var reservedFlags = []string{"server", "output", "input", "arg", "describe", "help"}

func (a *app) apiCmd() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "api [operationId]",
		Short: "Call any API operation by its operationId",
		Long: `Call any operation of the Lerryn HTTP API. Every operation in the contract
is a subcommand named by its operationId; without one, the operations are
listed. Arguments use the names of the MCP tools (api_describe shows the same
input schema): path, query and header parameters and the top-level request
body properties, each as a flag with underscores written as hyphens, for
example --run-id. Values can also come from --arg name=value or from a JSON
object given with --input.

The response body is printed as it is. Errors exit with a non-zero code and
print the API's problem document.`,
		Example: `  lerryn api
  lerryn api getRun --run-id 0193f6c2-...
  lerryn api getRun --describe
  lerryn api applyTask --input request.json`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageError(fmt.Errorf("unknown operation %q; run lerryn api to list the operations", args[0]))
			}
			s, err := a.connect(cmd, f, false)
			if err != nil {
				return err
			}
			ops := s.reg.Operations()
			list := make([]mcp.OperationSummary, 0, len(ops))
			for _, o := range ops {
				list = append(list, o.Summarize())
			}
			return s.emitValue(map[string]any{"operations": list, "count": len(list)}, func(w io.Writer) error {
				tw := newTable(w)
				for _, o := range ops {
					tool := ""
					if o.ToolName != "" {
						tool = "(" + o.ToolName + ")"
					}
					_, _ = fmt.Fprintf(tw.tw, "%s\t%s %s\t%s\t%s\n", o.OperationID, o.Method, o.Path, o.Summary, tool)
				}
				return tw.flush()
			})
		},
	}
	addFlags(cmd, f)
	reg, err := a.registry(context.Background())
	if err != nil {
		cmd.RunE = func(*cobra.Command, []string) error { return err }
		return cmd
	}
	for _, op := range reg.Operations() {
		cmd.AddCommand(a.operationCmd(f, op))
	}
	return cmd
}

// argValue converts a flag's text into the JSON value of an argument.
type argValue func() (any, error)

func (a *app) operationCmd(f *flags, op *mcp.Operation) *cobra.Command {
	var (
		input    string
		pairs    []string
		describe bool
	)
	values := map[string]argValue{}
	flagNames := map[string]string{}
	cmd := &cobra.Command{
		Use:   op.OperationID,
		Short: op.Summary,
		Long:  operationHelp(op),
		Args:  requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.connect(cmd, f, false)
			if err != nil {
				return err
			}
			if describe {
				return s.emitValue(op.Detail(), nil)
			}
			args, err := collectArgs(cmd, input, pairs, values, flagNames)
			if err != nil {
				return err
			}
			resp, err := s.Call(cmd.Context(), op.OperationID, args, nil)
			if err != nil {
				return s.report(err)
			}
			if resp.JSON() {
				return s.writeJSON(resp.Body)
			}
			_, err = s.out.Write(resp.Body)
			return err
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&input, "input", "", "arguments as a JSON object from a file, or - for standard input")
	fl.StringArrayVarP(&pairs, "arg", "a", nil, "argument name=value; the value is parsed as JSON when it is valid JSON (repeatable)")
	fl.BoolVar(&describe, "describe", false, "print the operation's description and schemas instead of calling it")

	props, _ := op.InputSchema["properties"].(map[string]any)
	required, _ := op.InputSchema["required"].([]string)
	for _, name := range op.Args() {
		flagName := strings.ReplaceAll(name, "_", "-")
		if slices.Contains(reservedFlags, flagName) {
			continue
		}
		schema, _ := props[name].(map[string]any)
		usage := argUsage(schema, slices.Contains(required, name))
		flagNames[name] = flagName
		switch typ := schemaType(schema); typ {
		case "boolean":
			b := new(bool)
			fl.BoolVar(b, flagName, false, usage)
			values[name] = func() (any, error) { return *b, nil }
		case "array":
			items, _ := schema["items"].(map[string]any)
			itemType := schemaType(items)
			if itemType == "object" || itemType == "array" || itemType == "" {
				values[name] = jsonFlag(fl.String(flagName, "", usage+" (JSON array)"), flagName)
				continue
			}
			l := fl.StringArray(flagName, nil, usage+" (repeatable)")
			values[name] = func() (any, error) {
				out := make([]any, 0, len(*l))
				for _, s := range *l {
					v, err := scalar(s, itemType, flagName)
					if err != nil {
						return nil, err
					}
					out = append(out, v)
				}
				return out, nil
			}
		case "string", "integer", "number":
			s := fl.String(flagName, "", usage)
			values[name] = func() (any, error) { return scalar(*s, typ, flagName) }
		default:
			values[name] = jsonFlag(fl.String(flagName, "", usage+" (JSON)"), flagName)
		}
	}
	return cmd
}

// collectArgs merges --input, then --arg, then the typed flags, later ones
// winning.
func collectArgs(cmd *cobra.Command, input string, pairs []string, values map[string]argValue, flagNames map[string]string) (map[string]any, error) {
	args := map[string]any{}
	if input != "" {
		var data []byte
		var err error
		if input == "-" {
			data, err = io.ReadAll(cmd.InOrStdin())
		} else {
			data, err = os.ReadFile(input) // #nosec G304 -- a path the user named
		}
		if err != nil {
			return nil, fmt.Errorf("--input: %w", err)
		}
		if err := json.Unmarshal(data, &args); err != nil {
			return nil, usageError(fmt.Errorf("--input: want a JSON object: %w", err))
		}
		if args == nil {
			return nil, usageError(fmt.Errorf("--input: want a JSON object, got null"))
		}
	}
	for _, p := range pairs {
		name, value, ok := strings.Cut(p, "=")
		if !ok || name == "" {
			return nil, usageError(fmt.Errorf("--arg %q: want name=value", p))
		}
		args[name] = jsonOrString(value)
	}
	for name, get := range values {
		if !cmd.Flags().Changed(flagNames[name]) {
			continue
		}
		v, err := get()
		if err != nil {
			return nil, err
		}
		args[name] = v
	}
	return args, nil
}

func jsonFlag(s *string, flagName string) argValue {
	return func() (any, error) {
		var v any
		if err := json.Unmarshal([]byte(*s), &v); err != nil {
			return nil, usageError(fmt.Errorf("--%s: want JSON: %w", flagName, err))
		}
		return v, nil
	}
}

// scalar converts flag text to a JSON value of the schema type. Numbers
// are checked here so a typo fails before the request; their range is left
// to the API, which reports it like any other validation error.
func scalar(s, typ, flagName string) (any, error) {
	switch typ {
	case "integer":
		if _, err := strconv.ParseInt(s, 10, 64); err != nil {
			return nil, usageError(fmt.Errorf("--%s: want an integer, got %q", flagName, s))
		}
		return json.Number(s), nil
	case "number":
		if _, err := strconv.ParseFloat(s, 64); err != nil {
			return nil, usageError(fmt.Errorf("--%s: want a number, got %q", flagName, s))
		}
		return json.Number(s), nil
	case "boolean":
		b, err := strconv.ParseBool(s)
		if err != nil {
			return nil, usageError(fmt.Errorf("--%s: want true or false, got %q", flagName, s))
		}
		return b, nil
	default:
		return s, nil
	}
}

// schemaType returns the JSON type of a schema, taking the first non-null
// type of a 3.1 type array, or "" when the schema has none.
func schemaType(schema map[string]any) string {
	switch t := schema["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if s, _ := x.(string); s != "" && s != "null" {
				return s
			}
		}
	}
	return ""
}

func argUsage(schema map[string]any, required bool) string {
	desc, _ := schema["description"].(string)
	desc = strings.Join(strings.Fields(desc), " ")
	if enum, ok := schema["enum"].([]any); ok {
		var vals []string
		for _, e := range enum {
			vals = append(vals, fmt.Sprint(e))
		}
		desc = strings.TrimSpace(desc + " One of: " + strings.Join(vals, ", ") + ".")
	}
	if required {
		desc = strings.TrimSpace(desc + " (required)")
	}
	return desc
}

func operationHelp(op *mcp.Operation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", op.Method, op.Path)
	if op.Description != "" {
		b.WriteString(op.Description)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Kind: %s (token scope %s).", op.Kind, op.Scope())
	if op.ToolName != "" {
		fmt.Fprintf(&b, " MCP tool: %s.", op.ToolName)
	}
	if r := op.NotCallableReason(); r != "" && !op.SessionOnly {
		fmt.Fprintf(&b, "\nNot callable from the CLI: %s", r)
	}
	if op.SessionOnly {
		b.WriteString("\nOnly a console session can call this operation; an access token cannot.")
	}
	return b.String()
}
