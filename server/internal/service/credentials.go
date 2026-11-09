package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/secrets"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Credentials manages users' credentials (tech design 12.3 to 12.5): fill
// requests created by agents, the encrypted values a user submits on the
// fill page, grants to tasks, and the values a runner fetches while
// preparing a run.
//
// Who may do what:
//
//   - A credential and its fill requests belong to one user. Nobody else,
//     platform admins included, sees them; other users get ErrNotFound.
//   - Task credentials are written through Fill from a browser session and
//     read back by RunSecrets only for a run assigned to the calling runner.
//   - The personal AI credential is isolated from task credentials. Its key
//     stays on the server and is sent only to the configured model provider.
//   - Granting needs the owner, who must be able to view the task. Taking
//     over a reference name another user's credential is bound to also
//     needs permission to modify the task.
type Credentials struct {
	db    *store.DB
	authz *Authorizer
	keys  secrets.Keyring
	log   *slog.Logger
	now   func() time.Time
	ai    aiProvider
}

// NewCredentials returns the credential service. keys encrypts and
// decrypts values; without it, filling in and fetching values fail.
func NewCredentials(db *store.DB, authz *Authorizer, keys secrets.Keyring, log *slog.Logger) *Credentials {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Credentials{db: db, authz: authz, keys: keys, log: log, now: store.Now, ai: newAIProvider(&notify.Guard{})}
}

// Credential rules (CreateCredentialRequestBody and the fill page).
const (
	defaultRequestTTL   = 24 * time.Hour
	minRequestTTL       = 10 * time.Minute
	maxRequestTTL       = 7 * 24 * time.Hour
	maxRequestFields    = 10
	maxPurpose          = 200
	maxFieldLabel       = 60
	maxFieldDescription = 200
	// maxFieldValue and maxCredentialValues bound what a user submits.
	// Values are injected as environment variables, and Linux limits one
	// to 128 KiB, so a credential stays well below that.
	maxFieldValue       = 64 << 10
	maxCredentialValues = 96 << 10
	// credentialNameAttempts bounds the suffixes tried when the owner
	// already has an active credential of the requested name.
	credentialNameAttempts = 20
)

// CredentialError is why a credential cannot be used: Code is
// credential_missing or credential_unavailable, Name the manifest's
// reference name and Detail a sentence for people and agents. It never
// carries a value.
type CredentialError struct {
	Code   string
	Name   string
	Detail string
}

func (e *CredentialError) Error() string { return e.Code + ": " + e.Detail }

// Unwrap lets errors.Is match ErrCredentialMissing or
// ErrCredentialUnavailable.
func (e *CredentialError) Unwrap() error {
	if e.Code == runproto.CodeCredentialMissing {
		return ErrCredentialMissing
	}
	return ErrCredentialUnavailable
}

// Credential errors.
var (
	ErrCredentialMissing     = errors.New("credential missing")
	ErrCredentialUnavailable = errors.New("credential unavailable")
	// ErrRequestClosed means a fill request was already fulfilled,
	// expired or cancelled.
	ErrRequestClosed = errors.New("credential request is closed")
)

// CredentialRequestDetail is a fill request with its task. Status is
// effective: a pending request past its expiry reads as expired.
type CredentialRequestDetail struct {
	Request store.CredentialRequest
	Task    *TaskRef
}

// CredentialDetail is a credential with its grants. Status is effective,
// like the request status.
type CredentialDetail struct {
	Credential store.Credential
	Grants     []GrantDetail
}

// GrantDetail is a grant of a credential to a task.
type GrantDetail struct {
	Grant store.CredentialGrant
	Task  TaskRef
}

// requestStatus is the effective status of r at now.
func requestStatus(r store.CredentialRequest, now time.Time) string {
	if r.Status == store.CredentialRequestPending && !now.Before(r.ExpiresAt) {
		return store.CredentialRequestExpired
	}
	return r.Status
}

// credentialStatus is the effective status of c at now.
func credentialStatus(c store.Credential, now time.Time) string {
	switch {
	case c.Status == store.CredentialRevoked || !c.RevokedAt.IsZero():
		return store.CredentialRevoked
	case !c.ExpiresAt.IsZero() && !now.Before(c.ExpiresAt):
		return store.CredentialExpired
	}
	return c.Status
}

