package service_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// t0 is a Monday, midnight UTC. Scheduler tests run on a fake clock that
// starts here; FireDue and RetryFailed are given its readings.
var t0 = time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)

type triggerFixture struct {
	*fixture
	mu       sync.Mutex
	clock    time.Time
	triggers *service.Triggers
	opts     service.TriggerOptions
}

func newTriggerFixture(t *testing.T, opts service.TriggerOptions) *triggerFixture {
	t.Helper()
	f := &triggerFixture{fixture: newFixture(t), clock: t0, opts: opts}
	f.triggers = f.newTriggers()
	return f
}

// newTriggers returns a fresh trigger service on the same database, as
// after a restart or on a second server.
func (f *triggerFixture) newTriggers() *service.Triggers {
	opts := f.opts
	opts.Now = f.now
	return service.NewTriggers(f.db, service.NewAuthorizer(f.db), f.blobs, nil, opts)
}

func (f *triggerFixture) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *triggerFixture) setClock(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clock = t
}

// activate makes the task's latest version the active one through the
// task service, as its creator. The test-before-activate rule is off
// here: a test run would be one more run in the queue these tests count.
func (f *triggerFixture) activate(task store.Task) {
	f.t.Helper()
	f.setActive(task, true)
}

func (f *triggerFixture) pause(task store.Task) {
	f.t.Helper()
	f.setActive(task, false)
}

func (f *triggerFixture) setActive(task store.Task, active bool) {
	f.t.Helper()
	tasks := service.NewTasks(f.db, service.NewAuthorizer(f.db), f.blobs, nil).
		WithTestBeforeActivate(func(context.Context) (bool, error) { return false, nil })
	if _, err := tasks.SetActivation(f.ctx, &auth.Principal{UserID: task.CreatedBy}, task.ID, active, nil); err != nil {
		f.t.Fatalf("set activation: %v", err)
	}
}

func (f *triggerFixture) set(p *auth.Principal, taskID uuid.UUID, ifMatch *int, inputs ...service.TriggerInput) service.TriggerSet {
	f.t.Helper()
	set, err := f.triggers.Set(f.ctx, p, taskID, service.SetTriggersInput{Triggers: inputs, IfMatch: ifMatch})
	if err != nil {
		f.t.Fatalf("set triggers: %v", err)
	}
	return set
}

func (f *triggerFixture) fireDue(at time.Time) int {
	f.t.Helper()
	n, err := f.triggers.FireDue(f.ctx, at)
	if err != nil {
		f.t.Fatalf("fire due: %v", err)
	}
	return n
}

func (f *triggerFixture) runsOf(taskID uuid.UUID) []store.Run {
	f.t.Helper()
	var runs []store.Run
	if err := f.db.NewSelect().Model(&runs).Where("task_id = ?", taskID).Order("queued_at", "scheduled_at", "id").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return runs
}

func (f *triggerFixture) fires(triggerID uuid.UUID) []store.TriggerFire {
	f.t.Helper()
	var fires []store.TriggerFire
	if err := f.db.NewSelect().Model(&fires).Where("trigger_id = ?", triggerID).Order("scheduled_at").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return fires
}

func cron(name, schedule string) service.TriggerInput {
	return service.TriggerInput{Name: name, Type: store.TriggerTypeCron, Schedule: schedule, Timezone: "UTC"}
}

func webhook(name string) service.TriggerInput {
	return service.TriggerInput{Name: name, Type: store.TriggerTypeWebhook}
}

func allowOverlap() service.Manifest {
	m := basicManifest()
	m.Concurrency = service.ConcurrencyAllow
	return m
}

