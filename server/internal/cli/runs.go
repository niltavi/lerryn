package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// maxServerWait is the longest wait getRun accepts per request.
const maxServerWait = 60 * time.Second

// logPollInterval is how often `runs logs --follow` asks for new lines when
// the last page was empty.
var logPollInterval = time.Second

// terminalStatuses are the run states that do not change any more (tech
// design 5.4).
var terminalStatuses = map[string]bool{
	"completed": true, "failed": true, "timed_out": true, "cancelled": true, "interrupted": true, "skipped": true,
}

func (a *app) runsCmd() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "runs",
		Short: "Start, inspect and cancel runs",
	}
	addFlags(cmd, f)
	cmd.AddCommand(a.runsStartCmd(f), a.runsGetCmd(f), a.runsWaitCmd(f), a.runsLogsCmd(f), a.runsCancelCmd(f))
	return cmd
}

type startOptions struct {
	version        int
	params         []string
	paramsJSON     string
	test           bool
	wait           time.Duration
	idempotencyKey string
}

func (a *app) runsStartCmd(f *flags) *cobra.Command {
	o := &startOptions{}
	cmd := &cobra.Command{
		Use:   "start <task>",
		Short: "Start a run of a task",
		Long: `Start a run of a task, by task ID or name, and print the run. With --wait,
wait for the run to end and exit with the run's outcome (see lerryn runs wait).`,
		Example: `  lerryn runs start 0193f6c2-... --test --wait 60s
  lerryn runs start weekday-digest --param source=crm --param limit=10`,
		Args: requireArgs(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			return s.report(s.startRun(cmd, args[0], o))
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&o.version, "version", 0, "task version to run (default: the latest)")
	fl.StringArrayVarP(&o.params, "param", "p", nil, "run parameter name=value; the value is parsed as JSON when it is valid JSON (repeatable)")
	fl.StringVar(&o.paramsJSON, "params-json", "", "run parameters as a JSON object, or @file to read them from a file")
	fl.BoolVar(&o.test, "test", false, "mark the run as a test run")
	fl.DurationVar(&o.wait, "wait", 0, "wait up to this long for the run to end, for example 60s or 10m")
	fl.StringVar(&o.idempotencyKey, "idempotency-key", "", "idempotency key, so a retried start does not start a second run")
	return cmd
}

func (s *session) startRun(cmd *cobra.Command, task string, o *startOptions) error {
	ctx := cmd.Context()
	taskID, err := s.resolveTask(cmd, task)
	if err != nil {
		return err
	}
	params, err := runParams(o.params, o.paramsJSON)
	if err != nil {
		return err
	}
	op, err := s.Operation("startRun")
	if err != nil {
		return err
	}
	args := map[string]any{"task_id": taskID}
	if o.version > 0 {
		args["version"] = o.version
	}
	if params != nil {
		args["params"] = params
	}
	if o.test {
		args["test"] = true
	}
	if o.idempotencyKey != "" {
		if !op.HasArg("idempotency_key") {
			return usageError(errors.New("--idempotency-key: this server's startRun does not take an idempotency key"))
		}
		args["idempotency_key"] = o.idempotencyKey
	}
	resp, err := s.Call(ctx, "startRun", args, nil)
	if err != nil {
		return err
	}
	if o.wait <= 0 {
		return s.emit(resp.Body, func(w io.Writer) error { return printRun(w, resp.Body) })
	}
	var run struct {
		ID string `json:"id"`
	}
	if err := resp.Decode(&run); err != nil {
		return err
	}
	return s.waitRun(ctx, run.ID, o.wait)
}

// runParams merges --params-json and --param into one object, or returns
// nil when neither is given so the manifest's params apply.
func runParams(pairs []string, jsonArg string) (map[string]any, error) {
	if len(pairs) == 0 && jsonArg == "" {
		return nil, nil
	}
	params := map[string]any{}
	if jsonArg != "" {
		data := []byte(jsonArg)
		if path, ok := strings.CutPrefix(jsonArg, "@"); ok {
			var err error
			if data, err = os.ReadFile(path); err != nil { // #nosec G304 -- a path the user named
				return nil, fmt.Errorf("--params-json: %w", err)
			}
		}
		if err := json.Unmarshal(data, &params); err != nil {
			return nil, usageError(fmt.Errorf("--params-json: want a JSON object: %w", err))
		}
		if params == nil {
			return nil, usageError(errors.New("--params-json: want a JSON object, got null"))
		}
	}
	for _, p := range pairs {
		name, value, ok := strings.Cut(p, "=")
		if !ok || name == "" {
			return nil, usageError(fmt.Errorf("--param %q: want name=value", p))
		}
		params[name] = jsonOrString(value)
	}
	return params, nil
}

// jsonOrString parses a command-line value as JSON when it is valid JSON,
// so 10, true and {"a":1} keep their types, and keeps it as a string
// otherwise.
func jsonOrString(v string) any {
	var x any
	if err := json.Unmarshal([]byte(v), &x); err == nil {
		return x
	}
	return v
}

func (a *app) runsGetCmd(f *flags) *cobra.Command {
	var wait time.Duration
	cmd := &cobra.Command{
		Use:   "get <run-id>",
		Short: "Show a run: status, stages, result and error",
		Args:  requireArgs(1, "<run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			if wait > maxServerWait {
				return usageError(fmt.Errorf("--wait is at most %s; use lerryn runs wait for longer", maxServerWait))
			}
			callArgs := map[string]any{"run_id": args[0]}
			if wait > 0 {
				callArgs["wait"] = int(wait.Seconds())
			}
			resp, err := s.Call(cmd.Context(), "getRun", callArgs, nil)
			if err != nil {
				return s.report(err)
			}
			return s.emit(resp.Body, func(w io.Writer) error { return printRun(w, resp.Body) })
		},
	}
	cmd.Flags().DurationVar(&wait, "wait", 0, "wait up to this long (at most 60s) for the run to end")
	return cmd
}

