package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// finishRun plays the runner: it claims the run, starts it and ends it
// with status, reporting result when not empty.
func (f *fixture) finishRun(runID uuid.UUID, status, result string) {
	f.t.Helper()
	runner := f.runner("runner-" + runID.String()[:8])
	got := f.claim(runner, 64)
	found := false
	for _, a := range got {
		found = found || a.RunID == runID
	}
	if !found {
		f.t.Fatalf("run %s was not claimed; claimed %v", runID, got)
	}
	if err := f.coord.Started(f.ctx, runner, runID, "", time.Now()); err != nil {
		f.t.Fatal(err)
	}
	if result != "" {
		if err := f.coord.AppendEvents(f.ctx, runner, runID, []runproto.Event{
			{Seq: 1, Kind: runproto.EventResult, Result: &runproto.Result{Status: result, Summary: "结果"}},
		}); err != nil {
			f.t.Fatal(err)
		}
	}
	code := 0
	fin := runproto.Finish{Status: status, ExitCode: &code}
	if status == runproto.StatusFailed {
		code = 1
		fin.ErrorStage = "fetch"
	}
	if fin.Status == runproto.StatusCompleted {
		f.putEmptyState(runner, runID)
	}
	if err := f.coord.Finish(f.ctx, runner, runID, fin); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) startTest(p *auth.Principal, taskID uuid.UUID, version int) store.Run {
	f.t.Helper()
	d, err := f.runs.Start(f.ctx, p, taskID, service.StartRunInput{Version: &version, Test: true})
	if err != nil {
		f.t.Fatalf("start test run: %v", err)
	}
	return d.Run
}

func TestApplyIfMatch(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	in := func(ifMatch *int) service.ApplyTaskInput {
		return service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "digest", Manifest: basicManifest(), Files: mainPy(), IfMatch: ifMatch}
	}
	n := func(v int) *int { return &v }

	// A version number for a task that does not exist is a conflict; 0
	// asserts that it does not.
	_, err := f.tasks.Apply(f.ctx, alice, in(n(3)))
	var conflict *service.VersionConflictError
	if !errors.As(err, &conflict) || conflict.Current != 0 || conflict.Detail == "" {
		t.Fatalf("If-Match on a missing task: %v", err)
	}
	res, err := f.tasks.Apply(f.ctx, alice, in(n(0)))
	if err != nil || !res.Created || res.Version != 1 || res.Diff != nil {
		t.Fatalf("create = %+v, %v", res, err)
	}

	_, err = f.tasks.Apply(f.ctx, alice, in(nil))
	wantErr(t, err, service.ErrPreconditionRequired)
	for _, stale := range []int{0, 2} {
		_, err = f.tasks.Apply(f.ctx, alice, in(n(stale)))
		if !errors.As(err, &conflict) || conflict.Current != 1 {
			t.Fatalf("If-Match %d: %v", stale, err)
		}
	}
	res, err = f.tasks.Apply(f.ctx, alice, in(n(1)))
	if err != nil || res.Created || res.Version != 2 || res.Task.Task.LatestSeq != 2 {
		t.Fatalf("second version = %+v, %v", res, err)
	}
	// Precondition checks come after the permission check.
	bob := f.user("bob")
	_, err = f.tasks.Apply(f.ctx, bob, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "digest", Manifest: basicManifest(), Files: mainPy()})
	wantErr(t, err, service.ErrNotFound)
}