// CreateRequestInput is a fill request from an agent. Either TaskID and
// Name, for a new credential granted to the task, or CredentialID, for a
// new version of an existing one.
type CreateRequestInput struct {
	TaskID       uuid.UUID
	Name         string
	CredentialID uuid.UUID
	Purpose      string
	Fields       []store.CredentialField
	// ExpiresIn is how long the fill link lasts; zero means 24 hours.
	ExpiresIn time.Duration
}

// CreateRequest creates a fill request addressed to p.
func (c *Credentials) CreateRequest(ctx context.Context, p *auth.Principal, in CreateRequestInput) (CredentialRequestDetail, error) {
	if p == nil {
		return CredentialRequestDetail{}, ErrUnauthenticated
	}
	if err := validateRequestInput(in); err != nil {
		return CredentialRequestDetail{}, err
	}
	var task *store.Task
	if in.TaskID != uuid.Nil {
		t, err := c.visibleTask(ctx, p, in.TaskID)
		if err != nil {
			return CredentialRequestDetail{}, err
		}
		task = &t
	}
	name := in.Name
	if in.CredentialID != uuid.Nil {
		cred, err := c.ownCredential(ctx, c.db, p, in.CredentialID)
		if err != nil {
			return CredentialRequestDetail{}, err
		}
		if credentialStatus(cred, c.now()) == store.CredentialRevoked {
			var v validation
			v.content("#/credential_id", "这个凭证已撤销，不能更新；请改为发起新的凭证请求")
			return CredentialRequestDetail{}, v.err()
		}
		if name == "" {
			name = cred.Name
		}
	}
	client, err := c.clientName(ctx, p)
	if err != nil {
		return CredentialRequestDetail{}, err
	}
	ttl := in.ExpiresIn
	if ttl == 0 {
		ttl = defaultRequestTTL
	}
	now := c.now()
	req := store.CredentialRequest{
		ID:           store.NewID(),
		UserID:       p.UserID,
		TaskID:       in.TaskID,
		CredentialID: in.CredentialID,
		Name:         name,
		Purpose:      in.Purpose,
		Fields:       in.Fields,
		Status:       store.CredentialRequestPending,
		Client:       client,
		CreatedAt:    now,
		ExpiresAt:    now.Add(ttl),
	}

	// Idempotency-Key retries are answered before this, by the API's
	// idempotency layer (httpapi/idempotency.go), with the first response.
	err = c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&req).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		details := map[string]any{"name": name, "fields": fieldNames(in.Fields)}
		if in.TaskID != uuid.Nil {
			details["task_id"] = in.TaskID.String()
		}
		if in.CredentialID != uuid.Nil {
			details["credential_id"] = in.CredentialID.String()
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "credential_request.create", resourceKind: "credential_request", resourceID: req.ID.String(),
			result: resultSuccess, details: details,
		})
	})
	if err != nil {
		return CredentialRequestDetail{}, err
	}
	d := CredentialRequestDetail{Request: req}
	if task != nil {
		d.Task = &TaskRef{ID: task.ID, Name: task.Name, ProjectID: task.ProjectID}
	}
	return d, nil
}

func validateRequestInput(in CreateRequestInput) error {
	var v validation
	switch {
	case in.CredentialID == uuid.Nil && (in.TaskID == uuid.Nil || in.Name == ""):
		v.content("#/task_id", "新凭证需要同时给出 task_id 和 name；更新已有凭证给出 credential_id")
	case in.TaskID != uuid.Nil && in.Name == "":
		v.content("#/name", "给出 task_id 时需要同时给出 name，即任务清单中的引用名")
	}
	if in.Name != "" && !credentialNamePattern.MatchString(in.Name) {
		v.body("#/name", "引用名为 1 到 64 个字符，只能包含大写字母、数字和下划线，以大写字母开头")
	}
	if n := utf8.RuneCountInString(strings.TrimSpace(in.Purpose)); n == 0 || utf8.RuneCountInString(in.Purpose) > maxPurpose {
		v.body("#/purpose", fmt.Sprintf("用途为 1 到 %d 个字符", maxPurpose))
	}
	if in.ExpiresIn != 0 && (in.ExpiresIn < minRequestTTL || in.ExpiresIn > maxRequestTTL) {
		v.body("#/expires_in_minutes", "有效期为 10 到 10080 分钟")
	}
	validateFieldSpecs(&v, in.Fields)
	return v.err()
}

