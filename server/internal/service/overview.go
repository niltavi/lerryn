package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Overview limits (API Overview and Calendar).
const (
	overviewItems       = 50
	attentionSummary    = 200
	expiringWithin      = 7 * 24 * time.Hour
	offlineForgetAfter  = 7 * 24 * time.Hour
	maxCalendarSpan     = 31 * 24 * time.Hour
	maxCalendarItems    = 2000
	maxPlannedPerWindow = 2000
)

// Attention kinds (API AttentionKind).
const (
	AttentionRunFailed          = "run_failed"
	AttentionRunTimedOut        = "run_timed_out"
	AttentionRunInterrupted     = "run_interrupted"
	AttentionCredentialRequest  = "credential_request"  // #nosec G101 -- an attention kind, not a secret
	AttentionCredentialExpiring = "credential_expiring" // #nosec G101 -- an attention kind, not a secret
	AttentionRunnerOffline      = "runner_offline"
)

// FirePlanner expands cron schedules, for the calendar and for the
// readable schedule text. Runs use scheduler.Planner by default; with nil
// the calendar shows each trigger's next fire only.
type FirePlanner interface {
	// Fires returns up to limit fire times of a cron schedule in
	// [from, to), in order.
	Fires(schedule, timezone string, from, to time.Time, limit int) ([]time.Time, error)
	// Describe returns the schedule in words, such as 每个工作日 08:00.
	Describe(schedule, timezone string) string
}

// WithFirePlanner sets the cron planner. It returns r for chaining.
func (r *Runs) WithFirePlanner(fp FirePlanner) *Runs {
	r.planner = fp
	return r
}

func (r *Runs) describe(schedule, timezone string) string {
	if r.planner != nil {
		if s := r.planner.Describe(schedule, timezone); s != "" {
			return s
		}
	}
	if timezone == "" {
		return "cron " + schedule
	}
	return fmt.Sprintf("cron %s（%s）", schedule, timezone)
}

// Overview is the console's overview board.
type Overview struct {
	GeneratedAt time.Time
	Counts      OverviewCounts
	Attention   []AttentionItem
	Active      []RunListItem
	Upcoming    []UpcomingFire
}

// OverviewCounts are the board's totals.
type OverviewCounts struct {
	Tasks, ActiveTasks, Attention, Running, Queued int
}

// AttentionItem is something for the caller to deal with. Fields that do
// not apply to its kind are zero.
type AttentionItem struct {
	Kind                string
	At                  time.Time
	Task                *TaskRef
	RunID               uuid.UUID
	Stage               string
	Summary             string
	CredentialRequestID uuid.UUID
	CredentialID        uuid.UUID
	RunnerID            string
	DueAt               time.Time
	// Path is the console page to handle it on.
	Path string
}

// UpcomingFire is the next planned fire of a task.
type UpcomingFire struct {
	Task         TaskRef
	TriggerID    uuid.UUID
	At           time.Time
	ScheduleText string
}

// Overview returns the overview board of what p can view.
func (r *Runs) Overview(ctx context.Context, p *auth.Principal, project *uuid.UUID) (Overview, error) {
	projects, err := r.authz.Visible(ctx, p, ActionViewTask, project)
	if err != nil {
		return Overview{}, err
	}
	o := Overview{GeneratedAt: store.Now()}
	if len(projects) > 0 {
		if err := r.overviewCounts(ctx, projects, &o.Counts); err != nil {
			return Overview{}, err
		}
		var active []store.Run
		if err := r.db.NewSelect().Model(&active).
			Where("project_id IN (?) AND status IN (?)", bun.List(projects), bun.List(activeStatuses)).
			Order("queued_at", "id").Limit(overviewItems).Scan(ctx); err != nil {
			return Overview{}, fmt.Errorf("active runs: %w", err)
		}
		if o.Active, err = r.listItems(ctx, active); err != nil {
			return Overview{}, err
		}
		if o.Upcoming, err = r.upcoming(ctx, projects); err != nil {
			return Overview{}, err
		}
	}
	items, err := r.attention(ctx, p, projects)
	if err != nil {
		return Overview{}, err
	}
	slices.SortStableFunc(items, func(a, b AttentionItem) int { return b.At.Compare(a.At) })
	o.Counts.Attention = len(items)
	o.Attention = items[:min(len(items), overviewItems)]
	return o, nil
}