func TestApplyDryRunAndDiff(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Description = "汇总"
	files := []service.FileInput{
		{Path: "main.py", Content: []byte("import lib\nprint('a')\nprint('b')\n")},
		{Path: "lib.py", Content: []byte("x = 1\n")},
		{Path: "run.sh", Content: []byte("#!/bin/sh\n")},
	}
	// A dry run of a new task saves nothing and names what is missing.
	m.Credentials = []service.CredentialSpec{{Name: "LLM_API_KEY"}}
	preview, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{
		ProjectID: alice.PersonalProjectID, Name: "digest", Manifest: m, Files: files, DryRun: true,
	})
	if err != nil || !preview.DryRun || !preview.Created || preview.Version != 1 || preview.Task != nil || preview.Diff != nil {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	codes := map[string]string{}
	for _, w := range preview.Warnings {
		codes[w.Code] = w.Pointer
	}
	if codes["credential_missing"] != "#/manifest/credentials/0" || codes["runner_unavailable"] != "" {
		t.Fatalf("warnings = %+v", preview.Warnings)
	}
	if _, ok := codes["version_not_tested"]; ok {
		t.Fatal("a submission is not warned that it is untested")
	}
	if n, _ := f.db.NewSelect().Model((*store.Task)(nil)).Count(f.ctx); n != 0 {
		t.Fatalf("dry run stored %d tasks", n)
	}
	m.Credentials = nil
	first := f.apply(alice, "digest", m, files...)

	changed := []service.FileInput{
		{Path: "main.py", Content: []byte("import lib\nprint('a')\nprint('c')\n")},
		{Path: "run.sh", Content: []byte("#!/bin/sh\n"), Executable: true},
		{Path: "new.py", Content: []byte("y = 2")},
	}
	m.Description = "汇总，新版"
	one := 1
	preview, err = f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{
		ProjectID: alice.PersonalProjectID, Name: "digest", Manifest: m, Files: changed, DryRun: true, IfMatch: &one,
	})
	if err != nil || preview.Created || preview.Version != 2 || preview.Task == nil || preview.Task.Task.LatestSeq != 1 {
		t.Fatalf("preview = %+v, %v", preview, err)
	}
	d := preview.Diff
	if d == nil || d.Base == nil || *d.Base != 1 || d.Target != 2 || d.Truncated {
		t.Fatalf("diff = %+v", d)
	}
	if !strings.Contains(d.ManifestPatch, `-  "description": "汇总",`) || !strings.Contains(d.ManifestPatch, `+  "description": "汇总，新版",`) {
		t.Fatalf("manifest patch:\n%s", d.ManifestPatch)
	}
	got := map[string]service.FileChange{}
	for _, c := range d.Files {
		got[c.Path] = c
	}
	if len(got) != 4 {
		t.Fatalf("changes = %+v", d.Files)
	}
	wantMain := "--- a/main.py\n+++ b/main.py\n@@ -1,3 +1,3 @@\n import lib\n print('a')\n-print('b')\n+print('c')\n"
	if c := got["main.py"]; c.Change != service.ChangeModified || c.Additions != 1 || c.Deletions != 1 || c.Patch != wantMain {
		t.Fatalf("main.py = %+v\n%s", c, c.Patch)
	}
	if c := got["lib.py"]; c.Change != service.ChangeRemoved || c.Deletions != 1 || !strings.HasPrefix(c.Patch, "--- a/lib.py\n+++ /dev/null\n") {
		t.Fatalf("lib.py = %+v", c)
	}
	if c := got["new.py"]; c.Change != service.ChangeAdded || c.Additions != 1 || !strings.Contains(c.Patch, "+y = 2\n\\ No newline at end of file\n") {
		t.Fatalf("new.py = %+v\n%s", c, c.Patch)
	}
	if c := got["run.sh"]; c.Change != service.ChangeModeChanged || c.Patch != "" {
		t.Fatalf("run.sh = %+v", c)
	}
	if v, _ := f.tasks.Get(f.ctx, alice, first.Task.ID); v.Task.LatestSeq != 1 {
		t.Fatal("dry run saved a version")
	}

	// The saved version diffs the same way, and diffs against the active
	// version once one is active.
	second := f.apply(alice, "digest", m, changed...)
	stored, err := f.tasks.Diff(f.ctx, alice, first.Task.ID, 2, nil)
	if err != nil || len(stored.Files) != 4 || *stored.Base != 1 {
		t.Fatalf("stored diff = %+v, %v", stored, err)
	}
	empty, err := f.tasks.Diff(f.ctx, alice, first.Task.ID, 1, nil)
	if err != nil || empty.Base != nil || len(empty.Files) != 3 {
		t.Fatalf("diff of version 1 = %+v, %v", empty, err)
	}
	same, err := f.tasks.Diff(f.ctx, alice, first.Task.ID, 2, &[]int{2}[0])
	if err != nil || len(same.Files) != 0 || same.ManifestPatch != "" {
		t.Fatalf("diff with itself = %+v, %v", same, err)
	}
	_, err = f.tasks.Diff(f.ctx, alice, first.Task.ID, 2, &[]int{9}[0])
	if fields, _ := fieldErrors(t, err); fields["?base"] == "" {
		t.Fatalf("unknown base: %v", err)
	}
	_ = second
}

