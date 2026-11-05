package service_test

import (
	"errors"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestSetConsoleManualRun(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	id := task.Task.ID
	if task.Task.ConsoleHideManualRun {
		t.Fatal("a new task hides the manual run button")
	}
	// An agent's access token: the switch must not get in its way.
	agent := *alice
	agent.Method = auth.MethodAccessToken

	off, on := false, true
	d, err := f.tasks.SetConsole(f.ctx, &agent, id, service.ConsoleOptions{ManualRun: &off})
	if err != nil {
		t.Fatal(err)
	}
	if !d.Task.ConsoleHideManualRun || d.Task.LatestSeq != 1 {
		t.Fatalf("task = %+v", d.Task)
	}
	audits, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ? AND resource_id = ?", "task.console", id.String()).Count(f.ctx)
	if err != nil || audits != 1 {
		t.Fatalf("audit rows = %d, %v", audits, err)
	}

	// The console's session is refused; the agent and reruns are not.
	if _, err := f.runs.Start(f.ctx, alice, id, service.StartRunInput{}); !errors.Is(err, service.ErrManualRunDisabled) {
		t.Fatalf("session start = %v, want ErrManualRunDisabled", err)
	}
	run := f.start(&agent, id)
	if _, err := f.runs.Cancel(f.ctx, &agent, run.ID); err != nil {
		t.Fatal(err)
	}
	rerun, err := f.runs.Rerun(f.ctx, alice, run.ID, service.RerunInput{})
	if err != nil {
		t.Fatalf("session rerun = %v", err)
	}
	if _, err := f.runs.Cancel(f.ctx, alice, rerun.Run.ID); err != nil {
		t.Fatal(err)
	}

	// Leaving the field out changes nothing; turning it back on lets the
	// session start runs again.
	if d, err := f.tasks.SetConsole(f.ctx, &agent, id, service.ConsoleOptions{}); err != nil || !d.Task.ConsoleHideManualRun {
		t.Fatalf("empty change = %+v, %v", d.Task, err)
	}
	if _, err := f.tasks.SetConsole(f.ctx, &agent, id, service.ConsoleOptions{ManualRun: &on}); err != nil {
		t.Fatal(err)
	}
	f.start(alice, id)
}

func TestSetConsolePermissions(t *testing.T) {
	f := newFixture(t)
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	task := f.apply(alice, "digest", basicManifest())
	f.grant(alice.PersonalProjectID, carol, service.RoleViewer)
	off := false
	for _, tc := range []struct {
		name string
		p    *auth.Principal
		want error
	}{
		{"stranger", bob, service.ErrNotFound},
		{"viewer", carol, service.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.tasks.SetConsole(f.ctx, tc.p, task.Task.ID, service.ConsoleOptions{ManualRun: &off}); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := f.tasks.Archive(f.ctx, alice, task.Task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.SetConsole(f.ctx, alice, task.Task.ID, service.ConsoleOptions{ManualRun: &off}); !errors.Is(err, service.ErrTaskArchived) {
		t.Fatalf("archived = %v", err)
	}
}

func TestBundleCarriesConsole(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	task := f.apply(alice, "digest", basicManifest())
	off := false
	if _, err := f.tasks.SetConsole(f.ctx, alice, task.Task.ID, service.ConsoleOptions{ManualRun: &off}); err != nil {
		t.Fatal(err)
	}
	b, err := f.tasks.Export(f.ctx, alice, task.Task.ID, service.ExportTaskInput{})
	if err != nil {
		t.Fatal(err)
	}
	if !b.HideManualRun {
		t.Fatal("export dropped the console option")
	}
	res, err := f.tasks.Import(f.ctx, bob, service.ImportTaskInput{ProjectID: bob.PersonalProjectID, Bundle: b})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Task.Task.ConsoleHideManualRun {
		t.Fatal("import dropped the console option")
	}
}
