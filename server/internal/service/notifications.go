package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Notification channel rules (API schemas CreateNotificationChannelRequest
// and NotificationDelivery).
const (
	maxChannelNameRunes   = 64
	maxChannelURLBytes    = 2048
	maxChannelsPerUser    = 50
	maxChannelSubscribers = 20
	// deliveryRetention is how long delivery records are kept.
	deliveryRetention = 30 * 24 * time.Hour
)

// Audit actions of notification channels.
const (
	auditChannelCreate = "notification_channel.create"
	auditChannelUpdate = "notification_channel.update"
	auditChannelDelete = "notification_channel.delete"
	auditChannelTest   = "notification_channel.test"
	resourceChannel    = "notification_channel"
)

// PlainKEKID marks a channel secret stored without encryption. It is what
// PlainSealer writes until a sealer backed by the master key is wired in;
// openSecret keeps reading such rows after that.
const PlainKEKID = "plain"

// SealedSecret is a channel secret as stored: ciphertext, the wrapped data
// key and the ID of the key that wrapped it (tech design 12.3).
type SealedSecret struct {
	Ciphertext []byte
	WrappedDEK []byte
	KEKID      string
}

// ChannelSealer encrypts channel secrets, the target URL and the signing
// secret, at rest. The credential encryption of tech design 12.3 is meant
// to implement it.
type ChannelSealer interface {
	Seal(ctx context.Context, plaintext []byte) (SealedSecret, error)
	Open(ctx context.Context, s SealedSecret) ([]byte, error)
}

// PlainSealer stores channel secrets as they are, marked PlainKEKID. The
// secrets are still write-only through the API: responses carry only the
// masked URL. Replace it with an encrypting sealer when one is available.
type PlainSealer struct{}

// Seal implements ChannelSealer.
func (PlainSealer) Seal(_ context.Context, plaintext []byte) (SealedSecret, error) {
	return SealedSecret{Ciphertext: plaintext, WrappedDEK: []byte{}, KEKID: PlainKEKID}, nil
}

// Open implements ChannelSealer.
func (PlainSealer) Open(_ context.Context, s SealedSecret) ([]byte, error) {
	if s.KEKID != PlainKEKID {
		return nil, fmt.Errorf("open channel secret: key %q is not available", s.KEKID)
	}
	return s.Ciphertext, nil
}

// NotificationSender posts one notification; *notify.Sender implements it.
type NotificationSender interface {
	Send(ctx context.Context, kind, target string, req notify.Request) (int, error)
}

// NotificationsOptions configures the notification service.
type NotificationsOptions struct {
	// PublicURL is server.public_url; console links start with it.
	PublicURL string
	// Guard checks target addresses; nil allows public addresses only.
	Guard *notify.Guard
	// Sender posts notifications; nil uses a notify.Sender with Guard.
	Sender NotificationSender
	// Sealer encrypts channel secrets; nil uses PlainSealer.
	Sealer ChannelSealer
	// Now is the clock; nil uses store.Now. Tests move it to exercise
	// retries and merging.
	Now    func() time.Time
	Logger *slog.Logger
}

// Notifications manages a user's notification channels and dispatches
// notifications to them (tech design 14).
type Notifications struct {
	db        *store.DB
	authz     *Authorizer
	publicURL string
	guard     *notify.Guard
	sender    NotificationSender
	sealer    ChannelSealer
	now       func() time.Time
	log       *slog.Logger
	wake      chan struct{}
}

// NewNotifications returns the notification service.
func NewNotifications(db *store.DB, authz *Authorizer, opts NotificationsOptions) *Notifications {
	n := &Notifications{
		db:        db,
		authz:     authz,
		publicURL: strings.TrimRight(opts.PublicURL, "/"),
		guard:     opts.Guard,
		sender:    opts.Sender,
		sealer:    opts.Sealer,
		now:       opts.Now,
		log:       opts.Logger,
		wake:      make(chan struct{}, 1),
	}
	if n.guard == nil {
		n.guard, _ = notify.NewGuard(nil)
	}
	if n.sender == nil {
		n.sender = notify.NewSender(n.guard, 0)
	}
	if n.sealer == nil {
		n.sealer = PlainSealer{}
	}
	if n.now == nil {
		n.now = store.Now
	}
	if n.log == nil {
		n.log = slog.New(slog.DiscardHandler)
	}
	return n
}

// SubscriptionInput subscribes a channel to events of a project, or of
// one task in it when TaskID is set.
type SubscriptionInput struct {
	ProjectID uuid.UUID
	TaskID    uuid.UUID
	Events    []string
}

// CreateChannelInput is a new notification channel.
type CreateChannelInput struct {
	Name          string
	Kind          string
	URL           string
	Enabled       *bool
	Subscriptions []SubscriptionInput
}

// UpdateChannelInput changes a channel. Nil fields stay as they are; a
// non-nil Subscriptions replaces them all.
type UpdateChannelInput struct {
	Name          *string
	URL           *string
	Enabled       *bool
	Subscriptions *[]SubscriptionInput
}

// ChannelDetail is a channel with its currently visible subscriptions and
// last delivery. Inaccessible subscriptions remain stored on the channel.
type ChannelDetail struct {
	Channel       store.NotificationChannel
	Subscriptions []SubscriptionDetail
	LastDelivery  *DeliveryDetail
}

// SubscriptionDetail is a subscription with the names it points at.
type SubscriptionDetail struct {
	Subscription store.NotificationSubscription
	ProjectName  string
	// Task is nil for a subscription to the whole project.
	Task *TaskRef
}

// TaskRef names a task.
type TaskRef struct {
	ID        uuid.UUID
	Name      string
	ProjectID uuid.UUID
}

// DeliveryDetail is a delivery with the task it is about, nil for tests
// and when the task is gone or no longer visible to the channel owner.
type DeliveryDetail struct {
	Delivery store.NotificationDelivery
	Task     *TaskRef
}

// channelSecret is the encrypted part of a channel.
type channelSecret struct {
	URL           string `json:"url"`
	SigningSecret string `json:"signing_secret,omitempty"`
}

// ListChannels returns p's channels, oldest first.
func (n *Notifications) ListChannels(ctx context.Context, p *auth.Principal) ([]ChannelDetail, error) {
	if p == nil {
		return nil, ErrUnauthenticated
	}
	var rows []store.NotificationChannel
	if err := n.db.NewSelect().Model(&rows).Where("owner_user_id = ?", p.UserID).Order("id").Scan(ctx); err != nil {
		return nil, fmt.Errorf("list notification channels: %w", err)
	}
	out := make([]ChannelDetail, len(rows))
	for i, c := range rows {
		d, err := n.detail(ctx, n.db, c)
		if err != nil {
			return nil, err
		}
		out[i] = d
	}
	return out, nil
}

// CreateChannel adds a channel for p. For generic webhooks it also returns
// the signing secret, which is never shown again.
func (n *Notifications) CreateChannel(ctx context.Context, p *auth.Principal, in CreateChannelInput) (ChannelDetail, string, error) {
	if p == nil {
		return ChannelDetail{}, "", ErrUnauthenticated
	}
	var v validation
	name := checkChannelName(&v, in.Name)
	if !slices.Contains(notify.Kinds, in.Kind) {
		v.body("#/kind", "取值为 webhook、feishu 或 slack")
	}
	n.checkURL(&v, in.URL)
	n.checkSubscriptionShapes(&v, in.Subscriptions)
	if err := v.err(); err != nil {
		return ChannelDetail{}, "", err
	}
	if err := n.checkSubscriptionAccess(ctx, p, in.Subscriptions); err != nil {
		return ChannelDetail{}, "", err
	}

	secret := channelSecret{URL: in.URL}
	if in.Kind == notify.KindWebhook {
		secret.SigningSecret = notify.NewSigningSecret()
	}
	sealed, err := n.seal(ctx, secret)
	if err != nil {
		return ChannelDetail{}, "", err
	}
	now := n.now()
	c := store.NotificationChannel{
		ID:               store.NewID(),
		OwnerUserID:      p.UserID,
		Name:             name,
		Kind:             in.Kind,
		Enabled:          in.Enabled == nil || *in.Enabled,
		URLPreview:       notify.Preview(in.URL),
		SecretCiphertext: sealed.Ciphertext,
		SecretWrappedDEK: sealed.WrappedDEK,
		SecretKEKID:      sealed.KEKID,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	var d ChannelDetail
	err = n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		count, err := tx.NewSelect().Model((*store.NotificationChannel)(nil)).Where("owner_user_id = ?", p.UserID).Count(ctx)
		if err != nil {
			return err
		}
		if count >= maxChannelsPerUser {
			v := validation{}
			v.content("#", fmt.Sprintf("每人最多 %d 个通知渠道", maxChannelsPerUser))
			return v.err()
		}
		if _, err := tx.NewInsert().Model(&c).Exec(ctx); err != nil {
			return err
		}
		if err := replaceSubscriptions(ctx, tx, c.ID, in.Subscriptions, now); err != nil {
			return err
		}
		if err := n.audit(ctx, tx, p, auditChannelCreate, c, nil); err != nil {
			return err
		}
		d, err = n.detail(ctx, tx, c)
		return err
	})
	if err != nil {
		return ChannelDetail{}, "", err
	}
	return d, secret.SigningSecret, nil
}