func validateFieldSpecs(v *validation, fields []store.CredentialField) {
	switch {
	case len(fields) == 0:
		v.body("#/fields", "至少需要一个字段")
		return
	case len(fields) > maxRequestFields:
		v.body("#/fields", fmt.Sprintf("最多 %d 个字段", maxRequestFields))
		return
	}
	seen := map[string]bool{}
	required := 0
	for i, f := range fields {
		at := fmt.Sprintf("#/fields/%d", i)
		switch {
		case !credentialNamePattern.MatchString(f.Name):
			v.body(at+"/name", "字段名为 1 到 64 个字符，只能包含大写字母、数字和下划线，以大写字母开头")
		case reservedEnvName(f.Name):
			v.content(at+"/name", fmt.Sprintf("字段名 %s 是平台保留的环境变量，请换一个名字", f.Name))
		case seen[f.Name]:
			v.content(at+"/name", fmt.Sprintf("字段名 %s 重复", f.Name))
		}
		seen[f.Name] = true
		if utf8.RuneCountInString(f.Label) > maxFieldLabel {
			v.body(at+"/label", fmt.Sprintf("名称最多 %d 个字符", maxFieldLabel))
		}
		if utf8.RuneCountInString(f.Description) > maxFieldDescription {
			v.body(at+"/description", fmt.Sprintf("说明最多 %d 个字符", maxFieldDescription))
		}
		if !f.Optional {
			required++
		}
	}
	if required == 0 {
		v.content("#/fields", "至少要有一个必填字段")
	}
}

func fieldNames(fields []store.CredentialField) []string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names
}

// clientName is the name of the access token p authenticated with, shown
// on the fill page as the agent that asked; empty for sessions.
func (c *Credentials) clientName(ctx context.Context, p *auth.Principal) (string, error) {
	if p.Method != auth.MethodAccessToken {
		return "", nil
	}
	var t store.AccessToken
	err := c.db.NewSelect().Model(&t).Column("name").Where("id = ?", p.CredentialID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return t.Name, err
}

// visibleTask loads a task p may view; archived tasks count as gone.
func (c *Credentials) visibleTask(ctx context.Context, p *auth.Principal, id uuid.UUID) (store.Task, error) {
	var t store.Task
	err := c.db.NewSelect().Model(&t).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !t.ArchivedAt.IsZero()) {
		return store.Task{}, ErrNotFound
	}
	if err != nil {
		return store.Task{}, err
	}
	if err := c.authz.Require(ctx, p, t.ProjectID, ActionViewTask); err != nil {
		return store.Task{}, err
	}
	return t, nil
}

func (c *Credentials) ownCredential(ctx context.Context, idb bun.IDB, p *auth.Principal, id uuid.UUID) (store.Credential, error) {
	var cred store.Credential
	err := idb.NewSelect().Model(&cred).Where("id = ? AND owner_user_id = ? AND name <> ?", id, p.UserID, aiCredentialName).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Credential{}, ErrNotFound
	}
	return cred, err
}

func (c *Credentials) ownRequest(ctx context.Context, idb bun.IDB, p *auth.Principal, id uuid.UUID) (store.CredentialRequest, error) {
	if p == nil {
		return store.CredentialRequest{}, ErrUnauthenticated
	}
	var r store.CredentialRequest
	err := idb.NewSelect().Model(&r).Where("id = ? AND user_id = ?", id, p.UserID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.CredentialRequest{}, ErrNotFound
	}
	return r, err
}

// GetRequest returns one of p's fill requests.
func (c *Credentials) GetRequest(ctx context.Context, p *auth.Principal, id uuid.UUID) (CredentialRequestDetail, error) {
	r, err := c.ownRequest(ctx, c.db, p, id)
	if err != nil {
		return CredentialRequestDetail{}, err
	}
	ds, err := c.requestDetails(ctx, []store.CredentialRequest{r})
	if err != nil {
		return CredentialRequestDetail{}, err
	}
	return ds[0], nil
}

