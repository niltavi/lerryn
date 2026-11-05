package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

type notificationRecorder struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *notificationRecorder) Send(_ context.Context, _, _ string, req notify.Request) (int, error) {
	var ev notify.Event
	if err := json.Unmarshal(req.Body, &ev); err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
	return 200, nil
}

func (r *notificationRecorder) received() []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Event(nil), r.events...)
}

func (f *notifyFixture) recordingNotifications(r *notificationRecorder) *service.Notifications {
	return service.NewNotifications(f.db, service.NewAuthorizer(f.db), service.NotificationsOptions{
		PublicURL: testPublicURL, Sender: r, Now: f.clock.Now,
	})
}

func newRetryNotifyFixture(t *testing.T) (*notifyFixture, *notificationRecorder) {
	t.Helper()
	f := newNotifyFixture(t)
	r := &notificationRecorder{}
	f.n = f.recordingNotifications(r)
	return f, r
}

func TestRunNotificationRetryOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name, status, skip string
		wantOriginal       bool
		wantRetry          bool
	}{
		{name: "queued retry", status: store.RunQueued},
		{name: "running retry", status: store.RunRunning},
		{name: "completed retry", status: store.RunCompleted},
		{name: "explicitly cancelled retry", status: store.RunCancelled},
		{name: "failed retry", status: store.RunFailed, wantRetry: true},
		{name: "timed out retry", status: store.RunTimedOut, wantRetry: true},
		{name: "interrupted retry", status: store.RunInterrupted, wantRetry: true},
		{name: "overlap skips retry", status: store.RunSkipped, skip: store.SkipOverlap, wantOriginal: true},
		{name: "full queue skips retry", status: store.RunSkipped, skip: store.SkipQueueFull, wantOriginal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f, recorder := newRetryNotifyFixture(t)
			alice := f.user("alice")
			ch, _ := f.channel(alice, notify.KindWebhook, "https://notify.example.com/hook", allRunEvents(alice.PersonalProjectID))
			task := f.apply(alice, "digest", basicManifest())
			original := f.endedRun(task, store.RunFailed, func(r *store.Run) { r.Attempt, r.MaxAttempts = 1, 2 })
			retry := f.endedRun(task, tt.status, func(r *store.Run) {
				r.Attempt, r.MaxAttempts, r.RetryOf = 2, 2, original.ID
				r.TriggerKind, r.SkipReason = store.TriggerRetry, tt.skip
				if tt.status == store.RunQueued || tt.status == store.RunRunning {
					r.FinishedAt = time.Time{}
				}
			})
			f.dispatch()
			f.clock.Advance(3 * time.Minute)
			f.dispatch()
			f.dispatch()
			want := 0
			if tt.wantOriginal || tt.wantRetry {
				want = 1
			}
			events := recorder.received()
			if len(events) != want || len(f.deliveries(ch.Channel.ID)) != want {
				t.Fatalf("notifications = %+v; want %d", events, want)
			}
			if want == 1 {
				wantRun, wantStatus := original.ID, store.RunFailed
				if tt.wantRetry {
					wantRun, wantStatus = retry.ID, tt.status
				}
				if ev := events[0]; ev.Run == nil || ev.Run.ID != wantRun || ev.Run.Status != wantStatus || ev.MergedCount != 1 {
					t.Fatalf("notification = %+v, want run %s with status %s", ev, wantRun, wantStatus)
				}
			}
		})
	}
}

func TestRunNotificationSkippedRetryChain(t *testing.T) {
	for _, testRun := range []bool{false, true} {
		f, recorder := newRetryNotifyFixture(t)
		alice := f.user("alice")
		f.channel(alice, notify.KindWebhook, "https://notify.example.com/hook", allRunEvents(alice.PersonalProjectID))
		task := f.apply(alice, "digest", basicManifest())
		first := f.endedRun(task, store.RunFailed, func(r *store.Run) { r.IsTest, r.Attempt, r.MaxAttempts = testRun, 1, 3 })
		second := f.endedRun(task, store.RunTimedOut, func(r *store.Run) {
			r.IsTest, r.Attempt, r.MaxAttempts, r.RetryOf = testRun, 2, 3, first.ID
			r.ErrorCode = "timeout"
		})
		f.endedRun(task, store.RunSkipped, func(r *store.Run) {
			r.IsTest, r.Attempt, r.MaxAttempts, r.RetryOf = testRun, 3, 3, second.ID
			r.SkipReason = store.SkipQueueFull
		})
		f.dispatch()
		f.clock.Advance(3 * time.Minute)
		f.dispatch()
		events := recorder.received()
		if testRun {
			if len(events) != 0 {
				t.Fatalf("test runs produced notifications: %+v", events)
			}
		} else if len(events) != 1 || events[0].Run == nil || events[0].Run.ID != second.ID || events[0].Type != notify.EventRunTimedOut {
			t.Fatalf("retry chain notifications = %+v, want the last real failure %s", events, second.ID)
		}
	}
}