func (a *app) runsWaitCmd(f *flags) *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <run-id>",
		Short: "Wait for a run to end and exit with its outcome",
		Long: `Wait for a run to end, print it, and exit with its outcome: 0 when the run
completed and did not report a failed result, 5 when it ended otherwise
(failed, timed_out, cancelled, interrupted, skipped, or a failed result),
and 6 when the timeout passed first.`,
		Args: requireArgs(1, "<run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			if timeout <= 0 {
				return usageError(errors.New("--timeout must be positive"))
			}
			return s.report(s.waitRun(cmd.Context(), args[0], timeout))
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait")
	return cmd
}

// waitRun polls getRun with the server-side wait until the run ends or
// timeout passes, prints the last state, and returns the outcome as an
// exit code.
func (s *session) waitRun(ctx context.Context, runID string, timeout time.Duration) error {
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	deadline, _ := waitCtx.Deadline()
	parentDeadline, parentHasDeadline := ctx.Deadline()
	parentLimited := parentHasDeadline && !parentDeadline.After(deadline)
	var last *Response
	timedOut := func() error {
		if last != nil {
			if err := s.emit(last.Body, func(w io.Writer) error { return printRun(w, last.Body) }); err != nil {
				return err
			}
		}
		return &ExitError{Code: ExitWaitTimeout,
			Err: fmt.Errorf("timed out waiting for run %s after %s", runID, timeout)}
	}
	checkWait := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if waitCtx.Err() != nil || !time.Now().Before(deadline) {
			if parentLimited {
				return context.DeadlineExceeded
			}
			return timedOut()
		}
		return nil
	}
	for {
		if err := checkWait(); err != nil {
			return err
		}
		wait := min(time.Until(deadline), maxServerWait).Truncate(time.Second)
		args := map[string]any{"run_id": runID}
		if wait >= time.Second {
			args["wait"] = int(wait.Seconds())
		}
		resp, err := s.Call(waitCtx, "getRun", args, nil)
		// A buffered response may still finish reading after cancellation.
		if stop := checkWait(); stop != nil {
			return stop
		}
		if err != nil {
			return err
		}
		var run runView
		if err := resp.Decode(&run); err != nil {
			return err
		}
		if err := checkWait(); err != nil {
			return err
		}
		last = resp
		ended := terminalStatuses[run.Status]
		if ended {
			if err := s.emit(resp.Body, func(w io.Writer) error { return printRun(w, resp.Body) }); err != nil {
				return err
			}
			switch {
			case run.succeeded():
				return nil
			default:
				return &ExitError{Code: ExitRunFailed, Err: fmt.Errorf("run %s ended %s", runID, run.outcome())}
			}
		}
		if wait < time.Second {
			// The API waits in whole seconds. Poll the fractional tail
			// within the same deadline, including any blocked HTTP read.
			delay := min(max(time.Until(deadline), 0), 250*time.Millisecond, timeout/2)
			timer := time.NewTimer(delay)
			select {
			case <-waitCtx.Done():
				timer.Stop()
			case <-timer.C:
			}
		}
	}
}

type runView struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	Version  int    `json:"version"`
	Status   string `json:"status"`
	Test     bool   `json:"test"`
	ExitCode *int   `json:"exit_code"`
	Error    *struct {
		Code    string  `json:"code"`
		Stage   *string `json:"stage"`
		Message string  `json:"message"`
	} `json:"error"`
	Stages []struct {
		ID      string  `json:"id"`
		Status  string  `json:"status"`
		Message *string `json:"message"`
	} `json:"stages"`
	Result *struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	} `json:"result"`
	Links struct {
		Console string `json:"console"`
	} `json:"links"`
}

// succeeded is the exit-code notion of success: the process completed, and
// the task did not itself report a failed result.
func (r *runView) succeeded() bool {
	return r.Status == "completed" && (r.Result == nil || r.Result.Status != "failed")
}