// ListRequests lists p's fill requests, newest first. status filters on
// the effective status; empty means pending.
func (c *Credentials) ListRequests(ctx context.Context, p *auth.Principal, status string, limitParam *int, cursor string) ([]CredentialRequestDetail, string, error) {
	if p == nil {
		return nil, "", ErrUnauthenticated
	}
	limit, after, err := pageParams(limitParam, cursor)
	if err != nil {
		return nil, "", err
	}
	if status == "" {
		status = store.CredentialRequestPending
	}
	now := c.now()
	q := c.db.NewSelect().Model((*store.CredentialRequest)(nil)).
		Where("user_id = ?", p.UserID).
		Order("id DESC").Limit(int64(limit) + 1)
	switch status {
	case store.CredentialRequestPending:
		q = q.Where("status = ? AND expires_at > ?", store.CredentialRequestPending, now)
	case store.CredentialRequestExpired:
		q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
			return q.Where("status = ?", store.CredentialRequestExpired).
				WhereOr("status = ? AND expires_at <= ?", store.CredentialRequestPending, now)
		})
	default:
		q = q.Where("status = ?", status)
	}
	if after != uuid.Nil {
		q = q.Where("id < ?", after)
	}
	var rows []store.CredentialRequest
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(rows[limit-1].ID)
	}
	ds, err := c.requestDetails(ctx, rows)
	return ds, next, err
}

// CancelRequest cancels one of p's pending requests. A request that is no
// longer pending is returned unchanged.
func (c *Credentials) CancelRequest(ctx context.Context, p *auth.Principal, id uuid.UUID) (CredentialRequestDetail, error) {
	if _, err := c.ownRequest(ctx, c.db, p, id); err != nil {
		return CredentialRequestDetail{}, err
	}
	now := c.now()
	err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Model((*store.CredentialRequest)(nil)).
			Set("status = ?", store.CredentialRequestCancelled).
			Set("cancelled_at = ?", now).
			Where("id = ? AND user_id = ? AND status = ? AND expires_at > ?", id, p.UserID, store.CredentialRequestPending, now).
			Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "credential_request.cancel", resourceKind: "credential_request", resourceID: id.String(),
			result: resultSuccess,
		})
	})
	if err != nil {
		return CredentialRequestDetail{}, err
	}
	return c.GetRequest(ctx, p, id)
}

func (c *Credentials) requestDetails(ctx context.Context, rows []store.CredentialRequest) ([]CredentialRequestDetail, error) {
	now := c.now()
	var taskIDs []uuid.UUID
	for _, r := range rows {
		if r.TaskID != uuid.Nil {
			taskIDs = append(taskIDs, r.TaskID)
		}
	}
	tasks, err := c.taskRefs(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	out := make([]CredentialRequestDetail, len(rows))
	for i, r := range rows {
		r.Status = requestStatus(r, now)
		out[i] = CredentialRequestDetail{Request: r}
		if t, ok := tasks[r.TaskID]; ok {
			out[i].Task = &t
		}
	}
	return out, nil
}

func (c *Credentials) taskRefs(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]TaskRef, error) {
	out := map[uuid.UUID]TaskRef{}
	if len(ids) == 0 {
		return out, nil
	}
	var tasks []store.Task
	if err := c.db.NewSelect().Model(&tasks).Column("id", "name", "project_id").
		Where("id IN (?)", bun.List(uniqueIDs(ids))).Scan(ctx); err != nil {
		return nil, err
	}
	for _, t := range tasks {
		out[t.ID] = TaskRef{ID: t.ID, Name: t.Name, ProjectID: t.ProjectID}
	}
	return out, nil
}

// pageParams checks the common limit and ID cursor parameters.
func pageParams(limitParam *int, cursor string) (int, uuid.UUID, error) {
	var v validation
	limit := defaultPageSize
	if limitParam != nil {
		limit = *limitParam
	}
	if limit < 1 || limit > maxPageSize {
		v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxPageSize))
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	return limit, after, v.err()
}