func TestSetTriggersRevisionsAndTokens(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest()).Task

	weekday := service.TriggerInput{Name: "weekday-morning", Type: store.TriggerTypeCron, Schedule: "0 8 * * 1-5", Timezone: "Asia/Shanghai"}
	preview, err := f.triggers.Set(f.ctx, alice, task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{weekday, webhook("crm-push")}, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || preview.Revision != 0 || preview.TaskActive {
		t.Fatalf("preview = %+v", preview)
	}
	c := preview.Triggers[0]
	// 08:00 in Shanghai is 00:00 UTC; t0 itself is excluded.
	if c.ScheduleText != "每个工作日 08:00（Asia/Shanghai）" || len(c.NextFires) != 5 || !c.NextFires[0].Equal(t0.Add(24*time.Hour)) {
		t.Fatalf("cron preview = %q %v", c.ScheduleText, c.NextFires)
	}
	if !c.NextFireAt.IsZero() {
		t.Fatal("next_fire_at must be empty while the task is inactive")
	}
	if preview.Triggers[1].Token != "" {
		t.Fatal("a dry run must not issue tokens")
	}
	if got, _ := f.triggers.List(f.ctx, alice, task.ID); len(got.Triggers) != 0 {
		t.Fatal("a dry run saved triggers")
	}

	set := f.set(alice, task.ID, nil, weekday, webhook("crm-push"))
	if set.Revision != 1 || len(set.Triggers) != 2 {
		t.Fatalf("set = %+v", set)
	}
	hook := set.Triggers[1]
	if !strings.HasPrefix(hook.Token, auth.TriggerTokenPrefix) || hook.Trigger.TokenPrefix != hook.Token[:len(auth.TriggerTokenPrefix)+4] {
		t.Fatalf("webhook token %q prefix %q", hook.Token, hook.Trigger.TokenPrefix)
	}
	if hook.Trigger.TokenHash == hook.Token || hook.Trigger.MaxBodyBytes != 1<<20 {
		t.Fatalf("stored webhook = %+v", hook.Trigger)
	}
	listed, err := f.triggers.List(f.ctx, alice, task.ID)
	if err != nil || listed.Revision != 1 || listed.Triggers[0].Token != "" || listed.Triggers[1].Token != "" {
		t.Fatalf("list = %+v, %v", listed, err)
	}

	_, err = f.triggers.Set(f.ctx, alice, task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{weekday}})
	wantErr(t, err, service.ErrPreconditionRequired)
	stale := 0
	_, err = f.triggers.Set(f.ctx, alice, task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{weekday}, IfMatch: &stale})
	wantErr(t, err, service.ErrVersionConflict)

	// Same name and type keep ID and token; another type is a new
	// trigger; triggers not listed are deleted.
	one := 1
	renamedType := service.TriggerInput{Name: "weekday-morning", Type: store.TriggerTypeWebhook}
	again := f.set(alice, task.ID, &one, webhook("crm-push"), renamedType)
	if again.Revision != 2 || len(again.Triggers) != 2 {
		t.Fatalf("again = %+v", again)
	}
	if again.Triggers[0].Trigger.ID != hook.Trigger.ID || again.Triggers[0].Trigger.TokenHash != hook.Trigger.TokenHash || again.Triggers[0].Token != "" {
		t.Fatal("a kept webhook must keep its ID and token")
	}
	if again.Triggers[1].Trigger.ID == c.Trigger.ID || again.Triggers[1].Token == "" {
		t.Fatal("a trigger that changed type must be new")
	}
	two := 2
	if empty := f.set(alice, task.ID, &two); empty.Revision != 3 || len(empty.Triggers) != 0 {
		t.Fatalf("clearing = %+v", empty)
	}

	var audits []store.AuditEvent
	if err := f.db.NewSelect().Model(&audits).Where("action = ?", "trigger.set").Order("ts").Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(audits) != 3 || audits[0].ResourceID != task.ID.String() {
		t.Fatalf("audit = %+v", audits)
	}
	for _, a := range audits {
		for _, v := range a.Details {
			if s, ok := v.(string); ok && strings.HasPrefix(s, auth.TriggerTokenPrefix) {
				t.Fatal("a token reached the audit log")
			}
		}
	}
}

