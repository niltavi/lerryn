package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Dispatch timing (tech design 14, contract webhooks.notification).
const (
	// DispatchInterval is how often RunDispatcher looks for new events
	// and due deliveries when nothing wakes it earlier.
	DispatchInterval = 5 * time.Second
	// MergeWindow is the shortest gap between two notifications about the
	// same task on one channel. Failures within it merge into one
	// notification that carries their count.
	MergeWindow = 10 * time.Minute

	// runLookback bounds how far back the scan looks for ended runs. Runs
	// that ended earlier, for instance while every server was down, are
	// no longer news.
	runLookback = 6 * time.Hour
	// retryGrace is how long a failed attempt that may be retried waits
	// for its retry to appear before it is notified as the last attempt.
	retryGrace = 2 * time.Minute
	// credentialNotice is how long before expiry credential_expiring
	// fires.
	credentialNotice = 7 * 24 * time.Hour
	// claimLease is how long a claimed delivery is reserved for the
	// sender; after a crash another server retries it then.
	claimLease = time.Minute
	// maxDeliveryAttempts is the first attempt plus three retries.
	maxDeliveryAttempts = 4
	// dispatchBatch bounds the events and deliveries one pass handles.
	dispatchBatch = 100
	// sendConcurrency bounds parallel sends, so one slow receiver does
	// not hold up the others.
	sendConcurrency = 8
	// cleanupInterval is how often old deliveries and event markers are
	// removed.
	cleanupInterval = time.Hour
)

// retryDelays are the waits after the first, second and third failed
// attempt.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute}

// runEventTypes maps the run statuses that notify to their event type.
var runEventTypes = map[string]string{
	store.RunFailed:      notify.EventRunFailed,
	store.RunTimedOut:    notify.EventRunTimedOut,
	store.RunInterrupted: notify.EventRunInterrupted,
}

// Wake asks a running dispatcher for a pass now, for instance because a
// run just ended in this process. It never blocks.
func (n *Notifications) Wake() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// RunDispatcher dispatches notifications until ctx ends: every
// DispatchInterval, and whenever Wake is called. Every server replica may
// run it; events fan out once and each delivery is claimed by one sender.
func (n *Notifications) RunDispatcher(ctx context.Context) {
	t := time.NewTicker(DispatchInterval)
	defer t.Stop()
	var lastCleanup time.Time
	for {
		if err := n.Dispatch(ctx); err != nil && ctx.Err() == nil {
			n.log.ErrorContext(ctx, "dispatch notifications", "error", err)
		}
		if now := n.now(); now.Sub(lastCleanup) >= cleanupInterval {
			if err := n.Cleanup(ctx); err != nil && ctx.Err() == nil {
				n.log.ErrorContext(ctx, "clean up notifications", "error", err)
			}
			lastCleanup = now
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.wake:
		}
	}
}

// Dispatch makes one pass: it turns new events into deliveries and sends
// the deliveries that are due.
func (n *Notifications) Dispatch(ctx context.Context) error {
	return errors.Join(n.scanRuns(ctx), n.scanCredentials(ctx), n.deliverDue(ctx))
}

// Cleanup removes finished deliveries older than the 30 days the contract
// keeps them, and event markers no scan can reach any more.
func (n *Notifications) Cleanup(ctx context.Context) error {
	cutoff := n.now().Add(-deliveryRetention)
	return n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewDelete().Model((*store.NotificationDelivery)(nil)).
			Where("created_at < ? AND status <> ?", cutoff, store.DeliveryPending).Exec(ctx); err != nil {
			return fmt.Errorf("delete old deliveries: %w", err)
		}
		if _, err := tx.NewDelete().Model((*store.NotificationEventRecord)(nil)).
			Where("created_at < ?", cutoff).Exec(ctx); err != nil {
			return fmt.Errorf("delete old notification events: %w", err)
		}
		return nil
	})
}

