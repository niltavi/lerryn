package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/events"
	"github.com/niltavi/lerryn/server/internal/scheduler"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Trigger rules (API schema TriggerInput, tech design 8).
const (
	maxTriggers           = 20
	defaultMaxBodyBytes   = 1 << 20
	minMaxBodyBytes       = 1 << 10
	maxMaxBodyBytes       = 10 << 20
	maxScheduleLen        = 100
	maxTimezoneLen        = 64
	nextFiresShown        = 5
	defaultMissedAfter    = 60 * time.Second
	defaultWebhookPerMin  = 60
	webhookRateWindow     = time.Minute
	idempotencyTTL        = 24 * time.Hour
	triggerTokenPrefixLen = 4
)

var triggerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// TriggerOptions configures the trigger service.
type TriggerOptions struct {
	// Queue bounds the run queue for scheduled, webhook and retry runs.
	Queue QueueLimits
	// MissedAfter is how late a scheduled time may be processed before it
	// counts as missed by downtime (tech design 8.2). Zero means 60s.
	MissedAfter time.Duration
	// WebhookPerMinute caps the calls each webhook trigger accepts per
	// minute and server. Zero means 60.
	WebhookPerMinute int
	// Now is the clock of API calls, store.Now when nil. The scheduler
	// passes its own clock reading to FireDue and RetryFailed.
	Now func() time.Time
}

// Triggers maintains task triggers, fires due cron triggers, accepts
// webhook calls and queues automatic retries (tech design 8).
type Triggers struct {
	db          *store.DB
	authz       *Authorizer
	blobs       blob.Store
	log         *slog.Logger
	queue       QueueLimits
	missedAfter time.Duration
	hooks       *auth.Limiter
	// bus announces queued runs to waiting claims; nil keeps it quiet.
	bus events.Bus
	// now is the clock for API calls; the scheduler passes its own time.
	now func() time.Time
	// plaintext rejects trigger params containing credential values;
	// nil skips the check.
	plaintext *Credentials
}

// NewTriggers returns the trigger service. Webhook bodies are kept in
// blobs.
func NewTriggers(db *store.DB, authz *Authorizer, blobs blob.Store, log *slog.Logger, opts TriggerOptions) *Triggers {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if opts.MissedAfter <= 0 {
		opts.MissedAfter = defaultMissedAfter
	}
	if opts.WebhookPerMinute <= 0 {
		opts.WebhookPerMinute = defaultWebhookPerMin
	}
	t := &Triggers{
		db: db, authz: authz, blobs: blobs, log: log,
		queue:       opts.Queue,
		missedAfter: opts.MissedAfter,
		hooks:       auth.NewLimiter(opts.WebhookPerMinute, webhookRateWindow),
		now:         opts.Now,
	}
	if t.now == nil {
		t.now = store.Now
	}
	return t
}

// WithEvents publishes run.queued on bus for every run the service
// queues, so that long-polling claims pick them up at once. It returns t
// for chaining.
func (t *Triggers) WithEvents(bus events.Bus) *Triggers {
	t.bus = bus
	return t
}

// WithPlaintextCheck rejects trigger params that contain the value of a
// credential the caller owns or one bound to the task (tech design 11.7).
// It returns t for chaining.
func (t *Triggers) WithPlaintextCheck(c *Credentials) *Triggers {
	t.plaintext = c
	return t
}

// TriggerInput is the desired state of one trigger.
type TriggerInput struct {
	Name    string
	Type    string
	Enabled *bool
	// Schedule and Timezone are for cron triggers.
	Schedule string
	Timezone string
	CatchUp  string
	// MaxBodyBytes is for webhook triggers; nil means 1 MiB.
	MaxBodyBytes *int64
	// Params, when not nil, replaces the manifest's params for runs of
	// this trigger.
	Params map[string]any
}

// SetTriggersInput replaces every trigger of a task.
type SetTriggersInput struct {
	Triggers []TriggerInput
	DryRun   bool
	// IfMatch is the revision read; required once the task has triggers.
	IfMatch *int
}

// TriggerSet is the triggers of a task.
type TriggerSet struct {
	TaskID     uuid.UUID
	Revision   int
	DryRun     bool
	TaskActive bool
	Triggers   []TriggerView
}