func (r *runView) outcome() string {
	if r.Status == "completed" && r.Result != nil {
		return "completed with a " + r.Result.Status + " result"
	}
	return r.Status
}

func printRun(w io.Writer, body []byte) error {
	var r runView
	if err := json.Unmarshal(body, &r); err != nil {
		return fmt.Errorf("decode run: %w", err)
	}
	tw := newTable(w)
	tw.row("Run", r.ID)
	task := fmt.Sprintf("%s  version %d", r.TaskID, r.Version)
	if r.Test {
		task += "  (test)"
	}
	tw.row("Task", task)
	status := r.Status
	if r.ExitCode != nil {
		status += fmt.Sprintf("  exit %d", *r.ExitCode)
	}
	tw.row("Status", status)
	for _, st := range r.Stages {
		line := st.ID + " " + st.Status
		if st.Message != nil && *st.Message != "" {
			line += ": " + *st.Message
		}
		tw.row("Stage", line)
	}
	if r.Result != nil {
		tw.row("Result", r.Result.Status+": "+r.Result.Summary)
	}
	if r.Error != nil {
		tw.row("Error", r.Error.Code+": "+r.Error.Message)
	}
	tw.row("Console", r.Links.Console)
	return tw.flush()
}

func (a *app) runsCancelCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Ask a run to stop",
		Long: `Ask a run to stop. A queued run is cancelled at once; a running one moves to
cancelling until its container has stopped. Use lerryn runs wait to see it
end.`,
		Args: requireArgs(1, "<run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			resp, err := s.Call(cmd.Context(), "cancelRun", map[string]any{"run_id": args[0]}, nil)
			if err != nil {
				return s.report(err)
			}
			return s.emit(resp.Body, func(w io.Writer) error { return printRun(w, resp.Body) })
		},
	}
}

type logsOptions struct {
	tail   int
	limit  int
	cursor string
	follow bool
}

func (a *app) runsLogsCmd(f *flags) *cobra.Command {
	o := &logsOptions{}
	cmd := &cobra.Command{
		Use:   "logs <run-id>",
		Short: "Print a run's log",
		Long: `Print a run's log. Text output prints the lines; JSON output prints the page
as the API returns it, with next_cursor for reading on. With --follow, the
command keeps reading until the run has ended and every line is printed;
JSON output is then one log line object per line.`,
		Args: requireArgs(1, "<run-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			return s.report(s.logs(cmd.Context(), args[0], o))
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&o.tail, "tail", 0, "print only the last N lines")
	fl.IntVar(&o.limit, "limit", 0, "lines per page (server default when omitted)")
	fl.StringVar(&o.cursor, "cursor", "", "continue after this next_cursor")
	fl.BoolVarP(&o.follow, "follow", "F", false, "keep printing new lines until the run ends")
	return cmd
}

type logPage struct {
	Lines []struct {
		Stream string `json:"stream"`
		Text   string `json:"text"`
	} `json:"lines"`
	NextCursor   string `json:"next_cursor"`
	Complete     bool   `json:"complete"`
	DroppedLines int64  `json:"dropped_lines"`
}

func (s *session) logs(ctx context.Context, runID string, o *logsOptions) error {
	if o.tail > 0 && o.cursor != "" {
		return usageError(errors.New("--tail and --cursor cannot be combined"))
	}
	args := map[string]any{"run_id": runID}
	if o.tail > 0 {
		args["tail"] = o.tail
	}
	if o.limit > 0 {
		args["limit"] = o.limit
	}
	if o.cursor != "" {
		args["cursor"] = o.cursor
	}
	for {
		resp, err := s.Call(ctx, "readRunLogs", args, nil)
		if err != nil {
			return err
		}
		var page logPage
		if err := resp.Decode(&page); err != nil {
			return err
		}
		if !o.follow {
			return s.emit(resp.Body, func(w io.Writer) error { return printLogLines(w, &page) })
		}
		if s.json {
			var raw struct {
				Lines []json.RawMessage `json:"lines"`
			}
			if err := resp.Decode(&raw); err != nil {
				return err
			}
			for _, l := range raw.Lines {
				if _, err := fmt.Fprintf(s.out, "%s\n", l); err != nil {
					return err
				}
			}
		} else if err := printLogLines(s.out, &page); err != nil {
			return err
		}
		if page.Complete {
			return nil
		}
		delete(args, "tail")
		args["cursor"] = page.NextCursor
		if len(page.Lines) == 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(logPollInterval):
			}
		}
	}
}

func printLogLines(w io.Writer, page *logPage) error {
	for _, l := range page.Lines {
		prefix := ""
		if l.Stream == "system" {
			prefix = "[lerryn] "
		}
		if _, err := fmt.Fprintf(w, "%s%s\n", prefix, l.Text); err != nil {
			return err
		}
	}
	return nil
}
