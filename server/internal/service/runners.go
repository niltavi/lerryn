package service

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Runner protocol versions (tech design 4.5). The server accepts runners
// that speak the current version or the one before, so runners can be
// upgraded one at a time after the servers.
const (
	ProtocolVersion    = runproto.StateProtocolVersion
	MinProtocolVersion = 1
)

// Runner credential prefixes. A registration token is what an admin hands
// to the operator of a runner; the runner exchanges it once for a
// credential of its own, which it presents on every call.
const (
	RunnerRegistrationTokenPrefix = "wl_rrt_" // #nosec G101 -- a prefix, not a credential
	RunnerCredentialPrefix        = "wl_rnr_" // #nosec G101 -- a prefix, not a credential
)

// Runner token rules.
const (
	maxRunnerTokenName = 64
	// A registration token is valid for 1 to 720 hours, 24 by default.
	MinRunnerTokenTTL     = time.Hour
	MaxRunnerTokenTTL     = 720 * time.Hour
	DefaultRunnerTokenTTL = 24 * time.Hour
)

// Runners manages runner registration and the admin's view of runners.
type Runners struct {
	db    *store.DB
	coord *Coordinator
	log   *slog.Logger
	// staticHash is the hash of the registration token from the server
	// configuration, empty when none is configured.
	staticHash string
}

// NewRunners returns the runner service. coord records registrations.
func NewRunners(db *store.DB, coord *Coordinator, log *slog.Logger) *Runners {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runners{db: db, coord: coord, log: log}
}

// WithStaticRegistrationToken accepts token, read from the server
// configuration, as a registration token besides those an admin creates.
// It lets a deployment start runners before anyone has signed in. It
// returns r for chaining.
func (r *Runners) WithStaticRegistrationToken(token string) *Runners {
	if token != "" {
		r.staticHash = auth.HashToken(token)
	}
	return r
}

// RunnerInfo is a runner as an admin sees it.
type RunnerInfo struct {
	ID    string
	Name  string
	Slots int
	// Labels are sorted.
	Labels []string
	// Remote is false for the embedded runner of all-in-one.
	Remote          bool
	ProtocolVersion int
	// Online is true when the runner called the server within the run
	// lease time.
	Online bool
	// Status is RunnerOnline, RunnerOffline or RunnerDisabled.
	Status string
	// Version is the release the runner reported, empty if unknown.
	Version string
	// ActiveRuns counts runs assigned to the runner that have not ended.
	ActiveRuns int
	LastSeenAt time.Time
	CreatedAt  time.Time
	// DisabledAt is zero while the runner is enabled. A disabled runner
	// finishes what it has but claims nothing new.
	DisabledAt time.Time
}

// Runner statuses (the API's RunnerStatus).
const (
	RunnerOnline   = "online"
	RunnerOffline  = "offline"
	RunnerDisabled = "disabled"
)

// requireRunnerAdmin is the permission check of runner administration.
func requireRunnerAdmin(p *auth.Principal) error {
	if p == nil || !p.IsAdmin {
		return ErrPermissionDenied
	}
	return nil
}

// ListRunnersInput selects a page of runners. Empty Status lists all; nil
// Limit means the default page size.
type ListRunnersInput struct {
	Status string
	Limit  *int
	Cursor string
}

