package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/mcp"
)

// maxResponseBytes bounds what the CLI reads from one response. The largest
// pages (logs) are well below it.
const maxResponseBytes = 64 << 20

// Client calls the Lerryn API. It is driven by the contract: requests are
// built from the operation index the MCP tools use, so every operation in
// the embedded spec is callable without hand-written code, with the same
// argument names as the MCP tools.
type Client struct {
	server *url.URL
	token  string
	http   *http.Client
	reg    *mcp.Registry
}

// Response is an API answer with a 2xx status.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// JSON reports whether the body is JSON.
func (r *Response) JSON() bool { return isJSONType(r.Header.Get("Content-Type")) }

// Decode decodes a JSON body into v.
func (r *Response) Decode(v any) error {
	if err := json.Unmarshal(r.Body, v); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// APIError is an error answer from the API, normally a problem document.
type APIError struct {
	Status  int
	Problem map[string]any // nil when the body is not a problem
	Body    []byte
}

func (e *APIError) Error() string {
	if e.Problem == nil {
		return fmt.Sprintf("HTTP %d: %s", e.Status, strings.TrimSpace(string(e.Body)))
	}
	msg := fmt.Sprintf("%v", e.Problem["title"])
	if d, _ := e.Problem["detail"].(string); d != "" {
		msg += ": " + d
	}
	return fmt.Sprintf("%s (%v, HTTP %d)", msg, e.Problem["code"], e.Status)
}

// Code returns the problem code, or "" when the body is not a problem.
func (e *APIError) Code() string {
	s, _ := e.Problem["code"].(string)
	return s
}

func newClient(server, token string, hc *http.Client, reg *mcp.Registry) (*Client, error) {
	u, err := url.Parse(server)
	if err != nil {
		return nil, fmt.Errorf("server URL: %w", err)
	}
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{server: u, token: token, http: hc, reg: reg}, nil
}

// Operation returns the operation with the given operationId, or an error
// that says the embedded contract lacks it.
func (c *Client) Operation(id string) (*mcp.Operation, error) {
	o, ok := c.reg.Operation(id)
	if !ok {
		return nil, fmt.Errorf("this lerryn build has no %s operation; upgrade the CLI to match the server", id)
	}
	return o, nil
}

// Call runs operation id with args in the MCP tool input shape. header
// adds request headers, such as a session cookie during login.
func (c *Client) Call(ctx context.Context, id string, args map[string]any, header http.Header) (*Response, error) {
	o, err := c.Operation(id)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("encode arguments: %w", err)
	}
	req, err := o.NewRequest(ctx, raw)
	if err != nil {
		var ae *mcp.ArgError
		if errors.As(err, &ae) {
			return nil, usageError(fmt.Errorf("%s: %w", id, err))
		}
		return nil, err
	}
	for k, v := range header {
		req.Header[k] = v
	}
	return c.Do(req)
}

// Do sends a request whose URL holds only a path and query to the server,
// with the access token.
func (c *Client) Do(req *http.Request) (*Response, error) {
	req = req.Clone(req.Context())
	c.resolve(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", c.server.Redacted(), unwrapURLError(err))
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("response is larger than %d bytes", maxResponseBytes)
	}
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode < 200 || resp.StatusCode > 299 || mt == "application/problem+json" {
		e := &APIError{Status: resp.StatusCode, Body: body}
		var p map[string]any
		if isJSONType(mt) && json.Unmarshal(body, &p) == nil {
			e.Problem = p
		}
		code := ExitAPI
		if resp.StatusCode == http.StatusUnauthorized {
			code = ExitAuth
		}
		return nil, &ExitError{Code: code, Err: e}
	}
	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: body}, nil
}

// resolve points a server-relative request at the configured server and
// adds the credential. The server URL may carry a path prefix for a
// deployment behind a reverse proxy.
func (c *Client) resolve(req *http.Request) {
	u := *c.server
	u.Path = strings.TrimRight(c.server.Path, "/") + req.URL.Path
	u.RawPath = ""
	u.RawQuery = req.URL.RawQuery
	if req.URL.RawPath != "" {
		u.RawPath = strings.TrimRight(c.server.EscapedPath(), "/") + req.URL.RawPath
	}
	req.URL = &u
	req.Host = u.Host
	req.RequestURI = ""
	if c.token != "" && req.Header.Get("Authorization") == "" && req.Header.Get("Cookie") == "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("User-Agent", userAgent())
}

func userAgent() string { return "lerryn-cli/" + buildinfo.Version }

func isJSONType(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = contentType
	}
	return mt == "application/json" || strings.HasSuffix(mt, "+json")
}

// unwrapURLError drops the *url.Error wrapper, whose message repeats the
// method and URL.
func unwrapURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// prettyJSON indents a JSON document; other bodies are returned as they are.
func prettyJSON(body []byte) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, body, "", "  "); err != nil {
		return body
	}
	return buf.Bytes()
}
