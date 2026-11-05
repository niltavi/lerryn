package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Errors of the task lifecycle (tech design 5.2). ErrPreconditionRequired
// and *VersionConflictError are shared with the trigger set (errors.go).
var (
	// ErrVersionNotTested means the deployment requires a successful test
	// run before activation and the version has none.
	ErrVersionNotTested = errors.New("version not tested")
	// ErrTaskArchived means the task is archived and can no longer run or
	// change.
	ErrTaskArchived = errors.New("task archived")
	// ErrManualRunDisabled means an agent turned the console's manual run
	// off for the task and a browser session tried to start a run.
	ErrManualRunDisabled = errors.New("manual run disabled")
)

// Issue is a problem found when checking a version (API ValidationIssue).
// Code is an ErrorCode value; Pointer is a JSON pointer into the
// submission, empty when the issue is about no particular place.
type Issue struct {
	Code    string
	Pointer string
	Message string
}

// Codes used in issues.
const (
	codeValidationFailed      = "validation_failed"
	codeCredentialMissing     = "credential_missing"     // #nosec G101 -- an error code, not a secret
	codeCredentialUnavailable = "credential_unavailable" // #nosec G101 -- an error code, not a secret
	codeVersionNotTested      = "version_not_tested"
)

// Task list and page sizes (common parameters Limit and Cursor).
const (
	defaultPage = 50
	maxPage     = 200
	// onlineWithin is how recently a runner must have been seen to count
	// as online: two leases, so a runner between heartbeats is not
	// reported offline.
	onlineWithin = 2 * runproto.LeaseTTL
)

// TaskDiff is the difference between two versions (API TaskDiff).
type TaskDiff struct {
	// Base is the version compared against, nil for an empty task.
	Base   *int
	Target int
	// ManifestPatch is a unified diff of the formatted manifests, empty
	// when they are equal.
	ManifestPatch string
	Files         []FileChange
	Truncated     bool
}

// FileChange is one changed file (API FileDiff).
type FileChange struct {
	Path      string
	Change    string
	Additions int
	Deletions int
	// Patch is the unified diff; empty when left out.
	Patch string
}

// File change kinds.
const (
	ChangeAdded       = "added"
	ChangeRemoved     = "removed"
	ChangeModified    = "modified"
	ChangeModeChanged = "mode_changed"
)

// proposedVersion is a version that exists only in a submission.
type proposedVersion struct {
	manifest Manifest
	files    []store.TaskFile
	contents map[string]string
}

// diffAgainst compares target with version base of the task; base nil
// compares with an empty task.
func (t *Tasks) diffAgainst(ctx context.Context, taskID uuid.UUID, base *int, target proposedVersion, targetSeq int) (TaskDiff, error) {
	d := TaskDiff{Base: base, Target: targetSeq}
	var (
		baseManifest *Manifest
		baseFiles    []store.TaskFile
	)
	if base != nil {
		v, err := t.version(ctx, taskID, *base)
		if err != nil {
			return TaskDiff{}, err
		}
		baseManifest, baseFiles = &v.Manifest, v.Files
	}
	d.ManifestPatch = manifestPatch(baseManifest, target.manifest)
	budget := maxDiffBytes - len(d.ManifestPatch)

	old := map[string]store.TaskFile{}
	for _, f := range baseFiles {
		old[f.Path] = f
	}
	now := map[string]store.TaskFile{}
	for _, f := range target.files {
		now[f.Path] = f
	}
	paths := make([]string, 0, len(old)+len(now))
	for p := range old {
		paths = append(paths, p)
	}
	for p := range now {
		if _, ok := old[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	for _, p := range paths {
		before, hadBefore := old[p]
		after, hasAfter := now[p]
		c := FileChange{Path: p}
		switch {
		case !hadBefore:
			c.Change = ChangeAdded
		case !hasAfter:
			c.Change = ChangeRemoved
		case before.BlobHash != after.BlobHash:
			c.Change = ChangeModified
		case before.Executable != after.Executable:
			c.Change = ChangeModeChanged
			d.Files = append(d.Files, c)
			continue
		default:
			continue
		}
		var a, b string
		if hadBefore {
			content, err := blob.ReadAll(ctx, t.blobs, before.BlobHash)
			if err != nil {
				return TaskDiff{}, fmt.Errorf("read %s of version %d: %w", p, *base, err)
			}
			a = string(content)
		}
		if hasAfter {
			b = target.contents[p]
		}
		var patch string
		c.Additions, c.Deletions, patch = fileDiff(p, a, b, hadBefore, hasAfter)
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			patch = ""
		}
		if len(patch) > budget {
			d.Truncated = true
			patch = ""
		}
		budget -= len(patch)
		c.Patch = patch
		d.Files = append(d.Files, c)
	}
	return d, nil
}

// manifestPatch diffs two manifests as formatted JSON.
func manifestPatch(before *Manifest, after Manifest) string {
	format := func(m *Manifest) string {
		if m == nil {
			return ""
		}
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			// Manifest holds only JSON values; this cannot fail.
			return ""
		}
		return string(b) + "\n"
	}
	_, _, patch := fileDiff("manifest.json", format(before), format(&after), before != nil, true)
	return patch
}