// UpdateChannel changes one of p's channels.
func (n *Notifications) UpdateChannel(ctx context.Context, p *auth.Principal, id uuid.UUID, in UpdateChannelInput) (ChannelDetail, error) {
	if p == nil {
		return ChannelDetail{}, ErrUnauthenticated
	}
	var v validation
	var name string
	if in.Name != nil {
		name = checkChannelName(&v, *in.Name)
	}
	if in.URL != nil {
		n.checkURL(&v, *in.URL)
	}
	if in.Subscriptions != nil {
		n.checkSubscriptionShapes(&v, *in.Subscriptions)
	}
	if err := v.err(); err != nil {
		return ChannelDetail{}, err
	}
	c, err := n.ownChannel(ctx, n.db, p, id)
	if err != nil {
		return ChannelDetail{}, err
	}
	if in.Subscriptions != nil {
		if err := n.checkSubscriptionAccess(ctx, p, *in.Subscriptions); err != nil {
			return ChannelDetail{}, err
		}
	}
	var sealed *SealedSecret
	if in.URL != nil {
		// The signing secret stays: receivers keep verifying with it.
		secret, err := n.openSecret(ctx, c)
		if err != nil {
			return ChannelDetail{}, err
		}
		secret.URL = *in.URL
		s, err := n.seal(ctx, secret)
		if err != nil {
			return ChannelDetail{}, err
		}
		sealed = &s
	}

	var d ChannelDetail
	err = n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		now := n.now()
		q := tx.NewUpdate().Model((*store.NotificationChannel)(nil)).
			Set("updated_at = ?", now).
			Where("id = ? AND owner_user_id = ?", id, p.UserID)
		changed := []string{}
		if in.Name != nil {
			q = q.Set("name = ?", name)
			changed = append(changed, "name")
		}
		if in.Enabled != nil {
			q = q.Set("enabled = ?", *in.Enabled)
			changed = append(changed, "enabled")
		}
		if sealed != nil {
			q = q.Set("url_preview = ?", notify.Preview(*in.URL)).
				Set("secret_ciphertext = ?", sealed.Ciphertext).
				Set("secret_wrapped_dek = ?", sealed.WrappedDEK).
				Set("secret_kek_id = ?", sealed.KEKID)
			changed = append(changed, "url")
		}
		if err := changedOne(q.Exec(ctx)); err != nil {
			if errors.Is(err, errNotChanged) {
				return ErrNotFound
			}
			return err
		}
		if in.Subscriptions != nil {
			if err := replaceSubscriptions(ctx, tx, id, *in.Subscriptions, now); err != nil {
				return err
			}
			changed = append(changed, "subscriptions")
		}
		c, err := n.ownChannel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		if err := n.audit(ctx, tx, p, auditChannelUpdate, c, map[string]any{"changed": changed}); err != nil {
			return err
		}
		d, err = n.detail(ctx, tx, c)
		return err
	})
	return d, err
}

