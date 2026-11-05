package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// DefaultMaxResultBytes caps the API response body that goes back to the
// agent. Agent clients put tool output straight into the model context and
// cut or reject large outputs (Claude Code warns at about 10k tokens), so the
// cap stays well below that. Paged API operations should size pages to fit.
const DefaultMaxResultBytes = 32 << 10

// Meta keys added to tool results.
const (
	metaHTTPStatus = "lerryn/http_status"
	metaTruncated  = "lerryn/truncated"
	metaTotalBytes = "lerryn/total_bytes"
	// metaOperationID names the operation an api_call reached.
	metaOperationID = "lerryn/operation_id"
)

// forwardedHeaders are copied from the MCP HTTP request to the in-process API
// request. Only the credential is forwarded; everything else the API needs
// comes from tool arguments.
var forwardedHeaders = []string{"Authorization"}

// ToolCall describes the MCP tool call behind an in-process API request.
type ToolCall struct {
	Tool        string
	OperationID string
}

type toolCallKey struct{}

// ToolCallFromContext reports whether an API request was made by an MCP tool
// call, and which one. The value can only be set in-process, so unlike a
// request header it cannot be forged by a client; audit uses it to record the
// entry point.
func ToolCallFromContext(ctx context.Context) (ToolCall, bool) {
	c, ok := ctx.Value(toolCallKey{}).(ToolCall)
	return c, ok
}

// ArgError is a problem with the tool arguments themselves, found before any
// API request is made.
type ArgError struct{ msg string }

func (e *ArgError) Error() string { return e.msg }

func argErrorf(format string, a ...any) error { return &ArgError{msg: fmt.Sprintf(format, a...)} }

// newRequest maps tool arguments onto an HTTP request for the tool's
// operation. It does only the structural mapping; validating values is left
// to the API so that MCP and REST report the same validation errors.
func (t *Operation) newRequest(ctx context.Context, rawArgs json.RawMessage, inbound http.Header) (*http.Request, error) {
	args := map[string]json.RawMessage{}
	if len(bytes.TrimSpace(rawArgs)) > 0 && !bytes.Equal(bytes.TrimSpace(rawArgs), []byte("null")) {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return nil, argErrorf("arguments must be a JSON object: %v", err)
		}
	}

	path := t.Path
	query := url.Values{}
	header := http.Header{}
	bodyFields := map[string]json.RawMessage{}
	var body []byte

	for _, name := range sortedKeys(args) {
		raw := args[name]
		b, ok := t.args[name]
		if !ok {
			return nil, argErrorf("unknown argument %q", name)
		}
		if isNull(raw) && b.in != inBody {
			continue
		}
		switch b.in {
		case inPath:
			s, err := scalarString(raw)
			if err != nil {
				return nil, argErrorf("argument %q: %v", name, err)
			}
			// PathEscape keeps dots, and the router would resolve a "." or
			// ".." segment to a different route.
			if s == "" || s == "." || s == ".." {
				return nil, argErrorf("argument %q: %q is not a valid path segment", name, s)
			}
			path = strings.Replace(path, "{"+b.name+"}", url.PathEscape(s), 1)
		case inQuery:
			values, err := queryValues(raw)
			if err != nil {
				return nil, argErrorf("argument %q: %v", name, err)
			}
			query[b.name] = values
		case inHeader:
			s, err := scalarString(raw)
			if err != nil {
				return nil, argErrorf("argument %q: %v", name, err)
			}
			if strings.ContainsAny(s, "\r\n") {
				return nil, argErrorf("argument %q: line breaks are not allowed in header values", name)
			}
			header.Set(b.name, s)
		case inBody:
			if t.bodyNested {
				body = raw
			} else {
				bodyFields[b.name] = raw
			}
		}
	}
	for name, b := range t.args {
		if b.in == inPath && strings.Contains(path, "{"+b.name+"}") {
			return nil, argErrorf("missing required argument %q", name)
		}
	}
	if !t.bodyNested && t.hasBody() {
		var err error
		if body, err = json.Marshal(bodyFields); err != nil {
			return nil, fmt.Errorf("encode body: %w", err)
		}
	}

	target := path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, t.Method, target, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	// Make the request look like one net/http hands to a server handler,
	// which handlers are entitled to rely on: a non-nil Body and a set
	// RequestURI.
	req.RequestURI = req.URL.RequestURI()
	if req.Body == nil {
		req.Body = http.NoBody
	}
	for k, v := range header {
		req.Header[k] = v
	}
	for _, h := range forwardedHeaders {
		if v := inbound.Values(h); len(v) > 0 {
			req.Header[h] = v
		}
	}
	req.Header.Set("Accept", "application/json, application/problem+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

func (t *Operation) hasBody() bool {
	for _, b := range t.args {
		if b.in == inBody {
			return true
		}
	}
	return false
}

func isNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// scalarString renders a JSON string, number or boolean the way it appears
// in a URL or header.
func scalarString(raw json.RawMessage) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	switch x := v.(type) {
	case string:
		return x, nil
	case json.Number:
		return x.String(), nil
	case bool:
		return strconv.FormatBool(x), nil
	default:
		return "", errors.New("want a string, number or boolean")
	}
}