// check finds what keeps a version from running or being activated
// now: an unknown runtime, credentials without a usable grant, tools that
// are not registered, no online runner and, with tested set to the
// version's state, a missing test run. errs fail a run in preparation;
// warns do not.
func (t *Tasks) check(ctx context.Context, projectID, taskID uuid.UUID, m Manifest, paths map[string]bool, tested bool) (errs, warns []Issue, err error) {
	var v validation
	validateRuntime(&v, "#/manifest/runtime", m.Runtime, paths)
	for _, f := range v.fields {
		errs = append(errs, Issue{Code: codeValidationFailed, Pointer: f.Pointer, Message: f.Detail})
	}

	credErrs, credWarns, err := t.checkCredentials(ctx, taskID, m.Credentials)
	if err != nil {
		return nil, nil, err
	}
	errs = append(errs, credErrs...)
	warns = append(warns, credWarns...)

	tools, err := projectTools(ctx, t.db, projectID, m.Tools)
	if err != nil {
		return nil, nil, err
	}
	toolErrs, toolWarns := toolIssues(m.Tools, m.Network, tools)
	errs = append(errs, toolErrs...)
	warns = append(warns, toolWarns...)

	q := t.db.NewSelect().Model((*store.Runner)(nil)).
		Where("last_seen_at >= ? AND disabled_at IS NULL", store.Now().Add(-onlineWithin)).
		Where("protocol_version >= ?", runproto.StateProtocolVersion)
	for _, label := range m.RunnerLabels {
		// The claim rule (tech design 4.4): the runner has every label.
		q = q.Where("EXISTS (SELECT 1 FROM runner_labels AS l WHERE l.runner_id = runner.id AND l.label = ?)", label)
	}
	online, err := q.Count(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("count online runners: %w", err)
	}
	switch {
	case online > 0:
	case len(m.RunnerLabels) > 0:
		warns = append(warns, Issue{Code: codeRunnerUnavailable, Pointer: "#/manifest/runner_labels",
			Message: fmt.Sprintf("当前没有带全部标签 %s 的在线 runner，运行会在队列中等待", strings.Join(m.RunnerLabels, "、"))})
	default:
		warns = append(warns, Issue{Code: codeRunnerUnavailable, Message: "当前没有在线的 runner，运行会在队列中等待"})
	}

	if !tested {
		required, err := t.requireTest(ctx)
		if err != nil {
			return nil, nil, fmt.Errorf("read the test-before-activate setting: %w", err)
		}
		if required {
			warns = append(warns, Issue{Code: codeVersionNotTested, Message: "激活前需要一次成功的测试运行：用 run_start 带 test 为 true 测试这个版本"})
		}
	}
	return errs, warns, nil
}