// List returns p's credentials ordered by name, with their grants.
// status filters on the effective status; empty means all.
func (c *Credentials) List(ctx context.Context, p *auth.Principal, status string, limitParam *int, cursor string) ([]CredentialDetail, string, error) {
	if p == nil {
		return nil, "", ErrUnauthenticated
	}
	var v validation
	limit := defaultPageSize
	if limitParam != nil {
		limit = *limitParam
	}
	if limit < 1 || limit > maxPageSize {
		v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxPageSize))
	}
	afterName, afterID, err := decodeNameCursor(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	now := c.now()
	q := c.db.NewSelect().Model((*store.Credential)(nil)).
		Where("owner_user_id = ? AND name <> ?", p.UserID, aiCredentialName).
		Order("name", "id").Limit(int64(limit) + 1)
	switch status {
	case "":
	case store.CredentialRevoked:
		q = q.Where("revoked_at IS NOT NULL")
	case store.CredentialExpired:
		q = q.Where("revoked_at IS NULL AND expires_at IS NOT NULL AND expires_at <= ?", now)
	default:
		q = q.Where("revoked_at IS NULL AND (expires_at IS NULL OR expires_at > ?)", now)
	}
	if afterID != uuid.Nil {
		q = q.Where("(name > ? OR (name = ? AND id > ?))", afterName, afterName, afterID)
	}
	var rows []store.Credential
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, "", err
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[limit-1]
		next = encodeNameCursor(last.Name, last.ID)
	}
	ds, err := c.credentialDetails(ctx, rows)
	return ds, next, err
}

func encodeNameCursor(name string, id uuid.UUID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(id.String() + "\x00" + name))
}

func decodeNameCursor(s string) (string, uuid.UUID, error) {
	if s == "" {
		return "", uuid.Nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return "", uuid.Nil, err
	}
	idText, name, ok := strings.Cut(string(b), "\x00")
	if !ok {
		return "", uuid.Nil, errors.New("unknown cursor form")
	}
	id, err := uuid.Parse(idText)
	return name, id, err
}

// Get returns one of p's credentials.
func (c *Credentials) Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (CredentialDetail, error) {
	if p == nil {
		return CredentialDetail{}, ErrUnauthenticated
	}
	cred, err := c.ownCredential(ctx, c.db, p, id)
	if err != nil {
		return CredentialDetail{}, err
	}
	ds, err := c.credentialDetails(ctx, []store.Credential{cred})
	if err != nil {
		return CredentialDetail{}, err
	}
	return ds[0], nil
}

func (c *Credentials) credentialDetails(ctx context.Context, rows []store.Credential) ([]CredentialDetail, error) {
	now := c.now()
	out := make([]CredentialDetail, len(rows))
	if len(rows) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	var grants []store.CredentialGrant
	if err := c.db.NewSelect().Model(&grants).Where("credential_id IN (?)", bun.List(ids)).Order("created_at", "id").Scan(ctx); err != nil {
		return nil, err
	}
	taskIDs := make([]uuid.UUID, len(grants))
	for i, g := range grants {
		taskIDs[i] = g.TaskID
	}
	tasks, err := c.taskRefs(ctx, taskIDs)
	if err != nil {
		return nil, err
	}
	index := map[uuid.UUID]int{}
	for i, r := range rows {
		r.Status = credentialStatus(r, now)
		out[i] = CredentialDetail{Credential: r, Grants: []GrantDetail{}}
		index[r.ID] = i
	}
	for _, g := range grants {
		i := index[g.CredentialID]
		out[i].Grants = append(out[i].Grants, GrantDetail{Grant: g, Task: tasks[g.TaskID]})
	}
	return out, nil
}

// Revoke revokes one of p's credentials. New runs of the tasks it is
// granted to then fail in preparation with credential_unavailable; grants
// are kept so the owner can see where it was used. Revoking twice is a
// no-op.
func (c *Credentials) Revoke(ctx context.Context, p *auth.Principal, id uuid.UUID) (CredentialDetail, error) {
	if p == nil {
		return CredentialDetail{}, ErrUnauthenticated
	}
	now := c.now()
	err := c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		cred, err := c.ownCredential(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if !cred.RevokedAt.IsZero() {
			return nil
		}
		if _, err := tx.NewUpdate().Model((*store.Credential)(nil)).
			Set("status = ?", store.CredentialRevoked).
			Set("revoked_at = ?", now).
			Set("updated_at = ?", now).
			Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "credential.revoke", resourceKind: "credential", resourceID: id.String(),
			result: resultSuccess, details: map[string]any{"name": cred.Name},
		})
	})
	if err != nil {
		return CredentialDetail{}, err
	}
	return c.Get(ctx, p, id)
}