func TestActivationNeedsATestedVersion(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Concurrency = service.ConcurrencyAllow
	task := f.apply(alice, "digest", m)
	id := task.Task.ID

	_, err := f.tasks.SetActivation(f.ctx, alice, id, true, nil)
	wantErr(t, err, service.ErrVersionNotTested)
	report, err := f.tasks.Validate(f.ctx, alice, id, 1)
	if err != nil || report.Activatable || len(report.Errors) != 0 {
		t.Fatalf("report = %+v, %v", report, err)
	}

	// Neither a manual run, nor a test run whose business result failed,
	// nor a failed test run tests the version.
	f.finishRun(f.start(alice, id).ID, runproto.StatusCompleted, "")
	f.finishRun(f.startTest(alice, id, 1).ID, runproto.StatusCompleted, "failed")
	f.finishRun(f.startTest(alice, id, 1).ID, runproto.StatusFailed, "")
	_, err = f.tasks.SetActivation(f.ctx, alice, id, true, nil)
	wantErr(t, err, service.ErrVersionNotTested)

	f.finishRun(f.startTest(alice, id, 1).ID, runproto.StatusCompleted, "partial")
	report, err = f.tasks.Validate(f.ctx, alice, id, 1)
	if err != nil || !report.Activatable {
		t.Fatalf("report after a successful test = %+v, %v", report, err)
	}
	active, err := f.tasks.SetActivation(f.ctx, alice, id, true, nil)
	if err != nil || active.Task.State != store.TaskActive || active.Task.ActiveSeq != 1 || active.Version.Version.TestedAt.IsZero() {
		t.Fatalf("activated = %+v, %v", active.Task, err)
	}

	// A new version does not move the active one.
	f.apply(alice, "digest", m)
	got, _ := f.tasks.Get(f.ctx, alice, id)
	if got.Task.ActiveSeq != 1 || got.Task.LatestSeq != 2 {
		t.Fatalf("task = %+v", got.Task)
	}
	_, err = f.tasks.SetActivation(f.ctx, alice, id, true, &[]int{7}[0])
	if fields, content := fieldErrors(t, err); fields["#/version"] == "" || !content {
		t.Fatalf("unknown version: %v", err)
	}

	paused, err := f.tasks.SetActivation(f.ctx, alice, id, false, nil)
	if err != nil || paused.Task.State != store.TaskInactive || paused.Task.ActiveSeq != 1 {
		t.Fatalf("paused = %+v, %v", paused.Task, err)
	}

	// The switch can be turned off.
	lenient := service.NewTasks(f.db, service.NewAuthorizer(f.db), f.blobs, nil).
		WithTestBeforeActivate(func(context.Context) (bool, error) { return false, nil })
	if d, err := lenient.SetActivation(f.ctx, alice, id, true, nil); err != nil || d.Task.ActiveSeq != 2 {
		t.Fatalf("activation without the test requirement = %+v, %v", d.Task, err)
	}

	// Viewers may not activate; strangers do not see the task.
	viewer, bob := f.user("viewer"), f.user("bob")
	f.grant(alice.PersonalProjectID, viewer, service.RoleViewer)
	_, err = f.tasks.SetActivation(f.ctx, viewer, id, false, nil)
	wantErr(t, err, service.ErrForbidden)
	_, err = f.tasks.SetActivation(f.ctx, bob, id, false, nil)
	wantErr(t, err, service.ErrNotFound)
	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action IN (?, ?)", "task.activate", "task.pause").Count(f.ctx)
	if err != nil || n != 3 {
		t.Fatalf("activation audit events = %d, %v", n, err)
	}
}

func TestArchiveTask(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	id := task.Task.ID
	now := store.Now()
	trigger := store.Trigger{ID: store.NewID(), TaskID: id, Name: "daily", Type: store.TriggerTypeCron, Enabled: true,
		Schedule: "0 8 * * *", Timezone: "UTC", NextFireAt: now.Add(time.Hour), CreatedBy: alice.UserID, CreatedAt: now, UpdatedAt: now}
	if _, err := f.db.NewInsert().Model(&trigger).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}

	archived, err := f.tasks.Archive(f.ctx, alice, id)
	if err != nil || archived.Task.ArchivedAt.IsZero() || archived.Task.State != store.TaskInactive {
		t.Fatalf("archived = %+v, %v", archived.Task, err)
	}
	if err := f.db.NewSelect().Model(&trigger).WherePK().Scan(f.ctx); err != nil || trigger.Enabled || !trigger.NextFireAt.IsZero() {
		t.Fatalf("trigger after archive = %+v, %v", trigger, err)
	}
	again, err := f.tasks.Archive(f.ctx, alice, id)
	if err != nil || !again.Task.ArchivedAt.Equal(archived.Task.ArchivedAt) {
		t.Fatalf("archive twice = %+v, %v", again.Task, err)
	}
	_, err = f.runs.Start(f.ctx, alice, id, service.StartRunInput{})
	wantErr(t, err, service.ErrTaskArchived)
	_, err = f.tasks.SetActivation(f.ctx, alice, id, false, nil)
	wantErr(t, err, service.ErrTaskArchived)

	// The name is free again, and lists keep the two apart.
	fresh := f.apply(alice, "digest", basicManifest())
	if fresh.Task.ID == id || fresh.Task.LatestSeq != 1 {
		t.Fatalf("new task = %+v", fresh.Task)
	}
	page, err := f.tasks.List(f.ctx, alice, service.TaskListInput{Name: "digest"})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].Task.ID != fresh.Task.ID {
		t.Fatalf("list = %+v, %v", page, err)
	}
	page, err = f.tasks.List(f.ctx, alice, service.TaskListInput{Archived: true})
	if err != nil || len(page.Tasks) != 1 || page.Tasks[0].Task.ID != id {
		t.Fatalf("archived list = %+v, %v", page, err)
	}
}