// checkCredentials checks each referenced credential for a usable grant
// to the task, by the rules a run start applies (checkRunCredentials).
// taskID is uuid.Nil for a task not created yet, which has no grants.
func (t *Tasks) checkCredentials(ctx context.Context, taskID uuid.UUID, refs []CredentialSpec) (errs, warns []Issue, err error) {
	if len(refs) == 0 {
		return nil, nil, nil
	}
	bindings := map[string]binding{}
	if taskID != uuid.Nil {
		if bindings, err = loadBindings(ctx, t.db, taskID); err != nil {
			return nil, nil, fmt.Errorf("read credential grants: %w", err)
		}
	}
	now := store.Now()
	for i, ref := range refs {
		at := fmt.Sprintf("#/manifest/credentials/%d", i)
		var issue *Issue
		if b, ok := bindings[ref.Name]; !ok {
			issue = &Issue{Code: codeCredentialMissing, Pointer: at,
				Message: fmt.Sprintf("凭证 %s 还没有填写或授权给这个任务：用 credential_request 请用户填写", ref.Name)}
		} else if why := unusable(b, now); why != "" {
			issue = &Issue{Code: codeCredentialUnavailable, Pointer: at, Message: why + "，请凭证所有者更新或重新授权"}
		}
		switch {
		case issue == nil:
		case ref.Optional:
			warns = append(warns, *issue)
		default:
			errs = append(errs, *issue)
		}
	}
	return errs, warns, nil
}

// clientName is the name of the access token p authenticated with,
// recorded on versions and runs to tell agents and machines apart; empty
// for sessions.
func clientName(ctx context.Context, idb bun.IDB, p *auth.Principal) (string, error) {
	if p == nil || p.Method != auth.MethodAccessToken {
		return "", nil
	}
	var name string
	err := idb.NewSelect().Model((*store.AccessToken)(nil)).Column("name").
		Where("id = ?", p.CredentialID).Scan(ctx, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("access token name: %w", err)
	}
	return name, nil
}

// Visible returns the projects p may perform action in. With only set, it
// returns that project alone, or ErrNotFound when p cannot see it, so an
// explicit filter on someone else's project reads like a missing one.
func (a *Authorizer) Visible(ctx context.Context, p *auth.Principal, action Action, only *uuid.UUID) ([]uuid.UUID, error) {
	if p == nil {
		return nil, ErrUnauthenticated
	}
	if only != nil {
		if err := a.Require(ctx, p, *only, action); err != nil {
			return nil, err
		}
		return []uuid.UUID{*only}, nil
	}
	var bindings []store.RoleBinding
	if err := a.db.NewSelect().Model(&bindings).Column("project_id", "role").Where("user_id = ?", p.UserID).Scan(ctx); err != nil {
		return nil, fmt.Errorf("project roles: %w", err)
	}
	var ids []uuid.UUID
	for _, b := range bindings {
		if slices.Contains(roleActions[b.Role], action) {
			ids = append(ids, b.ProjectID)
		}
	}
	return ids, nil
}

// pageSize applies the Limit parameter rules.
func pageSize(v *validation, limit *int) int {
	if limit == nil {
		return defaultPage
	}
	if *limit < 1 || *limit > maxPage {
		v.param("limit", fmt.Sprintf("取值为 1 到 %d", maxPage))
	}
	return *limit
}

// Cursors are base64url of a kind letter and '|'-separated fields, so a
// cursor from one list is rejected by another.
func encodePageCursor(kind string, fields ...string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(kind + "|" + strings.Join(fields, "|")))
}

func decodePageCursor(s, kind string, n int) ([]string, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(b), "|")
	if len(parts) != n+1 || parts[0] != kind {
		return nil, errors.New("cursor of another list")
	}
	return parts[1:], nil
}

// timeCursor is a position in a list ordered by a time, newest first,
// and then by ID.
type timeCursor struct {
	at time.Time
	id uuid.UUID
}

func encodeTimeCursor(kind string, at time.Time, id uuid.UUID) string {
	return encodePageCursor(kind, at.UTC().Format(time.RFC3339Nano), id.String())
}

func decodeTimeCursor(v *validation, s, kind string) *timeCursor {
	if s == "" {
		return nil
	}
	parts, err := decodePageCursor(s, kind, 2)
	var c timeCursor
	if err == nil {
		c.at, err = time.Parse(time.RFC3339Nano, parts[0])
	}
	if err == nil {
		c.id, err = uuid.Parse(parts[1])
	}
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
		return nil
	}
	return &c
}

// TaskListInput selects a page of tasks.
type TaskListInput struct {
	ProjectID *uuid.UUID
	// State is TaskActive or TaskInactive; empty for both.
	State string
	// Name matches the task name exactly; Query is a case-insensitive
	// substring of the name or description.
	Name     string
	Query    string
	Archived bool
	Limit    *int
	Cursor   string
}

