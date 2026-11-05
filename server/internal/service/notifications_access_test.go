package service_test

import (
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestQueuedNotificationsRecheckAccessBeforeSending(t *testing.T) {
	for _, queue := range []string{"retry", "merge window"} {
		for _, change := range []string{"removed member", "disabled owner", "unsubscribed"} {
			t.Run(queue+"/"+change, func(t *testing.T) {
				f := newNotifyFixture(t)
				alice, bob := f.user("alice"), f.user("bob")
				projects := service.NewProjects(f.db, service.NewAuthorizer(f.db), nil)
				project, err := projects.Create(f.ctx, alice, "shared")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := projects.SetMember(f.ctx, alice, project.Project.ID, bob.UserID, service.RoleViewer); err != nil {
					t.Fatal(err)
				}
				applied, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{
					ProjectID: project.Project.ID, Name: "digest", Manifest: basicManifest(), Files: mainPy(),
				})
				if err != nil {
					t.Fatal(err)
				}
				subs := []service.SubscriptionInput{allRunEvents(project.Project.ID)}
				var responseScript []int
				if queue == "retry" {
					responseScript = []int{500}
				}
				rcv := newReceiver(t, responseScript...)
				channel, _ := f.channel(bob, notify.KindWebhook, rcv.URL, subs...)
				f.endedRun(*applied.Task, store.RunFailed)
				f.dispatch()
				if queue == "merge window" {
					f.clock.Advance(time.Minute)
					f.endedRun(*applied.Task, store.RunFailed)
					f.dispatch()
				}
				if got := len(rcv.got()); got != 1 {
					t.Fatalf("receiver got %d requests before the access change, want one", got)
				}
				var pending store.NotificationDelivery
				for _, d := range f.deliveries(channel.Channel.ID) {
					if d.Status == store.DeliveryPending {
						pending = d
					}
				}
				if pending.NextAttemptAt.IsZero() {
					t.Fatal("no queued delivery to exercise the send-time access check")
				}
				admin := service.NewAdmin(f.accounts, nil)
				switch change {
				case "removed member":
					err = projects.RemoveMember(f.ctx, alice, project.Project.ID, bob.UserID)
				case "disabled owner":
					disabled := true
					_, err = admin.UpdateUser(f.ctx, alice, bob.UserID, service.AdminUpdateUserInput{Disabled: &disabled})
				case "unsubscribed":
					empty := []service.SubscriptionInput{}
					_, err = f.n.UpdateChannel(f.ctx, bob, channel.Channel.ID, service.UpdateChannelInput{Subscriptions: &empty})
				}
				if err != nil {
					t.Fatal(err)
				}
				f.clock.Advance(11 * time.Minute)
				f.dispatch()
				if got := len(rcv.got()); got != 1 {
					t.Fatalf("receiver got %d requests after access was removed, want the original one only", got)
				}
				for _, d := range f.deliveries(channel.Channel.ID) {
					if d.ID == pending.ID && (d.Status != store.DeliveryFailed || !d.NextAttemptAt.IsZero() ||
						d.HTTPStatus != 0 || d.Error == "" || !d.DeliveredAt.IsZero()) {
						t.Fatalf("blocked delivery was not terminated without sending: %+v", d)
					}
				}
				// Restoring access permits new notifications without resurrecting
				// a delivery that was already rejected after access was lost.
				switch change {
				case "removed member":
					_, err = projects.SetMember(f.ctx, alice, project.Project.ID, bob.UserID, service.RoleViewer)
				case "disabled owner":
					disabled := false
					_, err = admin.UpdateUser(f.ctx, alice, bob.UserID, service.AdminUpdateUserInput{Disabled: &disabled})
				case "unsubscribed":
					_, err = f.n.UpdateChannel(f.ctx, bob, channel.Channel.ID, service.UpdateChannelInput{Subscriptions: &subs})
				}
				if err != nil {
					t.Fatal(err)
				}
				f.clock.Advance(time.Hour)
				f.dispatch()
				if got := len(rcv.got()); got != 1 {
					t.Fatalf("old delivery resumed after access was restored: %d requests", got)
				}
				f.endedRun(*applied.Task, store.RunFailed)
				f.dispatch()
				if got := len(rcv.got()); got != 2 {
					t.Fatalf("new notification after access was restored: %d requests, want two", got)
				}
			})
		}
	}
}

func TestNotificationAccessQueryFailureCanRecover(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	rcv := newReceiver(t, 500)
	channel, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunFailed)
	f.dispatch()
	f.clock.Advance(time.Minute)
	// Make only the access lookup unavailable after fan-out has committed.
	// A database failure must not be mistaken for a revoked permission.
	if _, err := f.db.NewRaw("ALTER TABLE role_bindings RENAME TO unavailable_role_bindings").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	err := f.n.Dispatch(f.ctx)
	if _, restoreErr := f.db.NewRaw("ALTER TABLE unavailable_role_bindings RENAME TO role_bindings").Exec(f.ctx); restoreErr != nil {
		t.Fatal(restoreErr)
	}
	if err == nil || len(rcv.got()) != 1 {
		t.Fatalf("access query failure: %v, requests %d; want error without sending", err, len(rcv.got()))
	}
	deliveries := f.deliveries(channel.Channel.ID)
	if len(deliveries) != 1 || deliveries[0].Status != store.DeliveryPending || deliveries[0].NextAttemptAt.IsZero() {
		t.Fatalf("database failure terminated the delivery: %+v", deliveries)
	}
	f.clock.Advance(2 * time.Minute)
	f.dispatch()
	deliveries = f.deliveries(channel.Channel.ID)
	if len(rcv.got()) != 2 || len(deliveries) != 1 || deliveries[0].Status != store.DeliveryDelivered {
		t.Fatalf("recovered delivery: %d requests, %+v", len(rcv.got()), deliveries)
	}
}

func TestQueuedNotificationUsesCurrentSubscription(t *testing.T) {
	for _, change := range []string{"same subscription", "different event", "different task"} {
		t.Run(change, func(t *testing.T) {
			f := newNotifyFixture(t)
			alice := f.user("alice")
			rcv := newReceiver(t, 500)
			subs := []service.SubscriptionInput{allRunEvents(alice.PersonalProjectID)}
			channel, _ := f.channel(alice, notify.KindWebhook, rcv.URL, subs...)
			f.endedRun(f.apply(alice, "digest", basicManifest()), store.RunFailed)
			f.dispatch()
			f.clock.Advance(30 * time.Second)
			switch change {
			case "different event":
				subs[0].Events = []string{notify.EventRunTimedOut}
			case "different task":
				subs[0].TaskID = f.apply(alice, "other", basicManifest()).Task.ID
			}
			if _, err := f.n.UpdateChannel(f.ctx, alice, channel.Channel.ID, service.UpdateChannelInput{Subscriptions: &subs}); err != nil {
				t.Fatal(err)
			}
			f.clock.Advance(30 * time.Second)
			f.dispatch()
			deliveries := f.deliveries(channel.Channel.ID)
			wantRequests, wantStatus := 1, store.DeliveryFailed
			if change == "same subscription" {
				wantRequests, wantStatus = 2, store.DeliveryDelivered
			}
			if len(rcv.got()) != wantRequests || len(deliveries) != 1 || deliveries[0].Status != wantStatus {
				t.Fatalf("current subscription: %d requests, deliveries %+v; want %d, %s", len(rcv.got()), deliveries, wantRequests, wantStatus)
			}
		})
	}
}