func (r *Runs) overviewCounts(ctx context.Context, projects []uuid.UUID, c *OverviewCounts) error {
	var (
		n   int64
		err error
	)
	tasks := func() *bun.SelectQuery {
		return r.db.NewSelect().Model((*store.Task)(nil)).Where("project_id IN (?) AND archived_at IS NULL", bun.List(projects))
	}
	if n, err = tasks().Count(ctx); err != nil {
		return fmt.Errorf("count tasks: %w", err)
	}
	c.Tasks = int(n)
	if n, err = tasks().Where("state = ?", store.TaskActive).Count(ctx); err != nil {
		return fmt.Errorf("count active tasks: %w", err)
	}
	c.ActiveTasks = int(n)
	runs := func(statuses ...string) (int, error) {
		n, err := r.db.NewSelect().Model((*store.Run)(nil)).
			Where("project_id IN (?) AND status IN (?)", bun.List(projects), bun.List(statuses)).Count(ctx)
		return int(n), err
	}
	if c.Running, err = runs(store.RunPreparing, store.RunRunning, store.RunCancelling); err != nil {
		return fmt.Errorf("count running runs: %w", err)
	}
	if c.Queued, err = runs(store.RunQueued); err != nil {
		return fmt.Errorf("count queued runs: %w", err)
	}
	return nil
}

// attentionStatuses maps the failures that need attention to their kind.
var attentionStatuses = map[string]string{
	store.RunFailed:      AttentionRunFailed,
	store.RunTimedOut:    AttentionRunTimedOut,
	store.RunInterrupted: AttentionRunInterrupted,
}