// TaskSummary is a task in a list.
type TaskSummary struct {
	Task store.Task
	// LastRun is the latest run that is not a test run, nil if none.
	LastRun *store.Run
	// NextRunAt is the next cron fire of an active task, zero if none.
	NextRunAt  time.Time
	ActiveRuns int
}

// TaskPage is a page of tasks; NextCursor is empty on the last page.
type TaskPage struct {
	Tasks      []TaskSummary
	NextCursor string
}

const taskCursor = "t"

// List returns the tasks p can view, most recently updated first.
func (t *Tasks) List(ctx context.Context, p *auth.Principal, in TaskListInput) (TaskPage, error) {
	var v validation
	limit := pageSize(&v, in.Limit)
	after := decodeTimeCursor(&v, in.Cursor, taskCursor)
	switch in.State {
	case "", store.TaskActive, store.TaskInactive:
	default:
		v.param("state", "只能是 active 或 inactive")
	}
	if err := v.err(); err != nil {
		return TaskPage{}, err
	}
	projects, err := t.authz.Visible(ctx, p, ActionViewTask, in.ProjectID)
	if err != nil || len(projects) == 0 {
		return TaskPage{}, err
	}
	var tasks []store.Task
	q := t.db.NewSelect().Model(&tasks).Where("project_id IN (?)", bun.List(projects))
	if in.Archived {
		q = q.Where("archived_at IS NOT NULL")
	} else {
		q = q.Where("archived_at IS NULL")
	}
	if in.State != "" {
		q = q.Where("state = ?", in.State)
	}
	if in.Name != "" {
		q = q.Where("name = ?", in.Name)
	}
	if in.Query != "" {
		pattern := "%" + likeEscaper.Replace(strings.ToLower(in.Query)) + "%"
		q = q.Where(`(lower(name) LIKE ? ESCAPE '\' OR lower(description) LIKE ? ESCAPE '\')`, pattern, pattern)
	}
	if after != nil {
		q = q.Where("(updated_at < ? OR (updated_at = ? AND id < ?))", after.at, after.at, after.id)
	}
	if err := q.Order("updated_at DESC", "id DESC").Limit(int64(limit + 1)).Scan(ctx); err != nil {
		return TaskPage{}, fmt.Errorf("list tasks: %w", err)
	}
	var page TaskPage
	if len(tasks) > limit {
		tasks = tasks[:limit]
		last := tasks[limit-1]
		page.NextCursor = encodeTimeCursor(taskCursor, last.UpdatedAt, last.ID)
	}
	page.Tasks, err = t.summaries(ctx, tasks)
	return page, err
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// summaries adds the last run, the next fire and the active run count to
// each task, with one query each for the whole page.
func (t *Tasks) summaries(ctx context.Context, tasks []store.Task) ([]TaskSummary, error) {
	out := make([]TaskSummary, len(tasks))
	if len(tasks) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(tasks))
	index := map[uuid.UUID]int{}
	for i, task := range tasks {
		ids[i] = task.ID
		index[task.ID] = i
		out[i].Task = task
	}

	var last []store.Run
	err := t.db.NewSelect().Model(&last).
		Where("run.task_id IN (?) AND run.is_test = ?", bun.List(ids), false).
		Where("run.archived_at IS NULL").
		Where("run.queued_at = (SELECT MAX(r2.queued_at) FROM runs AS r2 WHERE r2.task_id = run.task_id AND r2.is_test = ?)", false).
		Order("run.id").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("last runs: %w", err)
	}
	for _, r := range last {
		i := index[r.TaskID]
		if out[i].LastRun == nil || r.ID.String() > out[i].LastRun.ID.String() {
			out[i].LastRun = &r
		}
	}

	var counts []struct {
		TaskID uuid.UUID `bun:"task_id"`
		N      int       `bun:"n"`
	}
	err = t.db.NewSelect().Model((*store.Run)(nil)).ColumnExpr("task_id, COUNT(*) AS n").
		Where("task_id IN (?) AND status IN (?)", bun.List(ids), bun.List(activeStatuses)).
		Group("task_id").Scan(ctx, &counts)
	if err != nil {
		return nil, fmt.Errorf("active runs: %w", err)
	}
	for _, c := range counts {
		out[index[c.TaskID]].ActiveRuns = c.N
	}

	next, err := nextFires(ctx, t.db, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Task.State == store.TaskActive {
			if f, ok := next[out[i].Task.ID]; ok {
				out[i].NextRunAt = f.At
			}
		}
	}
	return out, nil
}