// List returns a page of runners ordered by ID, and the cursor of the
// next page, empty on the last. Only admins may list runners.
func (r *Runners) List(ctx context.Context, p *auth.Principal, in ListRunnersInput) ([]RunnerInfo, string, error) {
	if err := requireRunnerAdmin(p); err != nil {
		return nil, "", err
	}
	var v validation
	limit := defaultPageSize
	if in.Limit != nil {
		limit = *in.Limit
	}
	if limit < 1 || limit > maxPageSize {
		v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxPageSize))
	}
	switch in.Status {
	case "", RunnerOnline, RunnerOffline, RunnerDisabled:
	default:
		v.param("status", "取值为 online、offline 或 disabled")
	}
	after, err := decodeRunnerCursor(in.Cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}

	cutoff := store.Now().Add(-runproto.LeaseTTL)
	q := r.db.NewSelect().Model((*store.Runner)(nil)).Order("id").Limit(int64(limit) + 1)
	if after != "" {
		q = q.Where("id > ?", after)
	}
	switch in.Status {
	case RunnerOnline:
		q = q.Where("disabled_at IS NULL AND last_seen_at > ?", cutoff)
	case RunnerOffline:
		q = q.Where("disabled_at IS NULL AND last_seen_at <= ?", cutoff)
	case RunnerDisabled:
		q = q.Where("disabled_at IS NOT NULL")
	}
	var rows []store.Runner
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, "", fmt.Errorf("list runners: %w", err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = base64.RawURLEncoding.EncodeToString([]byte(rows[limit-1].ID))
	}
	infos, err := r.infos(ctx, rows)
	return infos, next, err
}

func decodeRunnerCursor(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || !runnerIDPattern.Match(b) {
		return "", errors.New("invalid runner cursor")
	}
	return string(b), nil
}

// Get returns one runner. Only admins may read runners.
func (r *Runners) Get(ctx context.Context, p *auth.Principal, id string) (RunnerInfo, error) {
	if err := requireRunnerAdmin(p); err != nil {
		return RunnerInfo{}, err
	}
	return r.get(ctx, id)
}

func (r *Runners) get(ctx context.Context, id string) (RunnerInfo, error) {
	var row store.Runner
	err := r.db.NewSelect().Model(&row).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return RunnerInfo{}, ErrNotFound
	}
	if err != nil {
		return RunnerInfo{}, fmt.Errorf("read runner %s: %w", id, err)
	}
	infos, err := r.infos(ctx, []store.Runner{row})
	if err != nil {
		return RunnerInfo{}, err
	}
	return infos[0], nil
}

func (r *Runners) infos(ctx context.Context, rows []store.Runner) ([]RunnerInfo, error) {
	if len(rows) == 0 {
		return []RunnerInfo{}, nil
	}
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	var labels []store.RunnerLabel
	if err := r.db.NewSelect().Model(&labels).Where("runner_id IN (?)", bun.List(ids)).Order("label").Scan(ctx); err != nil {
		return nil, fmt.Errorf("runner labels: %w", err)
	}
	byRunner := map[string][]string{}
	for _, l := range labels {
		byRunner[l.RunnerID] = append(byRunner[l.RunnerID], l.Label)
	}
	var counts []struct {
		RunnerID string `bun:"runner_id"`
		N        int    `bun:"n"`
	}
	err := r.db.NewSelect().Model((*store.Run)(nil)).
		ColumnExpr("runner_id").ColumnExpr("COUNT(*) AS n").
		Where("runner_id IN (?) AND status IN (?)", bun.List(ids), bun.List(claimedStatuses)).
		GroupExpr("runner_id").Scan(ctx, &counts)
	if err != nil {
		return nil, fmt.Errorf("count active runs: %w", err)
	}
	active := map[string]int{}
	for _, c := range counts {
		active[c.RunnerID] = c.N
	}
	cutoff := store.Now().Add(-runproto.LeaseTTL)
	out := make([]RunnerInfo, len(rows))
	for i, row := range rows {
		out[i] = RunnerInfo{
			ID:              row.ID,
			Name:            row.Name,
			Slots:           row.Slots,
			Labels:          append([]string{}, byRunner[row.ID]...),
			Remote:          row.TokenHash != "",
			ProtocolVersion: row.ProtocolVersion,
			Online:          row.LastSeenAt.After(cutoff),
			Status:          runnerStatus(row, cutoff),
			Version:         row.Version,
			ActiveRuns:      active[row.ID],
			LastSeenAt:      row.LastSeenAt,
			CreatedAt:       row.CreatedAt,
			DisabledAt:      row.DisabledAt,
		}
	}
	return out, nil
}

