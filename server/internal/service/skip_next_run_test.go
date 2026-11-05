package service_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestSkipNextTaskRun(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap()).Task
	f.activate(task)
	disabled := cron("disabled", "0 * * * *")
	no := false
	disabled.Enabled = &no
	before := f.set(alice, task.ID, nil, cron("hourly-a", "0 * * * *"), cron("hourly-b", "0 * * * *"),
		cron("later", "0 */2 * * *"), disabled, webhook("push"))
	expected := t0.Add(time.Hour)
	after, err := f.triggers.SkipNext(f.ctx, alice, task.ID, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !after.TaskActive || after.Revision != before.Revision {
		t.Fatalf("skip changed task activation or trigger configuration: %+v", after)
	}
	for _, tr := range after.Triggers {
		if tr.Trigger.Type == store.TriggerTypeCron && tr.Trigger.Enabled {
			if !tr.NextFireAt.Equal(t0.Add(2*time.Hour)) || !tr.NextFires[0].Equal(tr.NextFireAt) {
				t.Fatalf("next fires disagree after skip: %+v", tr)
			}
		}
		if tr.Token != "" || !tr.Trigger.LastFiredAt.IsZero() {
			t.Fatalf("skip exposed a token or changed the last fire: %+v", tr)
		}
	}
	runs := f.runsOf(task.ID)
	if len(runs) != 2 {
		t.Fatalf("skipped runs = %d, want 2", len(runs))
	}
	for _, run := range runs {
		if run.Status != store.RunSkipped || run.SkipReason != store.SkipManual || !run.ScheduledAt.Equal(expected) || run.CreatedBy != alice.UserID {
			t.Fatalf("skipped run = %+v", run)
		}
	}
	if _, err := f.triggers.SkipNext(f.ctx, alice, task.ID, expected); err == nil {
		t.Fatal("a duplicate request skipped the following occurrence")
	}
	// A fresh scheduler honours the skip and resumes without user action.
	f.triggers = f.newTriggers()
	if got := f.fireDue(expected); got != 0 {
		t.Fatalf("skipped time fired %d runs", got)
	}
	if got := f.fireDue(t0.Add(2 * time.Hour)); got != 3 {
		t.Fatalf("following time fired %d runs, want 3", got)
	}
	var audits int64
	audits, err = f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ?", "task.skip_next").Count(f.ctx)
	if err != nil || audits != 1 {
		t.Fatalf("skip audits = %d, %v", audits, err)
	}
}

func TestSkipNextTaskRunCalendar(t *testing.T) {
	f := newTriggerFixture(t, service.TriggerOptions{})
	f.setClock(store.Now())
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest()).Task
	f.activate(task)
	set := f.set(alice, task.ID, nil, cron("minute", "* * * * *"))
	next := set.Triggers[0].NextFireAt
	if _, err := f.triggers.SkipNext(f.ctx, alice, task.ID, next); err != nil {
		t.Fatal(err)
	}
	to := next.Add(2 * time.Minute)
	cal, err := f.runs.Calendar(f.ctx, alice, service.CalendarInput{From: &next, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	if len(cal.Items) != 1 || !cal.Items[0].Start.Equal(next.Add(time.Minute)) {
		t.Fatalf("calendar still plans the skipped time: %+v", cal.Items)
	}
}

func TestSkipNextTaskRunGuards(t *testing.T) {
	for _, name := range []string{"inactive", "archived", "overdue", "changed", "no-cron", "viewer", "outsider"} {
		t.Run(name, func(t *testing.T) {
			f := newTriggerFixture(t, service.TriggerOptions{})
			alice := f.user("alice")
			task := f.apply(alice, "digest", basicManifest()).Task
			f.activate(task)
			input := cron("hourly", "0 * * * *")
			if name == "no-cron" {
				input = webhook("push")
			}
			f.set(alice, task.ID, nil, input)
			expected := t0.Add(time.Hour)
			caller := alice
			var want error
			switch name {
			case "inactive":
				f.pause(task)
			case "archived":
				_, err := f.tasks.Archive(f.ctx, alice, task.ID)
				if err != nil {
					t.Fatal(err)
				}
				want = service.ErrTaskArchived
			case "overdue":
				f.setClock(expected)
			case "changed":
				expected = expected.Add(time.Hour)
			case "viewer", "outsider":
				caller = f.user("bob")
				want = service.ErrNotFound
				if name == "viewer" {
					f.grant(alice.PersonalProjectID, caller, service.RoleViewer)
					want = service.ErrPermissionDenied
				}
			}
			_, err := f.triggers.SkipNext(f.ctx, caller, task.ID, expected)
			if err == nil || want != nil && !errors.Is(err, want) {
				t.Fatalf("skip error = %v, want %v", err, want)
			}
			if got := f.runsOf(task.ID); len(got) != 0 {
				t.Fatalf("rejected skip created %d runs", len(got))
			}
		})
	}
}

func TestSkipNextTaskRunConcurrent(t *testing.T) {
	for _, raceScheduler := range []bool{false, true} {
		t.Run(map[bool]string{false: "two-users", true: "scheduler"}[raceScheduler], func(t *testing.T) {
			f := newTriggerFixture(t, service.TriggerOptions{})
			alice := f.user("alice")
			task := f.apply(alice, "digest", basicManifest()).Task
			f.activate(task)
			f.set(alice, task.ID, nil, cron("hourly", "0 * * * *"))
			expected := t0.Add(time.Hour)
			var wg sync.WaitGroup
			for i := range 2 {
				wg.Go(func() {
					if raceScheduler && i == 1 {
						if _, err := f.newTriggers().FireDue(f.ctx, expected); err != nil {
							t.Error(err)
						}
					} else {
						// A racing caller may lose to the other user or scheduler.
						_, _ = f.newTriggers().SkipNext(f.ctx, alice, task.ID, expected)
					}
				})
			}
			wg.Wait()
			runs := f.runsOf(task.ID)
			if len(runs) != 1 || !runs[0].ScheduledAt.Equal(expected) {
				t.Fatalf("concurrent skip/fire produced %+v", runs)
			}
		})
	}
}
