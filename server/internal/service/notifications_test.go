package service_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// clock is a settable clock for the notification service.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// receiver is a local notification endpoint. It answers with the statuses
// in script, one per request, then with 200.
type receiver struct {
	*httptest.Server
	mu       sync.Mutex
	requests []received
	script   []int
}

type received struct {
	header http.Header
	body   []byte
}

func newReceiver(t *testing.T, script ...int) *receiver {
	t.Helper()
	r := &receiver{script: script}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.requests = append(r.requests, received{header: req.Header.Clone(), body: body})
		status := http.StatusOK
		if len(r.script) > 0 {
			status, r.script = r.script[0], r.script[1:]
		}
		r.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *receiver) got() []received {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]received(nil), r.requests...)
}

// notifyFixture is the service fixture plus a notification service whose
// guard allows loopback, so it can reach receivers started by the test.
type notifyFixture struct {
	*fixture
	clock *clock
	n     *service.Notifications
}

const testPublicURL = "https://lerryn.example.com"

func newNotifyFixture(t *testing.T) *notifyFixture {
	t.Helper()
	f := newFixture(t)
	c := &clock{t: store.Now()}
	return &notifyFixture{fixture: f, clock: c, n: f.notifications(c, "127.0.0.0/8", "::1/128")}
}

// notifications returns a notification service on f's database, as
// another server replica would have.
func (f *fixture) notifications(c *clock, allowed ...string) *service.Notifications {
	f.t.Helper()
	guard, err := notify.NewGuard(allowed)
	if err != nil {
		f.t.Fatal(err)
	}
	return service.NewNotifications(f.db, service.NewAuthorizer(f.db), service.NotificationsOptions{
		PublicURL: testPublicURL,
		Guard:     guard,
		Sender:    notify.NewSender(guard, 2*time.Second),
		Now:       c.Now,
	})
}

func (f *notifyFixture) channel(p *auth.Principal, kind, url string, subs ...service.SubscriptionInput) (service.ChannelDetail, string) {
	f.t.Helper()
	d, secret, err := f.n.CreateChannel(f.ctx, p, service.CreateChannelInput{Name: "值班", Kind: kind, URL: url, Subscriptions: subs})
	if err != nil {
		f.t.Fatalf("create channel: %v", err)
	}
	return d, secret
}

func (f *notifyFixture) dispatch() {
	f.t.Helper()
	if err := f.n.Dispatch(f.ctx); err != nil {
		f.t.Fatalf("dispatch: %v", err)
	}
}