func TestSetTriggersValidation(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest()).Task
	size := int64(512)
	tests := []struct {
		name   string
		inputs []service.TriggerInput
		want   string
	}{
		{"missing timezone", []service.TriggerInput{{Name: "a", Type: "cron", Schedule: "0 8 * * *"}}, "#/triggers/0/timezone"},
		{"unknown timezone", []service.TriggerInput{{Name: "a", Type: "cron", Schedule: "0 8 * * *", Timezone: "Asia/Beijing"}}, "#/triggers/0/timezone"},
		{"missing schedule", []service.TriggerInput{{Name: "a", Type: "cron", Timezone: "UTC"}}, "#/triggers/0/schedule"},
		{"bad schedule", []service.TriggerInput{cron("a", "0 25 * * *")}, "#/triggers/0/schedule"},
		{"every", []service.TriggerInput{cron("a", "@every 1m")}, "#/triggers/0/schedule"},
		{"duplicate name", []service.TriggerInput{cron("a", "@daily"), webhook("a")}, "#/triggers/1/name"},
		{"webhook schedule", []service.TriggerInput{{Name: "a", Type: "webhook", Schedule: "@daily"}}, "#/triggers/0/schedule"},
		{"cron body limit", []service.TriggerInput{{Name: "a", Type: "cron", Schedule: "@daily", Timezone: "UTC", MaxBodyBytes: &size}}, "#/triggers/0/max_body_bytes"},
		{"small body limit", []service.TriggerInput{{Name: "a", Type: "webhook", MaxBodyBytes: &size}}, "#/triggers/0/max_body_bytes"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := f.triggers.Set(f.ctx, alice, task.ID, service.SetTriggersInput{Triggers: tt.inputs, DryRun: true})
			fields, _ := fieldErrors(t, err)
			if _, ok := fields[tt.want]; !ok {
				t.Fatalf("errors %v, want one at %s", fields, tt.want)
			}
		})
	}
}

func TestTriggerPermissions(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	task := f.apply(alice, "digest", basicManifest()).Task
	f.grant(alice.PersonalProjectID, bob, service.RoleViewer)
	set := f.set(alice, task.ID, nil, webhook("push"))

	if _, err := f.triggers.List(f.ctx, bob, task.ID); err != nil {
		t.Fatalf("viewer list: %v", err)
	}
	_, err := f.triggers.Set(f.ctx, bob, task.ID, service.SetTriggersInput{DryRun: true})
	wantErr(t, err, service.ErrForbidden)
	_, err = f.triggers.RotateToken(f.ctx, bob, set.Triggers[0].Trigger.ID)
	wantErr(t, err, service.ErrForbidden)
	_, err = f.triggers.List(f.ctx, carol, task.ID)
	wantErr(t, err, service.ErrNotFound)
	_, err = f.triggers.RotateToken(f.ctx, carol, set.Triggers[0].Trigger.ID)
	wantErr(t, err, service.ErrNotFound)
}