// TriggerView is a trigger as clients see it.
type TriggerView struct {
	Trigger store.Trigger
	// NextFireAt is when the trigger fires next, zero while it cannot
	// fire: webhook, disabled, or the task inactive.
	NextFireAt time.Time
	// ScheduleText and NextFires describe a cron schedule; NextFires is
	// given even while the task is inactive.
	ScheduleText string
	NextFires    []time.Time
	// Token is the webhook token, only right after it was created.
	Token string
}

// List returns the triggers of a task.
func (t *Triggers) List(ctx context.Context, p *auth.Principal, taskID uuid.UUID) (TriggerSet, error) {
	task, err := t.loadTask(ctx, p, taskID, ActionViewTask)
	if err != nil {
		return TriggerSet{}, err
	}
	var rows []store.Trigger
	if err := t.db.NewSelect().Model(&rows).Where("task_id = ?", taskID).Order("name").Scan(ctx); err != nil {
		return TriggerSet{}, fmt.Errorf("list triggers: %w", err)
	}
	return t.view(task, rows, nil, false, t.now()), nil
}

// Set replaces the triggers of a task, matching existing ones by name:
// a trigger with the same name and type keeps its ID and webhook token,
// the rest are created, and triggers not listed are deleted. It creates
// no task version and changes no existing run.
func (t *Triggers) Set(ctx context.Context, p *auth.Principal, taskID uuid.UUID, in SetTriggersInput) (TriggerSet, error) {
	if err := validateTriggers(in.Triggers); err != nil {
		return TriggerSet{}, err
	}
	task, err := t.loadTask(ctx, p, taskID, ActionModifyTask)
	if err != nil {
		return TriggerSet{}, err
	}
	var existing []store.Trigger
	if err := t.db.NewSelect().Model(&existing).Where("task_id = ?", taskID).Scan(ctx); err != nil {
		return TriggerSet{}, fmt.Errorf("read triggers: %w", err)
	}
	if in.IfMatch != nil && *in.IfMatch != task.TriggersRevision {
		return TriggerSet{}, &VersionConflictError{Current: task.TriggersRevision}
	}
	if in.IfMatch == nil && len(existing) > 0 && !in.DryRun {
		return TriggerSet{}, ErrPreconditionRequired
	}
	if err := t.checkPlaintext(ctx, p, taskID, in.Triggers); err != nil {
		return TriggerSet{}, err
	}

	now := t.now()
	var plan *triggerPlan
	if in.DryRun {
		plan, err = planTriggers(task, existing, in.Triggers, p.UserID, now)
		if err != nil {
			return TriggerSet{}, err
		}
		return t.view(task, plan.rows, nil, true, now), nil
	}

	tokens := map[uuid.UUID]string{}
	err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		clear(tokens)
		// The revision moves only from the value read, so a concurrent
		// setTriggers that committed in between is a conflict even when
		// this request carried no If-Match.
		var revision int
		err := tx.NewUpdate().Model((*store.Task)(nil)).
			Set("triggers_revision = triggers_revision + 1").
			Where("id = ? AND triggers_revision = ?", taskID, task.TriggersRevision).
			Returning("triggers_revision").Scan(ctx, &revision)
		if errors.Is(err, sql.ErrNoRows) {
			var current store.Task
			if err := tx.NewSelect().Model(&current).Column("triggers_revision").Where("id = ?", taskID).Scan(ctx); err != nil {
				return err
			}
			return &VersionConflictError{Current: current.TriggersRevision}
		}
		if err != nil {
			return err
		}
		task.TriggersRevision = revision
		// Lock before rereading so a scheduler cannot advance a trigger
		// between this read and the update. SQLite serialises writers;
		// the no-op update provides the same row lock on PostgreSQL.
		if _, err := tx.NewUpdate().Model((*store.Trigger)(nil)).
			Set("updated_at = updated_at").Where("task_id = ?", taskID).Exec(ctx); err != nil {
			return err
		}
		var current []store.Trigger
		if err := tx.NewSelect().Model(&current).Where("task_id = ?", taskID).Scan(ctx); err != nil {
			return fmt.Errorf("reread triggers: %w", err)
		}
		plan, err = planTriggers(task, current, in.Triggers, p.UserID, now)
		if err != nil {
			return err
		}
		if len(plan.deleted) > 0 {
			if _, err := tx.NewDelete().Model((*store.Trigger)(nil)).Where("id IN (?)", bun.List(plan.deleted)).Exec(ctx); err != nil {
				return fmt.Errorf("delete triggers: %w", err)
			}
		}
		for i := range plan.rows {
			row := &plan.rows[i]
			if plan.created[row.ID] {
				if row.Type == store.TriggerTypeWebhook {
					token, hash, err := auth.NewToken(auth.TriggerTokenPrefix)
					if err != nil {
						return err
					}
					row.TokenHash, row.TokenPrefix = hash, auth.DisplayPrefix(token)
					tokens[row.ID] = token
				}
				if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
					return fmt.Errorf("insert trigger %s: %w", row.Name, err)
				}
				continue
			}
			if _, err := tx.NewUpdate().Model(row).WherePK().
				Column("enabled", "schedule", "timezone", "catch_up", "params", "max_body_bytes", "next_fire_at", "updated_at").
				Exec(ctx); err != nil {
				return fmt.Errorf("update trigger %s: %w", row.Name, err)
			}
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "trigger.set", resourceKind: "task", resourceID: taskID.String(),
			result: resultSuccess,
			details: map[string]any{
				"revision": revision, "created": plan.names(plan.created), "updated": plan.updatedNames(),
				"deleted": plan.deletedNames,
			},
		})
	})
	if err != nil {
		return TriggerSet{}, err
	}
	return t.view(task, plan.rows, tokens, false, now), nil
}

