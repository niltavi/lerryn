package httpapi

import (
	"net/http"
	"strings"
	"unicode"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/mcp"
	"github.com/niltavi/lerryn/server/internal/service"
)

// calls tells services which operation a request is addressed to, so their
// audit records carry the operation, its kind, the entry point and the
// request ID (tech design 12.7). The entry point comes from the context,
// never from a header: an MCP tool call is marked in-process by the MCP
// handler, which a client cannot forge.
//
// After a mutating operation it asks the audit service to record a generic
// event when the operation's service wrote none, so every change is
// audited even before its service records it explicitly.
func (h *handler) calls(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		route, params, err := contractRouter.FindRoute(r)
		if err != nil || route.Operation == nil {
			next.ServeHTTP(w, r)
			return
		}
		ctx := r.Context()
		rules := operations[operationKey(route.Operation.OperationID)]
		c := &auth.Call{
			OperationID: route.Operation.OperationID,
			Kind:        rules.kind,
			Via:         auth.ViaAPI,
			RequestID:   RequestIDFromContext(ctx),
		}
		if tc, ok := mcp.ToolCallFromContext(ctx); ok {
			c.Via, c.Tool = auth.ViaMCP, tc.Tool
		} else if p := auth.PrincipalFrom(ctx); p != nil && p.Method == auth.MethodSession {
			c.Via = auth.ViaConsole
		}
		r = r.WithContext(auth.WithCall(ctx, c))
		if h.audit == nil || (rules.kind != "mutate" && rules.kind != "execute") {
			// Reads, including the streaming ones, keep the original writer.
			next.ServeHTTP(w, r)
			return
		}
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		kind, id := resourceOf(route.Path, params)
		h.audit.RecordCall(r.Context(), service.CallOutcome{Status: sw.status(), ResourceKind: kind, ResourceID: id})
	})
}

// resourceOf names the resource of a request from its path: the last path
// parameter, such as {credentialId} in /credentials/{credentialId}, gives
// the kind (credential) and the ID. Paths without parameters give none.
func resourceOf(template string, params map[string]string) (kind, id string) {
	last := ""
	for _, seg := range strings.Split(template, "/") {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			last = seg[1 : len(seg)-1]
		}
	}
	if last == "" {
		return "", ""
	}
	name := strings.TrimSuffix(strings.TrimSuffix(last, "Id"), "Name")
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String(), params[last]
}

// statusWriter remembers the status of a response.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) status() int {
	if w.code == 0 {
		return http.StatusOK
	}
	return w.code
}
