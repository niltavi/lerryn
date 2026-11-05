package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/scheduler"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Scheduling rules (tech design 8.2).
const (
	// fireBatch bounds the triggers one FireDue call processes; the rest
	// wait for the next tick, a second later.
	fireBatch = 200
	// maxCountedMisses bounds how many missed times are counted one by
	// one after a long outage; beyond it the count is a lower bound.
	maxCountedMisses = 100_000
)

// FireDue processes the cron triggers whose next fire time has come by
// now and returns how many runs, queued or skipped, it recorded. Call it
// every second from the leader. Each trigger is processed in its own
// transaction that records the fire, creates the run or the skipped
// record, and moves next_fire_at on; two servers processing the same
// trigger at once cannot both succeed, because the move is a compare and
// swap and (trigger, scheduled time) is unique.
//
// Triggers of tasks that are not active only move on: nothing is
// recorded for the times they would have fired.
func (t *Triggers) FireDue(ctx context.Context, now time.Time) (int, error) {
	var due []store.Trigger
	err := t.db.NewSelect().Model(&due).
		Where("type = ? AND enabled AND next_fire_at IS NOT NULL AND next_fire_at <= ?", store.TriggerTypeCron, now).
		Order("next_fire_at", "id").Limit(fireBatch).Scan(ctx)
	if err != nil {
		return 0, fmt.Errorf("find due triggers: %w", err)
	}
	recorded := 0
	for _, trig := range due {
		n, err := t.fire(ctx, trig, now)
		if err != nil {
			if ctx.Err() != nil {
				return recorded, ctx.Err()
			}
			t.log.ErrorContext(ctx, "fire trigger", "trigger_id", trig.ID, "error", err)
			continue
		}
		recorded += n
	}
	return recorded, nil
}

// firePlan is what one fire of a trigger records.
type firePlan struct {
	// runs are scheduled times that get a run, subject to admission.
	runs []time.Time
	// missedAt and missed describe the skipped record for times missed by
	// downtime: the last of them and how many there were, from
	// missedFrom. missed is 0 when nothing was missed.
	missedAt, missedFrom time.Time
	missed               int
	// capped is true when there were more missed times than were counted.
	capped bool
}

// planFire splits the scheduled times from first up to now into times
// that run and times missed by downtime (tech design 8.2). A time more
// than grace before now was missed. Missed times are collapsed into one
// skipped record; with catch_up latest the last missed time runs instead,
// unless a time that is not late runs anyway.
func planFire(ctx context.Context, c *scheduler.Cron, first, now time.Time, grace time.Duration, catchUp string) (firePlan, error) {
	var p firePlan
	for f := first; !f.IsZero() && !f.After(now); f = c.Next(f) {
		if err := ctx.Err(); err != nil {
			return firePlan{}, err
		}
		if now.Sub(f) <= grace {
			p.runs = append(p.runs, f)
			continue
		}
		if p.missed == maxCountedMisses {
			p.capped = true
			// The count is a lower bound after the cap, but the range and
			// catch-up must still name the actual last missed time. Jump
			// over the rest of the outage instead of enumerating it.
			p.missedAt = c.Previous(now.Add(-grace))
			f = p.missedAt
			continue
		}
		if p.missed == 0 {
			p.missedFrom = f
		}
		p.missedAt = f
		p.missed++
	}
	if err := ctx.Err(); err != nil {
		return firePlan{}, err
	}
	if catchUp == store.CatchUpLatest && len(p.runs) == 0 && p.missed > 0 {
		p.runs = []time.Time{p.missedAt}
		if !p.capped {
			p.missed--
		}
		if p.missed > 0 {
			p.missedAt = c.Previous(p.missedAt)
		} else {
			p.missedFrom, p.missedAt = time.Time{}, time.Time{}
		}
	}
	return p, ctx.Err()
}