// SetDisabled disables or enables a runner. A disabled runner keeps the
// runs it has, so they can finish, but claims no new ones. Only admins
// may change runners.
func (r *Runners) SetDisabled(ctx context.Context, p *auth.Principal, id string, disabled bool) (RunnerInfo, error) {
	if err := requireRunnerAdmin(p); err != nil {
		return RunnerInfo{}, err
	}
	err := r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		q := tx.NewUpdate().Model((*store.Runner)(nil)).Where("id = ?", id)
		action := "runner.enable"
		if disabled {
			q = q.Set("disabled_at = COALESCE(disabled_at, ?)", store.Now())
			action = "runner.disable"
		} else {
			q = q.Set("disabled_at = NULL")
		}
		if err := changedOne(q.Exec(ctx)); err != nil {
			if errors.Is(err, errNotChanged) {
				return ErrNotFound
			}
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: action, resourceKind: "runner", resourceID: id,
			result: resultSuccess,
		})
	})
	if err != nil {
		return RunnerInfo{}, err
	}
	return r.get(ctx, id)
}

// RegistrationToken describes a registration token without the token.
type RegistrationToken struct {
	ID   uuid.UUID
	Name string
	// Prefix is the start of the token, for telling tokens apart.
	Prefix    string
	CreatedAt time.Time
	// ExpiresAt is when the token stops working if unused.
	ExpiresAt time.Time
}

// runnerStatus derives a runner's status: disabled wins, then whether it
// called within the lease time.
func runnerStatus(row store.Runner, cutoff time.Time) string {
	switch {
	case !row.DisabledAt.IsZero():
		return RunnerDisabled
	case row.LastSeenAt.After(cutoff):
		return RunnerOnline
	default:
		return RunnerOffline
	}
}

// CreateRegistrationToken creates a one-time registration token valid for
// ttl and returns it once; only its hash is stored. The name is an
// optional note. Only admins may create registration tokens.
func (r *Runners) CreateRegistrationToken(ctx context.Context, p *auth.Principal, name string, ttl time.Duration) (string, RegistrationToken, error) {
	if err := requireRunnerAdmin(p); err != nil {
		return "", RegistrationToken{}, err
	}
	var v validation
	name = strings.TrimSpace(name)
	if len(name) > maxRunnerTokenName {
		v.body("#/name", fmt.Sprintf("备注最多 %d 字节", maxRunnerTokenName))
	}
	if ttl < MinRunnerTokenTTL || ttl > MaxRunnerTokenTTL {
		v.body("#/expires_in_hours", "有效期为 1 到 720 小时")
	}
	if err := v.err(); err != nil {
		return "", RegistrationToken{}, err
	}
	token, hash, err := auth.NewToken(RunnerRegistrationTokenPrefix)
	if err != nil {
		return "", RegistrationToken{}, err
	}
	now := store.Now()
	row := store.RunnerToken{
		ID:        store.NewID(),
		Name:      name,
		TokenHash: hash,
		Prefix:    token[:len(RunnerRegistrationTokenPrefix)+4],
		CreatedBy: p.UserID,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&row).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "runner_token.create", resourceKind: "runner_token", resourceID: row.ID.String(),
			result: resultSuccess, details: map[string]any{"name": name},
		})
	})
	if err != nil {
		return "", RegistrationToken{}, fmt.Errorf("create registration token: %w", err)
	}
	return token, RegistrationToken{ID: row.ID, Name: row.Name, Prefix: row.Prefix, CreatedAt: row.CreatedAt, ExpiresAt: row.ExpiresAt}, nil
}

// RevokeRegistrationToken revokes a registration token. Runners that
// already exchanged it keep their credentials. Only admins may revoke.
func (r *Runners) RevokeRegistrationToken(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	if err := requireRunnerAdmin(p); err != nil {
		return err
	}
	return r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Model((*store.RunnerToken)(nil)).
			Set("revoked_at = COALESCE(revoked_at, ?)", store.Now()).
			Where("id = ?", id).Exec(ctx)
		if err := changedOne(res, err); err != nil {
			if errors.Is(err, errNotChanged) {
				return ErrNotFound
			}
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "runner_token.revoke", resourceKind: "runner_token", resourceID: id.String(),
			result: resultSuccess,
		})
	})
}

