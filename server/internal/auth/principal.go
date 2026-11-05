// Package auth holds the building blocks of authentication: the principal
// carried in a request context, password hashing, token generation and a
// rate limiter for login attempts. Account rules, such as who becomes an
// admin or how long a session lasts, live in the service package.
package auth

import (
	"context"
	"slices"
	"sync/atomic"

	"github.com/google/uuid"
)

// Method is how a request was authenticated.
type Method string

// Authentication methods. They match the AuthMethod enum of the API.
const (
	MethodAccessToken Method = "access_token"
	MethodSession     Method = "session"
)

// Scope limits what an access token may do. Each operation needs the scope
// named after its x-operation-kind: read, write (for mutate) or execute.
type Scope string

// Token scopes.
const (
	ScopeRead    Scope = "read"
	ScopeWrite   Scope = "write"
	ScopeExecute Scope = "execute"
)

// AllScopes lists every scope in canonical order. Sessions carry all of
// them.
var AllScopes = []Scope{ScopeRead, ScopeWrite, ScopeExecute}

// ValidScope reports whether s names a scope.
func ValidScope(s string) bool {
	return slices.Contains(AllScopes, Scope(s))
}

// Principal is the authenticated caller of a request.
type Principal struct {
	UserID      uuid.UUID
	Username    string
	DisplayName string
	IsAdmin     bool
	// PersonalProjectID is the user's personal project, where agents
	// create tasks unless told otherwise.
	PersonalProjectID uuid.UUID
	Method            Method
	Scopes            []Scope
	// CredentialID is the ID of the access token or session that
	// authenticated the request.
	CredentialID uuid.UUID
	// CredentialName is the name of the access token; empty for sessions.
	// Audit records it, so a leaked token can be traced.
	CredentialName string
	// ReadOnly marks a mobile device session. Its Scopes hold only
	// ScopeRead, so every mutating operation is refused.
	ReadOnly bool
}

// HasScope reports whether the principal may perform operations that need s.
func (p *Principal) HasScope(s Scope) bool {
	return slices.Contains(p.Scopes, s)
}

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal of the request, or nil when the
// request is anonymous.
func PrincipalFrom(ctx context.Context) *Principal {
	p, _ := ctx.Value(principalKey{}).(*Principal)
	return p
}

// Client describes where a request came from, for audit records and rate
// limits.
type Client struct {
	IP        string
	UserAgent string
}

type clientKey struct{}

// WithClient returns a context carrying c.
func WithClient(ctx context.Context, c Client) context.Context {
	return context.WithValue(ctx, clientKey{}, c)
}

// ClientFrom returns the client of the request; the zero value when unknown.
func ClientFrom(ctx context.Context) Client {
	c, _ := ctx.Value(clientKey{}).(Client)
	return c
}

// Entry points of a request, as audit records them.
const (
	ViaAPI     = "api"
	ViaMCP     = "mcp"
	ViaConsole = "console"
)

// Call describes the API operation a request is addressed to. The HTTP
// layer sets it before the operation runs; services read it when they
// write audit records, so every record carries the operation, its kind and
// the entry point without each service passing them along.
type Call struct {
	// OperationID is the contract operationId, such as setMember.
	OperationID string
	// Kind is the operation's x-operation-kind: read, mutate or execute.
	Kind string
	// Via is the entry point: ViaAPI, ViaMCP or ViaConsole.
	Via string
	// Tool is the MCP tool name when Via is ViaMCP.
	Tool      string
	RequestID string

	audited atomic.Bool
}

// MarkAudited records that a service wrote an audit event for the call, so
// the HTTP layer does not add a generic one.
func (c *Call) MarkAudited() { c.audited.Store(true) }

// Audited reports whether MarkAudited was called.
func (c *Call) Audited() bool { return c.audited.Load() }

type callKey struct{}

// WithCall returns a context carrying c.
func WithCall(ctx context.Context, c *Call) context.Context {
	return context.WithValue(ctx, callKey{}, c)
}

// CallFrom returns the call of the request, or nil outside an API request.
func CallFrom(ctx context.Context) *Call {
	c, _ := ctx.Value(callKey{}).(*Call)
	return c
}