// DeleteChannel deletes one of p's channels with its subscriptions and
// deliveries; pending notifications are dropped.
func (n *Notifications) DeleteChannel(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	if p == nil {
		return ErrUnauthenticated
	}
	return n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		c, err := n.ownChannel(ctx, tx, p, id)
		if err != nil {
			return err
		}
		// Deleted explicitly rather than through ON DELETE CASCADE, so the
		// outcome does not depend on SQLite's foreign key setting.
		if _, err := tx.NewDelete().Model((*store.NotificationDelivery)(nil)).Where("channel_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*store.NotificationSubscription)(nil)).Where("channel_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*store.NotificationChannel)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		return n.audit(ctx, tx, p, auditChannelDelete, c, nil)
	})
}

// TestChannel sends a test notification to one of p's channels right
// away and records the outcome. It is not retried.
func (n *Notifications) TestChannel(ctx context.Context, p *auth.Principal, id uuid.UUID) (DeliveryDetail, error) {
	if p == nil {
		return DeliveryDetail{}, ErrUnauthenticated
	}
	c, err := n.ownChannel(ctx, n.db, p, id)
	if err != nil {
		return DeliveryDetail{}, err
	}
	project, err := n.testProject(ctx, p, id)
	if err != nil {
		return DeliveryDetail{}, err
	}
	now := n.now()
	d := store.NotificationDelivery{
		ID:          store.NewID(),
		ChannelID:   c.ID,
		EventType:   notify.EventTest,
		MergedCount: 1,
		Status:      store.DeliveryPending,
		Attempts:    1,
		CreatedAt:   now,
		ScheduledAt: now,
	}
	ev := notify.Event{
		ID:          d.ID,
		Type:        notify.EventTest,
		OccurredAt:  now,
		Project:     project,
		MergedCount: 1,
		Link:        n.publicURL + "/",
	}
	payload, err := json.Marshal(ev)
	if err != nil {
		return DeliveryDetail{}, err
	}
	d.Payload = string(payload)
	status, sendErr := n.send(ctx, c, ev, p.UserID)
	d.HTTPStatus, d.LastAttemptAt = status, n.now()
	if sendErr != nil {
		d.Status, d.Error = store.DeliveryFailed, notify.Describe(sendErr)
		n.log.InfoContext(ctx, "test notification failed", "channel_id", c.ID, "http_status", status, "error", sendErr)
	} else {
		d.Status, d.DeliveredAt = store.DeliveryDelivered, d.LastAttemptAt
	}
	err = n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewInsert().Model(&d).Exec(ctx); err != nil {
			return err
		}
		return n.audit(ctx, tx, p, auditChannelTest, c, map[string]any{"status": d.Status})
	})
	if err != nil {
		if store.IsForeignKeyViolation(err) {
			// Deleted while the test was being sent.
			return DeliveryDetail{}, ErrNotFound
		}
		return DeliveryDetail{}, err
	}
	return DeliveryDetail{Delivery: d}, nil
}

