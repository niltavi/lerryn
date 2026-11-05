package service_test

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/notify"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestNotificationChannelMetadataRespectsCurrentAccess(t *testing.T) {
	for _, change := range []string{"removed member", "unknown role", "disabled owner"} {
		t.Run(change, func(t *testing.T) {
			f := newNotifyFixture(t)
			alice, bob := f.user("alice"), f.user("bob")
			projects := service.NewProjects(f.db, service.NewAuthorizer(f.db), nil)
			shared, err := projects.Create(f.ctx, alice, "shared-original")
			if err != nil {
				t.Fatal(err)
			}
			projectID := shared.Project.ID
			if _, err := projects.SetMember(f.ctx, alice, projectID, bob.UserID, service.RoleViewer); err != nil {
				t.Fatal(err)
			}
			applied, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{
				ProjectID: projectID, Name: "shared-task", Manifest: basicManifest(), Files: mainPy(),
			})
			if err != nil {
				t.Fatal(err)
			}
			personal := f.apply(bob, "personal-task", basicManifest())
			subs := []service.SubscriptionInput{
				{ProjectID: projectID, TaskID: applied.Task.Task.ID, Events: []string{notify.EventRunFailed}},
				{ProjectID: bob.PersonalProjectID, TaskID: personal.Task.ID, Events: []string{notify.EventRunFailed}},
			}
			rcv := newReceiver(t)
			channel, _ := f.channel(bob, notify.KindWebhook, rcv.URL, subs...)
			run := f.endedRun(*applied.Task, store.RunFailed)
			f.dispatch()
			if len(rcv.got()) != 1 {
				t.Fatal("shared-project delivery was not sent before access changed")
			}
			list, err := f.n.ListChannels(f.ctx, bob)
			if err != nil || len(list) != 1 || len(list[0].Subscriptions) != 2 || list[0].LastDelivery == nil || list[0].LastDelivery.Task == nil {
				t.Fatalf("accessible channel details are incomplete: %d channels, %v", len(list), err)
			}
			originalDelivery := list[0].LastDelivery.Delivery
			admin := service.NewAdmin(f.accounts, nil)
			switch change {
			case "removed member":
				err = projects.RemoveMember(f.ctx, alice, projectID, bob.UserID)
			case "unknown role":
				_, err = f.db.NewUpdate().Model((*store.RoleBinding)(nil)).Set("role = ?", "unknown-future-role").
					Where("project_id = ? AND user_id = ?", projectID, bob.UserID).Exec(f.ctx)
			case "disabled owner":
				_, err = admin.UpdateUser(f.ctx, alice, bob.UserID, service.AdminUpdateUserInput{Disabled: new(true)})
			}
			if err != nil {
				t.Fatal(err)
			}
			const privateName = "renamed-after-access-was-revoked"
			if _, err := projects.Rename(f.ctx, alice, projectID, privateName); err != nil {
				t.Fatal(err)
			}
			// Apply addresses tasks by their immutable name, so simulate a
			// maintenance rename without changing the subscribed task ID.
			const privateTaskName = "renamed-private-task"
			if _, err := f.db.NewUpdate().Model((*store.Task)(nil)).Set("name = ?", privateTaskName).
				Where("id = ?", applied.Task.Task.ID).Exec(f.ctx); err != nil {
				t.Fatal(err)
			}
			wantSubscriptions := 1
			if change == "disabled owner" {
				wantSubscriptions = 0
			} else if _, err := projects.Get(f.ctx, bob, projectID); !errors.Is(err, service.ErrNotFound) {
				t.Fatalf("project access control = %v, want not_found", err)
			}
			check := func(d service.ChannelDetail) {
				t.Helper()
				if d.Channel.ID != channel.Channel.ID || len(d.Subscriptions) != wantSubscriptions {
					t.Fatalf("channel %s has %d visible subscriptions, want %d", d.Channel.ID, len(d.Subscriptions), wantSubscriptions)
				}
				for _, sub := range d.Subscriptions {
					if sub.Subscription.ProjectID != bob.PersonalProjectID || sub.Task == nil || sub.Task.ID != personal.Task.ID {
						t.Fatalf("unexpected visible subscription = %+v", sub)
					}
				}
				if d.LastDelivery == nil || d.LastDelivery.Task != nil || d.LastDelivery.Delivery.ID != originalDelivery.ID || d.LastDelivery.Delivery.RunID != run.ID {
					t.Fatalf("historical delivery lost its status or revealed the current task: %+v", d.LastDelivery)
				}
			}
			list, err = f.n.ListChannels(f.ctx, bob)
			if err != nil || len(list) != 1 {
				t.Fatalf("list after access changed = %d channels, %v", len(list), err)
			}
			check(list[0])
			updated, err := f.n.UpdateChannel(f.ctx, bob, channel.Channel.ID, service.UpdateChannelInput{Name: new("my renamed channel")})
			if err != nil || updated.Channel.Name != "my renamed channel" {
				t.Fatalf("update own channel name = %q, %v", updated.Channel.Name, err)
			}
			check(updated)
			deliveries, _, err := f.n.ListDeliveries(f.ctx, bob, channel.Channel.ID, "", nil, "")
			if err != nil || len(deliveries) != 1 || deliveries[0].Task != nil || deliveries[0].Delivery.ID != originalDelivery.ID {
				t.Fatalf("delivery list after access changed = %+v, %v", deliveries, err)
			}
			var stored []store.NotificationSubscription
			if err := f.db.NewSelect().Model(&stored).Where("channel_id = ?", channel.Channel.ID).Scan(f.ctx); err != nil || len(stored) != 2 {
				t.Fatalf("read projection changed stored subscriptions: %+v, %v", stored, err)
			}
			_, err = f.n.TestChannel(f.ctx, bob, channel.Channel.ID)
			if change == "disabled owner" {
				if !errors.Is(err, service.ErrNotFound) || len(rcv.got()) != 1 {
					t.Fatalf("disabled owner test = %v, requests = %d", err, len(rcv.got()))
				}
			} else {
				if err != nil || len(rcv.got()) != 2 {
					t.Fatalf("test with mixed access = %v, requests = %d", err, len(rcv.got()))
				}
				if ev := decodeEvent(t, rcv.got()[1].body); ev.Project.ID != bob.PersonalProjectID {
					t.Fatalf("test disclosed an inaccessible project: %+v", ev.Project)
				}
			}
			switch change {
			case "removed member", "unknown role":
				_, err = projects.SetMember(f.ctx, alice, projectID, bob.UserID, service.RoleViewer)
			case "disabled owner":
				_, err = admin.UpdateUser(f.ctx, alice, bob.UserID, service.AdminUpdateUserInput{Disabled: new(false)})
			}
			if err != nil {
				t.Fatal(err)
			}
			list, err = f.n.ListChannels(f.ctx, bob)
			if err != nil || len(list) != 1 || len(list[0].Subscriptions) != 2 || list[0].Subscriptions[0].ProjectName != privateName ||
				list[0].Subscriptions[0].Task == nil || list[0].Subscriptions[0].Task.Name != privateTaskName {
				t.Fatalf("restored subscriptions do not expose the current names: %d channels, %v", len(list), err)
			}
			deliveries, _, err = f.n.ListDeliveries(f.ctx, bob, channel.Channel.ID, "", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, d := range deliveries {
				if d.Delivery.ID == originalDelivery.ID {
					found = d.Task != nil && d.Task.ID == applied.Task.Task.ID && d.Task.Name == privateTaskName
				}
			}
			if !found {
				t.Fatal("restored member cannot see the historical delivery's current task")
			}
			before := len(rcv.got())
			if _, err := f.n.TestChannel(f.ctx, bob, channel.Channel.ID); err != nil {
				t.Fatal(err)
			}
			if ev := decodeEvent(t, rcv.got()[before].body); ev.Project.ID != projectID || ev.Project.Name != privateName {
				t.Fatalf("restored test did not use the first visible subscribed project: %+v", ev.Project)
			}
			if err := f.n.DeleteChannel(f.ctx, bob, channel.Channel.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNotificationChannelInaccessibleSubscriptionsRemainManageable(t *testing.T) {
	f := newNotifyFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	projects := service.NewProjects(f.db, service.NewAuthorizer(f.db), nil)
	shared, err := projects.Create(f.ctx, alice, "shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projects.SetMember(f.ctx, alice, shared.Project.ID, bob.UserID, service.RoleViewer); err != nil {
		t.Fatal(err)
	}
	rcv := newReceiver(t)
	subs := []service.SubscriptionInput{allRunEvents(shared.Project.ID)}
	channel, _ := f.channel(bob, notify.KindWebhook, rcv.URL, subs...)
	if err := projects.RemoveMember(f.ctx, alice, shared.Project.ID, bob.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.n.TestChannel(f.ctx, bob, channel.Channel.ID); err != nil {
		t.Fatal(err)
	}
	if ev := decodeEvent(t, rcv.got()[0].body); ev.Project.ID != bob.PersonalProjectID {
		t.Fatalf("test without visible subscriptions did not use personal project: %+v", ev.Project)
	}
	_, _, err = f.n.CreateChannel(f.ctx, bob, service.CreateChannelInput{Name: "cannot subscribe", Kind: notify.KindWebhook, URL: rcv.URL, Subscriptions: subs})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.n.UpdateChannel(f.ctx, bob, channel.Channel.ID, service.UpdateChannelInput{Subscriptions: &subs})
	wantErr(t, err, service.ErrNotFound)
	off := false
	updated, err := f.n.UpdateChannel(f.ctx, bob, channel.Channel.ID, service.UpdateChannelInput{Enabled: &off})
	if err != nil || updated.Channel.Enabled || len(updated.Subscriptions) != 0 {
		t.Fatalf("disable channel with inaccessible subscription: enabled %t, subscriptions %d, %v", updated.Channel.Enabled, len(updated.Subscriptions), err)
	}
	empty := []service.SubscriptionInput{}
	if _, err := f.n.UpdateChannel(f.ctx, bob, channel.Channel.ID, service.UpdateChannelInput{Subscriptions: &empty}); err != nil {
		t.Fatal(err)
	}
	// A stale/corrupt personal-project reference must not fall back to an
	// arbitrary project or send an inaccessible name to the endpoint.
	stale := *bob
	stale.PersonalProjectID = uuid.New()
	before := len(rcv.got())
	_, err = f.n.TestChannel(f.ctx, &stale, channel.Channel.ID)
	if !errors.Is(err, service.ErrNotFound) || len(rcv.got()) != before {
		t.Fatalf("test without any visible project = %v, requests = %d", err, len(rcv.got()))
	}
	if err := f.n.DeleteChannel(f.ctx, bob, channel.Channel.ID); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationMetadataAccessFailureDoesNotCommitUpdatesOrSend(t *testing.T) {
	f := newNotifyFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	rcv := newReceiver(t)
	channel, _ := f.channel(alice, notify.KindWebhook, rcv.URL, allRunEvents(alice.PersonalProjectID))
	f.endedRun(task, store.RunFailed)
	f.dispatch()
	// Leave channel/delivery reads available while breaking the access
	// lookup. It must not turn a partial update into a successful response.
	if _, err := f.db.NewRaw("ALTER TABLE role_bindings RENAME TO unavailable_role_bindings").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, listErr := f.n.ListChannels(f.ctx, alice)
	_, updateErr := f.n.UpdateChannel(f.ctx, alice, channel.Channel.ID, service.UpdateChannelInput{Name: new("must roll back")})
	_, _, deliveriesErr := f.n.ListDeliveries(f.ctx, alice, channel.Channel.ID, "", nil, "")
	_, testErr := f.n.TestChannel(f.ctx, alice, channel.Channel.ID)
	if _, err := f.db.NewRaw("ALTER TABLE unavailable_role_bindings RENAME TO role_bindings").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{listErr, updateErr, deliveriesErr, testErr} {
		if err == nil || errors.Is(err, service.ErrNotFound) {
			t.Errorf("access lookup failure = %v, want database error", err)
		}
	}
	if len(rcv.got()) != 1 || len(f.deliveries(channel.Channel.ID)) != 1 {
		t.Fatal("access lookup failure sent or recorded a test notification")
	}
	list, err := f.n.ListChannels(f.ctx, alice)
	if err != nil || len(list) != 1 || list[0].Channel.Name != channel.Channel.Name || len(list[0].Subscriptions) != 1 {
		t.Fatalf("channel update was not rolled back or lookup did not recover: %d channels, %v", len(list), err)
	}
	if _, err := f.n.TestChannel(f.ctx, alice, channel.Channel.ID); err != nil || len(rcv.got()) != 2 {
		t.Fatalf("test after database recovery = %v, requests = %d", err, len(rcv.got()))
	}
}