// plannedFire is the next fire of a task's cron triggers.
type plannedFire struct {
	TriggerID uuid.UUID
	At        time.Time
	Schedule  string
	Timezone  string
}

// nextFires returns, for each task, the earliest next fire of its enabled
// cron triggers, as maintained by the scheduler.
func nextFires(ctx context.Context, idb bun.IDB, taskIDs []uuid.UUID) (map[uuid.UUID]plannedFire, error) {
	var triggers []store.Trigger
	err := idb.NewSelect().Model(&triggers).
		Where("task_id IN (?) AND type = ? AND enabled = ? AND next_fire_at IS NOT NULL", bun.List(taskIDs), store.TriggerTypeCron, true).
		Order("next_fire_at", "id").Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("next fires: %w", err)
	}
	out := map[uuid.UUID]plannedFire{}
	for _, tr := range triggers {
		if _, seen := out[tr.TaskID]; !seen {
			out[tr.TaskID] = plannedFire{TriggerID: tr.ID, At: tr.NextFireAt, Schedule: tr.Schedule, Timezone: tr.Timezone}
		}
	}
	return out, nil
}

// Archive archives a task: its triggers stop, it no longer runs or
// changes, and its name is free for a new task. Runs already started go
// on. Archiving an archived task returns it unchanged.
func (t *Tasks) Archive(ctx context.Context, p *auth.Principal, id uuid.UUID) (TaskDetail, error) {
	task, err := t.load(ctx, p, id, ActionModifyTask)
	if err != nil {
		return TaskDetail{}, err
	}
	if task.ArchivedAt.IsZero() {
		err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
			now := store.Now()
			err := tx.NewUpdate().Model(&task).
				Set("archived_at = ?", now).
				Set("state = ?", store.TaskInactive).
				Set("updated_at = ?", now).
				Where("id = ? AND archived_at IS NULL", id).
				Returning("*").Scan(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				// Archived concurrently; read what the other call wrote.
				return tx.NewSelect().Model(&task).Where("id = ?", id).Scan(ctx)
			}
			if err != nil {
				return err
			}
			if _, err := tx.NewUpdate().Model((*store.Trigger)(nil)).
				Set("enabled = ?", false).Set("next_fire_at = NULL").Set("updated_at = ?", now).
				Where("task_id = ?", id).Exec(ctx); err != nil {
				return fmt.Errorf("stop triggers: %w", err)
			}
			kind, actor := actorOf(p)
			return writeAudit(ctx, tx, auditEntry{
				actorKind: kind, actorID: actor,
				action: "task.archive", resourceKind: "task", resourceID: id.String(),
				result:  resultSuccess,
				details: map[string]any{"project_id": task.ProjectID.String(), "name": task.Name},
			})
		})
		if err != nil {
			return TaskDetail{}, err
		}
	}
	return t.detail(ctx, task)
}

// SetActivation activates version seq of a task (the latest when seq is
// nil), or pauses the task when active is false. With the
// test-before-activate switch on, a version without a successful test
// run is ErrVersionNotTested.
func (t *Tasks) SetActivation(ctx context.Context, p *auth.Principal, id uuid.UUID, active bool, seq *int) (TaskDetail, error) {
	task, err := t.load(ctx, p, id, ActionModifyTask)
	if err != nil {
		return TaskDetail{}, err
	}
	if !task.ArchivedAt.IsZero() {
		return TaskDetail{}, ErrTaskArchived
	}
	var version store.TaskVersion
	if active {
		n := task.LatestSeq
		if seq != nil {
			n = *seq
		}
		err := t.db.NewSelect().Model(&version).Where("task_id = ? AND seq = ?", id, n).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			var v validation
			v.content("#/version", fmt.Sprintf("任务没有版本 %d，最新版本是 %d", n, task.LatestSeq))
			return TaskDetail{}, v.err()
		}
		if err != nil {
			return TaskDetail{}, err
		}
		required, err := t.requireTest(ctx)
		if err != nil {
			return TaskDetail{}, fmt.Errorf("read the test-before-activate setting: %w", err)
		}
		if required && version.TestedAt.IsZero() {
			return TaskDetail{}, ErrVersionNotTested
		}
	}
	err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		now := store.Now()
		q := tx.NewUpdate().Model(&task).Set("updated_at = ?", now).
			Where("id = ? AND archived_at IS NULL", id).Returning("*")
		details := map[string]any{"project_id": task.ProjectID.String(), "active": active}
		if active {
			q = q.Set("state = ?", store.TaskActive).
				Set("active_version_id = ?", version.ID).
				Set("active_seq = ?", version.Seq).
				Set("activated_at = ?", now)
			details["version"] = version.Seq
		} else {
			q = q.Set("state = ?", store.TaskInactive)
		}
		if err := q.Scan(ctx); errors.Is(err, sql.ErrNoRows) {
			return ErrTaskArchived
		} else if err != nil {
			return err
		}
		action := "task.pause"
		if active {
			action = "task.activate"
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: action, resourceKind: "task", resourceID: id.String(),
			result: resultSuccess, details: details,
		})
	})
	if err != nil {
		return TaskDetail{}, err
	}
	return t.detail(ctx, task)
}