type notificationRetryHookKey struct{}

type notificationRetryHook struct {
	once  sync.Once
	after func()
}

func (h *notificationRetryHook) BeforeQuery(ctx context.Context, _ *bun.QueryEvent) context.Context {
	return ctx
}

func (h *notificationRetryHook) AfterQuery(ctx context.Context, ev *bun.QueryEvent) {
	if ctx.Value(notificationRetryHookKey{}) == h && ev.Err == nil && ev.Operation() == "SELECT" &&
		strings.Contains(ev.Query, `FROM "tasks"`) {
		h.once.Do(h.after)
	}
}

func TestRunNotificationSeesRetryCreatedBeforeMark(t *testing.T) {
	f, recorder := newRetryNotifyFixture(t)
	alice := f.user("alice")
	f.channel(alice, notify.KindWebhook, "https://notify.example.com/hook", allRunEvents(alice.PersonalProjectID))
	m := basicManifest()
	m.Retry = &service.RetryPolicy{MaxAttempts: 1}
	task := f.apply(alice, "digest", m)
	f.clock.Advance(5 * time.Minute)
	f.endedRun(task, store.RunFailed, func(r *store.Run) {
		r.Attempt, r.MaxAttempts = 1, 2
		r.FinishedAt = f.clock.Now().Add(-3 * time.Minute)
	})
	hook := &notificationRetryHook{after: func() {
		triggers := service.NewTriggers(f.db, service.NewAuthorizer(f.db), f.blobs, nil, service.TriggerOptions{Now: f.clock.Now})
		if n, err := triggers.RetryFailed(f.ctx, f.clock.Now()); err != nil || n != 1 {
			t.Fatalf("interleaved retry = %d, %v", n, err)
		}
	}}
	f.db.DB = f.db.WithQueryHook(hook)
	ctx := context.WithValue(f.ctx, notificationRetryHookKey{}, hook)
	if err := f.n.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if events := recorder.received(); len(events) != 0 {
		t.Fatalf("notified before the newly created retry ended: %+v", events)
	}
}

func TestSkippedRetryNotificationAcrossReplicas(t *testing.T) {
	f, recorder := newRetryNotifyFixture(t)
	alice := f.user("alice")
	ch, _ := f.channel(alice, notify.KindWebhook, "https://notify.example.com/hook", allRunEvents(alice.PersonalProjectID))
	task := f.apply(alice, "digest", basicManifest())
	original := f.endedRun(task, store.RunFailed, func(r *store.Run) { r.Attempt, r.MaxAttempts = 1, 2 })
	f.endedRun(task, store.RunSkipped, func(r *store.Run) {
		r.Attempt, r.MaxAttempts, r.RetryOf = 2, 2, original.ID
		r.SkipReason = store.SkipOverlap
	})
	var wg sync.WaitGroup
	for range 3 {
		n := f.recordingNotifications(recorder)
		wg.Go(func() {
			if err := n.Dispatch(f.ctx); err != nil {
				t.Errorf("dispatch: %v", err)
			}
		})
	}
	wg.Wait()
	f.dispatch()
	if events := recorder.received(); len(events) != 1 || events[0].Run == nil || events[0].Run.ID != original.ID {
		t.Fatalf("notifications across replicas = %+v", events)
	}
	if ds := f.deliveries(ch.Channel.ID); len(ds) != 1 || ds[0].Attempts != 1 {
		t.Fatalf("deliveries across replicas = %+v", ds)
	}
}
