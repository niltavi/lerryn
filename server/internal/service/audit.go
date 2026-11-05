package service

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Audit actor kinds and results (tech design 12.7). They match the
// AuditActor.kind and AuditEvent.result enums of the API.
const (
	actorUser      = "user"
	actorToken     = "token"
	actorRunner    = "runner"
	actorTrigger   = "trigger"
	actorAnonymous = "anonymous"
	actorSystem    = "system"

	resultSuccess = "success"
	resultFailure = "failure"
	resultDenied  = "denied"
)

// auditEntry is an audit record before the common fields are filled in.
//
// How to record (every service follows this, so records look the same
// whoever wrote them):
//
//   - Write the record with writeAudit inside the transaction of the
//     change, so the two commit or roll back together. Record failed or
//     refused attempts that matter (a wrong password, a denied change) with
//     auditFailure, outside any transaction.
//   - Set actorKind and actorID with actorOf(p) for a request principal, or
//     use actorRunner, actorTrigger or actorSystem for actions without one.
//   - action is the dotted name of the event, such as run.start. Inside an
//     API request the record takes the operationId instead (setMember),
//     which is what admins filter by; the dotted name is kept for actions
//     outside the API, such as a runner claiming a credential.
//   - resourceKind is a singular noun (task, run, credential, user,
//     project, setting) and resourceID the ID of the resource changed.
//   - details holds the facts an admin needs to understand the change, such
//     as a role before and after. Never put secrets, tokens, passwords or
//     task log content in it.
//
// The operation kind, entry point, MCP tool, request ID, client address and
// actor name come from the request context. An API mutation whose service
// wrote no record gets a generic one from the HTTP layer (RecordCall), so
// every change is audited even before its service records it explicitly.
type auditEntry struct {
	actorKind    string
	actorID      uuid.UUID
	actorName    string
	action       string
	resourceKind string
	resourceID   string
	result       string
	details      map[string]any
}

// actorOf returns the audit actor for p. Token requests are recorded as
// kind token with the token's owner as the actor; the token's name and ID
// are added from the request context by writeAudit.
func actorOf(p *auth.Principal) (string, uuid.UUID) {
	if p == nil {
		return actorAnonymous, uuid.Nil
	}
	if p.Method == auth.MethodAccessToken {
		return actorToken, p.UserID
	}
	return actorUser, p.UserID
}

// writeAudit inserts e through idb, inside the caller's transaction when
// there is one, so the record commits or rolls back with the action.
func writeAudit(ctx context.Context, idb bun.IDB, e auditEntry) error {
	ev := store.AuditEvent{
		ID:           store.NewID(),
		TS:           store.Now(),
		ActorKind:    e.actorKind,
		ActorID:      e.actorID,
		ActorName:    e.actorName,
		Action:       e.action,
		ResourceKind: e.resourceKind,
		ResourceID:   e.resourceID,
		Result:       e.result,
		IP:           auth.ClientFrom(ctx).IP,
		Details:      e.details,
	}
	if ev.ActorKind == "" {
		ev.ActorKind = actorSystem
	}
	if ev.Result == "" {
		ev.Result = resultSuccess
	}
	// The principal of the request names the actor when the record is
	// about it, and points at the token used.
	if p := auth.PrincipalFrom(ctx); p != nil && ev.ActorID == p.UserID &&
		(ev.ActorKind == actorUser || ev.ActorKind == actorToken) {
		if ev.ActorKind == actorToken && p.Method == auth.MethodAccessToken {
			if ev.ActorName == "" {
				ev.ActorName = p.CredentialName
			}
			ev.Details = withDetail(ev.Details, "token_id", p.CredentialID.String())
		} else if ev.ActorName == "" {
			ev.ActorName = p.Username
		}
		if p.ReadOnly {
			ev.Details = withDetail(ev.Details, "device_session", true)
		}
	}
	if c := auth.CallFrom(ctx); c != nil {
		if c.OperationID != "" {
			ev.Action = c.OperationID
		}
		ev.Kind, ev.Via, ev.Tool, ev.RequestID = c.Kind, c.Via, c.Tool, c.RequestID
	}
	if _, err := idb.NewInsert().Model(&ev).Exec(ctx); err != nil {
		return fmt.Errorf("write audit event %s: %w", ev.Action, err)
	}
	if c := auth.CallFrom(ctx); c != nil {
		c.MarkAudited()
	}
	return nil
}

// withDetail returns details with key set, copying so the caller's map is
// not changed.
func withDetail(details map[string]any, key string, value any) map[string]any {
	out := make(map[string]any, len(details)+1)
	maps.Copy(out, details)
	out[key] = value
	return out
}

// auditFailure records a failed attempt outside any transaction. A failure
// to write it is logged rather than returned: the caller is already
// answering with an error, and that answer must not change.
func auditFailure(ctx context.Context, db bun.IDB, log *slog.Logger, e auditEntry) {
	if e.result == "" || e.result == resultSuccess {
		e.result = resultFailure
	}
	if err := writeAudit(ctx, db, e); err != nil {
		log.ErrorContext(ctx, "write audit event", "action", e.action, "error", err)
	}
}