func TestListTasks(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	allow.Description = "Weekly CRM digest"
	names := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	ids := map[string]uuid.UUID{}
	for _, n := range names {
		ids[n] = f.apply(alice, n, allow).Task.ID
	}
	f.apply(bob, "alpha", basicManifest())

	// Runs: beta has a finished manual run and an active one, plus a test
	// run that does not count as its last run.
	f.finishRun(f.start(alice, ids["beta"]).ID, runproto.StatusFailed, "")
	f.start(alice, ids["beta"])
	f.startTest(alice, ids["beta"], 1)

	var seen []string
	limit := 2
	in := service.TaskListInput{Limit: &limit}
	for range 5 {
		page, err := f.tasks.List(f.ctx, alice, in)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range page.Tasks {
			seen = append(seen, s.Task.Name)
			if s.Task.Name == "beta" {
				if s.LastRun == nil || s.LastRun.Status != store.RunQueued || s.LastRun.IsTest || s.ActiveRuns != 2 {
					t.Errorf("beta summary = last %+v, active %d", s.LastRun, s.ActiveRuns)
				}
			} else if s.LastRun != nil || s.ActiveRuns != 0 {
				t.Errorf("%s summary = %+v", s.Task.Name, s)
			}
		}
		if page.NextCursor == "" {
			break
		}
		in.Cursor = page.NextCursor
	}
	// Newest first, each task once, bob's task never.
	if strings.Join(seen, ",") != "epsilon,delta,gamma,beta,alpha" {
		t.Fatalf("pages = %v", seen)
	}

	for _, tt := range []struct {
		in   service.TaskListInput
		want string
	}{
		{service.TaskListInput{Name: "gamma"}, "gamma"},
		{service.TaskListInput{Query: "ELT"}, "delta"},
		{service.TaskListInput{Query: "crm digest"}, "epsilon,delta,gamma,beta,alpha"},
		{service.TaskListInput{Query: "100%"}, ""},
		{service.TaskListInput{State: store.TaskActive}, ""},
		{service.TaskListInput{ProjectID: &alice.PersonalProjectID, Name: "alpha"}, "alpha"},
	} {
		page, err := f.tasks.List(f.ctx, alice, tt.in)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, s := range page.Tasks {
			got = append(got, s.Task.Name)
		}
		if strings.Join(got, ",") != tt.want {
			t.Errorf("list %+v = %v, want %s", tt.in, got, tt.want)
		}
	}
	_, err := f.tasks.List(f.ctx, bob, service.TaskListInput{ProjectID: &alice.PersonalProjectID})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.List(f.ctx, alice, service.TaskListInput{Cursor: "bogus"})
	if fields, _ := fieldErrors(t, err); fields["?cursor"] == "" {
		t.Fatalf("bad cursor: %v", err)
	}
}

func TestVersionsAndFiles(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	m := basicManifest()
	for i := range 3 {
		m.Description = strings.Repeat("v", i+1)
		f.apply(alice, "digest", m, service.FileInput{Path: "main.py", Content: []byte("print(" + strings.Repeat("1", i+1) + ")\n")},
			service.FileInput{Path: "lib/util.py", Content: []byte("x = 1\n")})
	}
	task, _ := f.tasks.Get(f.ctx, alice, mustTaskID(t, f, alice, "digest"))
	id := task.Task.ID

	limit := 2
	page, err := f.tasks.Versions(f.ctx, alice, id, &limit, "")
	if err != nil || len(page.Versions) != 2 || page.Versions[0].Version.Seq != 3 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	if v := page.Versions[0]; v.FileCount != 2 || v.Manifest.Description != "vvv" || v.Active {
		t.Fatalf("version summary = %+v", v)
	}
	page, err = f.tasks.Versions(f.ctx, alice, id, &limit, page.NextCursor)
	if err != nil || len(page.Versions) != 1 || page.Versions[0].Version.Seq != 1 || page.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}

	v, err := f.tasks.Version(f.ctx, alice, id, 2)
	if err != nil || v.Version.Seq != 2 || len(v.Files) != 2 || v.Files[0].Path != "lib/util.py" {
		t.Fatalf("version 2 = %+v, %v", v, err)
	}
	_, err = f.tasks.Version(f.ctx, alice, id, 9)
	wantErr(t, err, service.ErrNotFound)

	file, err := f.tasks.File(f.ctx, alice, id, 2, "main.py")
	if err != nil || file.Content != "print(11)\n" || file.File.Size != int64(len("print(11)\n")) {
		t.Fatalf("file = %+v, %v", file, err)
	}
	_, err = f.tasks.File(f.ctx, alice, id, 2, "missing.py")
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.File(f.ctx, bob, id, 2, "main.py")
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.Versions(f.ctx, bob, id, nil, "")
	wantErr(t, err, service.ErrNotFound)
}

