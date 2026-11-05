package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/scheduler"
	"github.com/niltavi/lerryn/server/internal/store"
)

// SkipNext skips the earliest pending cron time, including every trigger at
// that time. The expected time prevents a stale or repeated request from
// skipping a later occurrence. Recording and moving the schedule are atomic.
func (t *Triggers) SkipNext(ctx context.Context, p *auth.Principal, taskID uuid.UUID, expected time.Time) (TriggerSet, error) {
	task, err := t.loadTask(ctx, p, taskID, ActionModifyTask)
	if err != nil {
		return TriggerSet{}, err
	}
	var rows []store.Trigger
	var now time.Time
	err = t.writeRetrying(ctx, func(ctx context.Context, tx bun.Tx) error {
		// Match Set's task-then-trigger lock order. Reread the task after
		// locking so a concurrent pause or archive cannot be overlooked.
		if err := tx.NewUpdate().Model(&task).Set("updated_at = updated_at").
			WherePK().Returning("*").Scan(ctx); err != nil {
			return err
		}
		if !task.ArchivedAt.IsZero() {
			return ErrTaskArchived
		}
		if _, err := tx.NewUpdate().Model((*store.Trigger)(nil)).
			Set("updated_at = updated_at").Where("task_id = ?", taskID).Exec(ctx); err != nil {
			return err
		}
		rows = nil
		if err := tx.NewSelect().Model(&rows).Where("task_id = ?", taskID).Order("name").Scan(ctx); err != nil {
			return err
		}
		now = t.now()
		var next time.Time
		for _, row := range rows {
			if row.Type == store.TriggerTypeCron && row.Enabled && !row.NextFireAt.IsZero() &&
				(next.IsZero() || row.NextFireAt.Before(next)) {
				next = row.NextFireAt
			}
		}
		if !taskCanFire(task) || next.IsZero() || !next.After(now) || !next.Equal(expected) {
			v := validation{unprocessable: true}
			v.content("#/scheduled_at", "下次执行时间已变化或任务已停用，请刷新后重新确认")
			return v.err()
		}
		version, err := loadVersionByID(ctx, tx, task.ActiveVersionID)
		if err != nil {
			return err
		}
		ids := []string{}
		for i := range rows {
			row := &rows[i]
			if row.Type != store.TriggerTypeCron || !row.Enabled || !row.NextFireAt.Equal(next) {
				continue
			}
			c, err := scheduler.ParseCron(row.Schedule, row.Timezone)
			if err != nil {
				return fmt.Errorf("parse cron for skip: %w", err)
			}
			params, err := triggerParams(*row, version.Manifest)
			if err != nil {
				return err
			}
			if _, err := t.recordFire(ctx, tx, runSource{
				task: task, version: version, kind: store.TriggerCron, triggerID: row.ID,
				scheduledAt: next, params: params, createdBy: p.UserID, now: now,
				skip: store.SkipManual,
			}, 0); err != nil {
				return err
			}
			row.NextFireAt, row.UpdatedAt = c.Next(next), now
			if _, err := tx.NewUpdate().Model(row).WherePK().Column("next_fire_at", "updated_at").Exec(ctx); err != nil {
				return err
			}
			ids = append(ids, row.ID.String())
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor, action: "task.skip_next", resourceKind: "task", resourceID: taskID.String(),
			result:  resultSuccess,
			details: map[string]any{"project_id": task.ProjectID.String(), "scheduled_at": next, "trigger_ids": ids},
		})
	})
	if err != nil {
		return TriggerSet{}, err
	}
	return t.view(task, rows, nil, false, now), nil
}