// RotateToken gives a webhook trigger a new token; the old one stops
// working at once.
func (t *Triggers) RotateToken(ctx context.Context, p *auth.Principal, triggerID uuid.UUID) (TriggerView, error) {
	var row store.Trigger
	err := t.db.NewSelect().Model(&row).Where("id = ?", triggerID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return TriggerView{}, ErrNotFound
	}
	if err != nil {
		return TriggerView{}, err
	}
	task, err := t.loadTask(ctx, p, row.TaskID, ActionModifyTask)
	if err != nil {
		return TriggerView{}, err
	}
	if row.Type != store.TriggerTypeWebhook {
		v := validation{unprocessable: true}
		v.param("triggerId", "只有 webhook 触发器有令牌")
		return TriggerView{}, v.err()
	}
	token, hash, err := auth.NewToken(auth.TriggerTokenPrefix)
	if err != nil {
		return TriggerView{}, err
	}
	now := t.now()
	row.TokenHash, row.TokenPrefix, row.UpdatedAt = hash, auth.DisplayPrefix(token), now
	err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewUpdate().Model(&row).WherePK().Column("token_hash", "token_prefix", "updated_at").Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "trigger.rotate_token", resourceKind: "trigger", resourceID: row.ID.String(),
			result:  resultSuccess,
			details: map[string]any{"task_id": row.TaskID.String(), "name": row.Name, "token_prefix": row.TokenPrefix},
		})
	})
	if err != nil {
		return TriggerView{}, err
	}
	set := t.view(task, []store.Trigger{row}, map[uuid.UUID]string{row.ID: token}, false, now)
	return set.Triggers[0], nil
}

func (t *Triggers) loadTask(ctx context.Context, p *auth.Principal, id uuid.UUID, action Action) (store.Task, error) {
	var task store.Task
	err := t.db.NewSelect().Model(&task).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Task{}, ErrNotFound
	}
	if err != nil {
		return store.Task{}, err
	}
	if err := t.authz.Require(ctx, p, task.ProjectID, action); err != nil {
		return store.Task{}, err
	}
	return task, nil
}

// taskCanFire reports whether the task's triggers produce runs.
func taskCanFire(task store.Task) bool {
	return task.State == store.TaskActive && task.ArchivedAt.IsZero() && task.ActiveVersionID != uuid.Nil
}

func (t *Triggers) view(task store.Task, rows []store.Trigger, tokens map[uuid.UUID]string, dryRun bool, now time.Time) TriggerSet {
	set := TriggerSet{
		TaskID: task.ID, Revision: task.TriggersRevision, DryRun: dryRun,
		TaskActive: taskCanFire(task), Triggers: make([]TriggerView, 0, len(rows)),
	}
	for _, row := range rows {
		v := TriggerView{Trigger: row, Token: tokens[row.ID]}
		if row.Type == store.TriggerTypeCron {
			if c, err := scheduler.ParseCron(row.Schedule, row.Timezone); err == nil {
				v.ScheduleText = c.Describe()
				v.NextFires = c.NextN(now, nextFiresShown)
			} else {
				// Stored schedules were validated; a zone dropped from the
				// zone database is the only way to get here.
				t.log.Warn("stored cron schedule no longer parses", "trigger_id", row.ID, "error", err)
				v.ScheduleText = row.Schedule
			}
			if set.TaskActive && row.Enabled {
				v.NextFireAt = row.NextFireAt
			}
		}
		set.Triggers = append(set.Triggers, v)
	}
	return set
}