// scanRuns dispatches runs that ended as failed, timed out or interrupted
// and have not been dispatched yet. Scanning the runs table, rather than
// hooking each place that ends a run, covers every path: runner reports,
// lease expiry and runs failed at claim time.
func (n *Notifications) scanRuns(ctx context.Context) error {
	now := n.now()
	var runs []store.Run
	err := n.db.NewSelect().Model(&runs).
		Where("?TableAlias.status IN (?)", bun.List([]string{store.RunFailed, store.RunTimedOut, store.RunInterrupted})).
		Where("?TableAlias.is_test = ?", false).
		Where("?TableAlias.finished_at >= ?", now.Add(-runLookback)).
		Where("NOT EXISTS (SELECT 1 FROM notification_events AS e WHERE e.run_id = ?TableAlias.id)").
		Order("finished_at", "id").
		Limit(dispatchBatch).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan ended runs: %w", err)
	}
	var errs []error
	for _, r := range runs {
		if err := n.dispatchRun(ctx, r, now); err != nil {
			errs = append(errs, fmt.Errorf("dispatch run %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}

// dispatchRun fans one ended run out to the channels subscribed to it. An
// attempt that is followed by a retry is marked without notifying: the
// retry reports the outcome. A skipped retry has no execution outcome,
// so the last actual failure is notified instead.
func (n *Notifications) dispatchRun(ctx context.Context, r store.Run, now time.Time) error {
	eventType := runEventTypes[r.Status]
	var task store.Task
	if err := n.db.NewSelect().Model(&task).Column("id", "name", "project_id").Where("id = ?", r.TaskID).Scan(ctx); err != nil {
		return fmt.Errorf("task of run: %w", err)
	}
	var project store.Project
	if err := n.db.NewSelect().Model(&project).Column("id", "name").Where("id = ?", r.ProjectID).Scan(ctx); err != nil {
		return fmt.Errorf("project of run: %w", err)
	}

	err := n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		notifyIt := true
		if r.Attempt < r.MaxAttempts {
			// RetryFailed locks this row before creating its retry. Take
			// the same lock so a retry already being created is visible
			// before the notification decision and event marker commit.
			res, err := tx.NewUpdate().Model((*store.Run)(nil)).
				Set("retry_decided_at = retry_decided_at").Where("id = ?", r.ID).Exec(ctx)
			if err != nil {
				return err
			}
			if count, err := res.RowsAffected(); err != nil || count == 0 {
				return err
			}
			var retryStatus string
			err = tx.NewSelect().Model((*store.Run)(nil)).Column("status").
				Where("task_id = ? AND retry_of = ?", r.TaskID, r.ID).Limit(1).Scan(ctx, &retryStatus)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if now.Sub(r.FinishedAt) < retryGrace {
					return nil
				}
			case err != nil:
				return err
			default:
				notifyIt = retryStatus == store.RunSkipped
			}
		}
		rec := store.NotificationEventRecord{ID: store.NewID(), EventType: eventType, RunID: r.ID, CreatedAt: now}
		first, err := markEvent(ctx, tx, &rec)
		if err != nil || !first {
			return err
		}
		if !notifyIt {
			return nil
		}
		channels, err := subscribedChannels(ctx, tx, r.ProjectID, []uuid.UUID{r.TaskID}, eventType, r.FinishedAt)
		if err != nil {
			return err
		}
		for _, ch := range channels {
			ev := notify.Event{
				Type:        eventType,
				OccurredAt:  r.FinishedAt,
				Project:     notify.ProjectRef{ID: project.ID, Name: project.Name},
				Task:        &notify.TaskRef{ID: task.ID, Name: task.Name, ProjectID: task.ProjectID},
				Run:         n.runPayload(r),
				MergedCount: 1,
				Link:        n.publicURL + "/runs/" + r.ID.String(),
			}
			if err := n.enqueue(ctx, tx, ch.channelID, "task:"+task.ID.String(), ev, r.ID, now); err != nil {
				return err
			}
		}
		return setEventDeliveries(ctx, tx, rec.ID, len(channels))
	})
	if err != nil {
		return err
	}
	return nil
}

func (n *Notifications) runPayload(r store.Run) *notify.Run {
	out := &notify.Run{
		ID:      r.ID,
		Status:  r.Status,
		Version: r.VersionSeq,
		Attempt: max(r.Attempt, 1),
		Link:    n.publicURL + "/runs/" + r.ID.String(),
	}
	if r.ErrorCode != "" {
		out.ErrorCode = &r.ErrorCode
	}
	if r.ErrorMessage != "" {
		reason := notify.Reason(r.ErrorMessage)
		out.Reason = &reason
	}
	return out
}

// scanCredentials dispatches credential_expiring for active credentials
// that expire within credentialNotice, once per credential and expiry.
func (n *Notifications) scanCredentials(ctx context.Context) error {
	now := n.now()
	var creds []store.Credential
	err := n.db.NewSelect().Model(&creds).
		Column("id", "owner_user_id", "name", "expires_at").
		Where("?TableAlias.status = ?", store.CredentialActive).
		Where("?TableAlias.revoked_at IS NULL").
		Where("?TableAlias.expires_at > ? AND ?TableAlias.expires_at <= ?", now, now.Add(credentialNotice)).
		Where("NOT EXISTS (SELECT 1 FROM notification_events AS e WHERE e.credential_id = ?TableAlias.id AND e.credential_expires_at = ?TableAlias.expires_at)").
		Order("expires_at", "id").
		Limit(dispatchBatch).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("scan expiring credentials: %w", err)
	}
	var errs []error
	for _, c := range creds {
		if err := n.dispatchCredential(ctx, c, now); err != nil {
			errs = append(errs, fmt.Errorf("dispatch credential %s: %w", c.ID, err))
		}
	}
	return errors.Join(errs...)
}