func TestFireDueRecordsEachScheduledTimeOnce(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	set := f.set(alice, task.ID, nil, cron("every-5", "*/5 * * * *"))
	trig := set.Triggers[0].Trigger
	if !trig.NextFireAt.Equal(t0.Add(5 * time.Minute)) {
		t.Fatalf("next fire = %v", trig.NextFireAt)
	}
	f.activate(task)

	if n := f.fireDue(t0.Add(4 * time.Minute)); n != 0 {
		t.Fatalf("fired %d runs before the first scheduled time", n)
	}
	at := t0.Add(5*time.Minute + 300*time.Millisecond)
	if n := f.fireDue(at); n != 1 {
		t.Fatalf("fired %d runs, want 1", n)
	}
	// The same tick again, a restarted server and a second leader racing
	// it: the scheduled time is already recorded.
	if n := f.fireDue(at); n != 0 {
		t.Fatalf("a repeated tick fired %d runs", n)
	}
	var wg sync.WaitGroup
	for range 4 {
		other := f.newTriggers()
		wg.Go(func() {
			if _, err := other.FireDue(f.ctx, t0.Add(10*time.Minute)); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	runs := f.runsOf(task.ID)
	if len(runs) != 2 {
		t.Fatalf("got %d runs, want 2", len(runs))
	}
	for i, r := range runs {
		want := t0.Add(time.Duration(5*(i+1)) * time.Minute)
		if r.Status != store.RunQueued || r.TriggerKind != store.TriggerCron || r.TriggerID != trig.ID ||
			!r.ScheduledAt.Equal(want) || r.TriggerFireID == uuid.Nil || r.Attempt != 1 || r.MaxAttempts != 1 {
			t.Fatalf("run %d = %+v", i, r)
		}
	}
	fires := f.fires(trig.ID)
	if len(fires) != 2 || fires[0].RunID != runs[0].ID || fires[0].Outcome != store.FireQueued {
		t.Fatalf("fires = %+v", fires)
	}
	listed, _ := f.triggers.List(f.ctx, alice, task.ID)
	got := listed.Triggers[0]
	if !got.NextFireAt.Equal(t0.Add(15*time.Minute)) || !got.Trigger.LastFiredAt.Equal(t0.Add(10*time.Minute)) {
		t.Fatalf("after firing: next %v last %v", got.NextFireAt, got.Trigger.LastFiredAt)
	}
}

func TestFireDueUsesTriggerParams(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	m := allowOverlap()
	m.Params = map[string]any{"source": "crm"}
	task := f.apply(alice, "digest", m).Task
	withParams := cron("own", "*/5 * * * *")
	withParams.Params = map[string]any{"source": "erp"}
	f.set(alice, task.ID, nil, cron("manifest", "*/5 * * * *"), withParams)
	f.activate(task)
	f.fireDue(t0.Add(5 * time.Minute))
	params := map[string]bool{}
	for _, r := range f.runsOf(task.ID) {
		params[r.Params] = true
	}
	if !params[`{"source":"crm"}`] || !params[`{"source":"erp"}`] {
		t.Fatalf("params = %v", params)
	}
}

func TestInactiveTasksDoNotFire(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	trig := f.set(alice, task.ID, nil, cron("hourly", "@hourly")).Triggers[0].Trigger

	// Never activated: the trigger moves on and records nothing.
	f.fireDue(t0.Add(3*time.Hour + time.Second))
	if runs := f.runsOf(task.ID); len(runs) != 0 {
		t.Fatalf("an inactive task got runs: %+v", runs)
	}
	f.activate(task)
	// Activated later, it fires at the next hour and nothing earlier
	// counts as missed.
	f.fireDue(t0.Add(4*time.Hour + time.Second))
	runs := f.runsOf(task.ID)
	if len(runs) != 1 || runs[0].Status != store.RunQueued || !runs[0].ScheduledAt.Equal(t0.Add(4*time.Hour)) {
		t.Fatalf("runs = %+v", runs)
	}
	f.pause(task)
	f.fireDue(t0.Add(5*time.Hour + time.Second))
	if len(f.runsOf(task.ID)) != 1 {
		t.Fatal("a paused task fired")
	}
	if fires := f.fires(trig.ID); len(fires) != 1 {
		t.Fatalf("fires = %+v", fires)
	}
}

func TestMissedFires(t *testing.T) {
	tests := []struct {
		catchUp string
		// want lists the runs after the outage: status, skip reason and
		// scheduled minute.
		want []struct {
			status, reason string
			minute         int
		}
		missed map[int]int
	}{
		{
			catchUp: store.CatchUpSkip,
			want: []struct {
				status, reason string
				minute         int
			}{{store.RunSkipped, store.SkipMissed, 60}},
			missed: map[int]int{60: 12},
		},
		{
			catchUp: store.CatchUpLatest,
			want: []struct {
				status, reason string
				minute         int
			}{
				{store.RunSkipped, store.SkipMissed, 55}, {store.RunQueued, "", 60},
			},
			missed: map[int]int{55: 11, 60: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.catchUp, func(t *testing.T) {
			f := newTriggerFixture(t, service.TriggerOptions{})
			alice := f.user("alice")
			task := f.apply(alice, "digest", allowOverlap()).Task
			in := cron("every-5", "*/5 * * * *")
			in.CatchUp = tt.catchUp
			trig := f.set(alice, task.ID, nil, in).Triggers[0].Trigger
			f.activate(task)

			// The server was down from t0 until 62 minutes later: every
			// time from :05 to :60 is more than 60 seconds late.
			f.fireDue(t0.Add(62 * time.Minute))
			runs := f.runsOf(task.ID)
			if len(runs) != len(tt.want) {
				t.Fatalf("runs = %+v", runs)
			}
			slices := map[int]store.Run{}
			for _, r := range runs {
				slices[int(r.ScheduledAt.Sub(t0)/time.Minute)] = r
			}
			for _, w := range tt.want {
				r, ok := slices[w.minute]
				if !ok || r.Status != w.status || r.SkipReason != w.reason {
					t.Fatalf("run at :%d = %+v, want %s %s", w.minute, r, w.status, w.reason)
				}
				if w.reason == store.SkipMissed && (!strings.Contains(r.ErrorMessage, "错过") || r.FinishedAt.IsZero() || r.ConcurrencyKey != "") {
					t.Fatalf("skipped run = %+v", r)
				}
			}
			for _, fire := range f.fires(trig.ID) {
				m := int(fire.ScheduledAt.Sub(t0) / time.Minute)
				if want, ok := tt.missed[m]; !ok || fire.MissedCount != want {
					t.Fatalf("fire at :%d missed %d, want %v", m, fire.MissedCount, tt.missed)
				}
			}
			listed, _ := f.triggers.List(f.ctx, alice, task.ID)
			if next := listed.Triggers[0].NextFireAt; !next.Equal(t0.Add(65 * time.Minute)) {
				t.Fatalf("next fire = %v, want :65", next)
			}
		})
	}
}

// A fire processed late but within the grace period still runs.
func TestLateFireWithinGraceRuns(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{MissedAfter: 2 * time.Minute})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	f.set(alice, task.ID, nil, cron("every-5", "*/5 * * * *"))
	f.activate(task)
	f.fireDue(t0.Add(6*time.Minute + 59*time.Second))
	runs := f.runsOf(task.ID)
	if len(runs) != 1 || runs[0].Status != store.RunQueued {
		t.Fatalf("runs = %+v", runs)
	}
}

func TestOverlapPolicy(t *testing.T) {
	for _, policy := range []string{service.ConcurrencyForbid, service.ConcurrencyAllow} {
		t.Run(policy, func(t *testing.T) {
			f := newTriggerFixture(t, service.TriggerOptions{})
			alice := f.user("alice")
			m := basicManifest()
			m.Concurrency = policy
			task := f.apply(alice, "digest", m).Task
			trig := f.set(alice, task.ID, nil, cron("every-5", "*/5 * * * *")).Triggers[0].Trigger
			f.activate(task)
			// No runner claims anything, so the first run is still queued
			// when the second time comes.
			f.fireDue(t0.Add(5 * time.Minute))
			f.fireDue(t0.Add(10 * time.Minute))
			runs := f.runsOf(task.ID)
			if len(runs) != 2 || runs[0].Status != store.RunQueued {
				t.Fatalf("runs = %+v", runs)
			}
			second := runs[1]
			fires := f.fires(trig.ID)
			if policy == service.ConcurrencyForbid {
				if second.Status != store.RunSkipped || second.SkipReason != store.SkipOverlap || fires[1].Outcome != store.FireSkipped {
					t.Fatalf("second run = %+v", second)
				}
				return
			}
			if second.Status != store.RunQueued || fires[1].Outcome != store.FireQueued {
				t.Fatalf("second run = %+v", second)
			}
		})
	}
}

func TestQueueLimits(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{Queue: service.QueueLimits{MaxQueuedPerProject: 1}})
	alice, bob := f.user("alice"), f.user("bob")
	task := f.apply(alice, "digest", allowOverlap()).Task
	other := f.apply(bob, "other", allowOverlap()).Task
	f.set(alice, task.ID, nil, cron("every-5", "*/5 * * * *"))
	f.set(bob, other.ID, nil, cron("every-5", "*/5 * * * *"))
	f.activate(task)
	f.activate(other)
	f.fireDue(t0.Add(5 * time.Minute))
	f.fireDue(t0.Add(10 * time.Minute))
	runs := f.runsOf(task.ID)
	if len(runs) != 2 || runs[1].Status != store.RunSkipped || runs[1].SkipReason != store.SkipQueueFull {
		t.Fatalf("runs = %+v", runs)
	}
	if got := f.runsOf(other.ID); got[0].Status != store.RunQueued {
		t.Fatal("another project's queue is separate")
	}

	runs2 := service.NewRuns(f.db, service.NewAuthorizer(f.db), nil).WithQueueLimits(service.QueueLimits{MaxQueued: 2})
	_, err := runs2.Start(f.ctx, alice, task.ID, service.StartRunInput{})
	wantErr(t, err, service.ErrQueueFull)
}

func retryManifest(attempts int, delay string, on ...string) service.Manifest {
	m := basicManifest()
	m.Retry = &service.RetryPolicy{MaxAttempts: attempts, Delay: delay, On: on}
	return m
}

// failRun claims the run and finishes it with status and code.
func (f *triggerFixture) failRun(runner string, id uuid.UUID, status, code string) store.Run {
	f.t.Helper()
	got := f.claim(runner, 1)
	if len(got) != 1 || got[0].RunID != id {
		f.t.Fatalf("claimed %+v, want %s", got, id)
	}
	if status != runproto.StatusInterrupted {
		if err := f.coord.Started(f.ctx, runner, id, "", time.Now()); err != nil {
			f.t.Fatal(err)
		}
	}
	exit := 1
	if err := f.coord.Finish(f.ctx, runner, id, runproto.Finish{Status: status, ExitCode: &exit, ErrorCode: code}); err != nil {
		f.t.Fatal(err)
	}
	var run store.Run
	if err := f.db.NewSelect().Model(&run).Where("id = ?", id).Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return run
}

func (f *triggerFixture) retryFailed(at time.Time) int {
	f.t.Helper()
	n, err := f.triggers.RetryFailed(f.ctx, at)
	if err != nil {
		f.t.Fatal(err)
	}
	return n
}

func TestRetriesWaitAndStop(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", retryManifest(2, "10m")).Task
	runner := f.runner("r1")
	first := f.start(alice, task.ID)
	if first.MaxAttempts != 3 {
		t.Fatalf("max attempts = %d, want 1 + 2 retries", first.MaxAttempts)
	}
	failed := f.failRun(runner, first.ID, runproto.StatusFailed, "script_error")

	if n := f.retryFailed(time.Now()); n != 1 {
		t.Fatalf("queued %d retries", n)
	}
	// Deciding again, here or on another server, adds nothing.
	if n := f.retryFailed(time.Now()); n != 0 {
		t.Fatalf("queued %d more retries", n)
	}
	if n, _ := f.newTriggers().RetryFailed(f.ctx, time.Now()); n != 0 {
		t.Fatalf("a second server queued %d retries", n)
	}
	runs := f.runsOf(task.ID)
	retry := runs[1]
	if len(runs) != 2 || retry.Status != store.RunQueued || retry.TriggerKind != store.TriggerRetry || retry.RetryOf != first.ID ||
		retry.Attempt != 2 || retry.MaxAttempts != 3 || !retry.NotBefore.Equal(failed.FinishedAt.Add(10*time.Minute)) {
		t.Fatalf("retry = %+v", retry)
	}
	// The claim query honours not_before.
	if got := f.claim(runner, 4); len(got) != 0 {
		t.Fatalf("claimed a retry before its delay: %+v", got)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("not_before = ?", store.Now().Add(-time.Second)).Where("id = ?", retry.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.failRun(runner, retry.ID, runproto.StatusTimedOut, "timeout")
	f.retryFailed(time.Now())
	third := f.runsOf(task.ID)[2]
	if third.Attempt != 3 || third.RetryOf != retry.ID {
		t.Fatalf("third = %+v", third)
	}
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("not_before = NULL").Where("id = ?", third.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.failRun(runner, third.ID, runproto.StatusFailed, "script_error")
	if n := f.retryFailed(time.Now()); n != 0 || len(f.runsOf(task.ID)) != 3 {
		t.Fatal("the last attempt must not be retried")
	}
}

func TestNoRetry(t *testing.T) {
	tests := []struct {
		name     string
		manifest service.Manifest
		test     bool
		status   string
		code     string
	}{
		{"cancelled", retryManifest(2, ""), false, runproto.StatusCancelled, ""},
		{"credential revoked", retryManifest(2, ""), false, runproto.StatusFailed, "credential_unavailable"},
		{"status not listed", retryManifest(2, "", store.RunTimedOut), false, runproto.StatusFailed, "script_error"},
		{"test run", retryManifest(2, ""), true, runproto.StatusFailed, "script_error"},
		{"retries off", basicManifest(), false, runproto.StatusFailed, "script_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newTriggerFixture(t, service.TriggerOptions{})
			alice := f.user("alice")
			task := f.apply(alice, "digest", tt.manifest).Task
			runner := f.runner("r1")
			d, err := f.runs.Start(f.ctx, alice, task.ID, service.StartRunInput{Test: tt.test})
			if err != nil {
				t.Fatal(err)
			}
			if tt.status == runproto.StatusCancelled {
				f.claim(runner, 1)
				if _, err := f.runs.Cancel(f.ctx, alice, d.Run.ID); err != nil {
					t.Fatal(err)
				}
				if err := f.coord.Finish(f.ctx, runner, d.Run.ID, runproto.Finish{Status: tt.status}); err != nil {
					t.Fatal(err)
				}
			} else {
				f.failRun(runner, d.Run.ID, tt.status, tt.code)
			}
			if n := f.retryFailed(time.Now()); n != 0 || len(f.runsOf(task.ID)) != 1 {
				t.Fatalf("retried: %+v", f.runsOf(task.ID))
			}
		})
	}
}

// A retry of a task that forbids overlap is skipped when another run
// holds the task by then.
func TestRetrySkippedOnOverlap(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", retryManifest(1, "")).Task
	runner := f.runner("r1")
	first := f.start(alice, task.ID)
	f.failRun(runner, first.ID, runproto.StatusFailed, "script_error")
	next := f.start(alice, task.ID)
	f.retryFailed(time.Now())
	runs := f.runsOf(task.ID)
	retry := runs[len(runs)-1]
	if retry.ID == next.ID || retry.Status != store.RunSkipped || retry.SkipReason != store.SkipOverlap || retry.RetryOf != first.ID {
		t.Fatalf("retry = %+v", retry)
	}
}

func (f *triggerFixture) hookCall(trig service.TriggerView, token, key, body string) (service.WebhookResult, error) {
	return f.triggers.Webhook(f.ctx, service.WebhookCall{
		TriggerID: trig.Trigger.ID, Token: token, IdempotencyKey: key, ContentType: "application/json", Body: strings.NewReader(body),
	})
}

func TestWebhook(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{WebhookPerMinute: 1000})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	small := int64(1024)
	limited := webhook("limited")
	limited.MaxBodyBytes = &small
	set := f.set(alice, task.ID, nil, webhook("push"), limited)
	hook, token := set.Triggers[0], set.Triggers[0].Token

	_, err := f.hookCall(hook, token, "", "{}")
	wantErr(t, err, service.ErrNotFound) // the task is not active yet
	f.activate(task)

	for name, call := range map[string]service.WebhookCall{
		"no token":        {TriggerID: hook.Trigger.ID},
		"wrong token":     {TriggerID: hook.Trigger.ID, Token: auth.TriggerTokenPrefix + "nope"},
		"other's token":   {TriggerID: hook.Trigger.ID, Token: set.Triggers[1].Token},
		"unknown trigger": {TriggerID: uuid.New(), Token: token},
	} {
		if _, err := f.triggers.Webhook(f.ctx, call); !errors.Is(err, service.ErrUnauthenticated) {
			t.Errorf("%s: err = %v", name, err)
		}
	}

	res, err := f.hookCall(hook, token, "", `{"lead":1}`)
	if err != nil || res.Status != store.RunQueued || res.Duplicate {
		t.Fatalf("webhook = %+v, %v", res, err)
	}
	run := f.get(alice, res.RunID).Run
	if run.TriggerKind != store.TriggerWebhook || run.TriggerID != hook.Trigger.ID || run.InputContentType != "application/json" {
		t.Fatalf("run = %+v", run)
	}
	rc, err := f.blobs.Get(f.ctx, run.InputKey)
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(stored) != `{"lead":1}` {
		t.Fatalf("stored body %q", stored)
	}

	// Idempotency-Key: the same call returns the first run, another body
	// with the same key is a conflict, and keys are per trigger.
	first, err := f.hookCall(hook, token, "delivery-1", `{"lead":2}`)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.hookCall(hook, token, "delivery-1", `{"lead":2}`)
	if err != nil || !again.Duplicate || again.RunID != first.RunID {
		t.Fatalf("repeat = %+v, %v", again, err)
	}
	_, err = f.hookCall(hook, token, "delivery-1", `{"lead":3}`)
	wantErr(t, err, service.ErrIdempotencyConflict)
	if other, err := f.hookCall(set.Triggers[1], set.Triggers[1].Token, "delivery-1", `{"lead":2}`); err != nil || other.Duplicate {
		t.Fatalf("other trigger = %+v, %v", other, err)
	}

	_, err = f.hookCall(set.Triggers[1], set.Triggers[1].Token, "", strings.Repeat("x", 1025))
	var tooLarge *service.PayloadTooLargeError
	if !errors.As(err, &tooLarge) || tooLarge.Limit != 1024 {
		t.Fatalf("err = %v", err)
	}
	_, err = f.hookCall(hook, token, strings.Repeat("k", 129), "{}")
	fieldErrors(t, err)

	// Rotation: the old token stops working at once.
	rotated, err := f.triggers.RotateToken(f.ctx, alice, hook.Trigger.ID)
	if err != nil || rotated.Token == "" || rotated.Token == token {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	_, err = f.hookCall(hook, token, "", "{}")
	wantErr(t, err, service.ErrUnauthenticated)
	if _, err := f.hookCall(hook, rotated.Token, "", "{}"); err != nil {
		t.Fatal(err)
	}

	// Disabled triggers do not run.
	disabled := webhook("push")
	off := false
	disabled.Enabled = &off
	one := 1
	f.set(alice, task.ID, &one, disabled)
	_, err = f.hookCall(hook, rotated.Token, "", "{}")
	wantErr(t, err, service.ErrNotFound)
}

func TestWebhookConcurrentIdempotentCalls(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{WebhookPerMinute: 1000})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	hook := f.set(alice, task.ID, nil, webhook("push")).Triggers[0]
	f.activate(task)
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		ids = map[uuid.UUID]bool{}
	)
	for range 8 {
		wg.Go(func() {
			res, err := f.newTriggers().Webhook(f.ctx, service.WebhookCall{
				TriggerID: hook.Trigger.ID, Token: hook.Token, IdempotencyKey: "same", Body: bytes.NewReader([]byte("x")),
			})
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			ids[res.RunID] = true
			mu.Unlock()
		})
	}
	wg.Wait()
	if len(ids) != 1 || len(f.runsOf(task.ID)) != 1 {
		t.Fatalf("runs %v", ids)
	}
}

func TestWebhookRateLimit(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{WebhookPerMinute: 2})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	hook := f.set(alice, task.ID, nil, webhook("push")).Triggers[0]
	f.activate(task)
	for i := range 3 {
		_, err := f.hookCall(hook, hook.Token, "", "{}")
		if i < 2 && err != nil {
			t.Fatal(err)
		}
		if i == 2 {
			wantErr(t, err, service.ErrRateLimited)
		}
	}
}

func TestSweepDeletesExpiredIdempotencyRecords(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	hook := f.set(alice, task.ID, nil, webhook("push")).Triggers[0]
	f.activate(task)
	if _, err := f.hookCall(hook, hook.Token, "k", "{}"); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		n, err := f.db.NewSelect().Model((*store.IdempotencyRecord)(nil)).Count(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return int(n)
	}
	if err := f.triggers.Sweep(f.ctx, t0.Add(23*time.Hour)); err != nil || count() != 1 {
		t.Fatalf("swept a live record: %v", err)
	}
	if err := f.triggers.Sweep(f.ctx, t0.Add(25*time.Hour)); err != nil || count() != 0 {
		t.Fatalf("kept an expired record: %v", err)
	}
	// After expiry the key is free again.
	f.setClock(t0.Add(25 * time.Hour))
	if res, err := f.hookCall(hook, hook.Token, "k", "{}"); err != nil || res.Duplicate {
		t.Fatalf("reuse = %+v, %v", res, err)
	}
}