// ErrRunnerIDInUse means a registration token was presented for a runner
// ID that a live runner holds: it was seen within runproto.LeaseTTL or
// still has runs assigned. The runner protocol answers it as a conflict
// the runner retries, so a runner that lost its credential file gets a new
// one once its old registration has gone quiet.
var ErrRunnerIDInUse = errors.New("runner ID in use")

// Exchange registers a runner that presents a registration token and
// returns a new credential for it. A token an admin created works once;
// the token from the server configuration works until the file changes.
// A wrong, used, expired or revoked token is ErrUnauthenticated. An
// invalid registration is rejected before the token is used up.
//
// The static token is shared by every runner of a deployment and runner
// IDs are visible to users, so the token alone must not let anyone take
// over a runner that is in use: whoever holds a runner's credential gets
// the credentials and inputs of the runs assigned to it and reports their
// results. Exchange therefore refuses the embedded runner's ID for good
// (ErrForbidden), and the ID of a remote runner that is live
// (ErrRunnerIDInUse). There is no admin action to reset a runner, so a
// remote runner that lost its credential file recovers by registering
// again: that succeeds once its old registration has not been seen for
// runproto.LeaseTTL and its runs have been handed back. A refused
// exchange does not use up an admin's token.
func (r *Runners) Exchange(ctx context.Context, registrationToken string, reg runproto.Registration, protocol int) (string, error) {
	labels, err := checkRegistration(reg)
	if err != nil {
		return "", err
	}
	credential, hash, err := auth.NewToken(RunnerCredentialPrefix)
	if err != nil {
		return "", err
	}
	err = r.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if err := r.checkRegistrationToken(ctx, tx, registrationToken); err != nil {
			return err
		}
		if err := claimRunnerID(ctx, tx, runnerRow(reg, protocol, hash)); err != nil {
			return err
		}
		return replaceRunnerLabels(ctx, tx, reg.ID, labels)
	})
	if errors.Is(err, ErrRunnerIDInUse) || errors.Is(err, ErrForbidden) {
		r.log.WarnContext(ctx, "runner registration refused", "runner_id", reg.ID, "error", err)
	}
	if err != nil {
		return "", err
	}
	r.log.InfoContext(ctx, "runner registered", "runner_id", reg.ID, "labels", reg.Labels, "slots", reg.Slots)
	return credential, nil
}