// endedRun inserts a run of task that ended with status at the fixture's
// clock.
func (f *notifyFixture) endedRun(task service.TaskDetail, status string, mod ...func(*store.Run)) store.Run {
	f.t.Helper()
	now := f.clock.Now()
	r := store.Run{
		ID: store.NewID(), ProjectID: task.Task.ProjectID, TaskID: task.Task.ID,
		VersionID: task.Version.Version.ID, VersionSeq: task.Version.Version.Seq,
		Status: status, TriggerKind: "manual", Params: "{}", QueuedAt: now.Add(-time.Minute),
		StartedAt: now.Add(-time.Minute), FinishedAt: now,
		ErrorCode: "script_error", ErrorMessage: "进程以退出码 1 结束",
	}
	for _, m := range mod {
		m(&r)
	}
	if _, err := f.db.NewInsert().Model(&r).Exec(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *notifyFixture) deliveries(channelID uuid.UUID) []store.NotificationDelivery {
	f.t.Helper()
	var rows []store.NotificationDelivery
	if err := f.db.NewSelect().Model(&rows).Where("channel_id = ?", channelID).Order("id").Scan(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	return rows
}

func allRunEvents(project uuid.UUID) service.SubscriptionInput {
	return service.SubscriptionInput{ProjectID: project, Events: []string{notify.EventRunFailed, notify.EventRunTimedOut, notify.EventRunInterrupted}}
}

func decodeEvent(t *testing.T, body []byte) notify.Event {
	t.Helper()
	var ev notify.Event
	if err := json.Unmarshal(body, &ev); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return ev
}

func TestNotificationChannelLifecycle(t *testing.T) {
	f := newNotifyFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	task := f.apply(alice, "digest", basicManifest())
	url := "https://open.feishu.cn/open-apis/bot/v2/hook/0f1e2d3c-4b5a-6978-a91c"

	d, secret := f.channel(alice, notify.KindFeishu, url,
		service.SubscriptionInput{ProjectID: alice.PersonalProjectID, TaskID: task.Task.ID, Events: []string{notify.EventRunFailed}})
	if secret != "" {
		t.Errorf("feishu channel got signing secret %q", secret)
	}
	c := d.Channel
	if c.URLPreview != "https://open.feishu.cn/…/a91c" || !c.Enabled || c.Kind != notify.KindFeishu {
		t.Fatalf("channel = %+v", c)
	}
	if len(d.Subscriptions) != 1 || d.Subscriptions[0].Task == nil || d.Subscriptions[0].Task.Name != "digest" ||
		d.Subscriptions[0].ProjectName == "" {
		t.Fatalf("subscriptions = %+v", d.Subscriptions)
	}

	hook, secret := f.channel(alice, notify.KindWebhook, "https://hooks.example.com/in")
	if !strings.HasPrefix(secret, notify.SigningSecretPrefix) {
		t.Fatalf("webhook signing secret = %q", secret)
	}

	list, err := f.n.ListChannels(f.ctx, alice)
	if err != nil || len(list) != 2 || list[0].Channel.ID != c.ID || list[1].Channel.ID != hook.Channel.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if other, err := f.n.ListChannels(f.ctx, bob); err != nil || len(other) != 0 {
		t.Fatalf("bob's list = %+v, %v", other, err)
	}

	// Update every field; subscriptions are replaced as a whole.
	newURL := "https://open.feishu.cn/open-apis/bot/v2/hook/ffff-b0b0"
	off := false
	subs := []service.SubscriptionInput{allRunEvents(alice.PersonalProjectID)}
	up, err := f.n.UpdateChannel(f.ctx, alice, c.ID, service.UpdateChannelInput{
		Name: new("  运营群 "), URL: &newURL, Enabled: &off, Subscriptions: &subs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if up.Channel.Name != "运营群" || up.Channel.Enabled || up.Channel.URLPreview != "https://open.feishu.cn/…/b0b0" ||
		len(up.Subscriptions) != 1 || up.Subscriptions[0].Task != nil || len(up.Subscriptions[0].Subscription.Events) != 3 {
		t.Fatalf("updated = %+v", up)
	}

	// Another user's channel does not exist for bob.
	_, err = f.n.UpdateChannel(f.ctx, bob, c.ID, service.UpdateChannelInput{Enabled: &off})
	wantErr(t, err, service.ErrNotFound)
	wantErr(t, f.n.DeleteChannel(f.ctx, bob, c.ID), service.ErrNotFound)
	_, err = f.n.TestChannel(f.ctx, bob, c.ID)
	wantErr(t, err, service.ErrNotFound)
	_, _, err = f.n.ListDeliveries(f.ctx, bob, c.ID, "", nil, "")
	wantErr(t, err, service.ErrNotFound)

	if err := f.n.DeleteChannel(f.ctx, alice, c.ID); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.n.DeleteChannel(f.ctx, alice, c.ID), service.ErrNotFound)

	// Every change is audited, without the target URL.
	var audits []store.AuditEvent
	if err := f.db.NewSelect().Model(&audits).Where("resource_kind = ?", "notification_channel").Order("ts").Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, a := range audits {
		actions = append(actions, a.Action)
		b, _ := json.Marshal(a.Details)
		if strings.Contains(string(b), "hook/") || strings.Contains(string(b), "hooks.example.com") {
			t.Errorf("audit %s details %s contain the target", a.Action, b)
		}
	}
	want := "notification_channel.create notification_channel.create notification_channel.update notification_channel.delete"
	if strings.Join(actions, " ") != want {
		t.Errorf("audit actions = %v, want %s", actions, want)
	}
}

func TestNotificationChannelValidation(t *testing.T) {
	f := newNotifyFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	bobTask := f.apply(bob, "secret-task", basicManifest())
	strict := f.notifications(f.clock) // no internal network allowed

	tests := []struct {
		name string
		n    *service.Notifications
		in   service.CreateChannelInput
		want map[string]string
	}{
		{"blank name", f.n, service.CreateChannelInput{Name: "  ", Kind: "slack", URL: "https://hooks.slack.com/x"}, map[string]string{"#/name": ""}},
		{"unknown kind", f.n, service.CreateChannelInput{Name: "x", Kind: "email", URL: "https://x.example.com"}, map[string]string{"#/kind": ""}},
		{"not http", f.n, service.CreateChannelInput{Name: "x", Kind: "slack", URL: "ftp://x.example.com"}, map[string]string{"#/url": ""}},
		{"loopback", strict, service.CreateChannelInput{Name: "x", Kind: "webhook", URL: "http://127.0.0.1:9000/hook"}, map[string]string{"#/url": ""}},
		{"metadata address", strict, service.CreateChannelInput{Name: "x", Kind: "webhook", URL: "http://169.254.169.254/"}, map[string]string{"#/url": ""}},
		{"bad events", f.n, service.CreateChannelInput{Name: "x", Kind: "slack", URL: "https://hooks.slack.com/x", Subscriptions: []service.SubscriptionInput{
			{ProjectID: alice.PersonalProjectID, Events: []string{"run_failed", "run_failed", "run_started"}},
			{ProjectID: alice.PersonalProjectID},
		}}, map[string]string{"#/subscriptions/0/events/1": "", "#/subscriptions/0/events/2": "", "#/subscriptions/1/events": ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := tt.n.CreateChannel(f.ctx, alice, tt.in)
			got, _ := fieldErrors(t, err)
			for k := range tt.want {
				if _, ok := got[k]; !ok {
					t.Errorf("errors %v lack %s", got, k)
				}
			}
			if len(got) != len(tt.want) {
				t.Errorf("errors = %v, want keys of %v", got, tt.want)
			}
		})
	}

	// Subscriptions only point at what alice can see.
	for name, sub := range map[string]service.SubscriptionInput{
		"bob's project":            {ProjectID: bob.PersonalProjectID, Events: []string{"run_failed"}},
		"bob's task under alice's": {ProjectID: alice.PersonalProjectID, TaskID: bobTask.Task.ID, Events: []string{"run_failed"}},
		"unknown project":          {ProjectID: uuid.New(), Events: []string{"run_failed"}},
	} {
		_, _, err := f.n.CreateChannel(f.ctx, alice, service.CreateChannelInput{
			Name: "x", Kind: "slack", URL: "https://hooks.slack.com/x", Subscriptions: []service.SubscriptionInput{sub},
		})
		if err == nil || !strings.Contains(err.Error(), service.ErrNotFound.Error()) {
			t.Errorf("%s: err = %v, want not found", name, err)
		}
	}
}

// A15/A16: a failing run reaches a generic webhook as a signed
// NotificationEvent with the status, error class, a short reason and a
// console link, and never the credential the error message mentioned.
func TestRunFailureIsDeliveredOnce(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	ch, secret := f.channel(alice, notify.KindWebhook, rcv.URL+"/hook", allRunEvents(alice.PersonalProjectID))
	task := f.apply(alice, "digest", basicManifest())

	// Through the runner protocol, as a real failure arrives.
	run := f.start(alice, task.Task.ID)
	rn := f.runner("r1")
	f.claim(rn, 1)
	exit := 3
	if err := f.coord.Finish(f.ctx, rn, run.ID, runproto.Finish{
		Status: runproto.StatusFailed, ExitCode: &exit,
		ErrorMessage: "token wl_pat_0123456789abcdefABCDEF rejected\nby upstream",
		FinishedAt:   f.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	f.dispatch()
	f.dispatch()

	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("receiver got %d requests, want 1", len(got))
	}
	req := got[0]
	if err := notify.Verify(secret, req.header.Get(notify.HeaderSignature), req.body, time.Now()); err != nil {
		t.Fatalf("signature: %v", err)
	}
	ev := decodeEvent(t, req.body)
	if req.header.Get(notify.HeaderDelivery) != ev.ID.String() || req.header.Get(notify.HeaderEvent) != notify.EventRunFailed {
		t.Errorf("headers %v do not match the event %s", req.header, ev.ID)
	}
	if ev.Type != notify.EventRunFailed || ev.Task == nil || ev.Task.Name != "digest" || ev.Run == nil || ev.MergedCount != 1 {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Run.Status != "failed" || ev.Run.ErrorCode == nil || *ev.Run.ErrorCode != "script_error" || ev.Run.Attempt != 1 || ev.Run.Version != 1 {
		t.Errorf("run = %+v", ev.Run)
	}
	if ev.Run.Reason == nil || *ev.Run.Reason != "token [已隐藏] rejected by upstream" {
		t.Errorf("reason = %v", ev.Run.Reason)
	}
	if want := testPublicURL + "/runs/" + run.ID.String(); ev.Link != want || ev.Run.Link != want {
		t.Errorf("links %q, %q, want %q", ev.Link, ev.Run.Link, want)
	}
	if strings.Contains(string(req.body), "wl_pat_") {
		t.Errorf("payload leaks the token: %s", req.body)
	}

	ds := f.deliveries(ch.Channel.ID)
	if len(ds) != 1 || ds[0].Status != store.DeliveryDelivered || ds[0].Attempts != 1 || ds[0].HTTPStatus != 200 ||
		ds[0].RunID != run.ID || ds[0].TaskID != task.Task.ID || ds[0].ID != ev.ID {
		t.Fatalf("deliveries = %+v", ds)
	}
	page, _, err := f.n.ListDeliveries(f.ctx, alice, ch.Channel.ID, store.DeliveryDelivered, nil, "")
	if err != nil || len(page) != 1 || page[0].Task == nil || page[0].Task.Name != "digest" {
		t.Fatalf("list deliveries = %+v, %v", page, err)
	}
}

// Several replicas scanning at once fan an event out once and send each
// delivery once.
func TestDispatchIsOncePerEventAcrossReplicas(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	ch, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	tasks := []service.TaskDetail{f.apply(alice, "a", basicManifest()), f.apply(alice, "b", basicManifest()), f.apply(alice, "c", basicManifest())}
	for _, task := range tasks {
		f.endedRun(task, store.RunFailed)
	}

	replicas := []*service.Notifications{f.n, f.notifications(f.clock, "127.0.0.0/8"), f.notifications(f.clock, "127.0.0.0/8")}
	var wg sync.WaitGroup
	for _, r := range replicas {
		for range 3 {
			wg.Go(func() {
				if err := r.Dispatch(f.ctx); err != nil {
					t.Errorf("dispatch: %v", err)
				}
			})
		}
	}
	wg.Wait()

	if got := len(rcv.got()); got != 3 {
		t.Fatalf("receiver got %d requests, want 3", got)
	}
	seen := map[string]bool{}
	for _, r := range rcv.got() {
		id := r.header.Get(notify.HeaderDelivery)
		if seen[id] {
			t.Errorf("delivery %s sent twice", id)
		}
		seen[id] = true
	}
	markers, err := f.db.NewSelect().Model((*store.NotificationEventRecord)(nil)).Count(f.ctx)
	if err != nil || markers != 3 {
		t.Fatalf("event markers = %d, %v", markers, err)
	}
	if ds := f.deliveries(ch.Channel.ID); len(ds) != 3 {
		t.Fatalf("deliveries = %d", len(ds))
	}
}

// A receiver that keeps failing is retried after 1, 5 and 30 minutes,
// then the delivery fails for good. The delivery ID stays the same.
func TestDeliveryRetriesWithBackoff(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t, 500, 500, 500, 500, 500)
	ch, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunTimedOut, func(r *store.Run) { r.ErrorCode = "timeout" })

	steps := []struct {
		advance  time.Duration
		requests int
		attempts int
		status   string
	}{
		{0, 1, 1, store.DeliveryPending},
		{59 * time.Second, 1, 1, store.DeliveryPending},
		{time.Second, 2, 2, store.DeliveryPending},
		{4 * time.Minute, 2, 2, store.DeliveryPending},
		{time.Minute, 3, 3, store.DeliveryPending},
		{29 * time.Minute, 3, 3, store.DeliveryPending},
		{time.Minute, 4, 4, store.DeliveryFailed},
		{24 * time.Hour, 4, 4, store.DeliveryFailed},
	}
	for i, s := range steps {
		f.clock.Advance(s.advance)
		f.dispatch()
		ds := f.deliveries(ch.Channel.ID)
		if len(rcv.got()) != s.requests || len(ds) != 1 || ds[0].Attempts != s.attempts || ds[0].Status != s.status {
			t.Fatalf("step %d: %d requests, deliveries %+v; want %d requests, %d attempts, %s",
				i, len(rcv.got()), ds, s.requests, s.attempts, s.status)
		}
		if ds[0].HTTPStatus != 500 || ds[0].Error != "对方返回 HTTP 500" {
			t.Fatalf("step %d: http_status %d, error %q", i, ds[0].HTTPStatus, ds[0].Error)
		}
	}
	ids := map[string]bool{}
	for _, r := range rcv.got() {
		ids[r.header.Get(notify.HeaderDelivery)] = true
		if ev := decodeEvent(t, r.body); ev.Type != notify.EventRunTimedOut {
			t.Errorf("event type %s", ev.Type)
		}
	}
	if len(ids) != 1 {
		t.Errorf("retries used %d delivery IDs, want 1", len(ids))
	}
}

func TestDeliveryRecoversOnRetry(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t, 503)
	ch, _ := f.channel(alice, notify.KindSlack, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunInterrupted, func(r *store.Run) { r.ErrorCode = "run_interrupted" })
	f.dispatch()
	f.clock.Advance(time.Minute)
	f.dispatch()
	ds := f.deliveries(ch.Channel.ID)
	if len(ds) != 1 || ds[0].Status != store.DeliveryDelivered || ds[0].Attempts != 2 || ds[0].HTTPStatus != 200 || ds[0].Error != "" || ds[0].DeliveredAt.IsZero() {
		t.Fatalf("deliveries = %+v", ds)
	}
	got := rcv.got()
	if len(got) != 2 || !strings.Contains(string(got[1].body), "任务 digest 运行中断") {
		t.Fatalf("requests = %d, last body %s", len(got), got[len(got)-1].body)
	}
}

// Consecutive failures of a task within the merge window become one
// notification carrying their count and the latest run.
func TestConsecutiveFailuresMerge(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	ch, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	task := f.apply(alice, "digest", basicManifest())
	other := f.apply(alice, "other", basicManifest())

	f.endedRun(task, store.RunFailed)
	f.dispatch()
	f.clock.Advance(time.Minute)
	f.endedRun(task, store.RunFailed)
	f.dispatch()
	f.clock.Advance(time.Minute)
	last := f.endedRun(task, store.RunTimedOut, func(r *store.Run) { r.ErrorCode = "timeout" })
	f.endedRun(other, store.RunFailed) // another task is not held back
	f.dispatch()
	if got := len(rcv.got()); got != 2 {
		t.Fatalf("requests within the window = %d, want 2 (first failure of each task)", got)
	}

	f.clock.Advance(8 * time.Minute) // 10 minutes after the first notification
	f.dispatch()
	got := rcv.got()
	if len(got) != 3 {
		t.Fatalf("requests = %d, want 3", len(got))
	}
	ev := decodeEvent(t, got[2].body)
	if ev.MergedCount != 2 || ev.Run == nil || ev.Run.ID != last.ID || ev.Type != notify.EventRunTimedOut {
		t.Fatalf("merged event = %+v", ev)
	}
	ds := f.deliveries(ch.Channel.ID)
	// The merged delivery was created with the second failure, before the
	// other task's.
	if len(ds) != 3 || ds[1].MergedCount != 2 || ds[1].RunID != last.ID || ds[1].Status != store.DeliveryDelivered {
		t.Fatalf("deliveries = %+v", ds)
	}
}

func TestRunsThatDoNotNotify(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	task := f.apply(alice, "digest", basicManifest())

	f.endedRun(task, store.RunFailed, func(r *store.Run) { r.IsTest = true })
	f.endedRun(task, store.RunCompleted, func(r *store.Run) { r.ErrorCode, r.ErrorMessage = "", "" })
	f.endedRun(task, store.RunCancelled, func(r *store.Run) { r.ErrorCode, r.ErrorMessage = "", "" })
	f.endedRun(task, store.RunFailed, func(r *store.Run) { r.FinishedAt = f.clock.Now().Add(-7 * time.Hour) })
	// An attempt that was retried leaves the outcome to its retry.
	first := f.endedRun(task, store.RunFailed, func(r *store.Run) { r.Attempt, r.MaxAttempts = 1, 2 })
	f.endedRun(task, store.RunQueued, func(r *store.Run) {
		r.Attempt, r.MaxAttempts, r.RetryOf, r.FinishedAt = 2, 2, first.ID, time.Time{}
	})
	f.dispatch()
	f.clock.Advance(time.Hour)
	f.dispatch()
	if got := len(rcv.got()); got != 0 {
		t.Fatalf("receiver got %d requests, want 0", got)
	}
}

// A failed attempt that may still be retried waits briefly for its retry;
// when none appears it is notified as the last attempt.
func TestRetryableAttemptWithoutRetryIsNotified(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunFailed, func(r *store.Run) {
		r.Attempt, r.MaxAttempts, r.ErrorCode = 1, 3, "credential_unavailable"
	})
	f.dispatch()
	if got := len(rcv.got()); got != 0 {
		t.Fatalf("requests before the grace period = %d", got)
	}
	f.clock.Advance(2 * time.Minute)
	f.dispatch()
	if got := len(rcv.got()); got != 1 {
		t.Fatalf("requests after the grace period = %d, want 1", got)
	}
}

func TestSubscriptionScope(t *testing.T) {
	f := newNotifyFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	digest := f.apply(alice, "digest", basicManifest())
	other := f.apply(alice, "other", basicManifest())
	rcvTask, rcvTimeouts, rcvOff, rcvBob := newReceiver(t), newReceiver(t), newReceiver(t), newReceiver(t)

	f.channel(alice, notify.KindWebhook, rcvTask.URL,
		service.SubscriptionInput{ProjectID: alice.PersonalProjectID, TaskID: digest.Task.ID, Events: []string{notify.EventRunFailed}})
	f.channel(alice, notify.KindWebhook, rcvTimeouts.URL,
		service.SubscriptionInput{ProjectID: alice.PersonalProjectID, Events: []string{notify.EventRunTimedOut}})
	off, _ := f.channel(alice, notify.KindWebhook, rcvOff.URL, allRunEvents(alice.PersonalProjectID))
	disabled := false
	if _, err := f.n.UpdateChannel(f.ctx, alice, off.Channel.ID, service.UpdateChannelInput{Enabled: &disabled}); err != nil {
		t.Fatal(err)
	}
	// Bob may view alice's project, then loses access.
	f.grant(alice.PersonalProjectID, bob, service.RoleViewer)
	f.channel(bob, notify.KindWebhook, rcvBob.URL, allRunEvents(alice.PersonalProjectID))
	if _, err := f.db.NewDelete().Model((*store.RoleBinding)(nil)).
		Where("project_id = ? AND user_id = ?", alice.PersonalProjectID, bob.UserID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}

	f.endedRun(other, store.RunFailed)
	f.endedRun(digest, store.RunFailed)
	f.endedRun(other, store.RunTimedOut)
	f.dispatch()

	for name, tt := range map[string]struct {
		r    *receiver
		want []string
	}{
		"task subscription":     {rcvTask, []string{"digest"}},
		"timeouts of project":   {rcvTimeouts, []string{"other"}},
		"disabled channel":      {rcvOff, nil},
		"owner lost the access": {rcvBob, nil},
	} {
		var tasks []string
		for _, r := range tt.r.got() {
			tasks = append(tasks, decodeEvent(t, r.body).Task.Name)
		}
		if strings.Join(tasks, ",") != strings.Join(tt.want, ",") {
			t.Errorf("%s: notified tasks %v, want %v", name, tasks, tt.want)
		}
	}
}

func TestCredentialExpiringIsNotifiedOncePerExpiry(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	f.channel(alice, notify.KindWebhook, rcv.URL,
		service.SubscriptionInput{ProjectID: alice.PersonalProjectID, Events: []string{notify.EventCredentialExpiring}})
	task := f.apply(alice, "digest", basicManifest())
	now := f.clock.Now()
	cred := store.Credential{
		ID: store.NewID(), OwnerUserID: alice.UserID, Name: "LLM_API_KEY", Purpose: "调用模型",
		Fields: []store.CredentialField{{Name: "LLM_API_KEY", Secret: true}}, Status: store.CredentialActive,
		CurrentVersion: 1, ExpiresAt: now.Add(3 * 24 * time.Hour), CreatedAt: now, UpdatedAt: now,
	}
	far := cred
	far.ID, far.Name, far.ExpiresAt = store.NewID(), "LATER_KEY", now.Add(30*24*time.Hour)
	for _, c := range []*store.Credential{&cred, &far} {
		if _, err := f.db.NewInsert().Model(c).Exec(f.ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.NewInsert().Model(&store.CredentialGrant{
			ID: store.NewID(), CredentialID: c.ID, TaskID: task.Task.ID, Name: c.Name, GrantedBy: alice.UserID, CreatedAt: now,
		}).Exec(f.ctx); err != nil {
			t.Fatal(err)
		}
	}

	f.dispatch()
	f.dispatch()
	got := rcv.got()
	if len(got) != 1 {
		t.Fatalf("requests = %d, want 1", len(got))
	}
	ev := decodeEvent(t, got[0].body)
	if ev.Type != notify.EventCredentialExpiring || ev.Credential == nil || ev.Credential.Name != "LLM_API_KEY" ||
		!ev.Credential.ExpiresAt.Equal(cred.ExpiresAt) || ev.Task == nil || ev.Task.ID != task.Task.ID || ev.Run != nil ||
		ev.Link != testPublicURL+"/settings/credentials" {
		t.Fatalf("event = %+v", ev)
	}

	// Moving the expiry announces it again.
	if _, err := f.db.NewUpdate().Model((*store.Credential)(nil)).Set("expires_at = ?", now.Add(5*24*time.Hour)).
		Where("id = ?", cred.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	f.dispatch()
	if got := len(rcv.got()); got != 2 {
		t.Fatalf("requests after the expiry moved = %d, want 2", got)
	}
}

func TestTestNotification(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	ok, failing := newReceiver(t), newReceiver(t, 404)
	good, secret := f.channel(alice, notify.KindWebhook, ok.URL+"/hook")
	bad, _ := f.channel(alice, notify.KindFeishu, failing.URL)

	d, err := f.n.TestChannel(f.ctx, alice, good.Channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Delivery.Status != store.DeliveryDelivered || d.Delivery.EventType != notify.EventTest || d.Delivery.Attempts != 1 || d.Delivery.HTTPStatus != 200 {
		t.Fatalf("delivery = %+v", d.Delivery)
	}
	got := ok.got()
	if len(got) != 1 {
		t.Fatalf("requests = %d", len(got))
	}
	if err := notify.Verify(secret, got[0].header.Get(notify.HeaderSignature), got[0].body, time.Now()); err != nil {
		t.Fatal(err)
	}
	ev := decodeEvent(t, got[0].body)
	if ev.Type != notify.EventTest || ev.Project.ID != alice.PersonalProjectID || ev.Link != testPublicURL+"/" || ev.ID != d.Delivery.ID {
		t.Fatalf("test event = %+v", ev)
	}

	d, err = f.n.TestChannel(f.ctx, alice, bad.Channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Delivery.Status != store.DeliveryFailed || d.Delivery.HTTPStatus != 404 || d.Delivery.Error != "对方返回 HTTP 404" {
		t.Fatalf("failed test delivery = %+v", d.Delivery)
	}
	list, err := f.n.ListChannels(f.ctx, alice)
	if err != nil || list[1].LastDelivery == nil || list[1].LastDelivery.Delivery.ID != d.Delivery.ID {
		t.Fatalf("last delivery = %+v, %v", list[1].LastDelivery, err)
	}
	// Test deliveries are not retried.
	f.clock.Advance(time.Hour)
	f.dispatch()
	if len(failing.got()) != 1 {
		t.Fatalf("failing receiver got %d requests, want 1", len(failing.got()))
	}

	// A replica that does not allow loopback refuses to connect, even to a
	// channel saved where it was allowed.
	strict := f.notifications(f.clock)
	d, err = strict.TestChannel(f.ctx, alice, good.Channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Delivery.Status != store.DeliveryFailed || d.Delivery.HTTPStatus != 0 || d.Delivery.Error != "目标地址指向内网或本机，部署没有允许这类地址" {
		t.Fatalf("blocked delivery = %+v", d.Delivery)
	}
	if len(ok.got()) != 1 {
		t.Fatalf("receiver got %d requests, want still 1", len(ok.got()))
	}
}

func TestListDeliveriesPages(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t, 500)
	ch, _ := f.channel(alice, notify.KindWebhook, rcv.URL)
	for range 3 {
		if _, err := f.n.TestChannel(f.ctx, alice, ch.Channel.ID); err != nil {
			t.Fatal(err)
		}
	}
	page, next, err := f.n.ListDeliveries(f.ctx, alice, ch.Channel.ID, "", new(2), "")
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("first page = %d items, next %q, %v", len(page), next, err)
	}
	rest, next2, err := f.n.ListDeliveries(f.ctx, alice, ch.Channel.ID, "", new(2), next)
	if err != nil || len(rest) != 1 || next2 != "" || rest[0].Delivery.ID == page[0].Delivery.ID {
		t.Fatalf("second page = %+v, next %q, %v", rest, next2, err)
	}
	failed, _, err := f.n.ListDeliveries(f.ctx, alice, ch.Channel.ID, store.DeliveryFailed, nil, "")
	if err != nil || len(failed) != 1 {
		t.Fatalf("failed deliveries = %d, %v", len(failed), err)
	}
	_, _, err = f.n.ListDeliveries(f.ctx, alice, ch.Channel.ID, "", nil, "%%%")
	if got, _ := fieldErrors(t, err); len(got) != 1 {
		t.Fatalf("bad cursor errors = %v", got)
	}
}

func TestDeletedChannelDropsPendingDeliveries(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t, 500)
	ch, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunFailed)
	f.dispatch()
	if err := f.n.DeleteChannel(f.ctx, alice, ch.Channel.ID); err != nil {
		t.Fatal(err)
	}
	f.clock.Advance(time.Hour)
	f.dispatch()
	if len(rcv.got()) != 1 {
		t.Fatalf("requests = %d, want 1", len(rcv.got()))
	}
	if n, err := f.db.NewSelect().Model((*store.NotificationDelivery)(nil)).Count(f.ctx); err != nil || n != 0 {
		t.Fatalf("deliveries left = %d, %v", n, err)
	}
}

func TestCleanupRemovesOldRecords(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t)
	ch, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunFailed)
	f.dispatch()
	f.clock.Advance(31 * 24 * time.Hour)
	if err := f.n.Cleanup(f.ctx); err != nil {
		t.Fatal(err)
	}
	if ds := f.deliveries(ch.Channel.ID); len(ds) != 0 {
		t.Fatalf("deliveries = %+v", ds)
	}
	if n, _ := f.db.NewSelect().Model((*store.NotificationEventRecord)(nil)).Count(f.ctx); n != 0 {
		t.Fatalf("event markers = %d", n)
	}
}

// Runs.OnRunEnded fires for runs that end in this process, so the
// dispatcher does not wait for its next poll.
func TestRunEndedHookWakesListeners(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	ended := make(chan uuid.UUID, 4)
	f.runs.OnRunEnded(func(id uuid.UUID) { ended <- id })
	run := f.start(alice, f.apply(alice, "digest", basicManifest()).Task.ID)
	rn := f.runner("r1")
	f.claim(rn, 1)
	if err := f.coord.Finish(context.Background(), rn, run.ID, runproto.Finish{Status: runproto.StatusInterrupted}); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-ended:
		if id != run.ID {
			t.Fatalf("hook got %s, want %s", id, run.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("hook not called")
	}
}
