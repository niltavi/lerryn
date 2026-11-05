package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

// maxBundleFileBytes bounds what `lerryn tasks import` reads. The server
// takes request bodies of at most 4 MiB; a larger file cannot import, so
// it is refused before it is sent.
const maxBundleFileBytes = 4 << 20

type exportOptions struct {
	version    int
	history    bool
	noTriggers bool
	file       string
}

func (a *app) tasksExportCmd(f *flags) *cobra.Command {
	o := &exportOptions{}
	cmd := &cobra.Command{
		Use:   "export <task>",
		Short: "Export a task as a portable bundle",
		Long: `Export a task as a portable bundle that lerryn tasks import can bring into
any project of this or another deployment. The bundle holds the task name,
the manifest and files of the latest version (or --version), with
--history the versions before it, and the trigger definitions.

It holds no runs, no credential values or bindings, no webhook tokens and
no IDs of this deployment. The bundle is JSON; without --file it is
written to standard output.`,
		Example: `  lerryn tasks export weekday-digest --history --file digest.json
  lerryn tasks export 0190b2c4-… --version 3 > digest.json`,
		Args: requireArgs(1, "<task>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			return s.report(s.exportTask(cmd, args[0], o))
		},
	}
	fl := cmd.Flags()
	fl.IntVar(&o.version, "version", 0, "version to export (default: the latest)")
	fl.BoolVar(&o.history, "history", false, "include the versions before it")
	fl.BoolVar(&o.noTriggers, "no-triggers", false, "leave out the trigger definitions")
	fl.StringVar(&o.file, "file", "", "write the bundle to this file instead of standard output")
	return cmd
}

func (s *session) exportTask(cmd *cobra.Command, task string, o *exportOptions) error {
	ctx := cmd.Context()
	id, err := s.resolveTask(cmd, task)
	if err != nil {
		return err
	}
	args := map[string]any{"task_id": id}
	if cmd.Flags().Changed("version") {
		args["version"] = o.version
	}
	if o.history {
		args["history"] = true
	}
	if o.noTriggers {
		args["triggers"] = false
	}
	resp, err := s.Call(ctx, "exportTask", args, nil)
	if err != nil {
		return err
	}
	if o.file == "" {
		// The bundle is the output, whatever the output mode.
		return s.writeJSON(resp.Body)
	}
	// Task files may hold internal endpoints and logic, so the file is
	// private like the CLI's own configuration.
	if err := os.WriteFile(o.file, append(prettyJSON(resp.Body), '\n'), 0o600); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	var b struct {
		Name     string `json:"name"`
		Omitted  int    `json:"omitted_versions"`
		Versions []struct {
			Seq int `json:"seq"`
		} `json:"versions"`
		Triggers []json.RawMessage `json:"triggers"`
	}
	if err := resp.Decode(&b); err != nil {
		return err
	}
	seqs := make([]int, len(b.Versions))
	for i, v := range b.Versions {
		seqs[i] = v.Seq
	}
	summary := map[string]any{"file": o.file, "name": b.Name, "versions": seqs, "omitted_versions": b.Omitted, "triggers": len(b.Triggers)}
	return s.emitValue(summary, func(w io.Writer) error {
		if _, err := fmt.Fprintf(w, "Exported %s (versions %v, %d triggers) to %s.\n", b.Name, seqs, len(b.Triggers), o.file); err != nil {
			return err
		}
		if b.Omitted > 0 {
			_, err := fmt.Fprintf(w, "%d earlier versions were left out to keep the bundle within 4 MiB.\n", b.Omitted)
			return err
		}
		return nil
	})
}

type importOptions struct {
	project string
	name    string
	dryRun  bool
}