// Audit records generic audit events for API calls and answers the admin
// audit query.
type Audit struct {
	db  *store.DB
	log *slog.Logger
}

// NewAudit returns the audit service.
func NewAudit(db *store.DB, log *slog.Logger) *Audit {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Audit{db: db, log: log}
}

// CallOutcome is how an API call ended, for RecordCall.
type CallOutcome struct {
	// Status is the HTTP status of the response.
	Status int
	// ResourceKind and ResourceID name the resource from the request path,
	// when it has one.
	ResourceKind string
	ResourceID   string
}

// RecordCall writes a generic record for a mutating API call whose service
// wrote none, so every change is audited (tech design 12.7). It is a no-op
// for reads, for calls already audited, for requests rejected as malformed
// before the operation ran (400), and for anonymous requests refused for
// lack of credentials, which would let anyone fill the table.
func (a *Audit) RecordCall(ctx context.Context, out CallOutcome) {
	c := auth.CallFrom(ctx)
	if c == nil || c.Audited() || (c.Kind != "mutate" && c.Kind != "execute") {
		return
	}
	p := auth.PrincipalFrom(ctx)
	if out.Status == 400 || (p == nil && out.Status == 401) {
		return
	}
	result := resultSuccess
	switch {
	case out.Status == 401 || out.Status == 403:
		result = resultDenied
	case out.Status >= 400:
		result = resultFailure
	}
	kind, actor := actorOf(p)
	e := auditEntry{
		actorKind: kind, actorID: actor, action: c.OperationID,
		resourceKind: out.ResourceKind, resourceID: out.ResourceID, result: result,
		details: map[string]any{"status": out.Status},
	}
	if e.resourceKind == "" {
		e.resourceKind = "api"
	}
	if err := writeAudit(ctx, a.db, e); err != nil {
		a.log.ErrorContext(ctx, "write audit event", "action", c.OperationID, "error", err)
	}
}

// AuditFilter selects audit events. Zero values do not filter.
type AuditFilter struct {
	Since, Until time.Time
	ActorID      string
	Action       string
	ResourceKind string
	ResourceID   string
	Result       string
	Limit        *int
	Cursor       string
}

// List returns audit events, newest first. Only platform admins may query
// the audit log.
func (a *Audit) List(ctx context.Context, p *auth.Principal, f AuditFilter) ([]store.AuditEvent, string, error) {
	if err := requireAdmin(p); err != nil {
		return nil, "", err
	}
	var v validation
	limit := pageLimit(&v, f.Limit)
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if !f.Since.IsZero() && !f.Until.IsZero() && !f.Since.Before(f.Until) {
		v.param("since", "since 必须早于 until")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	// UUIDv7 IDs sort by creation time, so the ID is the keyset; ts and id
	// are written together by the application.
	q := a.db.NewSelect().Model((*store.AuditEvent)(nil)).Order("id DESC").Limit(int64(limit) + 1)
	if after != uuid.Nil {
		q = q.Where("id < ?", after)
	}
	if !f.Since.IsZero() {
		q = q.Where("ts >= ?", f.Since.UTC())
	}
	if !f.Until.IsZero() {
		q = q.Where("ts < ?", f.Until.UTC())
	}
	if f.ActorID != "" {
		id, err := uuid.Parse(f.ActorID)
		if err != nil {
			// IDs of every actor kind are UUIDs; anything else matches
			// nothing rather than failing the query on PostgreSQL.
			return []store.AuditEvent{}, "", nil
		}
		q = q.Where("actor_id = ?", id)
	}
	if f.Action != "" {
		q = q.Where("action = ?", f.Action)
	}
	if f.ResourceKind != "" {
		q = q.Where("resource_kind = ?", strings.ToLower(f.ResourceKind))
	}
	if f.ResourceID != "" {
		q = q.Where("resource_id = ?", f.ResourceID)
	}
	if f.Result != "" {
		q = q.Where("result = ?", f.Result)
	}
	var rows []store.AuditEvent
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	rows, next := page(rows, limit, func(e store.AuditEvent) uuid.UUID { return e.ID })
	return rows, next, nil
}

// requireAdmin returns ErrPermissionDenied unless p is a platform admin.
func requireAdmin(p *auth.Principal) error {
	if p == nil {
		return ErrUnauthenticated
	}
	if !p.IsAdmin {
		return ErrPermissionDenied
	}
	return nil
}

// pageLimit validates the limit parameter and returns it, or the default.
func pageLimit(v *validation, limitParam *int) int {
	limit := defaultPageSize
	if limitParam != nil {
		limit = *limitParam
	}
	if limit < 1 || limit > maxPageSize {
		v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxPageSize))
	}
	return limit
}

// page cuts rows, fetched with limit+1, to limit and returns the cursor of
// the next page, empty on the last one.
func page[T any](rows []T, limit int, id func(T) uuid.UUID) ([]T, string) {
	if len(rows) <= limit {
		return rows, ""
	}
	rows = rows[:limit]
	return rows, encodeCursor(id(rows[limit-1]))
}
