package service_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// bundleTask gives alice the task "digest" with two versions, the first
// one active, and three triggers.
func bundleTask(t *testing.T, f *fixture, alice *auth.Principal) service.TaskDetail {
	t.Helper()
	m := credManifest(service.CredentialSpec{Name: "LLM_API_KEY"}, service.CredentialSpec{Name: "CRM_COOKIE", As: "file", Optional: true})
	m.Description = "第一版"
	first := f.apply(alice, "digest", m)
	m.Description = "第二版"
	m.Stages = []service.StageSpec{{ID: "fetch", Name: "获取"}}
	f.apply(alice, "digest", m,
		service.FileInput{Path: "main.py", Content: []byte("print('v2 <ok> & done')\n")},
		service.FileInput{Path: "bin/run.sh", Content: []byte("#!/bin/sh\npython main.py\n"), Executable: true},
	)
	f.tasks.WithTestBeforeActivate(func(context.Context) (bool, error) { return false, nil })
	one := 1
	if _, err := f.tasks.SetActivation(f.ctx, alice, first.Task.ID, true, &one); err != nil {
		t.Fatal(err)
	}
	f.tasks.WithTestBeforeActivate(func(context.Context) (bool, error) { return true, nil })
	disabled := false
	body := int64(4096)
	triggers := service.NewTriggers(f.db, service.NewAuthorizer(f.db), f.blobs, nil, service.TriggerOptions{})
	if _, err := triggers.Set(f.ctx, alice, first.Task.ID, service.SetTriggersInput{Triggers: []service.TriggerInput{
		{Name: "morning", Type: store.TriggerTypeCron, Schedule: "0 8 * * 1-5", Timezone: "Asia/Shanghai", CatchUp: store.CatchUpLatest, Params: map[string]any{"source": "erp"}},
		{Name: "push", Type: store.TriggerTypeWebhook, MaxBodyBytes: &body},
		{Name: "paused", Type: store.TriggerTypeCron, Schedule: "@hourly", Timezone: "UTC", Enabled: &disabled},
	}}); err != nil {
		t.Fatal(err)
	}
	d, err := f.tasks.Get(f.ctx, alice, first.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExportTask(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	src := bundleTask(t, f, alice)
	id := src.Task.ID

	b, err := f.tasks.Export(f.ctx, alice, id, service.ExportTaskInput{Triggers: true})
	if err != nil {
		t.Fatal(err)
	}
	if b.Name != "digest" || b.ActiveVersion != 1 || b.OmittedVersions != 0 || len(b.Versions) != 1 {
		t.Fatalf("bundle = %+v", b)
	}
	v := b.Versions[0]
	if v.Seq != 2 || v.Manifest.Description != "第二版" || len(v.Files) != 2 {
		t.Fatalf("version = %+v", v)
	}
	if v.Files[0].Path != "bin/run.sh" || !v.Files[0].Executable || string(v.Files[1].Content) != "print('v2 <ok> & done')\n" {
		t.Fatalf("files = %+v", v.Files)
	}
	names := []string{}
	for _, tr := range b.Triggers {
		names = append(names, tr.Name)
	}
	if !slices.Equal(names, []string{"morning", "paused", "push"}) {
		t.Fatalf("triggers = %v", names)
	}
	morning, paused, push := b.Triggers[0], b.Triggers[1], b.Triggers[2]
	if morning.Schedule != "0 8 * * 1-5" || morning.Timezone != "Asia/Shanghai" || morning.CatchUp != store.CatchUpLatest ||
		morning.Params["source"] != "erp" || !*morning.Enabled {
		t.Errorf("morning = %+v", morning)
	}
	if *paused.Enabled || paused.Params != nil {
		t.Errorf("paused = %+v", paused)
	}
	if push.MaxBodyBytes == nil || *push.MaxBodyBytes != 4096 || push.CatchUp != "" || push.Schedule != "" {
		t.Errorf("push = %+v", push)
	}

	history, err := f.tasks.Export(f.ctx, alice, id, service.ExportTaskInput{History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Versions) != 2 || history.Versions[0].Seq != 1 || history.Versions[1].Seq != 2 || history.Triggers != nil {
		t.Fatalf("history = %+v", history)
	}
	one := 1
	if b, err := f.tasks.Export(f.ctx, alice, id, service.ExportTaskInput{Version: &one, History: true}); err != nil || len(b.Versions) != 1 || b.Versions[0].Seq != 1 {
		t.Fatalf("version 1 = %+v, %v", b, err)
	}
	three := 3
	_, err = f.tasks.Export(f.ctx, alice, id, service.ExportTaskInput{Version: &three})
	var verr *service.ValidationError
	if !errors.As(err, &verr) || verr.Fields[0].Parameter != "version" {
		t.Fatalf("version 3: %v", err)
	}

	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ? AND resource_id = ?", "task.export", id.String()).Count(f.ctx)
	if err != nil || n != 3 {
		t.Fatalf("task.export audit events = %d, %v", n, err)
	}
}

func TestImportTask(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	src := bundleTask(t, f, alice)
	b, err := f.tasks.Export(f.ctx, alice, src.Task.ID, service.ExportTaskInput{History: true, Triggers: true})
	if err != nil {
		t.Fatal(err)
	}
	project := bob.PersonalProjectID

	preview, err := f.tasks.Import(f.ctx, bob, service.ImportTaskInput{ProjectID: project, Bundle: b, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if !preview.DryRun || preview.Task != nil || len(preview.Triggers.Triggers) != 3 || preview.Triggers.Triggers[1].Token != "" {
		t.Fatalf("preview = %+v", preview)
	}
	if n, _ := f.db.NewSelect().Model((*store.Task)(nil)).Where("project_id = ?", project).Count(f.ctx); n != 0 {
		t.Fatalf("a dry run created %d tasks", n)
	}

	res, err := f.tasks.Import(f.ctx, bob, service.ImportTaskInput{ProjectID: project, Bundle: b})
	if err != nil {
		t.Fatal(err)
	}
	task := res.Task.Task
	if task.ProjectID != project || task.Name != "digest" || task.CreatedBy != bob.UserID || task.LatestSeq != 2 ||
		task.State != store.TaskInactive || task.ActiveSeq != 0 || task.Description != "第二版" || task.TriggersRevision != 1 {
		t.Fatalf("task = %+v", task)
	}
	if !slices.Equal(res.SourceSeqs, []int{1, 2}) {
		t.Errorf("source seqs = %v", res.SourceSeqs)
	}
	// Each version is the source's tree, created by bob and untested.
	for seq := 1; seq <= 2; seq++ {
		got, err := f.tasks.Version(f.ctx, bob, task.ID, seq)
		if err != nil {
			t.Fatal(err)
		}
		want, err := f.tasks.Version(f.ctx, alice, src.Task.ID, seq)
		if err != nil {
			t.Fatal(err)
		}
		if got.Version.TreeHash != want.Version.TreeHash || got.Version.CreatedBy != bob.UserID || !got.Version.TestedAt.IsZero() {
			t.Errorf("version %d = %+v, source %+v", seq, got.Version, want.Version)
		}
		if got.Manifest.Description != want.Manifest.Description {
			t.Errorf("version %d manifest = %+v", seq, got.Manifest)
		}
		for _, file := range got.Files {
			if _, err := blob.ReadAll(f.ctx, f.blobs, file.BlobHash); err != nil {
				t.Errorf("content of %s: %v", file.Path, err)
			}
		}
	}

	// Credentials are names to bind; webhook tokens are new.
	if len(res.Credentials) != 2 || res.Credentials[0].Spec.Name != "LLM_API_KEY" || res.Credentials[0].Spec.As != "env" ||
		res.Credentials[0].Status != service.TaskCredentialMissing || res.Credentials[1].Spec.As != "file" || !res.Credentials[1].Spec.Optional {
		t.Errorf("credentials = %+v", res.Credentials)
	}
	var missing bool
	for _, w := range res.Warnings {
		if w.Code == "credential_missing" && w.Pointer == "#/bundle/versions/1/manifest/credentials/0" {
			missing = true
		}
	}
	if !missing {
		t.Errorf("warnings = %+v", res.Warnings)
	}
	var rows []store.Trigger
	if err := f.db.NewSelect().Model(&rows).Where("task_id = ?", task.ID).Order("name").Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	var srcHook store.Trigger
	if err := f.db.NewSelect().Model(&srcHook).Where("task_id = ? AND name = ?", src.Task.ID, "push").Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].CatchUp != store.CatchUpLatest || rows[0].Params != `{"source":"erp"}` || rows[0].NextFireAt.IsZero() ||
		rows[1].Enabled || rows[2].MaxBodyBytes != 4096 || rows[2].CreatedBy != bob.UserID {
		t.Fatalf("triggers = %+v", rows)
	}
	var token string
	for _, tv := range res.Triggers.Triggers {
		if tv.Trigger.Name == "push" {
			token = tv.Token
		}
	}
	if token == "" || rows[2].TokenHash == "" || rows[2].TokenHash == srcHook.TokenHash || auth.HashToken(token) != rows[2].TokenHash {
		t.Errorf("webhook token = %q, hash %q, source hash %q", token, rows[2].TokenHash, srcHook.TokenHash)
	}

	// Test before activate still applies.
	_, err = f.tasks.SetActivation(f.ctx, bob, task.ID, true, nil)
	wantErr(t, err, service.ErrVersionNotTested)

	// The name is taken now; another name works.
	_, err = f.tasks.Import(f.ctx, bob, service.ImportTaskInput{ProjectID: project, Bundle: b})
	var conflict *service.VersionConflictError
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Detail, "digest") {
		t.Fatalf("second import: %v", err)
	}
	again, err := f.tasks.Import(f.ctx, bob, service.ImportTaskInput{ProjectID: project, Bundle: b, Name: "digest-copy"})
	if err != nil || again.Task.Task.Name != "digest-copy" {
		t.Fatalf("import as digest-copy = %+v, %v", again.Task, err)
	}

	var audit store.AuditEvent
	if err := f.db.NewSelect().Model(&audit).Where("action = ? AND resource_id = ?", "task.import", task.ID.String()).Scan(f.ctx); err != nil {
		t.Fatal(err)
	}
	if audit.ActorID != bob.UserID || audit.Details["source_name"] != "digest" || audit.Details["project_id"] != project.String() {
		t.Errorf("audit = %+v", audit)
	}
}

func TestImportValidation(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	valid := func() service.TaskBundle {
		return service.TaskBundle{Name: "digest", Versions: []service.BundleVersion{
			{Seq: 1, Manifest: basicManifest(), Files: mainPy()},
			{Seq: 3, Manifest: basicManifest(), Files: mainPy()},
		}}
	}
	tests := []struct {
		name     string
		change   func(b *service.TaskBundle, in *service.ImportTaskInput)
		pointers []string
	}{
		{"bad name", func(b *service.TaskBundle, _ *service.ImportTaskInput) { b.Name = "Digest" }, []string{"#/bundle/name"}},
		{"bad override", func(_ *service.TaskBundle, in *service.ImportTaskInput) { in.Name = "a_b" }, []string{"#/name"}},
		{"no versions", func(b *service.TaskBundle, _ *service.ImportTaskInput) { b.Versions = nil }, []string{"#/bundle/versions"}},
		{"versions out of order", func(b *service.TaskBundle, _ *service.ImportTaskInput) { b.Versions[1].Seq = 1 }, []string{"#/bundle/versions/1/seq"}},
		{"file path", func(b *service.TaskBundle, _ *service.ImportTaskInput) {
			b.Versions[1].Files = []service.FileInput{{Path: "../etc/passwd", Content: []byte("x")}}
		}, []string{"#/bundle/versions/1/files/0/path"}},
		{"too large", func(b *service.TaskBundle, _ *service.ImportTaskInput) {
			b.Versions[0].Files = []service.FileInput{{Path: "main.py", Content: make([]byte, 1<<20+1)}}
		}, []string{"#/bundle/versions/0/files"}},
		{"manifest", func(b *service.TaskBundle, _ *service.ImportTaskInput) { b.Versions[0].Manifest.Entrypoint = nil },
			[]string{"#/bundle/versions/0/manifest/entrypoint"}},
		{"trigger", func(b *service.TaskBundle, _ *service.ImportTaskInput) {
			b.Triggers = []service.TriggerInput{{Name: "morning", Type: store.TriggerTypeCron, Schedule: "0 8 * * *"}}
		}, []string{"#/bundle/triggers/0/timezone"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := valid()
			in := service.ImportTaskInput{ProjectID: alice.PersonalProjectID}
			tt.change(&b, &in)
			in.Bundle = b
			_, err := f.tasks.Import(f.ctx, alice, in)
			var verr *service.ValidationError
			if !errors.As(err, &verr) || !verr.Content {
				t.Fatalf("err = %v, want a 422 validation error", err)
			}
			var got []string
			for _, fe := range verr.Fields {
				got = append(got, fe.Pointer)
			}
			if !slices.Equal(got, tt.pointers) {
				t.Fatalf("pointers = %v, want %v (%v)", got, tt.pointers, err)
			}
		})
	}
	if n, _ := f.db.NewSelect().Model((*store.Task)(nil)).Count(f.ctx); n != 0 {
		t.Fatalf("rejected imports created %d tasks", n)
	}
	res, err := f.tasks.Import(f.ctx, alice, service.ImportTaskInput{ProjectID: alice.PersonalProjectID, Bundle: valid()})
	if err != nil || !slices.Equal(res.SourceSeqs, []int{1, 3}) || res.Task.Task.LatestSeq != 2 || res.Task.Task.TriggersRevision != 0 {
		t.Fatalf("valid import = %+v, %v", res, err)
	}
}

func TestTaskBundlePermissions(t *testing.T) {
	f := newFixture(t)
	alice, viewer, stranger := f.user("alice"), f.user("vera"), f.user("sam")
	f.grant(alice.PersonalProjectID, viewer, service.RoleViewer)
	task := f.apply(alice, "digest", basicManifest())

	// A viewer may export what they can already read, but not import.
	b, err := f.tasks.Export(f.ctx, viewer, task.Task.ID, service.ExportTaskInput{Triggers: true})
	if err != nil {
		t.Fatalf("viewer export: %v", err)
	}
	_, err = f.tasks.Import(f.ctx, viewer, service.ImportTaskInput{ProjectID: alice.PersonalProjectID, Bundle: b, Name: "copy"})
	wantErr(t, err, service.ErrPermissionDenied)
	if _, err := f.tasks.Import(f.ctx, viewer, service.ImportTaskInput{ProjectID: viewer.PersonalProjectID, Bundle: b}); err != nil {
		t.Fatalf("viewer import into their own project: %v", err)
	}

	// Someone outside the project learns nothing.
	_, err = f.tasks.Export(f.ctx, stranger, task.Task.ID, service.ExportTaskInput{})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.Import(f.ctx, stranger, service.ImportTaskInput{ProjectID: alice.PersonalProjectID, Bundle: b, Name: "copy"})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.Export(f.ctx, alice, uuid.New(), service.ExportTaskInput{})
	wantErr(t, err, service.ErrNotFound)
}

func TestExportHistoryLimits(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	var id uuid.UUID
	for i := range 52 {
		d := f.apply(alice, "many", basicManifest(), service.FileInput{Path: "main.py", Content: []byte(strings.Repeat("#", i+1))})
		id = d.Task.ID
	}
	b, err := f.tasks.Export(f.ctx, alice, id, service.ExportTaskInput{History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Versions) != 50 || b.Versions[0].Seq != 3 || b.Versions[49].Seq != 52 || b.OmittedVersions != 2 {
		t.Fatalf("history: %d versions from %d, omitted %d", len(b.Versions), b.Versions[0].Seq, b.OmittedVersions)
	}

	// Close to 1 MiB per version: only the newest versions that keep the
	// bundle importable are included.
	big := make([]byte, 1<<20-100)
	for i := range big {
		big[i] = 'a'
	}
	for i := range 5 {
		big[0] = byte('a' + i)
		d := f.apply(alice, "big", basicManifest(), service.FileInput{Path: "main.py", Content: slices.Clone(big)})
		id = d.Task.ID
	}
	b, err = f.tasks.Export(f.ctx, alice, id, service.ExportTaskInput{History: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Versions) != 3 || b.Versions[0].Seq != 3 || b.OmittedVersions != 2 {
		t.Fatalf("large history: %d versions from %d, omitted %d", len(b.Versions), b.Versions[0].Seq, b.OmittedVersions)
	}
}

func TestImportRejectsPlaintext(t *testing.T) {
	f := newPlaintextFixture(t)
	b, err := f.tasks.Export(f.ctx, f.alice, f.task.Task.ID, service.ExportTaskInput{Triggers: true})
	if err != nil {
		t.Fatal(err)
	}
	b.Versions[0].Files = append(b.Versions[0].Files, service.FileInput{Path: "config.py", Content: []byte("KEY = '" + testSecret + "'\n")})
	b.Triggers = []service.TriggerInput{{Name: "push", Type: store.TriggerTypeWebhook, Params: map[string]any{"token": testSecret}}}
	for _, dryRun := range []bool{true, false} {
		_, err = f.tasks.Import(f.ctx, f.agent, service.ImportTaskInput{ProjectID: f.alice.PersonalProjectID, Bundle: b, Name: "copy", DryRun: dryRun})
		rejected(t, err, []string{"LLM_API_KEY"}, "#/bundle/versions/0/files/1/content", "#/bundle/triggers/0/params/token")
	}
	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("result = ? AND resource_kind = ?", "denied", "task").Count(f.ctx)
	if err != nil || n < 2 {
		t.Fatalf("denied audit events = %d, %v", n, err)
	}
}