// queryValues supports scalars and arrays of scalars, the latter encoded as
// repeated keys (OpenAPI form style with explode, the default).
func queryValues(raw json.RawMessage) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		s, err := scalarString(raw)
		if err != nil {
			return nil, err
		}
		return []string{s}, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		s, err := scalarString(it)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// NewRequest maps arguments, given as a JSON object in the tool input
// shape, onto a request for the operation. The request URL holds only the
// path and query; a client sets the scheme and host. A problem with the
// arguments themselves is returned as an *ArgError.
func (o *Operation) NewRequest(ctx context.Context, args json.RawMessage) (*http.Request, error) {
	if o.Unsupported != "" {
		return nil, argErrorf("operation %s cannot be called with JSON arguments: %s", o.OperationID, o.Unsupported)
	}
	return o.newRequest(ctx, args, nil)
}

// call runs the tool against api and converts the response.
func (t *Tool) call(ctx context.Context, api http.Handler, args json.RawMessage, inbound http.Header, maxBytes int) (*mcpsdk.CallToolResult, error) {
	return t.Operation.call(ctx, api, t.Name, args, inbound, maxBytes, nextAction{action: t.Name, hint: "按工具的输入 schema 修正参数后重新调用"})
}

// nextAction is the next_actions entry of an argument error.
type nextAction struct{ action, hint string }

// errorHandler is implemented by an API proxy that can fail while reading
// a response. A normal ResponseWriter cannot retract headers or a partial
// body, so the proxy reports the failure before a tool result is made.
type errorHandler interface {
	ServeHTTPError(http.ResponseWriter, *http.Request) error
}

// call runs the operation against api on behalf of the named tool and
// converts the response.
func (o *Operation) call(ctx context.Context, api http.Handler, tool string, args json.RawMessage, inbound http.Header, maxBytes int, fix nextAction) (res *mcpsdk.CallToolResult, err error) {
	ctx = context.WithValue(ctx, toolCallKey{}, ToolCall{Tool: tool, OperationID: o.OperationID})
	req, err := o.newRequest(ctx, args, inbound)
	if err != nil {
		var ae *ArgError
		if errors.As(err, &ae) {
			return argErrorResult(fix, ae), nil
		}
		return nil, err
	}

	rec := newRecorder(maxBytes)
	defer func() {
		// The SDK runs tool handlers outside the HTTP server's goroutine, where
		// net/http's per-connection recovery does not reach, so a panic in the
		// API chain would otherwise kill the process.
		if p := recover(); p != nil {
			res, err = nil, fmt.Errorf("api handler panicked: %v", p)
		}
	}()
	if forwarder, ok := api.(errorHandler); ok {
		if err := forwarder.ServeHTTPError(rec, req); err != nil {
			res := &mcpsdk.CallToolResult{}
			res.SetError(err)
			return res, nil
		}
	} else {
		api.ServeHTTP(rec, req)
	}
	return rec.result(), nil
}

// argErrorResult answers arguments that cannot form a request with the same
// problem shape the API uses (api/README.md): a Chinese title, a request ID,
// and a next action that names the tool to call again.
func argErrorResult(fix nextAction, e *ArgError) *mcpsdk.CallToolResult {
	return problemResult(http.StatusBadRequest, "validation_failed", "参数校验失败", e.msg, fix)
}