// testProject names the first subscribed project p can still view, or p's
// personal project when none remain accessible.
func (n *Notifications) testProject(ctx context.Context, p *auth.Principal, channelID uuid.UUID) (notify.ProjectRef, error) {
	var subs []store.NotificationSubscription
	if err := n.db.NewSelect().Model(&subs).Column("project_id").Where("channel_id = ?", channelID).Order("id").Scan(ctx); err != nil {
		return notify.ProjectRef{}, fmt.Errorf("subscriptions of test notification: %w", err)
	}
	ids := make([]uuid.UUID, 0, len(subs)+1)
	for _, s := range subs {
		ids = append(ids, s.ProjectID)
	}
	ids = append(ids, p.PersonalProjectID)
	names, err := visibleNotificationProjectNames(ctx, n.db, p.UserID, ids)
	if err != nil {
		return notify.ProjectRef{}, err
	}
	for _, id := range ids {
		if name, ok := names[id]; ok {
			return notify.ProjectRef{ID: id, Name: name}, nil
		}
	}
	return notify.ProjectRef{}, ErrNotFound
}

// ListDeliveries returns a page of the deliveries of one of p's channels,
// newest first, optionally only those with status.
func (n *Notifications) ListDeliveries(ctx context.Context, p *auth.Principal, channelID uuid.UUID, status string, limitParam *int, cursor string) ([]DeliveryDetail, string, error) {
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
	if status != "" && !slices.Contains([]string{store.DeliveryPending, store.DeliveryDelivered, store.DeliveryFailed}, status) {
		v.param("status", "取值为 pending、delivered 或 failed")
	}
	after, err := decodeCursor(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	if _, err := n.ownChannel(ctx, n.db, p, channelID); err != nil {
		return nil, "", err
	}
	q := n.db.NewSelect().Model((*store.NotificationDelivery)(nil)).
		Where("channel_id = ?", channelID).
		Order("id DESC").
		Limit(int64(limit) + 1)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if after != uuid.Nil {
		q = q.Where("id < ?", after)
	}
	var rows []store.NotificationDelivery
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, "", fmt.Errorf("list deliveries of channel %s: %w", channelID, err)
	}
	next := ""
	if len(rows) > limit {
		rows = rows[:limit]
		next = encodeCursor(rows[limit-1].ID)
	}
	out, err := deliveryDetails(ctx, n.db, p.UserID, rows)
	return out, next, err
}

