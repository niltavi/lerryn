package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// sessionCookieName is the console session cookie that login returns. The
// CLI holds it only long enough to create an access token.
const sessionCookieName = "lerryn_session"

type loginOptions struct {
	flags
	username      string
	passwordStdin bool
	token         string
	tokenStdin    bool
	tokenName     string
	scopes        []string
	expiresInDays int
}

func (a *app) loginCmd() *cobra.Command {
	o := &loginOptions{}
	cmd := &cobra.Command{
		Use:   "login <server-url>",
		Short: "Log in to a Lerryn server and store an access token",
		Long: `Log in to a Lerryn server and store an access token in the CLI config file
(mode 0600). With a built-in account, give the username and password: the CLI
signs in, creates an access token named after this machine, and signs the
console session out again. With --token or --token-stdin, it stores an access
token created in the console instead.

The password is read from the terminal without echo, or from the first line
of standard input with --password-stdin.`,
		Example: `  lerryn login https://lerryn.example.com --username alice
  echo "$LERRYN_PASSWORD" | lerryn login https://lerryn.example.com --username alice --password-stdin
  lerryn login https://lerryn.example.com --token-stdin < token.txt`,
		Args: requireArgs(1, "<server-url>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			o.server = args[0]
			return a.login(cmd, o)
		},
	}
	f := cmd.Flags()
	f.StringVarP(&o.username, "username", "u", "", "built-in account username (prompted when omitted on a terminal)")
	f.BoolVar(&o.passwordStdin, "password-stdin", false, "read the password from the first line of standard input")
	f.StringVar(&o.token, "token", "", "store this access token instead of signing in (visible in shell history; prefer --token-stdin)")
	f.BoolVar(&o.tokenStdin, "token-stdin", false, "read an access token from standard input instead of signing in")
	f.StringVar(&o.tokenName, "token-name", "", "name of the token created by a password login (default: lerryn-cli on <hostname>)")
	f.StringSliceVar(&o.scopes, "scopes", []string{"read", "write", "execute"}, "scopes of the token created by a password login")
	f.IntVar(&o.expiresInDays, "expires-in-days", 0, "lifetime of the token created by a password login, 1 to 3650 days (default: no expiry)")
	cmd.Flags().StringVarP(&o.output, "output", "o", outputAuto, "output format: json, text, or auto")
	return cmd
}

func (a *app) login(cmd *cobra.Command, o *loginOptions) error {
	server, err := normalizeServer(o.server)
	if err != nil {
		return usageError(err)
	}
	if o.token != "" && o.tokenStdin {
		return usageError(errors.New("use either --token or --token-stdin"))
	}
	byToken := o.token != "" || o.tokenStdin
	if byToken && (o.username != "" || o.passwordStdin) {
		return usageError(errors.New("--username and --password-stdin sign in with a password; they cannot be combined with a token"))
	}
	// Ignore LERRYN_TOKEN and the stored token: login establishes them.
	o.server = server
	s, err := a.connect(cmd, &o.flags, false)
	if err != nil {
		return err
	}
	s.token = ""
	warnPlainHTTP(s.errw, server)

	var cfg Config
	if byToken {
		cfg, err = a.loginWithToken(cmd, s, o)
	} else {
		cfg, err = a.loginWithPassword(cmd, s, o)
	}
	if err != nil {
		return s.report(err)
	}
	cfg.Server = server
	if err := saveConfig(s.configPath, cfg); err != nil {
		return err
	}
	return s.emitValue(map[string]any{"server": server, "username": cfg.Username, "config": s.configPath}, func(w io.Writer) error {
		_, err := fmt.Fprintf(w, "Logged in to %s as %s.\n", server, cfg.Username)
		return err
	})
}

func (a *app) loginWithToken(cmd *cobra.Command, s *session, o *loginOptions) (Config, error) {
	token := o.token
	if o.tokenStdin {
		line, err := readLine(cmd.InOrStdin())
		if err != nil {
			return Config{}, fmt.Errorf("read token: %w", err)
		}
		token = line
	}
	token = strings.TrimSpace(token)
	if token == "" || strings.ContainsAny(token, " \t\r\n") {
		return Config{}, usageError(errors.New("the access token is empty or malformed"))
	}
	s.token = token
	p, err := principal(cmd.Context(), s)
	if err != nil {
		return Config{}, err
	}
	if p == nil {
		return Config{}, authError(errors.New("the server did not accept the access token"))
	}
	return Config{Token: token, Username: p.Username}, nil
}

