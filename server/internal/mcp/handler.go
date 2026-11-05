package mcp

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/logging"
)

// Options configures the MCP endpoint.
type Options struct {
	// MaxResultBytes caps the API response body returned per tool call.
	// Zero means DefaultMaxResultBytes.
	MaxResultBytes int

	// JSONResponse answers each POST with a single application/json body
	// instead of an SSE stream. Simpler for proxies, but rules out progress
	// notifications during a call.
	JSONResponse bool

	// DisableLocalhostProtection turns off the SDK's DNS rebinding guard,
	// which rejects requests that arrive on a loopback address with a
	// non-loopback Host header. A reverse proxy on the same host that
	// forwards to 127.0.0.1 and keeps the public Host trips that guard, so
	// deployments behind such a proxy must set this. Every tool call still
	// needs an access token, which is the actual defence.
	DisableLocalhostProtection bool

	Logger *slog.Logger
}

// NewServer returns an MCP server whose tools forward to api in-process.
func NewServer(reg *Registry, api http.Handler, opts Options) *mcpsdk.Server {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "lerryn", Version: buildinfo.Version}, &mcpsdk.ServerOptions{
		Logger: log,
	})
	for _, t := range reg.Tools() {
		srv.AddTool(&mcpsdk.Tool{
			Name:        t.Name,
			Title:       t.Title,
			Description: t.Description,
			InputSchema: t.InputSchema,
			Annotations: t.Annotations,
		}, toolHandler(t, api, opts.MaxResultBytes, log))
	}
	srv.AddTool(describeToolDef, describeHandler(reg))
	srv.AddTool(callToolDef, callHandler(reg, api, opts.MaxResultBytes, log))
	// The embedded skills are checked by their own tests; a broken one
	// costs the resources, not the tools.
	if err := addSkillResources(srv); err != nil {
		log.Error("register skill resources", "error", err)
	}
	return srv
}

// NewHandler returns the /mcp endpoint: Streamable HTTP in stateless mode,
// so any server replica can answer any request and the load balancer needs
// no session affinity.
func NewHandler(reg *Registry, api http.Handler, opts Options) http.Handler {
	srv := NewServer(reg, api, opts)
	return mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, &mcpsdk.StreamableHTTPOptions{
		Stateless:                    true,
		JSONResponse:                 opts.JSONResponse,
		Logger:                       opts.Logger,
		DisableLocalhostProtection:   opts.DisableLocalhostProtection,
		PropagateRequestCancellation: true,
	})
}

func toolHandler(t *Tool, api http.Handler, maxBytes int, log *slog.Logger) mcpsdk.ToolHandler {
	return func(ctx context.Context, req *mcpsdk.CallToolRequest) (*mcpsdk.CallToolResult, error) {
		var inbound http.Header
		if req.Extra != nil {
			inbound = req.Extra.Header
		}
		start := time.Now()
		res, err := t.call(ctx, api, req.Params.Arguments, inbound, maxBytes)
		if err != nil {
			logCallFailure(ctx, log, err, "tool", t.Name)
			return nil, err
		}
		log.InfoContext(ctx, "mcp tool call",
			"tool", t.Name,
			"operation_id", t.OperationID,
			"http_status", res.Meta[metaHTTPStatus],
			"is_error", res.IsError,
			"truncated", res.Meta[metaTruncated] == true,
			"duration", time.Since(start))
		return res, nil
	}
}

// logCallFailure logs a tool call that failed: as an error, or at debug
// level when the client disconnected and the call was abandoned with it.
func logCallFailure(ctx context.Context, log *slog.Logger, err error, attrs ...any) {
	level, msg := slog.LevelError, "mcp tool call failed"
	if logging.ClientGone(ctx, err) {
		level, msg = slog.LevelDebug, "mcp tool call abandoned by the client"
	}
	log.Log(ctx, level, msg, append(attrs, "error", err)...)
}