// ownChannel reads one of p's channels; other users' channels are
// ErrNotFound.
func (n *Notifications) ownChannel(ctx context.Context, idb bun.IDB, p *auth.Principal, id uuid.UUID) (store.NotificationChannel, error) {
	var c store.NotificationChannel
	err := idb.NewSelect().Model(&c).Where("id = ? AND owner_user_id = ?", id, p.UserID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, fmt.Errorf("read notification channel %s: %w", id, err)
	}
	return c, nil
}

func checkChannelName(v *validation, name string) string {
	name = strings.TrimSpace(name)
	if n := utf8.RuneCountInString(name); n < 1 || n > maxChannelNameRunes {
		v.body("#/name", fmt.Sprintf("名称为 1 到 %d 个字符", maxChannelNameRunes))
	}
	return name
}

func (n *Notifications) checkURL(v *validation, raw string) {
	switch err := n.guard.CheckURL(raw); {
	case len(raw) > maxChannelURLBytes:
		v.body("#/url", fmt.Sprintf("目标地址最长 %d 个字节", maxChannelURLBytes))
	case errors.Is(err, notify.ErrBlockedAddress):
		v.content("#/url", "目标地址指向内网或本机，部署没有允许这类地址")
	case err != nil:
		v.body("#/url", "目标地址必须是完整的 http 或 https 地址")
	}
}

// checkSubscriptionShapes validates subscriptions without the database.
func (n *Notifications) checkSubscriptionShapes(v *validation, subs []SubscriptionInput) {
	if len(subs) > maxChannelSubscribers {
		v.body("#/subscriptions", fmt.Sprintf("最多 %d 条订阅", maxChannelSubscribers))
	}
	for i, s := range subs {
		at := fmt.Sprintf("#/subscriptions/%d", i)
		if s.ProjectID == uuid.Nil {
			v.body(at+"/project_id", "缺少项目")
		}
		if len(s.Events) == 0 {
			v.body(at+"/events", "至少订阅一种事件")
		}
		for j, e := range s.Events {
			if !slices.Contains(notify.SubscribableEvents, e) {
				v.body(fmt.Sprintf("%s/events/%d", at, j), "取值为 run_failed、run_timed_out、run_interrupted 或 credential_expiring")
			} else if slices.Index(s.Events, e) != j {
				v.body(fmt.Sprintf("%s/events/%d", at, j), "事件重复")
			}
		}
	}
}

// checkSubscriptionAccess makes sure p can view every subscribed project
// and that each task belongs to its project. Anything p cannot see is
// ErrNotFound, like the resource itself would be.
func (n *Notifications) checkSubscriptionAccess(ctx context.Context, p *auth.Principal, subs []SubscriptionInput) error {
	for _, s := range subs {
		if err := n.authz.Require(ctx, p, s.ProjectID, ActionViewTask); err != nil {
			return err
		}
		if s.TaskID == uuid.Nil {
			continue
		}
		exists, err := n.db.NewSelect().Model((*store.Task)(nil)).
			Where("id = ? AND project_id = ?", s.TaskID, s.ProjectID).Exists(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
	}
	return nil
}

// replaceSubscriptions makes subs the subscriptions of the channel. They
// keep their position through their UUIDv7 IDs.
func replaceSubscriptions(ctx context.Context, tx bun.IDB, channelID uuid.UUID, subs []SubscriptionInput, now time.Time) error {
	if _, err := tx.NewDelete().Model((*store.NotificationSubscription)(nil)).Where("channel_id = ?", channelID).Exec(ctx); err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}
	rows := make([]store.NotificationSubscription, len(subs))
	for i, s := range subs {
		rows[i] = store.NotificationSubscription{
			ID:        store.NewID(),
			ChannelID: channelID,
			ProjectID: s.ProjectID,
			TaskID:    s.TaskID,
			Events:    slices.Clone(s.Events),
			CreatedAt: now,
		}
	}
	_, err := tx.NewInsert().Model(&rows).Exec(ctx)
	return err
}