// fire processes one due trigger and returns the runs it recorded.
func (t *Triggers) fire(ctx context.Context, candidate store.Trigger, now time.Time) (int, error) {
	recorded := 0
	var queued []uuid.UUID
	err := t.writeRetrying(ctx, func(ctx context.Context, tx bun.Tx) error {
		recorded, queued = 0, nil
		// Serialise with Set before reading the configuration. Its
		// pending time may stay unchanged when params or catch_up change.
		res, err := tx.NewUpdate().Model((*store.Trigger)(nil)).
			Set("next_fire_at = next_fire_at").
			Where("id = ? AND enabled AND next_fire_at = ?", candidate.ID, candidate.NextFireAt).Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		var trig store.Trigger
		if err := tx.NewSelect().Model(&trig).Where("id = ?", candidate.ID).Scan(ctx); err != nil {
			return err
		}
		c, err := scheduler.ParseCron(trig.Schedule, trig.Timezone)
		if err != nil {
			// Stored schedules were validated; stop an invalid trigger
			// rather than fail on it every second.
			t.log.ErrorContext(ctx, "stored cron schedule no longer parses; trigger stopped", "trigger_id", trig.ID, "error", err)
			_, err := tx.NewUpdate().Model((*store.Trigger)(nil)).Set("next_fire_at = NULL").Where("id = ?", trig.ID).Exec(ctx)
			return err
		}
		var task store.Task
		if err := tx.NewSelect().Model(&task).Where("id = ?", trig.TaskID).Scan(ctx); err != nil {
			return fmt.Errorf("task of trigger %s: %w", trig.ID, err)
		}
		next := c.Next(now)
		if !task.ArchivedAt.IsZero() {
			// Archived tasks never fire again.
			next = time.Time{}
		}
		canFire := taskCanFire(task)
		plan, err := planFire(ctx, c, trig.NextFireAt, now, t.missedAfter, trig.CatchUp)
		if err != nil {
			return err
		}
		q := tx.NewUpdate().Model((*store.Trigger)(nil)).
			Set("next_fire_at = ?", nullTime(next)).
			Where("id = ? AND next_fire_at = ?", trig.ID, trig.NextFireAt)
		if canFire {
			q = q.Set("last_fired_at = ?", plan.last())
		}
		res, err = q.Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			// Another server moved the trigger on first, or setTriggers
			// changed it; either way this fire is not ours.
			return err
		}
		if !canFire {
			return nil
		}
		version, err := loadVersionByID(ctx, tx, task.ActiveVersionID)
		if err != nil {
			return err
		}
		params, err := triggerParams(trig, version.Manifest)
		if err != nil {
			return err
		}
		base := runSource{
			task: task, version: version, kind: store.TriggerCron, triggerID: trig.ID,
			params: params, now: now,
		}
		if plan.missed > 0 {
			src := base
			src.scheduledAt = plan.missedAt
			src.skip = store.SkipMissed
			src.skipMessage = missedMessage(plan, c)
			if _, err := t.recordFire(ctx, tx, src, plan.missed); err != nil {
				return err
			}
			recorded++
		}
		for _, at := range plan.runs {
			src := base
			src.scheduledAt = at
			run, err := t.recordFire(ctx, tx, src, 0)
			if err != nil {
				return err
			}
			if run.Status == store.RunQueued {
				queued = append(queued, run.ID)
			}
			recorded++
		}
		return nil
	})
	if err == nil {
		t.queued(ctx, queued)
	}
	return recorded, err
}

// recordFire writes a trigger fire and its run, queued or skipped.
func (t *Triggers) recordFire(ctx context.Context, tx bun.IDB, src runSource, missed int) (store.Run, error) {
	src.fireID = store.NewID()
	run, err := t.admit(ctx, tx, src)
	if err != nil {
		return store.Run{}, err
	}
	outcome := store.FireQueued
	if run.Status == store.RunSkipped {
		outcome = store.FireSkipped
	}
	_, err = tx.NewInsert().Model(&store.TriggerFire{
		ID: src.fireID, TriggerID: src.triggerID, ScheduledAt: src.scheduledAt,
		Outcome: outcome, RunID: run.ID, MissedCount: missed, CreatedAt: src.now,
	}).Exec(ctx)
	return run, err
}

// last is the latest scheduled time the plan covers.
func (p firePlan) last() time.Time {
	last := p.missedAt
	for _, r := range p.runs {
		if r.After(last) {
			last = r
		}
	}
	return last
}

func missedMessage(p firePlan, c *scheduler.Cron) string {
	loc := c.Location()
	const layout = "2006-01-02 15:04"
	count := fmt.Sprintf("%d 次", p.missed)
	if p.capped {
		count = fmt.Sprintf("至少 %d 次", p.missed)
	}
	return fmt.Sprintf("服务停机错过 %s计划触发（%s 至 %s，%s），未补跑",
		count, p.missedFrom.In(loc).Format(layout), p.missedAt.In(loc).Format(layout), loc)
}

// triggerParams returns the params of a trigger's runs: the trigger's
// own, or the manifest's.
func triggerParams(trig store.Trigger, m Manifest) (string, error) {
	if trig.Params != "" {
		return trig.Params, nil
	}
	if m.Params == nil {
		return "{}", nil
	}
	b, err := json.Marshal(m.Params)
	if err != nil {
		return "", fmt.Errorf("encode params: %w", err)
	}
	return string(b), nil
}

// nullTime passes the zero time as NULL in raw SET clauses, which do not
// see the model's nullzero tags.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}