// ConsoleOptions are the changes setTaskConsole asks for; a nil field
// stays as it is.
type ConsoleOptions struct {
	ManualRun *bool
}

// SetConsole changes how the console shows a task. The options belong to
// the task, not to a version, so no version is created.
func (t *Tasks) SetConsole(ctx context.Context, p *auth.Principal, id uuid.UUID, in ConsoleOptions) (TaskDetail, error) {
	task, err := t.load(ctx, p, id, ActionModifyTask)
	if err != nil {
		return TaskDetail{}, err
	}
	if !task.ArchivedAt.IsZero() {
		return TaskDetail{}, ErrTaskArchived
	}
	if in.ManualRun == nil {
		return t.detail(ctx, task)
	}
	err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		err := tx.NewUpdate().Model(&task).
			Set("console_hide_manual_run = ?", !*in.ManualRun).
			Set("updated_at = ?", store.Now()).
			Where("id = ? AND archived_at IS NULL", id).Returning("*").Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskArchived
		} else if err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "task.console", resourceKind: "task", resourceID: id.String(),
			result:  resultSuccess,
			details: map[string]any{"project_id": task.ProjectID.String(), "manual_run": *in.ManualRun},
		})
	})
	if err != nil {
		return TaskDetail{}, err
	}
	return t.detail(ctx, task)
}

// VersionSummary is a version in a list.
type VersionSummary struct {
	Version   store.TaskVersion
	Manifest  Manifest
	FileCount int
	Active    bool
}

// VersionPage is a page of versions, newest first.
type VersionPage struct {
	Versions   []VersionSummary
	NextCursor string
}

const versionCursor = "v"

// Versions lists the versions of a task, newest first.
func (t *Tasks) Versions(ctx context.Context, p *auth.Principal, id uuid.UUID, limit *int, cursor string) (VersionPage, error) {
	var v validation
	size := pageSize(&v, limit)
	before := 0
	if cursor != "" {
		parts, err := decodePageCursor(cursor, versionCursor, 1)
		if err == nil {
			before, err = strconv.Atoi(parts[0])
		}
		if err != nil || before < 1 {
			v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
		}
	}
	if err := v.err(); err != nil {
		return VersionPage{}, err
	}
	task, err := t.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return VersionPage{}, err
	}
	var versions []store.TaskVersion
	q := t.db.NewSelect().Model(&versions).Where("task_id = ?", id)
	if before > 0 {
		q = q.Where("seq < ?", before)
	}
	if err := q.Order("seq DESC").Limit(int64(size + 1)).Scan(ctx); err != nil {
		return VersionPage{}, fmt.Errorf("list versions: %w", err)
	}
	var page VersionPage
	if len(versions) > size {
		versions = versions[:size]
		page.NextCursor = encodePageCursor(versionCursor, strconv.Itoa(versions[size-1].Seq))
	}
	if len(versions) == 0 {
		return page, nil
	}
	ids := make([]uuid.UUID, len(versions))
	for i, ver := range versions {
		ids[i] = ver.ID
	}
	var counts []struct {
		VersionID uuid.UUID `bun:"version_id"`
		N         int       `bun:"n"`
	}
	if err := t.db.NewSelect().Model((*store.TaskFile)(nil)).ColumnExpr("version_id, COUNT(*) AS n").
		Where("version_id IN (?)", bun.List(ids)).Group("version_id").Scan(ctx, &counts); err != nil {
		return VersionPage{}, fmt.Errorf("count version files: %w", err)
	}
	n := map[uuid.UUID]int{}
	for _, c := range counts {
		n[c.VersionID] = c.N
	}
	for _, ver := range versions {
		m, err := decodeManifest(ver.Manifest)
		if err != nil {
			return VersionPage{}, fmt.Errorf("version %s: %w", ver.ID, err)
		}
		page.Versions = append(page.Versions, VersionSummary{
			Version: ver, Manifest: m, FileCount: n[ver.ID], Active: task.ActiveSeq == ver.Seq,
		})
	}
	return page, nil
}