// Grant binds one of p's credentials to a task under a reference name,
// replacing whatever the name was bound to (tech design 12.4).
func (c *Credentials) Grant(ctx context.Context, p *auth.Principal, credentialID, taskID uuid.UUID, name string) (GrantDetail, error) {
	if p == nil {
		return GrantDetail{}, ErrUnauthenticated
	}
	if !credentialNamePattern.MatchString(name) {
		var v validation
		v.body("#/name", "引用名为 1 到 64 个字符，只能包含大写字母、数字和下划线，以大写字母开头")
		return GrantDetail{}, v.err()
	}
	cred, err := c.ownCredential(ctx, c.db, p, credentialID)
	if err != nil {
		return GrantDetail{}, err
	}
	task, err := c.visibleTask(ctx, p, taskID)
	if err != nil {
		return GrantDetail{}, err
	}
	if s := credentialStatus(cred, c.now()); s != store.CredentialActive {
		var v validation
		v.content("#/name", "凭证已撤销或已过期，不能授权；请先更新凭证")
		return GrantDetail{}, v.err()
	}
	var g store.CredentialGrant
	err = c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		var err error
		g, err = c.bind(ctx, tx, p, cred.ID, task, name)
		return err
	})
	if err != nil {
		return GrantDetail{}, err
	}
	return GrantDetail{Grant: g, Task: TaskRef{ID: task.ID, Name: task.Name, ProjectID: task.ProjectID}}, nil
}

// bind writes the grant of credentialID to task under name, inside tx.
// A credential has one grant per task, so binding it under a new name
// moves that grant. Replacing a binding to another user's credential
// needs the right to modify the task: viewers may lend their own
// credentials to a task, but not take it away from what others set up.
func (c *Credentials) bind(ctx context.Context, tx bun.Tx, p *auth.Principal, credentialID uuid.UUID, task store.Task, name string) (store.CredentialGrant, error) {
	var current store.CredentialGrant
	err := tx.NewSelect().Model(&current).Where("task_id = ? AND name = ?", task.ID, name).Scan(ctx)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return store.CredentialGrant{}, err
	case current.CredentialID == credentialID:
		return current, nil
	default:
		var owner store.Credential
		if err := tx.NewSelect().Model(&owner).Column("owner_user_id").Where("id = ?", current.CredentialID).Scan(ctx); err != nil {
			return store.CredentialGrant{}, err
		}
		if owner.OwnerUserID != p.UserID {
			if err := c.authz.Require(ctx, p, task.ProjectID, ActionModifyTask); err != nil {
				return store.CredentialGrant{}, err
			}
		}
		if _, err := tx.NewDelete().Model((*store.CredentialGrant)(nil)).Where("id = ?", current.ID).Exec(ctx); err != nil {
			return store.CredentialGrant{}, err
		}
	}
	if _, err := tx.NewDelete().Model((*store.CredentialGrant)(nil)).
		Where("credential_id = ? AND task_id = ?", credentialID, task.ID).Exec(ctx); err != nil {
		return store.CredentialGrant{}, err
	}
	g := store.CredentialGrant{
		ID: store.NewID(), CredentialID: credentialID, TaskID: task.ID, Name: name,
		GrantedBy: p.UserID, CreatedAt: c.now(),
	}
	if _, err := tx.NewInsert().Model(&g).Exec(ctx); err != nil {
		return store.CredentialGrant{}, err
	}
	kind, actor := actorOf(p)
	details := map[string]any{"task_id": task.ID.String(), "name": name}
	if current.ID != uuid.Nil {
		details["replaced_credential_id"] = current.CredentialID.String()
	}
	return g, writeAudit(ctx, tx, auditEntry{
		actorKind: kind, actorID: actor,
		action: "credential.grant", resourceKind: "credential", resourceID: credentialID.String(),
		result: resultSuccess, details: details,
	})
}