// dispatchCredential notifies the channels subscribed to tasks that use
// credential c. Each channel gets one notification naming the first such
// task it follows.
func (n *Notifications) dispatchCredential(ctx context.Context, c store.Credential, now time.Time) error {
	var tasks []store.Task
	err := n.db.NewSelect().Model(&tasks).Column("id", "name", "project_id").
		Where("archived_at IS NULL").
		Where("id IN (SELECT task_id FROM credential_grants WHERE credential_id = ?)", c.ID).
		Order("id").
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("tasks using the credential: %w", err)
	}
	byProject := map[uuid.UUID][]uuid.UUID{}
	taskByID := map[uuid.UUID]store.Task{}
	projectIDs := []uuid.UUID{}
	for _, t := range tasks {
		if _, ok := byProject[t.ProjectID]; !ok {
			projectIDs = append(projectIDs, t.ProjectID)
		}
		byProject[t.ProjectID] = append(byProject[t.ProjectID], t.ID)
		taskByID[t.ID] = t
	}
	projects, err := projectNames(ctx, n.db, projectIDs)
	if err != nil {
		return err
	}
	return n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		rec := store.NotificationEventRecord{
			ID: store.NewID(), EventType: notify.EventCredentialExpiring,
			CredentialID: c.ID, CredentialExpiresAt: c.ExpiresAt, CreatedAt: now,
		}
		first, err := markEvent(ctx, tx, &rec)
		if err != nil || !first {
			return err
		}
		sent := map[uuid.UUID]bool{}
		for _, projectID := range projectIDs {
			channels, err := subscribedChannels(ctx, tx, projectID, byProject[projectID], notify.EventCredentialExpiring, now)
			if err != nil {
				return err
			}
			for _, ch := range channels {
				if sent[ch.channelID] {
					continue
				}
				sent[ch.channelID] = true
				t := taskByID[ch.taskID]
				link := n.publicURL + "/tasks/" + t.ID.String()
				if ch.ownerID == c.OwnerUserID {
					link = n.publicURL + "/settings/credentials"
				}
				ev := notify.Event{
					Type:        notify.EventCredentialExpiring,
					OccurredAt:  now,
					Project:     notify.ProjectRef{ID: projectID, Name: projects[projectID]},
					Task:        &notify.TaskRef{ID: t.ID, Name: t.Name, ProjectID: t.ProjectID},
					Credential:  &notify.Credential{ID: c.ID, Name: c.Name, ExpiresAt: c.ExpiresAt},
					MergedCount: 1,
					Link:        link,
				}
				if err := n.enqueue(ctx, tx, ch.channelID, "", ev, uuid.Nil, now); err != nil {
					return err
				}
			}
		}
		return setEventDeliveries(ctx, tx, rec.ID, len(sent))
	})
}