// claimRunnerID stores row as a new runner, or gives a quiet remote
// runner the new credential in row. The update is guarded by the
// credential read and the cutoff, so two exchanges for one ID cannot both
// win and a heartbeat in between keeps the runner.
func claimRunnerID(ctx context.Context, tx bun.Tx, row store.Runner) error {
	var old store.Runner
	err := tx.NewSelect().Model(&old).Column("id", "token_hash", "last_seen_at").Where("id = ?", row.ID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.NewInsert().Model(&row).Exec(ctx); err != nil {
			if store.IsUniqueViolation(err) {
				return fmt.Errorf("%w: runner %s registered at the same time", ErrRunnerIDInUse, row.ID)
			}
			return fmt.Errorf("register runner %s: %w", row.ID, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read runner %s: %w", row.ID, err)
	}
	if old.TokenHash == "" {
		return fmt.Errorf("%w: runner ID %s belongs to the embedded runner; choose another ID", ErrForbidden, row.ID)
	}
	cutoff := row.LastSeenAt.Add(-runproto.LeaseTTL)
	if old.LastSeenAt.After(cutoff) {
		return fmt.Errorf("%w: runner %s was seen %s ago; another runner with this ID may be running, or retry once it has stopped",
			ErrRunnerIDInUse, row.ID, row.LastSeenAt.Sub(old.LastSeenAt).Round(time.Second))
	}
	active, err := tx.NewSelect().Model((*store.Run)(nil)).
		Where("runner_id = ? AND status IN (?)", row.ID, bun.List(claimedStatuses)).Count(ctx)
	if err != nil {
		return fmt.Errorf("count runs of runner %s: %w", row.ID, err)
	}
	if active > 0 {
		return fmt.Errorf("%w: runner %s still has %d runs assigned", ErrRunnerIDInUse, row.ID, active)
	}
	res, err := tx.NewUpdate().Model(&row).
		Column("name", "slots", "protocol_version", "version", "last_seen_at", "token_hash").
		Where("id = ? AND token_hash = ? AND last_seen_at <= ?", row.ID, old.TokenHash, cutoff).
		Exec(ctx)
	if err := changedOne(res, err); err != nil {
		if errors.Is(err, errNotChanged) {
			return fmt.Errorf("%w: runner %s registered at the same time", ErrRunnerIDInUse, row.ID)
		}
		return fmt.Errorf("register runner %s: %w", row.ID, err)
	}
	return nil
}

// Update records a registered runner's name, slots, labels and protocol
// version, as Register does for the embedded runner.
func (r *Runners) Update(ctx context.Context, reg runproto.Registration, protocol int) error {
	return r.coord.register(ctx, reg, protocol)
}

// checkRegistrationToken accepts the static token or uses up an admin's
// token inside tx, so a registration refused later in tx keeps the token.
func (r *Runners) checkRegistrationToken(ctx context.Context, tx bun.Tx, token string) error {
	if token == "" {
		return ErrUnauthenticated
	}
	hash := auth.HashToken(token)
	if r.staticHash != "" && subtle.ConstantTimeCompare([]byte(hash), []byte(r.staticHash)) == 1 {
		return nil
	}
	if !strings.HasPrefix(token, RunnerRegistrationTokenPrefix) {
		return ErrUnauthenticated
	}
	now := store.Now()
	res, err := tx.NewUpdate().Model((*store.RunnerToken)(nil)).
		Set("last_used_at = ?", now).
		Where("token_hash = ? AND revoked_at IS NULL AND last_used_at IS NULL", hash).
		Where("(expires_at IS NULL OR expires_at > ?)", now).
		Exec(ctx)
	if err := changedOne(res, err); err != nil {
		if errors.Is(err, errNotChanged) {
			return ErrUnauthenticated
		}
		return err
	}
	return nil
}

// Authenticate returns the ID of the runner a credential belongs to. A
// disabled runner still authenticates: it may finish its runs. An unknown
// credential is ErrUnauthenticated.
func (r *Runners) Authenticate(ctx context.Context, credential string) (string, error) {
	if !strings.HasPrefix(credential, RunnerCredentialPrefix) {
		return "", ErrUnauthenticated
	}
	var row store.Runner
	err := r.db.NewSelect().Model(&row).Column("id").
		Where("token_hash = ?", auth.HashToken(credential)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnauthenticated
	}
	if err != nil {
		return "", fmt.Errorf("authenticate runner: %w", err)
	}
	return row.ID, nil
}

// CheckProtocol reports whether the server speaks a runner's protocol
// version.
func CheckProtocol(version int) error {
	if version < MinProtocolVersion || version > ProtocolVersion {
		return fmt.Errorf("%w: runner protocol version %d is not supported; this server speaks %d to %d",
			ErrProtocol, version, MinProtocolVersion, ProtocolVersion)
	}
	return nil
}

// insertRequiredLabels stores the labels a run requires from its runner.
func insertRequiredLabels(ctx context.Context, idb bun.IDB, runID uuid.UUID, labels []string) error {
	if len(labels) == 0 {
		return nil
	}
	labels = slices.Clone(labels)
	slices.Sort(labels)
	labels = slices.Compact(labels)
	rows := make([]store.RunRequiredLabel, len(labels))
	for i, l := range labels {
		rows[i] = store.RunRequiredLabel{ID: store.NewID(), RunID: runID, Label: l}
	}
	if _, err := idb.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("store required labels of run %s: %w", runID, err)
	}
	return nil
}
