package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// maxPasswordInput bounds what is read as a password; the service allows
// 256 characters.
const maxPasswordInput = 4 << 10

// newAdminCmd groups the account commands a maintainer runs on the server
// itself (tech design 12.1). They open the configured database directly,
// so they work before anyone can sign in and when every admin is locked
// out; access to the server host and its configuration is the
// authorisation.
func newAdminCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "admin",
		Short: "Create accounts and reset passwords directly in the database",
	}
	var (
		displayName, email string
		notAdmin, stdin    bool
	)
	create := &cobra.Command{
		Use:   "create <username>",
		Short: "Create a built-in admin account",
		Long: "Create a built-in account with its personal project, as an admin (or, with --no-admin,\n" +
			"as a regular user). Works with open registration closed. The password is prompted for\n" +
			"on a terminal, or read from the first line of standard input with --password-stdin.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, log, err := loadFromFlags(cmd, "admin")
			if err != nil {
				return err
			}
			password, err := readPassword(cmd, stdin)
			if err != nil {
				return err
			}
			return runAdminCreate(cmd.Context(), cfg, log, cmd.OutOrStdout(), service.AdminCreateUserInput{
				RegisterInput: service.RegisterInput{Username: args[0], Password: password, DisplayName: displayName, Email: email},
				IsAdmin:       !notAdmin,
			})
		},
	}
	create.Flags().StringVar(&displayName, "display-name", "", "display name")
	create.Flags().StringVar(&email, "email", "", "email address")
	create.Flags().BoolVar(&notAdmin, "no-admin", false, "create a regular user instead of an admin")
	create.Flags().BoolVar(&stdin, "password-stdin", false, "read the password from standard input")

	var resetStdin bool
	reset := &cobra.Command{
		Use:   "reset-password <username>",
		Short: "Set a new password for an account",
		Long: "Set a new password for an account and end its sessions and device sessions; access\n" +
			"tokens keep working. The password is prompted for on a terminal, or read from the first\n" +
			"line of standard input with --password-stdin.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, log, err := loadFromFlags(cmd, "admin")
			if err != nil {
				return err
			}
			password, err := readPassword(cmd, resetStdin)
			if err != nil {
				return err
			}
			return runAdminResetPassword(cmd.Context(), cfg, log, cmd.OutOrStdout(), args[0], password)
		},
	}
	reset.Flags().BoolVar(&resetStdin, "password-stdin", false, "read the password from standard input")
	cmd.AddCommand(create, reset)
	return cmd
}

// readPassword reads a password from the first line of standard input, or
// prompts for it twice without echo when standard input is a terminal.
func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	in := cmd.InOrStdin()
	if f, ok := in.(*os.File); ok && !fromStdin && term.IsTerminal(int(f.Fd())) { // #nosec G115 -- a file descriptor
		prompt := func(label string) (string, error) {
			_, _ = fmt.Fprint(cmd.ErrOrStderr(), label)
			b, err := term.ReadPassword(int(f.Fd())) // #nosec G115 -- a file descriptor
			_, _ = fmt.Fprintln(cmd.ErrOrStderr())
			return string(b), err
		}
		first, err := prompt("New password: ")
		if err != nil {
			return "", err
		}
		again, err := prompt("Repeat the password: ")
		if err != nil {
			return "", err
		}
		if first != again {
			return "", errors.New("the passwords do not match")
		}
		return first, nil
	}
	if !fromStdin {
		return "", errors.New("standard input is not a terminal; pass --password-stdin to read the password from it")
	}
	return firstLine(in)
}

// firstLine returns the first line of r without its line ending; the rest
// of the password is kept as typed, spaces included.
func firstLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(io.LimitReader(r, maxPasswordInput)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read the password: %w", err)
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("the password is empty")
	}
	return line, nil
}

// openAdmin opens the configured database, which must be fully migrated,
// and returns the admin service on it.
func openAdmin(ctx context.Context, cfg config.Config, log *slog.Logger) (*service.Admin, func(), error) {
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return nil, nil, err
	}
	if err := store.CheckMigrations(ctx, db); err != nil {
		_ = db.Close()
		return nil, nil, err
	}
	return service.NewAdmin(service.NewAccounts(db, log), log), func() { _ = db.Close() }, nil
}

func runAdminCreate(ctx context.Context, cfg config.Config, log *slog.Logger, out io.Writer, in service.AdminCreateUserInput) error {
	admin, done, err := openAdmin(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer done()
	u, err := admin.CreateUserLocally(ctx, in, service.ViaCLI)
	if err != nil {
		return describeAccountError(err)
	}
	log.InfoContext(ctx, "account created", "user_id", u.User.ID, "username", u.User.Username, "is_admin", u.User.IsAdmin)
	role := "user"
	if u.User.IsAdmin {
		role = "admin"
	}
	_, err = fmt.Fprintf(out, "created %s %s (%s)\n", role, u.User.Username, u.User.ID)
	return err
}

func runAdminResetPassword(ctx context.Context, cfg config.Config, log *slog.Logger, out io.Writer, username, password string) error {
	admin, done, err := openAdmin(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer done()
	u, err := admin.ResetPasswordLocally(ctx, username, password)
	if err != nil {
		return describeAccountError(err)
	}
	log.InfoContext(ctx, "password reset", "user_id", u.ID, "username", u.Username)
	msg := fmt.Sprintf("reset the password of %s; its sessions were ended\n", u.Username)
	if !u.DisabledAt.IsZero() {
		msg += "note: the account is disabled; an admin can restore it in the console\n"
	}
	_, err = io.WriteString(out, msg)
	return err
}

// describeAccountError turns service errors into messages for the
// command line.
func describeAccountError(err error) error {
	var verr *service.ValidationError
	switch {
	case errors.Is(err, service.ErrUsernameTaken):
		return errors.New("the username is taken")
	case errors.Is(err, service.ErrNotFound):
		return errors.New("no account has this username")
	case errors.As(err, &verr):
		var parts []string
		for _, f := range verr.Fields {
			parts = append(parts, f.Detail)
		}
		return fmt.Errorf("invalid input: %s", strings.Join(parts, "; "))
	}
	return err
}

// bootstrapAdmin creates the configured initial admin when the deployment
// has no account yet (auth.initial_admin). With accounts present it does
// nothing, so the setting can stay in place after the first start.
func bootstrapAdmin(ctx context.Context, cfg config.Config, admin *service.Admin, log *slog.Logger) error {
	ia := cfg.Auth.InitialAdmin
	if ia.Username == "" {
		return nil
	}
	f, err := os.Open(ia.PasswordFile) // #nosec G304 -- the operator chooses the path
	if err != nil {
		return fmt.Errorf("auth.initial_admin.password_file: %w", err)
	}
	password, err := firstLine(f)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("auth.initial_admin.password_file: %w", err)
	}
	u, err := admin.BootstrapAdmin(ctx, service.RegisterInput{Username: ia.Username, Password: password})
	switch {
	case errors.Is(err, service.ErrAccountsExist):
		log.DebugContext(ctx, "initial admin not created: the deployment has accounts")
		return nil
	case err != nil:
		return fmt.Errorf("create the initial admin: %w", describeAccountError(err))
	}
	log.InfoContext(ctx, "initial admin created", "user_id", u.User.ID, "username", u.User.Username)
	return nil
}