// markEvent records an event as dispatched. It reports false when another
// server, or an earlier pass, already did: the unique keys of
// notification_events decide, so the event fans out once.
func markEvent(ctx context.Context, tx bun.IDB, rec *store.NotificationEventRecord) (bool, error) {
	res, err := tx.NewInsert().Model(rec).On("CONFLICT DO NOTHING").Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("mark notification event: %w", err)
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func setEventDeliveries(ctx context.Context, tx bun.IDB, id uuid.UUID, count int) error {
	if count == 0 {
		return nil
	}
	_, err := tx.NewUpdate().Model((*store.NotificationEventRecord)(nil)).
		Set("deliveries = ?", count).Where("id = ?", id).Exec(ctx)
	return err
}

// subscriber is a channel that follows an event, with the subscribed task
// that matched first.
type subscriber struct {
	channelID uuid.UUID
	ownerID   uuid.UUID
	taskID    uuid.UUID
}

// subscribedChannels returns the enabled channels with a subscription to
// eventType for the project or one of taskIDs in it, made before at. The
// channel owner must still be an active member of the project:
// subscriptions are checked when saved, and access lost later stops them.
func subscribedChannels(ctx context.Context, idb bun.IDB, projectID uuid.UUID, taskIDs []uuid.UUID, eventType string, at time.Time) ([]subscriber, error) {
	var rows []struct {
		ChannelID uuid.UUID `bun:"channel_id"`
		OwnerID   uuid.UUID `bun:"owner_user_id"`
		TaskID    uuid.UUID `bun:"task_id"`
		Events    string    `bun:"events"`
	}
	q := idb.NewSelect().
		TableExpr("notification_subscriptions AS s").
		ColumnExpr("s.channel_id, c.owner_user_id, s.task_id, s.events").
		Join("JOIN notification_channels AS c ON c.id = s.channel_id").
		Join("JOIN role_bindings AS rb ON rb.project_id = s.project_id AND rb.user_id = c.owner_user_id").
		Join("JOIN users AS u ON u.id = c.owner_user_id").
		Where("s.project_id = ?", projectID).
		Where("c.enabled = ?", true).
		Where("u.disabled_at IS NULL").
		Where("s.created_at <= ?", at).
		OrderExpr("s.channel_id, s.id")
	if len(taskIDs) > 0 {
		q = q.Where("(s.task_id IS NULL OR s.task_id IN (?))", bun.List(taskIDs))
	} else {
		q = q.Where("s.task_id IS NULL")
	}
	if err := q.Scan(ctx, &rows); err != nil {
		return nil, fmt.Errorf("subscribed channels: %w", err)
	}
	var out []subscriber
	seen := map[uuid.UUID]int{}
	for _, r := range rows {
		var events []string
		if err := json.Unmarshal([]byte(r.Events), &events); err != nil || !slices.Contains(events, eventType) {
			continue
		}
		task := r.TaskID
		if task == uuid.Nil && len(taskIDs) > 0 {
			task = taskIDs[0]
		}
		if i, ok := seen[r.ChannelID]; ok {
			// A task subscription names its task more precisely than the
			// project subscription of the same channel.
			if r.TaskID != uuid.Nil && out[i].taskID != r.TaskID {
				out[i].taskID = r.TaskID
			}
			continue
		}
		seen[r.ChannelID] = len(out)
		out = append(out, subscriber{channelID: r.ChannelID, ownerID: r.OwnerID, taskID: task})
	}
	return out, nil
}

// enqueue adds ev for a channel. With a mergeKey, a failure of a task
// merges into that task's delivery that has not been attempted yet, and
// otherwise waits until MergeWindow after the previous one; this keeps a
// task that fails over and over from flooding the channel.
func (n *Notifications) enqueue(ctx context.Context, tx bun.IDB, channelID uuid.UUID, mergeKey string, ev notify.Event, runID uuid.UUID, now time.Time) error {
	// Lock the channel row so concurrent enqueues for it on PostgreSQL
	// see each other's deliveries; SQLite serialises writers anyway.
	if _, err := tx.NewUpdate().Model((*store.NotificationChannel)(nil)).
		Set("updated_at = updated_at").Where("id = ?", channelID).Exec(ctx); err != nil {
		return err
	}
	scheduled := now
	if mergeKey != "" {
		var prev []store.NotificationDelivery
		if err := tx.NewSelect().Model(&prev).
			Where("channel_id = ? AND merge_key = ?", channelID, mergeKey).
			Order("created_at DESC", "id DESC").Limit(1).Scan(ctx); err != nil {
			return err
		}
		if len(prev) == 1 {
			p := prev[0]
			if p.Status == store.DeliveryPending && p.Attempts == 0 {
				merged, err := mergeInto(ctx, tx, p, ev, runID)
				if err != nil || merged {
					return err
				}
			}
			base := p.ScheduledAt
			if base.IsZero() {
				base = p.CreatedAt
			}
			if next := base.Add(MergeWindow); next.After(scheduled) {
				scheduled = next
			}
		}
	}
	d := store.NotificationDelivery{
		ID:            store.NewID(),
		ChannelID:     channelID,
		EventType:     ev.Type,
		MergeKey:      mergeKey,
		RunID:         runID,
		MergedCount:   1,
		Status:        store.DeliveryPending,
		NextAttemptAt: scheduled,
		ScheduledAt:   scheduled,
		CreatedAt:     now,
	}
	if ev.Task != nil {
		d.TaskID = ev.Task.ID
	}
	ev.ID, ev.MergedCount = d.ID, 1
	payload, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	d.Payload = string(payload)
	_, err = tx.NewInsert().Model(&d).Exec(ctx)
	return err
}

// mergeInto folds ev into the waiting delivery p: the payload becomes the
// latest run with the merged count. It reports false when p was claimed
// in the meantime.
func mergeInto(ctx context.Context, tx bun.IDB, p store.NotificationDelivery, ev notify.Event, runID uuid.UUID) (bool, error) {
	ev.ID, ev.MergedCount = p.ID, p.MergedCount+1
	payload, err := json.Marshal(ev)
	if err != nil {
		return false, err
	}
	res, err := tx.NewUpdate().Model((*store.NotificationDelivery)(nil)).
		Set("merged_count = ?", ev.MergedCount).
		Set("event_type = ?", ev.Type).
		Set("run_id = ?", runID).
		Set("payload = ?", string(payload)).
		Where("id = ? AND status = ? AND attempts = 0", p.ID, store.DeliveryPending).
		Exec(ctx)
	if err := changedOne(res, err); err != nil {
		if errors.Is(err, errNotChanged) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// deliverDue claims due deliveries and sends them.
func (n *Notifications) deliverDue(ctx context.Context) error {
	now := n.now()
	var due []store.NotificationDelivery
	err := n.db.NewSelect().Model(&due).
		Column("id", "attempts").
		Where("status = ? AND next_attempt_at <= ?", store.DeliveryPending, now).
		Order("next_attempt_at", "id").
		Limit(dispatchBatch).
		Scan(ctx)
	if err != nil {
		return fmt.Errorf("find due deliveries: %w", err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
		sem  = make(chan struct{}, sendConcurrency)
	)
	for _, d := range due {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := n.deliver(ctx, d.ID, d.Attempts); err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("deliver %s: %w", d.ID, err))
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

// deliver claims one delivery, sends it and records the outcome. A claim
// counts as an attempt and reserves the delivery for claimLease, so a
// server that dies mid-send leaves it to be retried, with the same
// delivery ID, by another.
func (n *Notifications) deliver(ctx context.Context, id uuid.UUID, attempts int) error {
	now := n.now()
	var d store.NotificationDelivery
	claimed := false
	err := n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewUpdate().Model((*store.NotificationDelivery)(nil)).
			Set("attempts = ?", attempts+1).
			Set("next_attempt_at = ?", now.Add(claimLease)).
			Where("id = ? AND status = ? AND attempts = ? AND next_attempt_at <= ?", id, store.DeliveryPending, attempts, now).
			Exec(ctx)
		if err := changedOne(res, err); err != nil {
			if errors.Is(err, errNotChanged) {
				return nil
			}
			return err
		}
		claimed = true
		return tx.NewSelect().Model(&d).Where("id = ?", id).Scan(ctx)
	})
	if err != nil || !claimed {
		return err
	}

	var c store.NotificationChannel
	if err := n.db.NewSelect().Model(&c).Where("id = ?", d.ChannelID).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // deleted with its deliveries
		}
		return err
	}
	var (
		status  int
		sendErr error
		detail  string
		canSend = c.Enabled
	)
	if !c.Enabled {
		detail = "渠道已停用，通知没有发送"
	} else {
		var ev notify.Event
		if err := json.Unmarshal([]byte(d.Payload), &ev); err != nil {
			return fmt.Errorf("decode payload: %w", err)
		}
		canSend, err = n.deliveryStillSubscribed(ctx, c, ev)
		if err != nil {
			return err
		}
		if !canSend {
			detail = "订阅或项目权限已失效，通知没有发送"
		} else {
			status, sendErr = n.send(ctx, c, ev, c.OwnerUserID)
			if ctx.Err() != nil {
				// Shutting down: leave the claim to expire and the delivery
				// to be retried, rather than recording a failure.
				return nil
			}
			detail = notify.Describe(sendErr)
		}
	}
	done := n.now()
	q := n.db.NewUpdate().Model((*store.NotificationDelivery)(nil)).
		Set("last_attempt_at = ?", done).
		Set("http_status = ?", nullableInt(status)).
		Where("id = ? AND attempts = ?", d.ID, d.Attempts)
	switch {
	case canSend && sendErr == nil:
		q = q.Set("status = ?", store.DeliveryDelivered).
			Set("delivered_at = ?", done).
			Set("next_attempt_at = NULL").
			Set("error = NULL")
	case canSend && d.Attempts < maxDeliveryAttempts:
		q = q.Set("next_attempt_at = ?", done.Add(retryDelays[d.Attempts-1])).
			Set("error = ?", detail)
	default:
		q = q.Set("status = ?", store.DeliveryFailed).
			Set("next_attempt_at = NULL").
			Set("error = ?", detail)
	}
	err = n.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		_, err := q.Conn(tx).Exec(ctx)
		return err
	})
	if sendErr != nil {
		n.log.InfoContext(ctx, "notification attempt failed",
			"delivery_id", d.ID, "channel_id", c.ID, "kind", c.Kind, "attempt", d.Attempts, "http_status", status, "error", sendErr)
	}
	return err
}

// A delivery may have waited through a merge window or retry delay since
// fan-out. Recheck current access and subscriptions before sending its data.
func (n *Notifications) deliveryStillSubscribed(ctx context.Context, c store.NotificationChannel, ev notify.Event) (bool, error) {
	p := &auth.Principal{UserID: c.OwnerUserID}
	if err := n.authz.Require(ctx, p, ev.Project.ID, ActionViewTask); err != nil {
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrPermissionDenied) {
			return false, nil
		}
		return false, err
	}
	var tasks []uuid.UUID
	if ev.Task != nil {
		tasks = append(tasks, ev.Task.ID)
	}
	channels, err := subscribedChannels(ctx, n.db, ev.Project.ID, tasks, ev.Type, n.now())
	if err != nil {
		return false, err
	}
	for _, ch := range channels {
		if ch.channelID == c.ID {
			return true, nil
		}
	}
	return false, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
