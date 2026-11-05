package agentevals

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ToolCall is one MCP tools/call the agent sent, as the proxy saw it on the
// wire: the arguments it sent and the result the server returned.
type ToolCall struct {
	Seq       int             `json:"seq"`
	At        time.Time       `json:"at"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	IsError   bool            `json:"is_error"`
	// Code is the problem code of an error result.
	Code string `json:"code,omitempty"`
	// Result is the structured result, or the text when there is none.
	Result json.RawMessage `json:"result,omitempty"`
}

// Operation returns the API operation a call reached: the operationId of
// an api_call, otherwise the tool name.
func (c ToolCall) Operation() string {
	if c.Name != "api_call" {
		return c.Name
	}
	var a struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.Unmarshal(c.Arguments, &a)
	return "api_call:" + a.OperationID
}

// Arg returns a top-level argument as a string ("" when absent). For an
// api_call it looks inside arguments.arguments too.
func (c ToolCall) Arg(name string) string {
	var top map[string]json.RawMessage
	_ = json.Unmarshal(c.Arguments, &top)
	v, ok := top[name]
	if !ok && c.Name == "api_call" {
		var inner map[string]json.RawMessage
		_ = json.Unmarshal(top["arguments"], &inner)
		v = inner[name]
	}
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(v))
}

// ResultField returns a top-level string field of the result.
func (c ToolCall) ResultField(name string) string {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(c.Result, &m)
	var s string
	if json.Unmarshal(m[name], &s) == nil {
		return s
	}
	return ""
}

// Hook runs inside the proxy, before a call is forwarded or after its
// result was read and before the agent receives it. Scenarios use it to
// play the other side: another session, or the user filling a form.
type Hook struct {
	Before func(c ToolCall)
	After  func(c ToolCall)
}

// Proxy sits between the agent and /mcp and records every tool call.
type Proxy struct {
	URL string

	srv   *httptest.Server
	hook  Hook
	mu    sync.Mutex
	calls []ToolCall
}

// NewProxy starts a proxy to target (the instance's base URL).
func NewProxy(target string, hook Hook) (*Proxy, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	p := &Proxy{hook: hook}
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.FlushInterval = -1 // stream SSE responses through
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.serve(rp, w, r)
	}))
	p.URL = p.srv.URL + "/mcp"
	return p, nil
}

// Close stops the proxy.
func (p *Proxy) Close() { p.srv.Close() }

// Calls returns the recorded tool calls in order.
func (p *Proxy) Calls() []ToolCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ToolCall(nil), p.calls...)
}

type rpcRequest struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"params"`
}

func (p *Proxy) serve(rp *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	var call *ToolCall
	if r.Method == http.MethodPost {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
		var req rpcRequest
		if json.Unmarshal(body, &req) == nil && req.Method == "tools/call" {
			call = &ToolCall{At: time.Now().UTC(), Name: req.Params.Name, Arguments: req.Params.Arguments}
			if len(call.Arguments) == 0 {
				call.Arguments = json.RawMessage("{}")
			}
		}
	}
	if call == nil {
		rp.ServeHTTP(w, r)
		return
	}
	if p.hook.Before != nil {
		p.hook.Before(*call)
	}
	// Buffer the answer: the result is parsed and the After hook runs
	// before the agent sees it, so a hook's side effect is in place when
	// the agent acts on the result.
	rec := httptest.NewRecorder()
	rp.ServeHTTP(rec, r)
	parseResult(call, rec.Header().Get("Content-Type"), rec.Body.Bytes())
	p.mu.Lock()
	call.Seq = len(p.calls) + 1
	p.calls = append(p.calls, *call)
	p.mu.Unlock()
	if p.hook.After != nil {
		p.hook.After(*call)
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}

// parseResult reads a JSON-RPC answer, plain or as an SSE stream.
func parseResult(c *ToolCall, contentType string, body []byte) {
	var msgs [][]byte
	if strings.HasPrefix(contentType, "text/event-stream") {
		sc := bufio.NewScanner(bytes.NewReader(body))
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				msgs = append(msgs, []byte(strings.TrimSpace(d)))
			}
		}
	} else {
		msgs = [][]byte{body}
	}
	for _, m := range msgs {
		var resp struct {
			Result *struct {
				IsError           bool            `json:"isError"`
				StructuredContent json.RawMessage `json:"structuredContent"`
				Content           []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(m, &resp) != nil {
			continue
		}
		if resp.Error != nil {
			c.IsError = true
			c.Result, _ = json.Marshal(resp.Error.Message)
			return
		}
		if resp.Result == nil {
			continue
		}
		c.IsError = resp.Result.IsError
		c.Result = resp.Result.StructuredContent
		if len(c.Result) == 0 && len(resp.Result.Content) > 0 {
			text := resp.Result.Content[0].Text
			if json.Valid([]byte(text)) {
				c.Result = json.RawMessage(text)
			} else {
				c.Result, _ = json.Marshal(text)
			}
		}
		if c.IsError {
			c.Code = c.ResultField("code")
		}
		return
	}
}