// problemResult is a tool error in the API's problem shape, for errors the
// MCP layer finds itself before any API request is made.
func problemResult(status int, code, title, detail string, next nextAction) *mcpsdk.CallToolResult {
	problem := map[string]any{
		"type":       "urn:lerryn:error:" + code,
		"title":      title,
		"status":     status,
		"code":       code,
		"detail":     detail,
		"request_id": "req_" + rand.Text(),
		"next_actions": []map[string]string{
			{"action": next.action, "hint": next.hint},
		},
	}
	text, _ := json.Marshal(problem)
	return &mcpsdk.CallToolResult{
		IsError:           true,
		Content:           textContent(string(text)),
		StructuredContent: problem,
	}
}

// recorder is an in-memory ResponseWriter that keeps at most limit bytes of
// the body and counts the rest, so a huge response cannot exhaust memory.
type recorder struct {
	header      http.Header
	status      int
	wroteHeader bool
	limit       int
	buf         bytes.Buffer
	total       int
}

func newRecorder(limit int) *recorder {
	if limit <= 0 {
		limit = DefaultMaxResultBytes
	}
	return &recorder{header: http.Header{}, limit: limit}
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status = status
	r.wroteHeader = true
}

func (r *recorder) Write(p []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	r.total += len(p)
	if room := r.limit - r.buf.Len(); room > 0 {
		r.buf.Write(p[:min(room, len(p))])
	}
	return len(p), nil
}

func (r *recorder) truncated() bool { return r.total > r.buf.Len() }

// result converts the recorded response into a tool result.
//   - problem+json becomes a tool error whose text is the problem JSON, so
//     the agent sees code and next_actions even if the client ignores
//     structuredContent;
//   - other non-2xx responses become a tool error with the status and body;
//   - a 2xx JSON object becomes structuredContent plus the same JSON as text.
func (r *recorder) result() *mcpsdk.CallToolResult {
	if !r.wroteHeader {
		r.status = http.StatusOK
	}
	mediaType, _, _ := mime.ParseMediaType(r.header.Get("Content-Type"))
	body := r.buf.Bytes()
	res := &mcpsdk.CallToolResult{Meta: mcpsdk.Meta{metaHTTPStatus: r.status}}
	if r.truncated() {
		res.Meta[metaTruncated] = true
		res.Meta[metaTotalBytes] = r.total
	}

	isProblem := mediaType == "application/problem+json"
	success := r.status >= 200 && r.status < 300 && !isProblem
	res.IsError = !success

	text := trimPartialRune(body)
	switch {
	case len(body) == 0:
		res.Content = textContent(fmt.Sprintf("HTTP %d %s, empty body.", r.status, http.StatusText(r.status)))
	case !utf8.Valid(text):
		res.Content = textContent(fmt.Sprintf(
			"HTTP %d, %d bytes of %s. Binary content is not returned over MCP; download it with the HTTP API.",
			r.status, r.total, r.header.Get("Content-Type")))
	case r.truncated():
		// Structured content is dropped: a cut JSON document cannot be parsed.
		res.Content = textContent(r.truncatedText(text))
	default:
		if isJSON(mediaType) {
			if obj, ok := jsonObject(body); ok {
				res.StructuredContent = obj
			}
		}
		s := string(body)
		if !success && !isProblem {
			s = fmt.Sprintf("HTTP %d: %s", r.status, s)
		}
		res.Content = textContent(s)
	}
	return res
}

func textContent(s string) []mcpsdk.Content {
	return []mcpsdk.Content{&mcpsdk.TextContent{Text: s}}
}

// trimPartialRune drops a multi-byte character cut in half by truncation, so
// the text stays valid UTF-8.
func trimPartialRune(body []byte) []byte {
	for range utf8.UTFMax - 1 {
		if len(body) == 0 {
			break
		}
		if r, size := utf8.DecodeLastRune(body); r != utf8.RuneError || size != 1 {
			break
		}
		body = body[:len(body)-1]
	}
	return body
}

func (r *recorder) truncatedText(body []byte) string {
	prefix := ""
	if r.status < 200 || r.status >= 300 {
		prefix = fmt.Sprintf("HTTP %d: ", r.status)
	}
	return fmt.Sprintf("%s%s\n\n[Truncated: showing %d of %d bytes. The result is incomplete and not valid JSON. "+
		"Narrow the request, for example with limit and cursor, to read the rest.]",
		prefix, body, len(body), r.total)
}

func isJSON(mediaType string) bool {
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

// jsonObject returns body as structured content when it is a JSON object.
// Older protocol versions only allow an object there, so arrays and scalars
// are returned as text only.
func jsonObject(body []byte) (json.RawMessage, bool) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return nil, false
	}
	return json.RawMessage(trimmed), true
}