// detail loads the subscriptions and last delivery of c.
func (n *Notifications) detail(ctx context.Context, idb bun.IDB, c store.NotificationChannel) (ChannelDetail, error) {
	d := ChannelDetail{Channel: c, Subscriptions: []SubscriptionDetail{}}
	var subs []store.NotificationSubscription
	if err := idb.NewSelect().Model(&subs).Where("channel_id = ?", c.ID).Order("id").Scan(ctx); err != nil {
		return d, fmt.Errorf("subscriptions of channel %s: %w", c.ID, err)
	}
	projectIDs := make([]uuid.UUID, 0, len(subs))
	for _, s := range subs {
		projectIDs = append(projectIDs, s.ProjectID)
	}
	projects, err := visibleNotificationProjectNames(ctx, idb, c.OwnerUserID, projectIDs)
	if err != nil {
		return d, err
	}
	taskIDs := []uuid.UUID{}
	for _, s := range subs {
		if _, ok := projects[s.ProjectID]; ok && s.TaskID != uuid.Nil {
			taskIDs = append(taskIDs, s.TaskID)
		}
	}
	tasks, err := taskRefs(ctx, idb, c.OwnerUserID, taskIDs)
	if err != nil {
		return d, err
	}
	for _, s := range subs {
		name, ok := projects[s.ProjectID]
		if !ok {
			// Owning a channel does not grant access to its old projects.
			// Keep the stored subscription so restoring membership can use it.
			continue
		}
		sd := SubscriptionDetail{Subscription: s, ProjectName: name}
		if t, ok := tasks[s.TaskID]; ok {
			sd.Task = &t
		}
		d.Subscriptions = append(d.Subscriptions, sd)
	}
	var last []store.NotificationDelivery
	if err := idb.NewSelect().Model(&last).Where("channel_id = ?", c.ID).Order("id DESC").Limit(1).Scan(ctx); err != nil {
		return d, fmt.Errorf("last delivery of channel %s: %w", c.ID, err)
	}
	if len(last) == 1 {
		details, err := deliveryDetails(ctx, idb, c.OwnerUserID, last)
		if err != nil {
			return d, err
		}
		d.LastDelivery = &details[0]
	}
	return d, nil
}

func deliveryDetails(ctx context.Context, idb bun.IDB, ownerID uuid.UUID, rows []store.NotificationDelivery) ([]DeliveryDetail, error) {
	ids := []uuid.UUID{}
	for _, r := range rows {
		if r.TaskID != uuid.Nil {
			ids = append(ids, r.TaskID)
		}
	}
	tasks, err := taskRefs(ctx, idb, ownerID, ids)
	if err != nil {
		return nil, err
	}
	out := make([]DeliveryDetail, len(rows))
	for i, r := range rows {
		out[i] = DeliveryDetail{Delivery: r}
		if t, ok := tasks[r.TaskID]; ok {
			out[i].Task = &t
		}
	}
	return out, nil
}

func projectNames(ctx context.Context, idb bun.IDB, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []store.Project
	if err := idb.NewSelect().Model(&rows).Column("id", "name").Where("id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("project names: %w", err)
	}
	for _, r := range rows {
		out[r.ID] = r.Name
	}
	return out, nil
}

// visibleNotificationProjectNames reads names and access through the same
// transaction as the channel projection. A failed access lookup must fail the
// request rather than pretend a subscription was removed.
func visibleNotificationProjectNames(ctx context.Context, idb bun.IDB, ownerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID   uuid.UUID
		Name string
		Role string
	}
	if err := idb.NewSelect().TableExpr("projects AS p").
		ColumnExpr("p.id, p.name, rb.role").
		Join("JOIN role_bindings AS rb ON rb.project_id = p.id").
		Join("JOIN users AS u ON u.id = rb.user_id").
		Where("rb.user_id = ?", ownerID).
		Where("u.disabled_at IS NULL").
		Where("p.id IN (?)", bun.List(uniqueIDs(ids))).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("visible notification project names: %w", err)
	}
	for _, r := range rows {
		if slices.Contains(roleActions[r.Role], ActionViewTask) {
			out[r.ID] = r.Name
		}
	}
	return out, nil
}

