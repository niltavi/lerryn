package mcp

import (
	"fmt"
	"net/http"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// annotations derives MCP tool hints from x-operation-kind and the HTTP
// method, following the table in api/README.md:
//
//	kind     readOnly destructive         idempotent           openWorld
//	read     true     false               true                 false
//	mutate   false    method is DELETE    method is PUT/DELETE false
//	execute  false    true                false                true
//
// execute is open-world because a run executes user code that can reach
// external systems; read and mutate only touch Lerryn's own state. Where the
// table is wrong for an operation, x-mcp.hints overrides it (see withHints).
func annotations(kind OperationKind, method string) (*mcpsdk.ToolAnnotations, error) {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return nil, fmt.Errorf("method %s is not supported", method)
	}
	if kind == KindRead && (method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete) {
		return nil, fmt.Errorf("x-operation-kind read contradicts method %s", method)
	}
	if kind != KindRead && (method == http.MethodGet || method == http.MethodHead) {
		return nil, fmt.Errorf("x-operation-kind %s contradicts safe method %s", kind, method)
	}

	switch kind {
	case KindRead:
		return &mcpsdk.ToolAnnotations{
			ReadOnlyHint:    true,
			DestructiveHint: new(false),
			IdempotentHint:  true,
			OpenWorldHint:   new(false),
		}, nil
	case KindMutate:
		return &mcpsdk.ToolAnnotations{
			DestructiveHint: new(method == http.MethodDelete),
			IdempotentHint:  method == http.MethodPut || method == http.MethodDelete,
			OpenWorldHint:   new(false),
		}, nil
	default:
		return &mcpsdk.ToolAnnotations{
			DestructiveHint: new(true),
			IdempotentHint:  false,
			OpenWorldHint:   new(true),
		}, nil
	}
}

// withHints applies the overrides an operation declares in x-mcp.hints.
func withHints(a *mcpsdk.ToolAnnotations, h *xHints) *mcpsdk.ToolAnnotations {
	if h == nil {
		return a
	}
	if h.Destructive != nil {
		a.DestructiveHint = new(*h.Destructive)
	}
	if h.Idempotent != nil {
		a.IdempotentHint = *h.Idempotent
	}
	if h.OpenWorld != nil {
		a.OpenWorldHint = new(*h.OpenWorld)
	}
	return a
}

// checkKind rejects an x-operation-kind that contradicts the HTTP method.
// It applies to every operation, exposed or not, because the kind also
// decides the token scope (api/README.md).
func checkKind(kind OperationKind, method string) error {
	if kind == KindRead && (method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete) {
		return fmt.Errorf("x-operation-kind read contradicts method %s", method)
	}
	if kind != KindRead && (method == http.MethodGet || method == http.MethodHead) {
		return fmt.Errorf("x-operation-kind %s contradicts safe method %s", kind, method)
	}
	return nil
}