func (a *app) loginWithPassword(cmd *cobra.Command, s *session, o *loginOptions) (Config, error) {
	ctx := cmd.Context()
	in := cmd.InOrStdin()
	interactive := a.opts.IsTerminal(in)
	username := strings.TrimSpace(o.username)
	if username == "" {
		if !interactive || o.passwordStdin {
			return Config{}, usageError(errors.New("give --username, or --token-stdin to store an existing access token"))
		}
		_, _ = fmt.Fprint(s.errw, "Username: ")
		line, err := readLine(in)
		if err != nil {
			return Config{}, fmt.Errorf("read username: %w", err)
		}
		username = strings.TrimSpace(line)
	}
	var password string
	switch {
	case o.passwordStdin:
		line, err := readLine(in)
		if err != nil {
			return Config{}, fmt.Errorf("read password: %w", err)
		}
		password = line
	case interactive:
		_, _ = fmt.Fprint(s.errw, "Password: ")
		p, err := readPassword(in)
		_, _ = fmt.Fprintln(s.errw)
		if err != nil {
			return Config{}, fmt.Errorf("read password: %w", err)
		}
		password = p
	default:
		return Config{}, usageError(errors.New("standard input is not a terminal; pass the password with --password-stdin"))
	}

	// Writes made with the session are checked against the deployment's
	// public origin, which discover reports. It can differ from the URL the
	// CLI uses, for example behind an internal load balancer.
	origin, err := publicOrigin(ctx, s)
	if err != nil {
		return Config{}, err
	}
	resp, err := s.loginRequest(ctx, username, password)
	if err != nil {
		return Config{}, err
	}
	cookie := sessionCookie(resp)
	if cookie == "" {
		return Config{}, errors.New("the server did not return a session")
	}
	sessionHeader := http.Header{"Cookie": {sessionCookieName + "=" + cookie}, "Origin": {origin}}
	defer func() {
		// The session is only a means to the token; end it so it does not
		// linger as a second credential.
		_, _ = s.Call(context.WithoutCancel(ctx), "logout", nil, sessionHeader)
	}()

	name := o.tokenName
	if name == "" {
		host, _ := os.Hostname()
		name = "lerryn-cli"
		if host != "" {
			name += " on " + host
		}
	}
	args := map[string]any{"name": name, "scopes": o.scopes}
	if o.expiresInDays > 0 {
		args["expires_in_days"] = o.expiresInDays
	}
	created, err := s.Call(ctx, "createToken", args, sessionHeader)
	if err != nil {
		return Config{}, err
	}
	var tok struct {
		Token       string `json:"token"`
		AccessToken struct {
			ID string `json:"id"`
		} `json:"access_token"`
	}
	if err := created.Decode(&tok); err != nil {
		return Config{}, err
	}
	if tok.Token == "" {
		return Config{}, errors.New("the server did not return an access token")
	}
	var me struct {
		Username string `json:"username"`
	}
	_ = resp.Decode(&me)
	if me.Username == "" {
		me.Username = username
	}
	return Config{Token: tok.Token, TokenID: tok.AccessToken.ID, Username: me.Username}, nil
}

func (s *session) loginRequest(ctx context.Context, username, password string) (*Response, error) {
	return s.Call(ctx, "login", map[string]any{"username": username, "password": password}, nil)
}

func sessionCookie(r *Response) string {
	for _, c := range (&http.Response{Header: r.Header}).Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

type principalInfo struct {
	Username          string   `json:"username"`
	Scopes            []string `json:"scopes"`
	AuthMethod        string   `json:"auth_method"`
	PersonalProjectID string   `json:"personal_project_id"`
}

type discovery struct {
	Principal *principalInfo `json:"principal"`
	Links     struct {
		Console string `json:"console"`
	} `json:"links"`
}

func discover(ctx context.Context, s *session) (*discovery, error) {
	resp, err := s.Call(ctx, "discover", nil, nil)
	if err != nil {
		return nil, err
	}
	var d discovery
	if err := resp.Decode(&d); err != nil {
		return nil, err
	}
	return &d, nil
}

func principal(ctx context.Context, s *session) (*principalInfo, error) {
	d, err := discover(ctx, s)
	if err != nil {
		return nil, err
	}
	return d.Principal, nil
}

// publicOrigin returns the origin of the console link discover reports, or
// the server URL's origin when there is none.
func publicOrigin(ctx context.Context, s *session) (string, error) {
	d, err := discover(ctx, s)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(d.Links.Console)
	if err != nil || u.Host == "" {
		u = s.server
	}
	return u.Scheme + "://" + u.Host, nil
}

// warnPlainHTTP warns when a credential would cross the network unencrypted.
func warnPlainHTTP(w io.Writer, server string) {
	u, err := url.Parse(server)
	if err != nil || u.Scheme != "http" {
		return
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return
	}
	_, _ = fmt.Fprintf(w, "warning: %s uses plain http; the password and access token are sent unencrypted\n", server)
}

func readLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" && errors.Is(err, io.EOF) {
		return "", errors.New("standard input is empty")
	}
	return line, nil
}