// RevokeGrant withdraws one of p's credentials from a task. The owner may
// do so even after losing access to the task.
func (c *Credentials) RevokeGrant(ctx context.Context, p *auth.Principal, credentialID, taskID uuid.UUID) error {
	if p == nil {
		return ErrUnauthenticated
	}
	return c.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := c.ownCredential(ctx, tx, p, credentialID); err != nil {
			return err
		}
		var g store.CredentialGrant
		err := tx.NewSelect().Model(&g).Where("credential_id = ? AND task_id = ?", credentialID, taskID).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*store.CredentialGrant)(nil)).Where("id = ?", g.ID).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "credential.revoke_grant", resourceKind: "credential", resourceID: credentialID.String(),
			result: resultSuccess, details: map[string]any{"task_id": taskID.String(), "name": g.Name},
		})
	})
}

// TaskCredential is the state of one credential a task's manifest
// references.
type TaskCredential struct {
	Spec CredentialSpec
	// Status is bound, missing, revoked or expired.
	Status string
	// Credential is the bound credential, nil when missing.
	Credential *store.Credential
	Owner      *store.User
	// PendingRequestID is the caller's own open fill request for the
	// reference name, uuid.Nil when there is none.
	PendingRequestID uuid.UUID
}

// Task credential statuses (TaskCredential.status in the contract).
const (
	TaskCredentialBound   = "bound"
	TaskCredentialMissing = "missing"
)

// TaskCredentials lists the credentials of the task's active version, or
// of its latest version when it has never been activated.
func (c *Credentials) TaskCredentials(ctx context.Context, p *auth.Principal, taskID uuid.UUID) (version int, items []TaskCredential, err error) {
	if p == nil {
		return 0, nil, ErrUnauthenticated
	}
	task, err := c.visibleTask(ctx, p, taskID)
	if err != nil {
		return 0, nil, err
	}
	seq := task.LatestSeq
	if task.ActiveSeq > 0 {
		seq = task.ActiveSeq
	}
	v, err := loadVersion(ctx, c.db, task.ID, seq)
	if err != nil {
		return 0, nil, err
	}
	now := c.now()
	bindings, err := loadBindings(ctx, c.db, task.ID)
	if err != nil {
		return 0, nil, err
	}
	var pending []store.CredentialRequest
	if err := c.db.NewSelect().Model(&pending).Column("id", "name").
		Where("task_id = ? AND user_id = ? AND status = ? AND expires_at > ?", task.ID, p.UserID, store.CredentialRequestPending, now).
		Order("id DESC").Scan(ctx); err != nil {
		return 0, nil, err
	}
	items = make([]TaskCredential, len(v.Manifest.Credentials))
	for i, spec := range v.Manifest.Credentials {
		spec.As = spec.as()
		item := TaskCredential{Spec: spec, Status: TaskCredentialMissing}
		if b, ok := bindings[spec.Name]; ok {
			item.Credential, item.Owner = &b.cred, &b.owner
			item.Status = credentialStatus(b.cred, now)
			if item.Status == store.CredentialActive {
				item.Status = TaskCredentialBound
			}
			item.Credential.Status = credentialStatus(b.cred, now)
		}
		for _, r := range pending {
			if r.Name == spec.Name {
				item.PendingRequestID = r.ID
				break
			}
		}
		items[i] = item
	}
	return v.Version.Seq, items, nil
}

// binding is a grant with its credential and the credential's owner.
type binding struct {
	grant store.CredentialGrant
	cred  store.Credential
	owner store.User
}

// loadBindings returns the task's grants by reference name.
func loadBindings(ctx context.Context, idb bun.IDB, taskID uuid.UUID) (map[string]binding, error) {
	var grants []store.CredentialGrant
	if err := idb.NewSelect().Model(&grants).Where("task_id = ?", taskID).Scan(ctx); err != nil {
		return nil, err
	}
	out := map[string]binding{}
	if len(grants) == 0 {
		return out, nil
	}
	credIDs := make([]uuid.UUID, len(grants))
	for i, g := range grants {
		credIDs[i] = g.CredentialID
	}
	var creds []store.Credential
	if err := idb.NewSelect().Model(&creds).Where("id IN (?)", bun.List(credIDs)).Scan(ctx); err != nil {
		return nil, err
	}
	ownerIDs := make([]uuid.UUID, len(creds))
	byID := map[uuid.UUID]store.Credential{}
	for i, cr := range creds {
		ownerIDs[i] = cr.OwnerUserID
		byID[cr.ID] = cr
	}
	var owners []store.User
	if err := idb.NewSelect().Model(&owners).Column("id", "username", "display_name", "disabled_at").
		Where("id IN (?)", bun.List(uniqueIDs(ownerIDs))).Scan(ctx); err != nil {
		return nil, err
	}
	ownerByID := map[uuid.UUID]store.User{}
	for _, u := range owners {
		ownerByID[u.ID] = u
	}
	for _, g := range grants {
		cr, ok := byID[g.CredentialID]
		if !ok {
			continue
		}
		out[g.Name] = binding{grant: g, cred: cr, owner: ownerByID[cr.OwnerUserID]}
	}
	return out, nil
}

