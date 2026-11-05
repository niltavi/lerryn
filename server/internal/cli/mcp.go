package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/mcp"
)

func (a *app) mcpCmd() *cobra.Command {
	f := &flags{}
	var stdio bool
	cmd := &cobra.Command{
		Use:   "mcp --stdio",
		Short: "Serve the Lerryn MCP tools over stdio for clients without remote MCP",
		Long: `Serve the Lerryn MCP tools on standard input and output, for agent clients
that only start local MCP servers. The tools are the same as on the server's
/mcp endpoint, built from the contract embedded in this binary; each call is
sent to the server's HTTP API with the stored access token (or LERRYN_TOKEN).
Diagnostics go to standard error; standard output carries only the protocol.`,
		Example: `  claude mcp add lerryn -- lerryn mcp --stdio`,
		Args:    requireArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdio {
				return usageError(errors.New("only --stdio is supported; remote clients connect to the server's /mcp endpoint directly"))
			}
			f.output = outputJSON // never print text to the protocol stream
			s, err := a.connect(cmd, f, true)
			if err != nil {
				return err
			}
			log := slog.New(slog.NewJSONHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn}))
			return serveMCP(cmd.Context(), s.Client, &mcpsdk.IOTransport{
				Reader: io.NopCloser(cmd.InOrStdin()),
				Writer: nopWriteCloser{cmd.OutOrStdout()},
			}, log)
		},
	}
	cmd.Flags().BoolVar(&stdio, "stdio", false, "serve on standard input and output")
	cmd.PersistentFlags().StringVar(&f.server, "server", "", "server URL (env "+EnvServer+"; default from lerryn login)")
	return cmd
}

// serveMCP serves the registry's tools on t until the client disconnects or
// ctx ends. Tool calls go through the same mcp.NewServer code as /mcp; only
// the API handler differs, forwarding over HTTP instead of in-process.
func serveMCP(ctx context.Context, c *Client, t mcpsdk.Transport, log *slog.Logger) error {
	srv := mcp.NewServer(c.reg, remoteAPI{c: c}, mcp.Options{Logger: log})
	err := srv.Run(ctx, t)
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// remoteAPI is an http.Handler that sends the in-process requests built by
// the MCP tools to the remote server. The access token is the CLI's own;
// nothing the MCP client sends is forwarded, since stdio carries no headers.
type remoteAPI struct{ c *Client }

// copiedHeaders are the response headers the tool result conversion reads.
var copiedHeaders = []string{"Content-Type", "X-Request-Id", "Retry-After", "Etag"}

func (r remoteAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if err := r.ServeHTTPError(w, req); err != nil {
		// A response may already have been written. Abort it rather than
		// presenting an incomplete body as a completed HTTP response.
		panic(http.ErrAbortHandler)
	}
}

// ServeHTTPError lets the MCP forwarder discard a partial response when
// reading it fails, without buffering the API response a second time.
func (r remoteAPI) ServeHTTPError(w http.ResponseWriter, req *http.Request) error {
	out := req.Clone(req.Context())
	out.Header.Del("Authorization")
	out.Header.Del("Cookie")
	r.c.resolve(out)
	resp, err := r.c.http.Do(out) // #nosec G704 -- the server is the one the user logged in to
	if err != nil {
		if req.Context().Err() != nil {
			return req.Context().Err()
		}
		// Same problem shape as the API, so the agent sees a code and a
		// next step instead of a transport error.
		writeProblem(w, http.StatusBadGateway, "无法连接 Lerryn 服务端", "请求服务端失败："+unwrapURLError(err).Error())
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	for _, h := range copiedHeaders {
		if v := resp.Header.Values(h); len(v) > 0 {
			w.Header()[h] = v
		}
	}
	w.WriteHeader(resp.StatusCode)
	// The MCP recorder keeps only what fits in a tool result and counts the
	// rest, so copying the whole body does not hold it in memory.
	if _, err := io.Copy(w, resp.Body); err != nil {
		return fmt.Errorf("read API response: %w", unwrapURLError(err))
	}
	return req.Context().Err()
}

func writeProblem(w http.ResponseWriter, status int, title, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":       "urn:lerryn:error:internal_error",
		"title":      title,
		"status":     status,
		"code":       "internal_error",
		"detail":     detail,
		"request_id": "req_" + rand.Text(),
		"next_actions": []map[string]string{
			{"action": "discover", "hint": "确认服务端地址可以访问后重试"},
		},
	})
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
