// Package cli is the lerryn command-line client: login, the friendly
// commands for the common task and run flow, a generic `lerryn api` command
// for every operation in the embedded contract, and `lerryn mcp --stdio`.
//
// The client has no hand-written request code. Requests are built from the
// same operation index as the MCP tools (server/internal/mcp), so a new
// operation in api/ is reachable from the CLI as soon as it is embedded.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/niltavi/lerryn/server/internal/mcp"
)

// Options configures the CLI. The zero value uses the process environment.
type Options struct {
	// Spec is the OpenAPI contract the client is driven by.
	Spec []byte
	// Getenv reads environment variables; os.Getenv when nil.
	Getenv func(string) string
	// HTTPClient sends API requests; http.DefaultClient when nil.
	HTTPClient *http.Client
	// IsTerminal reports whether a stream is an interactive terminal; it
	// decides the default output format and whether prompts are possible.
	IsTerminal func(any) bool
}

// app is the state shared by the client commands of one invocation.
type app struct {
	opts Options

	regOnce sync.Once
	reg     *mcp.Registry
	regErr  error
}

// flags are the options every client command accepts.
type flags struct {
	server string
	output string
}

const (
	outputAuto = "auto"
	outputJSON = "json"
	outputText = "text"
)

// AddCommands adds the client commands to root.
func AddCommands(root *cobra.Command, opts Options) {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.IsTerminal == nil {
		opts.IsTerminal = isTerminal
	}
	a := &app{opts: opts}
	for _, c := range []*cobra.Command{
		a.loginCmd(),
		a.logoutCmd(),
		a.discoverCmd(),
		a.tasksCmd(),
		a.runsCmd(),
		a.apiCmd(),
		a.mcpCmd(),
		a.skillsCmd(),
	} {
		c.GroupID = groupClient
		root.AddCommand(c)
	}
	if !root.ContainsGroup(groupClient) {
		root.AddGroup(&cobra.Group{ID: groupClient, Title: "Client commands:"})
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError(err) })
}

const groupClient = "client"

func (a *app) registry(ctx context.Context) (*mcp.Registry, error) {
	a.regOnce.Do(func() {
		a.reg, a.regErr = mcp.Load(ctx, a.opts.Spec)
		if a.regErr != nil {
			a.regErr = fmt.Errorf("load the embedded API contract: %w", a.regErr)
		}
	})
	return a.reg, a.regErr
}

func addFlags(cmd *cobra.Command, f *flags) {
	cmd.PersistentFlags().StringVar(&f.server, "server", "", "server URL (env "+EnvServer+"; default from lerryn login)")
	cmd.PersistentFlags().StringVarP(&f.output, "output", "o", outputAuto,
		"output format: json, text, or auto (text on a terminal, json otherwise)")
}

// session is what a client command works with: where the server is, how to
// reach it, and how to print.
type session struct {
	*Client
	cfg        Config
	configPath string
	json       bool
	out, errw  io.Writer
}

// connect resolves the server and credential. The token comes from
// LERRYN_TOKEN, else from the config file, but a stored token is only sent
// to the server it was created for: pointing --server elsewhere must not
// leak it.
func (a *app) connect(cmd *cobra.Command, f *flags, needToken bool) (*session, error) {
	mode, err := a.outputMode(cmd, f)
	if err != nil {
		return nil, err
	}
	path, err := configPath(a.opts.Getenv)
	if err != nil {
		return nil, err
	}
	cfg, err := loadConfig(path)
	if err != nil {
		return nil, err
	}
	server := f.server
	if server == "" {
		server = a.opts.Getenv(EnvServer)
	}
	if server == "" {
		server = cfg.Server
	}
	if server == "" {
		return nil, authError(errors.New("no server configured; run lerryn login <server-url> or set " + EnvServer))
	}
	if server, err = normalizeServer(server); err != nil {
		return nil, usageError(err)
	}
	token := a.opts.Getenv(EnvToken)
	if token == "" && cfg.Token != "" && sameServer(server, cfg.Server) {
		token = cfg.Token
	}
	if needToken && token == "" {
		return nil, authError(fmt.Errorf("not logged in to %s; run lerryn login %s or set %s", server, server, EnvToken))
	}
	reg, err := a.registry(cmd.Context())
	if err != nil {
		return nil, err
	}
	c, err := newClient(server, token, a.opts.HTTPClient, reg)
	if err != nil {
		return nil, err
	}
	return &session{Client: c, cfg: cfg, configPath: path, json: mode == outputJSON, out: cmd.OutOrStdout(), errw: cmd.ErrOrStderr()}, nil
}

func (a *app) outputMode(cmd *cobra.Command, f *flags) (string, error) {
	switch f.output {
	case outputJSON, outputText:
		return f.output, nil
	case outputAuto, "":
		if a.opts.IsTerminal(cmd.OutOrStdout()) {
			return outputText, nil
		}
		return outputJSON, nil
	default:
		return "", usageError(fmt.Errorf("--output %q: want json, text or auto", f.output))
	}
}

// emit prints a result: the JSON body in json mode, or text in text mode.
func (s *session) emit(body []byte, text func(io.Writer) error) error {
	if s.json || text == nil {
		return s.writeJSON(body)
	}
	return text(s.out)
}

func (s *session) writeJSON(body []byte) error {
	if len(body) == 0 {
		return nil
	}
	_, err := fmt.Fprintf(s.out, "%s\n", prettyJSON(body))
	return err
}

func (s *session) emitValue(v any, text func(io.Writer) error) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("encode output: %w", err)
	}
	return s.emit(data, text)
}

// report prints an error for the user and marks it as printed. An API
// problem goes to stdout as JSON in json mode, so an agent parses a single
// stream, and to stderr as text otherwise.
func (s *session) report(err error) error {
	if err == nil {
		return nil
	}
	var ee *ExitError
	var ae *APIError
	if !errors.As(err, &ae) || !errors.As(err, &ee) || ee.Reported {
		return err
	}
	if s.json {
		body := ae.Body
		if ae.Problem == nil {
			body, _ = json.Marshal(map[string]any{"status": ae.Status, "body": string(ae.Body)})
		}
		_ = s.writeJSON(body)
	}
	_, _ = fmt.Fprintln(s.errw, "error:", ae.Error())
	if !s.json && ae.Problem != nil {
		if id, _ := ae.Problem["request_id"].(string); id != "" {
			_, _ = fmt.Fprintln(s.errw, "request id:", id)
		}
		for _, e := range list(ae.Problem["errors"]) {
			m, _ := e.(map[string]any)
			_, _ = fmt.Fprintf(s.errw, "  %v: %v\n", firstOf(m, "pointer", "parameter", "field"), m["message"])
		}
		for _, n := range list(ae.Problem["next_actions"]) {
			m, _ := n.(map[string]any)
			_, _ = fmt.Fprintf(s.errw, "next: %v  %v\n", m["action"], m["hint"])
		}
	}
	return &ExitError{Code: ee.Code, Err: err, Reported: true}
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

func firstOf(m map[string]any, keys ...string) any {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil && v != "" {
			return v
		}
	}
	return "-"
}

// isTerminal reports whether w is a terminal.
func isTerminal(w any) bool {
	f, ok := w.(interface{ Fd() uintptr })
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd())) // #nosec G115 -- file descriptors fit in an int
}

// requireArgs is cobra.ExactArgs with a usage exit code.
func requireArgs(n int, names ...string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != n {
			return usageError(fmt.Errorf("want %d argument(s): %s; got %d", n, strings.Join(names, " "), len(args)))
		}
		return nil
	}
}