func taskRefs(ctx context.Context, idb bun.IDB, ownerID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]TaskRef, error) {
	out := map[uuid.UUID]TaskRef{}
	if len(ids) == 0 {
		return out, nil
	}
	var rows []struct {
		ID        uuid.UUID
		Name      string
		ProjectID uuid.UUID
		Role      string
	}
	if err := idb.NewSelect().TableExpr("tasks AS t").
		ColumnExpr("t.id, t.name, t.project_id, rb.role").
		Join("JOIN role_bindings AS rb ON rb.project_id = t.project_id").
		Join("JOIN users AS u ON u.id = rb.user_id").
		Where("rb.user_id = ?", ownerID).
		Where("u.disabled_at IS NULL").
		Where("t.id IN (?)", bun.List(uniqueIDs(ids))).Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("task names: %w", err)
	}
	for _, r := range rows {
		if slices.Contains(roleActions[r.Role], ActionViewTask) {
			out[r.ID] = TaskRef{ID: r.ID, Name: r.Name, ProjectID: r.ProjectID}
		}
	}
	return out, nil
}

func (n *Notifications) seal(ctx context.Context, s channelSecret) (SealedSecret, error) {
	plain, err := json.Marshal(s)
	if err != nil {
		return SealedSecret{}, err
	}
	sealed, err := n.sealer.Seal(ctx, plain)
	if err != nil {
		return SealedSecret{}, fmt.Errorf("seal channel secret: %w", err)
	}
	return sealed, nil
}

// openSecret decrypts the target and signing secret of c. Rows written by
// PlainSealer stay readable after an encrypting sealer is configured.
func (n *Notifications) openSecret(ctx context.Context, c store.NotificationChannel) (channelSecret, error) {
	sealed := SealedSecret{Ciphertext: c.SecretCiphertext, WrappedDEK: c.SecretWrappedDEK, KEKID: c.SecretKEKID}
	var (
		plain []byte
		err   error
	)
	if sealed.KEKID == PlainKEKID {
		plain, err = PlainSealer{}.Open(ctx, sealed)
	} else {
		plain, err = n.sealer.Open(ctx, sealed)
	}
	if err != nil {
		return channelSecret{}, fmt.Errorf("open secret of channel %s: %w", c.ID, err)
	}
	var s channelSecret
	if err := json.Unmarshal(plain, &s); err != nil {
		return channelSecret{}, fmt.Errorf("decode secret of channel %s: %w", c.ID, err)
	}
	return s, nil
}

// send renders ev for c and posts it. owner's time zone, when set, is used
// for times in the text.
func (n *Notifications) send(ctx context.Context, c store.NotificationChannel, ev notify.Event, owner uuid.UUID) (int, error) {
	secret, err := n.openSecret(ctx, c)
	if err != nil {
		return 0, err
	}
	req, err := notify.Build(c.Kind, ev, secret.SigningSecret, time.Now(), n.ownerLocation(ctx, owner))
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, notify.Timeout)
	defer cancel()
	return n.sender.Send(ctx, c.Kind, secret.URL, req)
}

func (n *Notifications) ownerLocation(ctx context.Context, userID uuid.UUID) *time.Location {
	var u store.User
	if err := n.db.NewSelect().Model(&u).Column("timezone").Where("id = ?", userID).Scan(ctx); err != nil || u.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(u.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

func (n *Notifications) audit(ctx context.Context, tx bun.IDB, p *auth.Principal, action string, c store.NotificationChannel, extra map[string]any) error {
	details := map[string]any{"name": c.Name, "kind": c.Kind}
	for k, v := range extra {
		details[k] = v
	}
	kind, actor := actorOf(p)
	return writeAudit(ctx, tx, auditEntry{
		actorKind: kind, actorID: actor,
		action: action, resourceKind: resourceChannel, resourceID: c.ID.String(),
		result: resultSuccess, details: details,
	})
}
