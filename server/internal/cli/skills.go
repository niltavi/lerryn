package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/skills"
)

// Skill install targets: where each agent client looks for skills.
const targetClaudeCode = "claude-code"

func (a *app) skillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "List or install the Lerryn skills for agent clients",
		Long: `The skills teach an agent client how to build, test, activate and debug Lerryn
tasks, handle credentials and operate a deployment. They are embedded in this
binary and match its API contract.`,
		Args: requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.skillsListCmd(), a.skillsInstallCmd())
	return cmd
}

func (a *app) skillsListCmd() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the embedded skills",
		Args:  requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, err := a.outputMode(cmd, f)
			if err != nil {
				return err
			}
			list, err := skills.List()
			if err != nil {
				return err
			}
			if mode == outputJSON {
				type item struct {
					Name        string   `json:"name"`
					Description string   `json:"description"`
					Files       []string `json:"files"`
				}
				out := struct {
					Version string `json:"version"`
					Items   []item `json:"items"`
				}{Version: buildinfo.Version, Items: []item{}}
				for _, s := range list {
					out.Items = append(out.Items, item{s.Name, s.Description, s.Files})
				}
				return writeJSONValue(cmd.OutOrStdout(), out)
			}
			w := cmd.OutOrStdout()
			for _, s := range list {
				if _, err := fmt.Fprintf(w, "%s\n  %s\n", s.Name, s.Description); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&f.output, "output", "o", outputAuto, "output format: json, text, or auto")
	return cmd
}

func (a *app) skillsInstallCmd() *cobra.Command {
	f := &flags{}
	var (
		dir, target string
		project     bool
		force       bool
		names       []string
	)
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the skills into an agent client's skills directory",
		Long: `Write the embedded skills into a skills directory, one subdirectory per skill.

By default they go to Claude Code's user skills (~/.claude/skills); --project
uses .claude/skills under the current directory, and --dir any directory, for
clients that read Agent Skills from elsewhere. Reinstalling replaces the
skills this command wrote before, including files dropped from a skill, and
leaves every other entry alone. A same-named directory this command did not
write is kept unless --force is given.`,
		Example: `  lerryn skills install
  lerryn skills install --project
  lerryn skills install --dir ~/agents/skills --skill lerryn-automation`,
		Args: requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			mode, err := a.outputMode(cmd, f)
			if err != nil {
				return err
			}
			if target != targetClaudeCode {
				return usageError(fmt.Errorf("--target %q: only %s is known; use --dir for other clients", target, targetClaudeCode))
			}
			if dir != "" && project {
				return usageError(errors.New("--dir and --project cannot be combined"))
			}
			if dir == "" {
				if dir, err = a.claudeSkillsDir(project); err != nil {
					return err
				}
			}
			installed, err := skills.Install(dir, skills.InstallOptions{Names: names, Version: buildinfo.Version, Force: force})
			if errors.Is(err, skills.ErrNotManaged) {
				return &ExitError{Code: ExitFailure, Err: err}
			}
			if err != nil {
				return err
			}
			if mode == outputJSON {
				return writeJSONValue(cmd.OutOrStdout(), struct {
					Version   string             `json:"version"`
					Directory string             `json:"directory"`
					Installed []skills.Installed `json:"installed"`
				}{buildinfo.Version, dir, installed})
			}
			w := cmd.OutOrStdout()
			for _, in := range installed {
				verb := "installed"
				if in.Replaced {
					verb = "updated"
				}
				if _, err := fmt.Fprintf(w, "%s %s -> %s\n", verb, in.Name, in.Path); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "skills directory to install into")
	cmd.Flags().StringVar(&target, "target", targetClaudeCode, "agent client whose skills directory to use")
	cmd.Flags().BoolVar(&project, "project", false, "install into .claude/skills under the current directory")
	cmd.Flags().BoolVar(&force, "force", false, "replace same-named directories that lerryn did not install")
	cmd.Flags().StringSliceVar(&names, "skill", nil, "install only these skills (repeatable)")
	cmd.Flags().StringVarP(&f.output, "output", "o", outputAuto, "output format: json, text, or auto")
	return cmd
}

// claudeSkillsDir is Claude Code's user or project skills directory.
func (a *app) claudeSkillsDir(project bool) (string, error) {
	if project {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("find the current directory: %w", err)
		}
		return filepath.Join(wd, ".claude", "skills"), nil
	}
	home := a.opts.Getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", fmt.Errorf("find the home directory: %w", err)
		}
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

func writeJSONValue(w io.Writer, v any) error {
	return (&session{out: w}).emitValue(v, nil)
}
