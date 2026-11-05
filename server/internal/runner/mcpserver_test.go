package runner

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// mcpTestToken is the bearer token the test MCP server requires.
const mcpTestToken = "calc-token-7Hq2" // #nosec G101 -- a test token

// startCalcServer serves a small MCP server for sbx mcp call: "add"
// returns a+b as text and structured content, "fail" returns an error
// result. Every request needs Authorization: Bearer mcpTestToken. With
// jsonResponse the server answers in JSON, otherwise as SSE. It listens on
// addr ("127.0.0.1:0" for host tests, ":0" for containers) and returns its
// port.
func startCalcServer(t *testing.T, addr string, jsonResponse bool) int {
	t.Helper()
	srv := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "calc", Version: "1"}, nil)
	type addIn struct {
		A int `json:"a"`
		B int `json:"b"`
		// Token lets tests send a credential in the arguments.
		Token string `json:"token,omitempty"`
	}
	type addOut struct {
		Sum int `json:"sum"`
	}
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "add", Description: "a + b"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, in addIn) (*mcpsdk.CallToolResult, addOut, error) {
		return &mcpsdk.CallToolResult{Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: strconv.Itoa(in.A + in.B)}}}, addOut{Sum: in.A + in.B}, nil
	})
	mcpsdk.AddTool(srv, &mcpsdk.Tool{Name: "fail", Description: "always fails"}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ struct{}) (*mcpsdk.CallToolResult, any, error) {
		return &mcpsdk.CallToolResult{IsError: true, Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: "quota \"exceeded\" for today"}}}, nil, nil
	})
	h := mcpsdk.NewStreamableHTTPHandler(func(*http.Request) *mcpsdk.Server { return srv }, &mcpsdk.StreamableHTTPOptions{
		JSONResponse: jsonResponse, DisableLocalhostProtection: true,
	})
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+mcpTestToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})}
	go func() { _ = hs.Serve(l) }()
	t.Cleanup(func() { _ = hs.Close() })
	return l.Addr().(*net.TCPAddr).Port
}

// calcTools is LERRYN_TOOLS for a run with the calc server as "calc", an
// HTTP tool "prices" and an unreachable MCP tool "gone".
func calcTools(host string, port int) []runproto.Tool {
	return []runproto.Tool{
		{Name: "calc", Kind: runproto.ToolMCP, URL: fmt.Sprintf("http://%s:%d/mcp?x=1&y=2", host, port)},
		{Name: "prices", Kind: runproto.ToolHTTP, URL: "http://prices.invalid/"},
		{Name: "gone", Kind: runproto.ToolMCP, URL: "http://127.0.0.1:1/mcp"},
	}
}

// describeToolCalls summarises the tool.call events.
func describeToolCalls(evs []runproto.Event) []string {
	var out []string
	for _, e := range evs {
		if c := e.ToolCall; c != nil {
			s := fmt.Sprintf("%s %s ok=%v args=%s", c.Tool, c.Method, c.OK, c.ArgsSummary)
			if c.Error != "" {
				s += " error=" + c.Error
			}
			out = append(out, s)
		}
	}
	return out
}

// wantToolCalls checks the calls recorded by the sbx mcp call sequence the
// SDK tests run, in order.
func wantToolCalls(t *testing.T, got []string) {
	t.Helper()
	want := []string{
		`calc add ok=true args={"a":2,"b":3}`,
		`calc fail ok=false args={} error=quota "exceeded" for today`,
		"nope",
		"prices",
		"gone",
		"calc add ok=false args={\"a\":1} error=HTTP 401",
	}
	if len(got) != len(want) {
		t.Fatalf("tool calls = %q\nwant %d like %q", got, len(want), want)
	}
	for i, w := range want {
		switch w {
		case "nope":
			w = "nope x ok=false args={} error=tool nope is not one of this run's tools"
		case "prices":
			w = "prices x ok=false args={} error=tool prices is "
		case "gone":
			w = "gone x ok=false args={} error="
		}
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("tool call %d = %q, want prefix %q", i, got[i], w)
		}
	}
	if !strings.Contains(got[4], "reach") {
		t.Errorf("unreachable tool: %q", got[4])
	}
}