// triggerPlan is what setTriggers will write.
type triggerPlan struct {
	// rows are the triggers after the change, in request order.
	rows    []store.Trigger
	created map[uuid.UUID]bool
	deleted []uuid.UUID
	// deletedNames are the names of deleted triggers, for the audit
	// record; a trigger replaced by one of another type appears in both
	// created and deleted.
	deletedNames []string
}

func (p *triggerPlan) names(ids map[uuid.UUID]bool) []string {
	out := []string{}
	for _, r := range p.rows {
		if ids[r.ID] {
			out = append(out, r.Name)
		}
	}
	return out
}

func (p *triggerPlan) updatedNames() []string {
	out := []string{}
	for _, r := range p.rows {
		if !p.created[r.ID] {
			out = append(out, r.Name)
		}
	}
	return out
}

// planTriggers works out the rows setTriggers writes. Inputs were
// validated, so every cron input parses.
func planTriggers(task store.Task, existing []store.Trigger, inputs []TriggerInput, userID uuid.UUID, now time.Time) (*triggerPlan, error) {
	byName := map[string]store.Trigger{}
	for _, e := range existing {
		byName[e.Name] = e
	}
	plan := &triggerPlan{created: map[uuid.UUID]bool{}}
	kept := map[uuid.UUID]bool{}
	for _, in := range inputs {
		row, ok := byName[in.Name]
		if !ok || row.Type != in.Type {
			row = store.Trigger{ID: store.NewID(), TaskID: task.ID, Name: in.Name, Type: in.Type, CreatedBy: userID, CreatedAt: now}
			plan.created[row.ID] = true
		} else {
			kept[row.ID] = true
		}
		prev := row
		row.Enabled = in.Enabled == nil || *in.Enabled
		row.CatchUp = in.CatchUp
		if row.CatchUp == "" {
			row.CatchUp = store.CatchUpSkip
		}
		row.Params = ""
		if in.Params != nil {
			b, err := json.Marshal(in.Params)
			if err != nil {
				return nil, fmt.Errorf("encode trigger params: %w", err)
			}
			row.Params = string(b)
		}
		row.UpdatedAt = now
		switch in.Type {
		case store.TriggerTypeCron:
			row.Schedule, row.Timezone, row.MaxBodyBytes = in.Schedule, in.Timezone, 0
			row.NextFireAt = time.Time{}
			if row.Enabled {
				// An unchanged schedule keeps its pending fire time, so that
				// editing params does not skip or repeat a fire.
				same := !plan.created[row.ID] && prev.Enabled && prev.Schedule == row.Schedule &&
					prev.Timezone == row.Timezone && !prev.NextFireAt.IsZero()
				if same {
					row.NextFireAt = prev.NextFireAt
				} else {
					c, err := scheduler.ParseCron(row.Schedule, row.Timezone)
					if err != nil {
						return nil, err
					}
					row.NextFireAt = c.Next(now)
				}
			}
		case store.TriggerTypeWebhook:
			row.Schedule, row.Timezone, row.NextFireAt = "", "", time.Time{}
			row.MaxBodyBytes = defaultMaxBodyBytes
			if in.MaxBodyBytes != nil {
				row.MaxBodyBytes = *in.MaxBodyBytes
			}
		}
		plan.rows = append(plan.rows, row)
	}
	for _, e := range existing {
		if !kept[e.ID] {
			plan.deleted = append(plan.deleted, e.ID)
			plan.deletedNames = append(plan.deletedNames, e.Name)
		}
	}
	return plan, nil
}