func (a *app) tasksImportCmd(f *flags) *cobra.Command {
	o := &importOptions{}
	cmd := &cobra.Command{
		Use:   "import <bundle-file>",
		Short: "Create a task from a bundle made by lerryn tasks export",
		Long: `Create a task from a bundle made by lerryn tasks export, in your personal
project or the one given with --project. You become the creator of the
task and of every version; versions are numbered from 1 again.

Imported versions are untested, so test before activating as usual. The
task starts inactive. Credential references keep their names and are not
bound: request them with credential_request or have their owners grant
them. Webhook triggers get new tokens, printed once. Use - to read the
bundle from standard input.`,
		Example: `  lerryn tasks import digest.json
  lerryn tasks import digest.json --project 0190b2c4-… --name digest-copy --dry-run`,
		Args: requireArgs(1, "<bundle-file>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			return s.report(s.importTask(cmd, args[0], o))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.project, "project", "", "project ID (default: your personal project)")
	fl.StringVar(&o.name, "name", "", "task name (default: the name in the bundle)")
	fl.BoolVar(&o.dryRun, "dry-run", false, "validate and show the result without creating the task")
	return cmd
}

func (s *session) importTask(cmd *cobra.Command, file string, o *importOptions) error {
	ctx := cmd.Context()
	var r io.Reader
	if file == "-" {
		r = cmd.InOrStdin()
	} else {
		fh, err := os.Open(file) // #nosec G304 -- a path the user named
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
		defer func() { _ = fh.Close() }()
		r = fh
	}
	data, err := io.ReadAll(io.LimitReader(r, maxBundleFileBytes+1))
	if err != nil {
		return fmt.Errorf("read bundle: %w", err)
	}
	if len(data) > maxBundleFileBytes {
		return usageError(fmt.Errorf("%s is larger than 4 MiB; export it again without --history", file))
	}
	var bundle map[string]any
	if err := json.Unmarshal(data, &bundle); err != nil || bundle == nil {
		return usageError(fmt.Errorf("%s is not a task bundle: expected the JSON object lerryn tasks export writes", file))
	}
	project := o.project
	if project == "" {
		p, err := principal(ctx, s)
		if err != nil {
			return err
		}
		if p == nil || p.PersonalProjectID == "" {
			return authError(errors.New("the server did not report a personal project; pass --project"))
		}
		project = p.PersonalProjectID
	}
	args := map[string]any{"project_id": project, "bundle": bundle}
	if o.name != "" {
		args["name"] = o.name
	}
	if o.dryRun {
		args["dry_run"] = true
	}
	resp, err := s.Call(ctx, "importTask", args, nil)
	if err != nil {
		return err
	}
	return s.emit(resp.Body, func(w io.Writer) error { return printImported(w, resp) })
}

// printImported prints an ImportTaskResult: the task, what to do before
// it runs, and the new webhook tokens.
func printImported(w io.Writer, resp *Response) error {
	var r struct {
		DryRun bool `json:"dry_run"`
		Task   *struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Links struct {
				Console string `json:"console"`
			} `json:"links"`
		} `json:"task"`
		Versions []struct {
			SourceSeq int `json:"source_seq"`
			Seq       int `json:"seq"`
		} `json:"versions"`
		Triggers []struct {
			Name    string `json:"name"`
			Webhook *struct {
				URL   string  `json:"url"`
				Token *string `json:"token"`
			} `json:"webhook"`
		} `json:"triggers"`
		Credentials []struct {
			Name     string `json:"name"`
			Optional bool   `json:"optional"`
		} `json:"credentials"`
		Warnings []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"warnings"`
	}
	if err := resp.Decode(&r); err != nil {
		return err
	}
	if r.DryRun || r.Task == nil {
		if _, err := fmt.Fprintf(w, "Dry run: nothing saved; this would create a task with %d versions and %d triggers.\n", len(r.Versions), len(r.Triggers)); err != nil {
			return err
		}
	} else {
		tw := newTable(w)
		tw.row("Task", r.Task.Name)
		tw.row("ID", r.Task.ID)
		tw.row("Versions", strconv.Itoa(len(r.Versions)))
		tw.row("Console", r.Task.Links.Console)
		if err := tw.flush(); err != nil {
			return err
		}
	}
	for _, c := range r.Credentials {
		note := ""
		if c.Optional {
			note = " (optional)"
		}
		if _, err := fmt.Fprintf(w, "Needs binding: credential %s%s\n", c.Name, note); err != nil {
			return err
		}
	}
	for _, t := range r.Triggers {
		if t.Webhook == nil || t.Webhook.Token == nil {
			continue
		}
		if _, err := fmt.Fprintf(w, "Webhook %s: %s\n  token (shown once): %s\n", t.Name, t.Webhook.URL, *t.Webhook.Token); err != nil {
			return err
		}
	}
	for _, warn := range r.Warnings {
		if _, err := fmt.Fprintf(w, "Warning: %s: %s\n", warn.Code, warn.Message); err != nil {
			return err
		}
	}
	if !r.DryRun {
		_, err := fmt.Fprintln(w, "The versions are untested: run one with --test before activating the task.")
		return err
	}
	return nil
}