func readPassword(r io.Reader) (string, error) {
	f, ok := r.(interface{ Fd() uintptr })
	if !ok {
		return readLine(r)
	}
	b, err := term.ReadPassword(int(f.Fd())) // #nosec G115 -- file descriptors fit in an int
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (a *app) logoutCmd() *cobra.Command {
	f := &flags{}
	var keep bool
	cmd := &cobra.Command{
		Use:   "logout",
		Short: "Remove the stored access token",
		Long: `Remove the stored access token from the CLI config file. A token that
lerryn login created is revoked on the server first, unless --keep-token is
given; a token stored with --token is left valid, since it may be in use
elsewhere.`,
		Args: requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.connect(cmd, f, false)
			if err != nil {
				return err
			}
			cfg := s.cfg
			if cfg.Token == "" {
				return s.emitValue(map[string]any{"logged_out": false}, func(w io.Writer) error {
					_, err := fmt.Fprintln(w, "Not logged in.")
					return err
				})
			}
			revoked := false
			if cfg.TokenID != "" && !keep {
				// Revoke with the stored token, against the server it belongs to.
				c, err := newClient(cfg.Server, cfg.Token, a.opts.HTTPClient, s.reg)
				if err != nil {
					return err
				}
				_, err = c.Call(cmd.Context(), "revokeToken", map[string]any{"token_id": cfg.TokenID}, nil)
				var ae *APIError
				switch {
				case err == nil:
					revoked = true
				case errors.As(err, &ae) && (ae.Status == http.StatusUnauthorized || ae.Status == http.StatusNotFound):
					revoked = true // already revoked or expired
				default:
					_, _ = fmt.Fprintf(s.errw, "warning: could not revoke the token on the server: %v\n", err)
				}
			}
			if err := os.Remove(s.configPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove config: %w", err)
			}
			return s.emitValue(map[string]any{"logged_out": true, "server": cfg.Server, "token_revoked": revoked}, func(w io.Writer) error {
				_, err := fmt.Fprintf(w, "Logged out of %s.\n", cfg.Server)
				return err
			})
		},
	}
	addFlags(cmd, f)
	cmd.Flags().BoolVar(&keep, "keep-token", false, "do not revoke the token on the server")
	return cmd
}

func (a *app) discoverCmd() *cobra.Command {
	f := &flags{}
	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Show the server's version, capabilities, runtimes and the current identity",
		Args:  requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := a.connect(cmd, f, false)
			if err != nil {
				return err
			}
			resp, err := s.Call(cmd.Context(), "discover", nil, nil)
			if err != nil {
				return s.report(err)
			}
			return s.emit(resp.Body, func(w io.Writer) error { return printDiscovery(w, resp) })
		},
	}
	addFlags(cmd, f)
	return cmd
}

func printDiscovery(w io.Writer, resp *Response) error {
	var d struct {
		Server struct {
			Version string `json:"version"`
		} `json:"server"`
		API struct {
			Version     string `json:"version"`
			SpecVersion string `json:"spec_version"`
		} `json:"api"`
		Runtimes []struct {
			Language string `json:"language"`
			Version  string `json:"version"`
		} `json:"runtimes"`
		Principal *principalInfo `json:"principal"`
		Links     struct {
			Console string `json:"console"`
		} `json:"links"`
	}
	if err := resp.Decode(&d); err != nil {
		return err
	}
	tw := newTable(w)
	tw.row("Server", d.Server.Version)
	tw.row("API", strings.TrimSpace(d.API.Version+" "+d.API.SpecVersion))
	var rts []string
	for _, r := range d.Runtimes {
		rts = append(rts, r.Language+" "+r.Version)
	}
	tw.row("Runtimes", strings.Join(rts, ", "))
	if p := d.Principal; p != nil {
		tw.row("User", p.Username)
		tw.row("Scopes", strings.Join(p.Scopes, ", "))
		tw.row("Project", p.PersonalProjectID)
	} else {
		tw.row("User", "(anonymous)")
	}
	tw.row("Console", d.Links.Console)
	return tw.flush()
}