// validateTriggers checks a setTriggers request against the trigger
// rules and every cron schedule against the cron parser.
func validateTriggers(inputs []TriggerInput) error {
	var v validation
	const at = "#/triggers"
	if len(inputs) > maxTriggers {
		v.body(at, fmt.Sprintf("一个任务最多 %d 个触发器", maxTriggers))
		return v.err()
	}
	seen := map[string]bool{}
	for i, in := range inputs {
		p := fmt.Sprintf("%s/%d", at, i)
		switch {
		case !triggerNamePattern.MatchString(in.Name):
			v.body(p+"/name", "触发器名为 1 到 63 个字符，只能包含小写字母、数字和连字符，以字母或数字开头")
		case seen[in.Name]:
			v.content(p+"/name", fmt.Sprintf("触发器名 %s 重复", in.Name))
		}
		seen[in.Name] = true
		switch in.CatchUp {
		case "", store.CatchUpSkip, store.CatchUpLatest:
		default:
			v.body(p+"/catch_up", "catch_up 只能是 skip 或 latest")
		}
		if in.Params != nil {
			if b, err := json.Marshal(in.Params); err != nil || len(b) > maxParamsBytes {
				v.content(p+"/params", "参数序列化为 JSON 后不能超过 16 KiB")
			}
		}
		switch in.Type {
		case store.TriggerTypeCron:
			validateCronInput(&v, p, in)
		case store.TriggerTypeWebhook:
			if in.Schedule != "" {
				v.content(p+"/schedule", "schedule 只用于 cron 触发器")
			}
			if in.Timezone != "" {
				v.content(p+"/timezone", "timezone 只用于 cron 触发器")
			}
			if in.CatchUp != "" {
				v.content(p+"/catch_up", "catch_up 只用于 cron 触发器")
			}
			if b := in.MaxBodyBytes; b != nil && (*b < minMaxBodyBytes || *b > maxMaxBodyBytes) {
				v.body(p+"/max_body_bytes", "请求体上限为 1024 到 10485760 字节")
			}
		default:
			v.body(p+"/type", "触发器类型只能是 cron 或 webhook")
		}
	}
	return v.err()
}

func validateCronInput(v *validation, p string, in TriggerInput) {
	if in.MaxBodyBytes != nil {
		v.content(p+"/max_body_bytes", "max_body_bytes 只用于 webhook 触发器")
	}
	switch {
	case in.Schedule == "":
		v.content(p+"/schedule", "cron 触发器必须给出 schedule，例如 \"0 8 * * 1-5\"")
	case len(in.Schedule) > maxScheduleLen:
		v.body(p+"/schedule", fmt.Sprintf("schedule 最多 %d 个字符", maxScheduleLen))
	}
	switch {
	case in.Timezone == "":
		v.content(p+"/timezone", "cron 触发器必须给出 IANA 时区名，例如 Asia/Shanghai")
	case len(in.Timezone) > maxTimezoneLen:
		v.body(p+"/timezone", fmt.Sprintf("timezone 最多 %d 个字符", maxTimezoneLen))
	}
	if in.Schedule == "" || in.Timezone == "" || len(in.Schedule) > maxScheduleLen || len(in.Timezone) > maxTimezoneLen {
		return
	}
	_, err := scheduler.ParseCron(in.Schedule, in.Timezone)
	switch {
	case errors.Is(err, scheduler.ErrInvalidTimezone):
		v.content(p+"/timezone", "不是有效的 IANA 时区名，例如 Asia/Shanghai、UTC")
	case errors.Is(err, scheduler.ErrInvalidSchedule):
		v.content(p+"/schedule", "cron 表达式无效："+cronProblem(err)+"。格式为五段：分 时 日 月 周，例如 \"0 8 * * 1-5\"，也接受 @daily、@hourly")
	case err != nil:
		v.content(p+"/schedule", "cron 表达式无效")
	}
}

// cronProblem is the parser's reason, which names the offending field.
func cronProblem(err error) string {
	msg := err.Error()
	if i := len(scheduler.ErrInvalidSchedule.Error()) + 2; len(msg) > i {
		return msg[i:]
	}
	return msg
}

// checkPlaintext rejects trigger params containing credential values: every
// run the trigger starts would carry them in its parameter snapshot.
func (t *Triggers) checkPlaintext(ctx context.Context, p *auth.Principal, taskID uuid.UUID, inputs []TriggerInput) error {
	if t.plaintext == nil {
		return nil
	}
	var texts []ScanText
	for i, in := range inputs {
		if in.Params == nil {
			continue
		}
		b, err := json.Marshal(in.Params)
		if err != nil {
			return fmt.Errorf("encode trigger params: %w", err)
		}
		more, err := JSONScanTexts(fmt.Sprintf("#/triggers/%d/params", i), b)
		if err != nil {
			return err
		}
		texts = append(texts, more...)
	}
	return t.plaintext.CheckPlaintext(ctx, p, taskID, texts)
}