// attention collects what needs dealing with: tasks whose last ended run
// failed, the caller's own credential requests and expiring credentials,
// and, for admins, runners that went offline.
func (r *Runs) attention(ctx context.Context, p *auth.Principal, projects []uuid.UUID) ([]AttentionItem, error) {
	var items []AttentionItem
	now := store.Now()
	if len(projects) > 0 {
		// The last non-test run of each task that ended with an outcome;
		// cancelled and skipped runs say nothing about whether it works.
		outcomes := []string{store.RunCompleted, store.RunFailed, store.RunTimedOut, store.RunInterrupted}
		var runs []store.Run
		err := r.db.NewSelect().Model(&runs).
			Where("run.project_id IN (?) AND run.is_test = ? AND run.status IN (?)", bun.List(projects), false, bun.List(outcomes)).
			Where("run.archived_at IS NULL").
			// Runners can report equal finish times. Queue time and ID
			// break ties so a task has exactly one latest outcome.
			Where(`NOT EXISTS (SELECT 1 FROM runs AS r2
				WHERE r2.task_id = run.task_id AND r2.is_test = ? AND r2.status IN (?)
				AND (r2.finished_at > run.finished_at OR
					(r2.finished_at = run.finished_at AND r2.queued_at > run.queued_at) OR
					(r2.finished_at = run.finished_at AND r2.queued_at = run.queued_at AND r2.id > run.id)))`,
				false, bun.List(outcomes)).
			Where("run.task_id IN (SELECT id FROM tasks WHERE archived_at IS NULL)").
			Scan(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed runs: %w", err)
		}
		var failed []store.Run
		for _, run := range runs {
			if _, ok := attentionStatuses[run.Status]; ok {
				failed = append(failed, run)
			}
		}
		listed, err := r.listItems(ctx, failed)
		if err != nil {
			return nil, err
		}
		stageNames, err := r.errorStageNames(ctx, failed)
		if err != nil {
			return nil, err
		}
		for _, it := range listed {
			run := it.Run
			items = append(items, AttentionItem{
				Kind:    attentionStatuses[run.Status],
				At:      run.FinishedAt,
				Task:    &TaskRef{ID: run.TaskID, Name: it.TaskName, ProjectID: run.ProjectID},
				RunID:   run.ID,
				Stage:   stageNames[run.ID],
				Summary: truncateRunes(run.ErrorMessage, attentionSummary),
				Path:    "/runs/" + run.ID.String(),
			})
		}
	}

	var requests []store.CredentialRequest
	if err := r.db.NewSelect().Model(&requests).
		Where("user_id = ? AND status = ? AND expires_at > ?", p.UserID, store.CredentialRequestPending, now).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("credential requests: %w", err)
	}
	refs, err := r.taskRefs(ctx, requests)
	if err != nil {
		return nil, err
	}
	for _, req := range requests {
		items = append(items, AttentionItem{
			Kind: AttentionCredentialRequest, At: req.CreatedAt, Task: refs[req.TaskID],
			Summary: truncateRunes(req.Name, attentionSummary), CredentialRequestID: req.ID,
			DueAt: req.ExpiresAt, Path: "/c/" + req.ID.String(),
		})
	}

	var creds []store.Credential
	if err := r.db.NewSelect().Model(&creds).
		Where("owner_user_id = ? AND expires_at IS NOT NULL AND expires_at <= ?", p.UserID, now.Add(expiringWithin)).
		Where("status != ?", store.CredentialRevoked).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("expiring credentials: %w", err)
	}
	for _, c := range creds {
		if !c.ExpiresAt.After(now) {
			// Expired: only worth a mention while a task still uses it.
			n, err := r.db.NewSelect().Model((*store.CredentialGrant)(nil)).Where("credential_id = ?", c.ID).Count(ctx)
			if err != nil {
				return nil, fmt.Errorf("credential grants: %w", err)
			}
			if n == 0 {
				continue
			}
		}
		items = append(items, AttentionItem{
			Kind: AttentionCredentialExpiring, At: c.UpdatedAt, Summary: truncateRunes(c.Name, attentionSummary),
			CredentialID: c.ID, DueAt: c.ExpiresAt, Path: "/credentials/" + c.ID.String(),
		})
	}

	if p.IsAdmin {
		var runners []store.Runner
		if err := r.db.NewSelect().Model(&runners).
			Where("last_seen_at < ? AND last_seen_at >= ?", now.Add(-onlineWithin), now.Add(-offlineForgetAfter)).
			Scan(ctx); err != nil {
			return nil, fmt.Errorf("offline runners: %w", err)
		}
		for _, rn := range runners {
			items = append(items, AttentionItem{
				Kind: AttentionRunnerOffline, At: rn.LastSeenAt, Summary: truncateRunes(rn.Name, attentionSummary),
				RunnerID: rn.ID, Path: "/admin/runners/" + rn.ID,
			})
		}
	}
	return items, nil
}

// errorStageNames returns the display name of the stage each run failed
// in.
func (r *Runs) errorStageNames(ctx context.Context, runs []store.Run) (map[uuid.UUID]string, error) {
	out := map[uuid.UUID]string{}
	var versionIDs []uuid.UUID
	for _, run := range runs {
		if run.ErrorStage != "" {
			versionIDs = append(versionIDs, run.VersionID)
		}
	}
	if len(versionIDs) == 0 {
		return out, nil
	}
	var versions []store.TaskVersion
	if err := r.db.NewSelect().Model(&versions).Column("id", "manifest").Where("id IN (?)", bun.List(versionIDs)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("stage names: %w", err)
	}
	stages := map[uuid.UUID][]StageSpec{}
	for _, v := range versions {
		if m, err := decodeManifest(v.Manifest); err == nil {
			stages[v.ID] = m.Stages
		}
	}
	for _, run := range runs {
		if run.ErrorStage == "" {
			continue
		}
		out[run.ID] = run.ErrorStage
		for _, s := range stages[run.VersionID] {
			if s.ID == run.ErrorStage {
				out[run.ID] = s.Name
			}
		}
	}
	return out, nil
}