func mustTaskID(t *testing.T, f *fixture, p *auth.Principal, name string) uuid.UUID {
	t.Helper()
	page, err := f.tasks.List(f.ctx, p, service.TaskListInput{Name: name})
	if err != nil || len(page.Tasks) != 1 {
		t.Fatalf("find %s: %+v, %v", name, page, err)
	}
	return page.Tasks[0].Task.ID
}

func TestValidateVersion(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Credentials = []service.CredentialSpec{{Name: "LLM_API_KEY"}, {Name: "OPTIONAL_KEY", Optional: true}, {Name: "OLD_KEY"}}
	m.Tools = []string{"crm-mcp"}
	task := f.apply(alice, "digest", m)
	id := task.Task.ID
	f.runner("online")

	// OLD_KEY is granted but its credential is revoked.
	now := store.Now()
	cred := store.Credential{ID: store.NewID(), OwnerUserID: alice.UserID, Name: "old", Purpose: "旧的", Fields: []store.CredentialField{{Name: "OLD_KEY", Secret: true}},
		Status: store.CredentialRevoked, CurrentVersion: 1, CreatedAt: now, UpdatedAt: now}
	if _, err := f.db.NewInsert().Model(&cred).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewInsert().Model(&store.CredentialGrant{ID: store.NewID(), CredentialID: cred.ID, TaskID: id, Name: "OLD_KEY", GrantedBy: alice.UserID, CreatedAt: now}).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}

	r, err := f.tasks.Validate(f.ctx, alice, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	errs := map[string]string{}
	for _, e := range r.Errors {
		errs[e.Pointer] = e.Code
	}
	want := map[string]string{ // #nosec G101 -- error codes, not secrets
		"#/manifest/credentials/0": "credential_missing",
		"#/manifest/credentials/2": "credential_unavailable",
		"#/manifest/tools/0":       "validation_failed",
	}
	if len(errs) != len(want) {
		t.Fatalf("errors = %+v", r.Errors)
	}
	for k, v := range want {
		if errs[k] != v {
			t.Errorf("error at %s = %q, want %q", k, errs[k], v)
		}
	}
	warns := map[string]string{}
	for _, w := range r.Warnings {
		warns[w.Code] = w.Pointer
	}
	if warns["credential_missing"] != "#/manifest/credentials/1" || !hasKey(warns, "version_not_tested") || hasKey(warns, "runner_unavailable") {
		t.Fatalf("warnings = %+v", r.Warnings)
	}
	if r.Activatable {
		t.Fatal("a version with errors is not activatable")
	}
	_, err = f.tasks.Validate(f.ctx, f.user("bob"), id, 1)
	wantErr(t, err, service.ErrNotFound)

	// Runner labels: only a runner with every label counts.
	gpu := basicManifest()
	gpu.RunnerLabels = []string{"gpu", "intranet"}
	gpuTask := f.apply(alice, "gpu", gpu).Task.ID
	labelWarning := func() string {
		t.Helper()
		r, err := f.tasks.Validate(f.ctx, alice, gpuTask, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range r.Warnings {
			if w.Code == "runner_unavailable" {
				return w.Pointer
			}
		}
		return ""
	}
	if got := labelWarning(); got != "#/manifest/runner_labels" {
		t.Fatalf("label warning at %q", got)
	}
	if err := f.coord.Register(f.ctx, runproto.Registration{ID: "gpu-1", Slots: 1, Labels: []string{"gpu", "intranet", "default"}}); err != nil {
		t.Fatal(err)
	}
	if got := labelWarning(); got != "" {
		t.Fatalf("label warning with a matching runner at %q", got)
	}
}

func hasKey(m map[string]string, k string) bool {
	_, ok := m[k]
	return ok
}
