package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

// manifestFiles are the names `lerryn tasks apply` looks for in a task
// directory, in order.
var manifestFiles = []string{"lerryn.yaml", "lerryn.yml"}

// skippedDirs are never uploaded: they hold caches and installed
// dependencies that the platform rebuilds from the lock file.
var skippedDirs = []string{"__pycache__", "node_modules"}

func (a *app) tasksCmd() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "tasks",
		Short: "Submit and inspect tasks",
	}
	addFlags(cmd, f)
	cmd.AddCommand(a.tasksApplyCmd(f), a.tasksGetCmd(f), a.tasksExportCmd(f), a.tasksImportCmd(f))
	return cmd
}

type applyOptions struct {
	name            string
	project         string
	manifest        string
	expectedVersion int
	dryRun          bool
}

func (a *app) tasksApplyCmd(f *flags) *cobra.Command {
	o := &applyOptions{}
	cmd := &cobra.Command{
		Use:   "apply <dir>",
		Short: "Create a task or submit a new version from a directory",
		Long: `Create a task or submit a new version from a directory. The directory holds
a manifest file, lerryn.yaml, and the task's files. The manifest has the
fields of the task manifest (runtime, entrypoint, params, stages, limits,
network, concurrency, ...) plus an optional name; the name defaults to the
directory name.

Every other file is uploaded as UTF-8 text with its executable bit. Hidden
files and directories (such as .git, .venv and .env), __pycache__ and
node_modules are skipped, and symbolic links are refused.`,
		Example: `  lerryn tasks apply ./weekday-digest
  lerryn tasks apply ./weekday-digest --expected-version 3`,
		Args: requireArgs(1, "<dir>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			return s.report(s.applyTask(cmd, args[0], o))
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&o.name, "name", "", "task name (default: the manifest's name, else the directory name)")
	fl.StringVar(&o.project, "project", "", "project ID (default: your personal project)")
	fl.StringVarP(&o.manifest, "manifest", "f", "", "manifest file (default: lerryn.yaml in the directory)")
	fl.IntVar(&o.expectedVersion, "expected-version", 0, "fail unless the task's latest version is this one")
	fl.BoolVar(&o.dryRun, "dry-run", false, "validate and show the result without creating a version")
	return cmd
}

func (s *session) applyTask(cmd *cobra.Command, dir string, o *applyOptions) error {
	ctx := cmd.Context()
	t, err := readTaskDir(dir, o.manifest)
	if err != nil {
		return err
	}
	name := o.name
	if name == "" {
		name = t.name
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
	op, err := s.Operation("applyTask")
	if err != nil {
		return err
	}
	args := map[string]any{"project_id": project, "task_name": name, "manifest": t.manifest, "files": t.files}
	if cmd.Flags().Changed("expected-version") {
		// The contract may express the expected version as an If-Match
		// header or a body field; use whichever this operation declares.
		switch {
		case op.HasArg("if_match"):
			args["if_match"] = strconv.Itoa(o.expectedVersion)
		case op.HasArg("expected_version"):
			args["expected_version"] = o.expectedVersion
		default:
			return usageError(errors.New("--expected-version: this server's applyTask does not take an expected version"))
		}
	}
	if o.dryRun {
		if !op.HasArg("dry_run") {
			return usageError(errors.New("--dry-run: this server's applyTask does not support a dry run"))
		}
		args["dry_run"] = true
	}
	resp, err := s.Call(ctx, "applyTask", args, nil)
	if err != nil {
		return err
	}
	return s.emit(resp.Body, func(w io.Writer) error { return printApplied(w, resp) })
}

// printApplied prints an ApplyTaskResult: what happened, the task, and
// the warnings to deal with before running or activating it.
func printApplied(w io.Writer, resp *Response) error {
	var r struct {
		DryRun  bool            `json:"dry_run"`
		Created bool            `json:"created"`
		Version int             `json:"version"`
		Task    json.RawMessage `json:"task"`
		Diff    *struct {
			Base          *int    `json:"base"`
			ManifestPatch *string `json:"manifest_patch"`
			Files         []struct {
				Path      string `json:"path"`
				Change    string `json:"change"`
				Additions int    `json:"additions"`
				Deletions int    `json:"deletions"`
			} `json:"files"`
		} `json:"diff"`
		Warnings []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"warnings"`
	}
	if err := resp.Decode(&r); err != nil {
		return err
	}
	switch {
	case r.DryRun:
		if _, err := fmt.Fprintf(w, "Dry run: nothing saved; this would create version %d.\n", r.Version); err != nil {
			return err
		}
	case r.Created:
		if _, err := fmt.Fprintf(w, "Created the task with version %d.\n", r.Version); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(w, "Created version %d.\n", r.Version); err != nil {
			return err
		}
	}
	if len(r.Task) > 0 && string(r.Task) != "null" {
		if err := printTaskJSON(w, r.Task); err != nil {
			return err
		}
	}
	if d := r.Diff; d != nil && d.Base != nil {
		if len(d.Files) == 0 && d.ManifestPatch == nil {
			if _, err := fmt.Fprintf(w, "No changes from version %d.\n", *d.Base); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintf(w, "Changes from version %d:\n", *d.Base); err != nil {
			return err
		}
		if d.ManifestPatch != nil {
			if _, err := fmt.Fprintln(w, "  manifest"); err != nil {
				return err
			}
		}
		for _, f := range d.Files {
			if _, err := fmt.Fprintf(w, "  %s (%s, +%d -%d)\n", f.Path, f.Change, f.Additions, f.Deletions); err != nil {
				return err
			}
		}
	}
	for _, warn := range r.Warnings {
		if _, err := fmt.Fprintf(w, "Warning: %s: %s\n", warn.Code, warn.Message); err != nil {
			return err
		}
	}
	return nil
}

func (a *app) tasksGetCmd(f *flags) *cobra.Command {
	return &cobra.Command{
		Use:   "get <task-id>",
		Short: "Show a task and its latest version",
		Args:  requireArgs(1, "<task-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			resp, err := s.Call(cmd.Context(), "getTask", map[string]any{"task_id": args[0]}, nil)
			if err != nil {
				return s.report(err)
			}
			return s.emit(resp.Body, func(w io.Writer) error { return printTask(w, resp) })
		},
	}
}

func printTask(w io.Writer, resp *Response) error {
	return printTaskJSON(w, resp.Body)
}

func printTaskJSON(w io.Writer, body []byte) error {
	var t struct {
		ID            string `json:"id"`
		Name          string `json:"name"`
		Description   string `json:"description"`
		LatestVersion struct {
			Seq   int `json:"seq"`
			Files []struct {
				Path string `json:"path"`
			} `json:"files"`
		} `json:"latest_version"`
		Links struct {
			Console string `json:"console"`
		} `json:"links"`
	}
	if err := json.Unmarshal(body, &t); err != nil {
		return fmt.Errorf("decode task: %w", err)
	}
	tw := newTable(w)
	tw.row("Task", t.Name)
	tw.row("ID", t.ID)
	tw.row("Version", t.LatestVersion.Seq)
	tw.row("Description", t.Description)
	files := make([]string, 0, len(t.LatestVersion.Files))
	for _, f := range t.LatestVersion.Files {
		files = append(files, f.Path)
	}
	tw.row("Files", strings.Join(files, ", "))
	tw.row("Console", t.Links.Console)
	return tw.flush()
}

// taskDir is a task read from a directory.
type taskDir struct {
	name     string
	manifest map[string]any
	files    []fileInput
}

// fileInput matches TaskFileInput in the contract.
type fileInput struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
}

func readTaskDir(dir, manifestPath string) (*taskDir, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("task directory: %w", err)
	}
	if !info.IsDir() {
		return nil, usageError(fmt.Errorf("%s is not a directory", dir))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("task directory: %w", err)
	}
	if manifestPath == "" {
		for _, n := range manifestFiles {
			p := filepath.Join(dir, n)
			if _, err := os.Stat(p); err == nil {
				manifestPath = p
				break
			}
		}
		if manifestPath == "" {
			return nil, usageError(fmt.Errorf("no %s in %s", manifestFiles[0], dir))
		}
	}
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return nil, err
	}
	t := &taskDir{name: filepath.Base(abs), manifest: manifest}
	if n, ok := manifest["name"]; ok {
		s, isString := n.(string)
		if !isString || s == "" {
			return nil, fmt.Errorf("%s: name must be a non-empty string", manifestPath)
		}
		t.name = s
		delete(manifest, "name")
	}
	manifestAbs, _ := filepath.Abs(manifestPath)

	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("task directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	err = fs.WalkDir(root.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == "." {
			return nil
		}
		base := d.Name()
		if strings.HasPrefix(base, ".") || (d.IsDir() && slices.Contains(skippedDirs, base)) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%s is a symbolic link; copy the file into the task directory instead", p)
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("%s is not a regular file", p)
		}
		if filepath.Join(abs, filepath.FromSlash(p)) == manifestAbs {
			return nil
		}
		data, err := root.ReadFile(p)
		if err != nil {
			return err
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("%s is not UTF-8 text; task files must be text", p)
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		t.files = append(t.files, fileInput{Path: p, Content: string(data), Executable: fi.Mode().Perm()&0o111 != 0})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read task directory: %w", err)
	}
	if len(t.files) == 0 {
		return nil, usageError(fmt.Errorf("%s has no task files besides the manifest", dir))
	}
	return t, nil
}

func readManifest(path string) (map[string]any, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- a path the user named
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var m map[string]any
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m == nil {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return m, nil
}

// resolveTask returns the task ID for an argument that is either an ID or
// a task name. Names are looked up with listTasks when the server has it.
func (s *session) resolveTask(cmd *cobra.Command, arg string) (string, error) {
	if _, err := uuid.Parse(arg); err == nil {
		return arg, nil
	}
	op, ok := s.reg.Operation("listTasks")
	if !ok {
		return "", usageError(fmt.Errorf("%q is not a task ID, and this server cannot look tasks up by name; use the id from lerryn tasks apply", arg))
	}
	args := map[string]any{}
	if op.HasArg("name") {
		// An exact-name filter: the first page has every match.
		args["name"] = arg
	}
	for range 100 { // pages; a bound so a misbehaving cursor cannot loop forever
		resp, err := s.Call(cmd.Context(), "listTasks", args, nil)
		if err != nil {
			return "", err
		}
		var page struct {
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
			NextCursor *string `json:"next_cursor"`
		}
		if err := resp.Decode(&page); err != nil {
			return "", err
		}
		var found []string
		for _, it := range page.Items {
			if it.Name == arg {
				found = append(found, it.ID)
			}
		}
		if len(found) == 1 {
			return found[0], nil
		}
		if len(found) > 1 {
			return "", usageError(fmt.Errorf("several tasks are named %q; pass the task ID", arg))
		}
		if page.NextCursor == nil || *page.NextCursor == "" || !op.HasArg("cursor") {
			break
		}
		args["cursor"] = *page.NextCursor
	}
	return "", &ExitError{Code: ExitAPI, Err: fmt.Errorf("no task named %q", arg)}
}