// unusable says why a bound credential cannot be used now, or "".
func unusable(b binding, now time.Time) string {
	switch {
	case credentialStatus(b.cred, now) == store.CredentialRevoked:
		return fmt.Sprintf("凭证 %s 已被所有者撤销", b.grant.Name)
	case credentialStatus(b.cred, now) == store.CredentialExpired:
		return fmt.Sprintf("凭证 %s 已过期", b.grant.Name)
	case !b.owner.DisabledAt.IsZero():
		// A disabled member's credentials stop working along with the
		// member's sessions and tokens.
		return fmt.Sprintf("凭证 %s 的所有者账号已停用", b.grant.Name)
	}
	return ""
}

// checkRunCredentials returns a *CredentialError for the first credential
// of specs the task cannot use now, so startRun answers with it instead of
// queueing a run that would fail in preparation.
func checkRunCredentials(ctx context.Context, idb bun.IDB, taskID uuid.UUID, specs []CredentialSpec) error {
	if len(specs) == 0 {
		return nil
	}
	bs, err := loadBindings(ctx, idb, taskID)
	if err != nil {
		return err
	}
	now := store.Now()
	for _, spec := range specs {
		if spec.Optional {
			continue
		}
		b, ok := bs[spec.Name]
		if !ok {
			return missingCredential(spec.Name)
		}
		if why := unusable(b, now); why != "" {
			return &CredentialError{Code: runproto.CodeCredentialUnavailable, Name: spec.Name, Detail: why}
		}
	}
	return nil
}

func missingCredential(name string) *CredentialError {
	return &CredentialError{
		Code: runproto.CodeCredentialMissing, Name: name,
		Detail: fmt.Sprintf("任务引用的凭证 %s 尚未填写，或没有授权给这个任务", name),
	}
}

// credentialAAD binds a sealed value to its credential and version.
func credentialAAD(id uuid.UUID, version int) []byte {
	return []byte("lerryn/credential/" + id.String() + "/v" + strconv.Itoa(version))
}

// credentialPayload is the plaintext of a credential version.
type credentialPayload struct {
	V      int               `json:"v"`
	Values map[string]string `json:"values"`
}

func (c *Credentials) seal(id uuid.UUID, version int, values map[string]string) (secrets.Sealed, error) {
	if c.keys == nil {
		return secrets.Sealed{}, errors.New("credentials: no master key configured")
	}
	plain, err := json.Marshal(credentialPayload{V: 1, Values: values})
	if err != nil {
		return secrets.Sealed{}, err
	}
	defer clear(plain)
	return secrets.Seal(c.keys, plain, credentialAAD(id, version))
}

func (c *Credentials) open(v store.CredentialVersion) (map[string]string, error) {
	if c.keys == nil {
		return nil, errors.New("credentials: no master key configured")
	}
	plain, err := secrets.Open(c.keys, secrets.Sealed{Ciphertext: v.Ciphertext, WrappedDEK: v.WrappedDEK, KEKID: v.KEKID},
		credentialAAD(v.CredentialID, v.Version))
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	var p credentialPayload
	if err := json.Unmarshal(plain, &p); err != nil || p.V != 1 {
		return nil, fmt.Errorf("credential %s version %d: unreadable payload", v.CredentialID, v.Version)
	}
	return p.Values, nil
}

// fieldSensitive reports whether field name of fields is secret. Fields
// default to secret.
func fieldSensitive(fields []store.CredentialField, name string) bool {
	i := slices.IndexFunc(fields, func(f store.CredentialField) bool { return f.Name == name })
	return i < 0 || fields[i].Secret
}