// Version returns version seq of a task with its files.
func (t *Tasks) Version(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int) (VersionDetail, error) {
	if _, err := t.load(ctx, p, id, ActionViewTask); err != nil {
		return VersionDetail{}, err
	}
	return t.version(ctx, id, seq)
}

// FileContent is a file of a version with its content.
type FileContent struct {
	File    store.TaskFile
	Content string
}

// File returns the content of one file of a version. A path the version
// does not have is ErrNotFound.
func (t *Tasks) File(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int, path string) (FileContent, error) {
	v, err := t.Version(ctx, p, id, seq)
	if err != nil {
		return FileContent{}, err
	}
	i := slices.IndexFunc(v.Files, func(f store.TaskFile) bool { return f.Path == path })
	if i < 0 {
		return FileContent{}, ErrNotFound
	}
	f := v.Files[i]
	content, err := blob.ReadAll(ctx, t.blobs, f.BlobHash)
	if err != nil {
		return FileContent{}, fmt.Errorf("read %s of version %d: %w", path, seq, err)
	}
	return FileContent{File: f, Content: string(content)}, nil
}

// Diff compares version seq with version base. Without base it compares
// with the active version, or with the previous version when none is
// active or seq is the active one; version 1 then compares with an empty
// task.
func (t *Tasks) Diff(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int, base *int) (TaskDiff, error) {
	task, err := t.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return TaskDiff{}, err
	}
	target, err := t.version(ctx, id, seq)
	if err != nil {
		return TaskDiff{}, err
	}
	var from *int
	switch {
	case base != nil:
		if *base > task.LatestSeq || *base < 1 {
			var v validation
			v.param("base", fmt.Sprintf("任务没有版本 %d，最新版本是 %d", *base, task.LatestSeq))
			return TaskDiff{}, v.err()
		}
		from = base
	case task.ActiveSeq != 0 && task.ActiveSeq != seq:
		from = &task.ActiveSeq
	case seq > 1:
		prev := seq - 1
		from = &prev
	}
	contents := map[string]string{}
	for _, f := range target.Files {
		content, err := blob.ReadAll(ctx, t.blobs, f.BlobHash)
		if err != nil {
			return TaskDiff{}, fmt.Errorf("read %s of version %d: %w", f.Path, seq, err)
		}
		contents[f.Path] = string(content)
	}
	return t.diffAgainst(ctx, id, from, proposedVersion{manifest: target.Manifest, files: target.Files, contents: contents}, seq)
}

// ValidationReport says whether a version can run and be activated now.
type ValidationReport struct {
	Seq         int
	Errors      []Issue
	Warnings    []Issue
	Activatable bool
}

// Validate checks version seq against the deployment as it is now.
func (t *Tasks) Validate(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int) (ValidationReport, error) {
	task, err := t.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return ValidationReport{}, err
	}
	v, err := t.version(ctx, id, seq)
	if err != nil {
		return ValidationReport{}, err
	}
	paths := map[string]bool{}
	for _, f := range v.Files {
		paths[f.Path] = true
	}
	errs, warns, err := t.check(ctx, task.ProjectID, task.ID, v.Manifest, paths, !v.Version.TestedAt.IsZero())
	if err != nil {
		return ValidationReport{}, err
	}
	notTested := slices.ContainsFunc(warns, func(i Issue) bool { return i.Code == codeVersionNotTested })
	return ValidationReport{
		Seq:         seq,
		Errors:      errs,
		Warnings:    warns,
		Activatable: len(errs) == 0 && !notTested && task.ArchivedAt.IsZero(),
	}, nil
}