// taskRefs names the tasks of credential requests that have one.
func (r *Runs) taskRefs(ctx context.Context, requests []store.CredentialRequest) (map[uuid.UUID]*TaskRef, error) {
	out := map[uuid.UUID]*TaskRef{}
	var ids []uuid.UUID
	for _, req := range requests {
		if req.TaskID != uuid.Nil {
			ids = append(ids, req.TaskID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	var tasks []store.Task
	if err := r.db.NewSelect().Model(&tasks).Column("id", "name", "project_id").Where("id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("tasks of credential requests: %w", err)
	}
	for _, t := range tasks {
		out[t.ID] = &TaskRef{ID: t.ID, Name: t.Name, ProjectID: t.ProjectID}
	}
	return out, nil
}

// upcoming lists the next fire of each active task, soonest first.
func (r *Runs) upcoming(ctx context.Context, projects []uuid.UUID) ([]UpcomingFire, error) {
	tasks, err := r.activeTasks(ctx, projects, nil)
	if err != nil || len(tasks) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	next, err := nextFires(ctx, r.db, ids)
	if err != nil {
		return nil, err
	}
	var out []UpcomingFire
	for taskID, f := range next {
		out = append(out, UpcomingFire{Task: tasks[taskID], TriggerID: f.TriggerID, At: f.At, ScheduleText: r.describe(f.Schedule, f.Timezone)})
	}
	slices.SortFunc(out, func(a, b UpcomingFire) int {
		if c := a.At.Compare(b.At); c != 0 {
			return c
		}
		return strings.Compare(a.Task.Name, b.Task.Name)
	})
	return out[:min(len(out), overviewItems)], nil
}

// activeTasks returns the unarchived, activated tasks of the projects,
// or the one task when only is set.
func (r *Runs) activeTasks(ctx context.Context, projects []uuid.UUID, only *uuid.UUID) (map[uuid.UUID]TaskRef, error) {
	var tasks []store.Task
	q := r.db.NewSelect().Model(&tasks).Column("id", "name", "project_id").
		Where("project_id IN (?) AND archived_at IS NULL AND state = ?", bun.List(projects), store.TaskActive)
	if only != nil {
		q = q.Where("id = ?", *only)
	}
	if err := q.Scan(ctx); err != nil {
		return nil, fmt.Errorf("active tasks: %w", err)
	}
	out := make(map[uuid.UUID]TaskRef, len(tasks))
	for _, t := range tasks {
		out[t.ID] = TaskRef{ID: t.ID, Name: t.Name, ProjectID: t.ProjectID}
	}
	return out, nil
}

// CalendarInput selects a calendar window; nil From and To mean the
// current UTC day.
type CalendarInput struct {
	ProjectID *uuid.UUID
	TaskID    *uuid.UUID
	From, To  *time.Time
}

// CalendarItem is a run or a planned fire. Planned items have no RunID.
type CalendarItem struct {
	Task   TaskRef
	RunID  uuid.UUID
	Status string
	Start  time.Time
	End    time.Time
	// TriggerID and ScheduleText are set for planned fires.
	TriggerID    uuid.UUID
	ScheduleText string
}

// Calendar is the runs and planned fires of a window, by start time.
type Calendar struct {
	From, To  time.Time
	Items     []CalendarItem
	Truncated bool
}

// Calendar returns the non-test runs that started (or were queued, if
// not started) in the window, and the fires still planned in it.
func (r *Runs) Calendar(ctx context.Context, p *auth.Principal, in CalendarInput) (Calendar, error) {
	var v validation
	now := store.Now()
	from := now.Truncate(24 * time.Hour)
	if in.From != nil {
		from = in.From.UTC()
	}
	to := from.Add(24 * time.Hour)
	if in.To != nil {
		to = in.To.UTC()
	}
	switch {
	case !to.After(from):
		v.param("to", "结束时间必须晚于开始时间")
	case to.Sub(from) > maxCalendarSpan:
		v.param("to", "时间段最长 31 天")
	}
	if err := v.err(); err != nil {
		return Calendar{}, err
	}
	projects, err := r.authz.Visible(ctx, p, ActionViewTask, in.ProjectID)
	if err != nil {
		return Calendar{}, err
	}
	cal := Calendar{From: from, To: to}
	if len(projects) == 0 {
		return cal, nil
	}
	var runs []store.Run
	q := r.db.NewSelect().Model(&runs).
		Where("project_id IN (?) AND is_test = ?", bun.List(projects), false).
		Where("archived_at IS NULL").
		Where("COALESCE(started_at, queued_at) >= ? AND COALESCE(started_at, queued_at) < ?", from, to)
	if in.TaskID != nil {
		q = q.Where("task_id = ?", *in.TaskID)
	}
	if err := q.OrderExpr("COALESCE(started_at, queued_at), id").Limit(maxCalendarItems + 1).Scan(ctx); err != nil {
		return Calendar{}, fmt.Errorf("calendar runs: %w", err)
	}
	names := map[uuid.UUID]bool{}
	for _, run := range runs {
		names[run.TaskID] = true
	}
	taskName := map[uuid.UUID]string{}
	if len(names) > 0 {
		if taskName, err = taskNames(ctx, r.db, names); err != nil {
			return Calendar{}, err
		}
	}
	for _, run := range runs {
		start := run.StartedAt
		if start.IsZero() {
			start = run.QueuedAt
		}
		cal.Items = append(cal.Items, CalendarItem{
			Task:  TaskRef{ID: run.TaskID, Name: taskName[run.TaskID], ProjectID: run.ProjectID},
			RunID: run.ID, Status: run.Status, Start: start, End: run.FinishedAt,
		})
	}

	planned, err := r.plannedFires(ctx, projects, in.TaskID, maxTime(from, now), to)
	if err != nil {
		return Calendar{}, err
	}
	cal.Items = append(cal.Items, planned...)
	slices.SortStableFunc(cal.Items, func(a, b CalendarItem) int { return a.Start.Compare(b.Start) })
	if len(cal.Items) > maxCalendarItems {
		cal.Items, cal.Truncated = cal.Items[:maxCalendarItems], true
	}
	return cal, nil
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// plannedFires lists the cron fires of active tasks in [from, to). With a
// planner every fire is listed; without one, each trigger's next fire.
func (r *Runs) plannedFires(ctx context.Context, projects []uuid.UUID, only *uuid.UUID, from, to time.Time) ([]CalendarItem, error) {
	if !to.After(from) {
		return nil, nil
	}
	tasks, err := r.activeTasks(ctx, projects, only)
	if err != nil || len(tasks) == 0 {
		return nil, err
	}
	ids := make([]uuid.UUID, 0, len(tasks))
	for id := range tasks {
		ids = append(ids, id)
	}
	var triggers []store.Trigger
	if err := r.db.NewSelect().Model(&triggers).
		Where("task_id IN (?) AND type = ? AND enabled = ?", bun.List(ids), store.TriggerTypeCron, true).
		Order("id").Scan(ctx); err != nil {
		return nil, fmt.Errorf("cron triggers: %w", err)
	}
	// The calendar keeps the earliest maxCalendarItems items, so no more
	// fires than that are needed. Once that many are collected, the window
	// shrinks to end at the latest one kept, which bounds the work and the
	// memory however many triggers fire however often.
	const keep = maxCalendarItems + 1
	var out []CalendarItem
	for _, tr := range triggers {
		var times []time.Time
		if r.planner != nil {
			// A user may have skipped an occurrence ahead of the clock.
			times, err = r.planner.Fires(tr.Schedule, tr.Timezone, maxTime(from, tr.NextFireAt), to, min(keep, maxPlannedPerWindow))
			if err != nil {
				r.log.WarnContext(ctx, "plan cron fires", "trigger_id", tr.ID, "error", err)
				continue
			}
		} else if !tr.NextFireAt.IsZero() && !tr.NextFireAt.Before(from) && tr.NextFireAt.Before(to) {
			times = []time.Time{tr.NextFireAt}
		}
		text := r.describe(tr.Schedule, tr.Timezone)
		for _, at := range times {
			out = append(out, CalendarItem{Task: tasks[tr.TaskID], Start: at, TriggerID: tr.ID, ScheduleText: text})
		}
		if len(out) > keep {
			slices.SortStableFunc(out, func(a, b CalendarItem) int { return a.Start.Compare(b.Start) })
			out = out[:keep]
			to = out[keep-1].Start.Add(time.Nanosecond)
		}
	}
	return out, nil
}
