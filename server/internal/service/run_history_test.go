package service_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestListRunsFilters(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	a := f.apply(alice, "a", allow).Task.ID
	b := f.apply(alice, "b", allow).Task.ID
	f.apply(bob, "c", allow)

	failed := f.start(alice, a)
	f.finishRun(failed.ID, runproto.StatusFailed, "")
	test := f.startTest(alice, a, 1)
	queued := f.start(alice, b)
	rerun, err := f.runs.Rerun(f.ctx, alice, failed.ID, service.RerunInput{})
	if err != nil {
		t.Fatal(err)
	}

	ids := func(in service.RunListInput) []uuid.UUID {
		t.Helper()
		page, err := f.runs.List(f.ctx, alice, in)
		if err != nil {
			t.Fatal(err)
		}
		var out []uuid.UUID
		for _, it := range page.Runs {
			out = append(out, it.Run.ID)
			if it.TaskName == "" {
				t.Errorf("run %s has no task name", it.Run.ID)
			}
		}
		return out
	}
	no, yes := false, true
	for _, tt := range []struct {
		name string
		in   service.RunListInput
		want []uuid.UUID
	}{
		{"all, newest first", service.RunListInput{}, []uuid.UUID{rerun.Run.ID, queued.ID, test.ID, failed.ID}},
		{"task", service.RunListInput{TaskID: &b}, []uuid.UUID{queued.ID}},
		{"statuses", service.RunListInput{Statuses: []string{store.RunFailed, store.RunTimedOut}}, []uuid.UUID{failed.ID}},
		{"tests only", service.RunListInput{Test: &yes}, []uuid.UUID{test.ID}},
		{"no tests", service.RunListInput{Test: &no, TaskID: &a}, []uuid.UUID{rerun.Run.ID, failed.ID}},
		{"rerun of", service.RunListInput{RerunOf: &failed.ID}, []uuid.UUID{rerun.Run.ID}},
		{"trigger", service.RunListInput{Trigger: store.TriggerCron}, nil},
		{"until", service.RunListInput{Until: &failed.QueuedAt}, nil},
		{"since", service.RunListInput{Since: &rerun.Run.QueuedAt}, []uuid.UUID{rerun.Run.ID}},
	} {
		if got := ids(tt.in); !equalIDs(got, tt.want) {
			t.Errorf("%s: got %v, want %v", tt.name, got, tt.want)
		}
	}

	// Pages continue without repeats.
	limit := 3
	page, err := f.runs.List(f.ctx, alice, service.RunListInput{Limit: &limit})
	if err != nil || len(page.Runs) != 3 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = f.runs.List(f.ctx, alice, service.RunListInput{Limit: &limit, Cursor: page.NextCursor})
	if err != nil || len(page.Runs) != 1 || page.Runs[0].Run.ID != failed.ID || page.NextCursor != "" {
		t.Fatalf("second page = %+v, %v", page, err)
	}
	// Bob sees his own project only, and no runs there.
	page, err = f.runs.List(f.ctx, bob, service.RunListInput{})
	if err != nil || len(page.Runs) != 0 {
		t.Fatalf("bob's runs = %+v, %v", page, err)
	}
	_, err = f.runs.List(f.ctx, bob, service.RunListInput{ProjectID: &alice.PersonalProjectID})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.List(f.ctx, alice, service.RunListInput{Statuses: []string{"done"}})
	if fields, _ := fieldErrors(t, err); fields["?status"] == "" {
		t.Fatalf("unknown status: %v", err)
	}
}

func equalIDs(a, b []uuid.UUID) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRerun(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	task := f.apply(alice, "digest", m)
	orig, err := f.runs.Start(f.ctx, alice, task.Task.ID, service.StartRunInput{Params: map[string]any{"big": json.Number("12345678901234567890")}, Test: true})
	if err != nil {
		t.Fatal(err)
	}

	_, err = f.runs.Rerun(f.ctx, alice, orig.Run.ID, service.RerunInput{})
	var rip *service.RunInProgressError
	if !errors.As(err, &rip) || rip.RunID != orig.Run.ID {
		t.Fatalf("rerun of an active run: %v", err)
	}
	f.finishRun(orig.Run.ID, runproto.StatusFailed, "")
	// The fix is a new version; the rerun uses it and keeps the params.
	f.apply(alice, "digest", m)
	rerun, err := f.runs.Rerun(f.ctx, alice, orig.Run.ID, service.RerunInput{})
	if err != nil {
		t.Fatal(err)
	}
	r := rerun.Run
	if r.RerunOf != orig.Run.ID || r.VersionSeq != 2 || !r.IsTest || r.Params != `{"big":12345678901234567890}` || r.TriggerKind != store.TriggerManual || r.Attempt != 1 {
		t.Fatalf("rerun = %+v", r)
	}
	f.finishRun(r.ID, runproto.StatusCompleted, "")
	no := false
	again, err := f.runs.Rerun(f.ctx, alice, orig.Run.ID, service.RerunInput{Version: &[]int{1}[0], Params: map[string]any{}, Test: &no})
	if err != nil || again.Run.VersionSeq != 1 || again.Run.Params != "{}" || again.Run.IsTest {
		t.Fatalf("rerun with overrides = %+v, %v", again.Run, err)
	}
	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ?", "run.rerun").Count(f.ctx)
	if err != nil || n != 2 {
		t.Fatalf("run.rerun audit events = %d, %v", n, err)
	}
	viewer := f.user("viewer")
	f.grant(alice.PersonalProjectID, viewer, service.RoleViewer)
	_, err = f.runs.Rerun(f.ctx, viewer, orig.Run.ID, service.RerunInput{})
	wantErr(t, err, service.ErrForbidden)
	_, err = f.runs.Rerun(f.ctx, f.user("bob"), orig.Run.ID, service.RerunInput{})
	wantErr(t, err, service.ErrNotFound)
}

func TestRunEvents(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("r1")
	f.claim(runner, 1)
	if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	// Repeating Started records nothing new.
	if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{
		{Seq: 1, Kind: runproto.EventStageStart, Stage: "fetch"},
		{Seq: 2, Kind: runproto.EventStageDone, Stage: "fetch"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(1, 2, runproto.StreamStdout), runproto.Dropped{Lines: 5, Bytes: 50}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AddArtifact(f.ctx, runner, run.ID, runproto.Artifact{Path: "out.txt", Size: 2, Content: strings.NewReader("hi")}); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{
		{Seq: 3, Kind: runproto.EventResult, Result: &runproto.Result{Status: "ok", Summary: "好"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Before the end the page is not complete.
	page, err := f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{})
	if err != nil || page.Complete {
		t.Fatalf("page = %+v, %v", page, err)
	}
	zero := 0
	f.putEmptyState(runner, run.ID)
	if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}

	page, err = f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{})
	if err != nil || !page.Complete {
		t.Fatalf("page = %+v, %v", page, err)
	}
	var got []string
	for i, e := range page.Events {
		if e.Seq != int64(i+1) {
			t.Errorf("event %d has seq %d", i, e.Seq)
		}
		label := e.Kind
		if e.Kind == store.EventStatus {
			label += " " + e.Payload
		}
		got = append(got, label)
	}
	want := []string{
		`status {"to":"queued"}`, `status {"from":"queued","to":"preparing"}`, `status {"from":"preparing","to":"running"}`,
		"stage.start", "stage.done", "log.truncated", "artifact", "result", `status {"from":"running","to":"completed"}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	// Paging and the kind filter.
	limit := 4
	first, err := f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{Limit: &limit})
	if err != nil || len(first.Events) != 4 || first.Complete {
		t.Fatalf("first page = %+v, %v", first, err)
	}
	rest, err := f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{Cursor: first.NextCursor, Limit: &limit})
	if err != nil || len(rest.Events) != 4 || rest.Events[0].Seq != 5 {
		t.Fatalf("second page = %+v, %v", rest, err)
	}
	status, err := f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{Kinds: []string{store.EventStatus}})
	if err != nil || len(status.Events) != 4 || !status.Complete {
		t.Fatalf("status events = %+v, %v", status, err)
	}
	_, err = f.runs.Events(f.ctx, f.user("bob"), run.ID, service.EventsInput{})
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{Kinds: []string{"bogus"}})
	if fields, _ := fieldErrors(t, err); fields["?kind"] == "" {
		t.Fatalf("unknown kind: %v", err)
	}

	// The run records who claimed it.
	d := f.get(alice, run.ID)
	if d.Run.RunnerName != "r1.local" || d.Run.ClaimedAt.IsZero() {
		t.Fatalf("claim fields = %q %v", d.Run.RunnerName, d.Run.ClaimedAt)
	}
}

func TestFollowLogs(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("r1")
	f.claim(runner, 1)
	if err := f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(1, 5, runproto.StreamStdout), runproto.Dropped{}); err != nil {
		t.Fatal(err)
	}

	tail := 2
	fl, err := f.runs.FollowLogs(f.ctx, alice, run.ID, service.FollowInput{Tail: &tail})
	if err != nil {
		t.Fatal(err)
	}
	b, err := fl.Next(f.ctx, time.Second)
	if err != nil || len(b.Lines) != 2 || b.Lines[0].Seq != 4 || b.End != nil {
		t.Fatalf("tail batch = %+v, %v", b, err)
	}

	// A waiting follower wakes on the next append, without polling.
	got := make(chan service.LogBatch, 1)
	go func() {
		b, err := fl.Next(context.Background(), 30*time.Second)
		if err != nil {
			t.Error(err)
		}
		got <- b
	}()
	time.Sleep(50 * time.Millisecond)
	if err := f.coord.AppendLogs(f.ctx, runner, run.ID, f.logLines(6, 1, runproto.StreamStderr), runproto.Dropped{Lines: 3}); err != nil {
		t.Fatal(err)
	}
	select {
	case b = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the follower did not wake on the append")
	}
	if len(b.Lines) != 1 || b.Lines[0].Seq != 6 || b.DroppedLines != 3 {
		t.Fatalf("woken batch = %+v", b)
	}
	// An idle follower returns an empty batch after the wait.
	if b, err = fl.Next(f.ctx, 20*time.Millisecond); err != nil || len(b.Lines) != 0 || b.End != nil || b.DroppedLines != 0 {
		t.Fatalf("idle batch = %+v, %v", b, err)
	}

	zero := 0
	if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	f.putEmptyState(runner, run.ID)
	if err := f.coord.Finish(f.ctx, runner, run.ID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		t.Fatal(err)
	}
	if b, err = fl.Next(f.ctx, time.Second); err != nil || b.End == nil || b.End.LastSeq != 6 || b.End.Status != store.RunCompleted || b.End.DroppedLines != 3 {
		t.Fatalf("end batch = %+v, %v", b, err)
	}

	// Resuming after line 3 reads from line 4 and has seen the truncation.
	last := int64(3)
	resumed, err := f.runs.FollowLogs(f.ctx, alice, run.ID, service.FollowInput{LastEventID: &last})
	if err != nil {
		t.Fatal(err)
	}
	b, err = resumed.Next(f.ctx, time.Second)
	if err != nil || len(b.Lines) != 3 || b.Lines[0].Seq != 4 || b.End == nil || b.DroppedLines != 0 {
		t.Fatalf("resumed batch = %+v, %v", b, err)
	}

	_, err = f.runs.FollowLogs(f.ctx, f.user("bob"), run.ID, service.FollowInput{})
	wantErr(t, err, service.ErrNotFound)

	d, err := f.runs.DownloadLogs(f.ctx, alice, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := d.WriteTo(f.ctx, &buf); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 6 || !strings.HasSuffix(lines[0], " stdout line 1") || !strings.HasSuffix(lines[5], " stderr line 6") {
		t.Fatalf("download:\n%s", buf.String())
	}
	if _, err := time.Parse(time.RFC3339, strings.Fields(lines[0])[0]); err != nil {
		t.Fatalf("line does not start with a time: %q", lines[0])
	}
}

func TestArtifacts(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	f.runs.WithBlobs(f.blobs)
	task := f.apply(alice, "digest", basicManifest())
	run := f.start(alice, task.Task.ID)
	runner := f.runner("r1")
	f.claim(runner, 1)

	add := func(path, content string) error {
		return f.coord.AddArtifact(f.ctx, runner, run.ID, runproto.Artifact{Path: path, Size: int64(len(content)), Content: strings.NewReader(content)})
	}
	for path, content := range map[string]string{
		"report.json": `{"n": 1}`, "notes/readme.md": "# 说明\n", "page.html": "<script>alert(1)</script>", "data.bin": "\x00\x01\x02",
	} {
		if err := add(path, content); err != nil {
			t.Fatalf("add %s: %v", path, err)
		}
	}
	// Resending replaces; bad paths and sizes break the protocol.
	if err := add("report.json", `{"n": 2}`); err != nil {
		t.Fatal(err)
	}
	wantErr(t, add("../escape", "x"), service.ErrProtocol)
	wantErr(t, f.coord.AddArtifact(f.ctx, runner, run.ID, runproto.Artifact{Path: "short.txt", Size: 10, Content: strings.NewReader("abc")}), service.ErrProtocol)
	wantErr(t, f.coord.AddArtifact(f.ctx, "other", run.ID, runproto.Artifact{Path: "x.txt", Size: 1, Content: strings.NewReader("x")}), runproto.ErrLeaseLost)

	page, err := f.runs.Artifacts(f.ctx, alice, run.ID, nil, "")
	if err != nil || len(page.Artifacts) != 4 {
		t.Fatalf("artifacts = %+v, %v", page, err)
	}
	byPath := map[string]store.Artifact{}
	var order []string
	for _, a := range page.Artifacts {
		byPath[a.Path] = a
		order = append(order, a.Path)
	}
	if strings.Join(order, ",") != "data.bin,notes/readme.md,page.html,report.json" {
		t.Fatalf("order = %v", order)
	}
	for path, want := range map[string]string{
		"report.json": service.PreviewJSON, "notes/readme.md": service.PreviewText, "page.html": service.PreviewNone, "data.bin": service.PreviewNone,
	} {
		if got := service.ArtifactPreview(byPath[path].ContentType); got != want {
			t.Errorf("%s (%s) preview = %s, want %s", path, byPath[path].ContentType, got, want)
		}
	}
	a, content, err := f.runs.OpenArtifact(f.ctx, alice, byPath["report.json"].ID)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(content)
	_ = content.Close()
	if string(body) != `{"n": 2}` || a.Size != 8 {
		t.Fatalf("content = %q, artifact %+v", body, a)
	}
	_, _, err = f.runs.OpenArtifact(f.ctx, bob, a.ID)
	wantErr(t, err, service.ErrNotFound)
	_, err = f.runs.Artifacts(f.ctx, bob, run.ID, nil, "")
	wantErr(t, err, service.ErrNotFound)

	limit := 3
	page, err = f.runs.Artifacts(f.ctx, alice, run.ID, &limit, "")
	if err != nil || len(page.Artifacts) != 3 || page.NextCursor == "" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	page, err = f.runs.Artifacts(f.ctx, alice, run.ID, &limit, page.NextCursor)
	if err != nil || len(page.Artifacts) != 1 || page.Artifacts[0].Path != "report.json" {
		t.Fatalf("second page = %+v, %v", page, err)
	}

	// Retention leaves the row and refuses the content.
	if _, err := f.db.NewUpdate().Model((*store.Artifact)(nil)).Set("expired_at = ?", store.Now()).Where("id = ?", a.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, _, err = f.runs.OpenArtifact(f.ctx, alice, a.ID)
	wantErr(t, err, service.ErrExpired)
	if errors.Is(err, service.ErrNotFound) {
		t.Fatal("an expired artifact reads as missing; it should read as expired")
	}
}

func TestOverviewAndCalendar(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	allow.Stages = []service.StageSpec{{ID: "fetch", Name: "获取数据"}}
	broken := f.apply(alice, "broken", allow).Task.ID
	healthy := f.apply(alice, "healthy", allow).Task.ID
	scheduled := f.apply(alice, "scheduled", allow).Task.ID

	f.finishRun(f.start(alice, broken).ID, runproto.StatusFailed, "")
	f.finishRun(f.start(alice, healthy).ID, runproto.StatusFailed, "")
	f.finishRun(f.start(alice, healthy).ID, runproto.StatusCompleted, "")
	// A failed test run does not need attention.
	f.finishRun(f.startTest(alice, scheduled, 1).ID, runproto.StatusFailed, "")
	running := f.start(alice, healthy)
	runner := f.runner("busy")
	f.claim(runner, 1)
	if err := f.coord.Started(f.ctx, runner, running.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := f.coord.AppendEvents(f.ctx, runner, running.ID, []runproto.Event{{Seq: 1, Kind: runproto.EventStageStart, Stage: "fetch"}}); err != nil {
		t.Fatal(err)
	}
	// The scheduled task is active with a cron trigger.
	f.finishRun(f.startTest(alice, scheduled, 1).ID, runproto.StatusCompleted, "")
	if _, err := f.tasks.SetActivation(f.ctx, alice, scheduled, true, nil); err != nil {
		t.Fatal(err)
	}
	queued := f.start(alice, scheduled)
	now := store.Now()
	next := now.Truncate(time.Hour).Add(time.Hour)
	trigger := store.Trigger{ID: store.NewID(), TaskID: scheduled, Name: "hourly", Type: store.TriggerTypeCron, Enabled: true,
		Schedule: "0 * * * *", Timezone: "UTC", NextFireAt: next, CreatedBy: alice.UserID, CreatedAt: now, UpdatedAt: now}
	if _, err := f.db.NewInsert().Model(&trigger).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Alice has a pending credential request.
	req := store.CredentialRequest{ID: store.NewID(), UserID: alice.UserID, TaskID: broken, Name: "LLM_API_KEY", Purpose: "调用模型",
		Fields: []store.CredentialField{{Name: "LLM_API_KEY", Secret: true}}, Status: store.CredentialRequestPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if _, err := f.db.NewInsert().Model(&req).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}

	o, err := f.runs.Overview(f.ctx, alice, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := o.Counts; c.Tasks != 3 || c.ActiveTasks != 1 || c.Running != 1 || c.Queued != 1 || c.Attention != 2 {
		t.Fatalf("counts = %+v", c)
	}
	kinds := map[string]service.AttentionItem{}
	for _, a := range o.Attention {
		kinds[a.Kind] = a
	}
	if a := kinds[service.AttentionRunFailed]; a.Task == nil || a.Task.ID != broken || a.Stage != "获取数据" || a.Summary == "" || a.Path == "" {
		t.Fatalf("failure item = %+v", a)
	}
	if a := kinds[service.AttentionCredentialRequest]; a.CredentialRequestID != req.ID || a.Task == nil || a.Task.Name != "broken" || a.Path != "/c/"+req.ID.String() {
		t.Fatalf("request item = %+v", a)
	}
	if len(o.Active) != 2 || o.Active[0].Run.ID != running.ID || o.Active[0].CurrentStage != "获取数据" || o.Active[1].Run.ID != queued.ID {
		t.Fatalf("active = %+v", o.Active)
	}
	if len(o.Upcoming) != 1 || o.Upcoming[0].Task.ID != scheduled || !o.Upcoming[0].At.Equal(next) || o.Upcoming[0].ScheduleText != "每小时的第 0 分钟（UTC）" {
		t.Fatalf("upcoming = %+v", o.Upcoming)
	}
	// The task list shows the next fire of the active task only.
	page, _ := f.tasks.List(f.ctx, alice, service.TaskListInput{Name: "scheduled"})
	if len(page.Tasks) != 1 || !page.Tasks[0].NextRunAt.Equal(next) {
		t.Fatalf("next run = %+v", page.Tasks)
	}

	// Bob sees nothing of it, not even the request.
	bob := f.user("bob")
	if o, err := f.runs.Overview(f.ctx, bob, nil); err != nil || o.Counts.Tasks != 0 || len(o.Attention) != 0 || len(o.Active) != 0 {
		t.Fatalf("bob's overview = %+v, %v", o, err)
	}
	_, err = f.runs.Overview(f.ctx, bob, &alice.PersonalProjectID)
	wantErr(t, err, service.ErrNotFound)

	// The calendar has today's non-test runs and every planned fire still
	// ahead in the window: the next three hours.
	from, to := now.Add(-time.Hour), now.Add(3*time.Hour)
	cal, err := f.runs.Calendar(f.ctx, alice, service.CalendarInput{From: &from, To: &to})
	if err != nil {
		t.Fatal(err)
	}
	var runs, planned int
	for _, it := range cal.Items {
		if it.RunID == uuid.Nil {
			if want := next.Add(time.Duration(planned) * time.Hour); !it.Start.Equal(want) || it.Task.ID != scheduled ||
				it.TriggerID != trigger.ID || it.ScheduleText != "每小时的第 0 分钟（UTC）" {
				t.Errorf("planned item %d = %+v, want at %v", planned, it, want)
			}
			planned++
		} else {
			runs++
		}
	}
	if runs != 5 || planned != 3 || cal.Truncated {
		t.Fatalf("calendar has %d runs and %d planned: %+v", runs, planned, cal.Items)
	}
	only, err := f.runs.Calendar(f.ctx, alice, service.CalendarInput{TaskID: &broken, From: &from, To: &to})
	if err != nil || len(only.Items) != 1 {
		t.Fatalf("task calendar = %+v, %v", only, err)
	}
	long := from.Add(32 * 24 * time.Hour)
	_, err = f.runs.Calendar(f.ctx, alice, service.CalendarInput{From: &from, To: &long})
	if fields, _ := fieldErrors(t, err); fields["?to"] == "" {
		t.Fatalf("long window: %v", err)
	}
	def, err := f.runs.Calendar(f.ctx, alice, service.CalendarInput{})
	if err != nil || def.To.Sub(def.From) != 24*time.Hour || !def.From.Equal(now.Truncate(24*time.Hour)) {
		t.Fatalf("default window = %v to %v, %v", def.From, def.To, err)
	}
}

func TestIdempotency(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	idem := service.NewIdempotency(f.db, nil)

	c, err := idem.Begin(f.ctx, alice, "k1", "startRun", "h1")
	if err != nil || c.Replay != nil {
		t.Fatalf("first = %+v, %v", c, err)
	}
	_, err = idem.Begin(f.ctx, alice, "k1", "startRun", "h1")
	wantErr(t, err, service.ErrIdempotencyInProgress)
	if err := idem.Complete(f.ctx, c, 202, `{"id":"x"}`); err != nil {
		t.Fatal(err)
	}
	again, err := idem.Begin(f.ctx, alice, "k1", "startRun", "h1")
	if err != nil || again.Replay == nil || again.Replay.Status != 202 || again.Replay.Body != `{"id":"x"}` {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	_, err = idem.Begin(f.ctx, alice, "k1", "startRun", "h2")
	wantErr(t, err, service.ErrIdempotencyConflict)
	_, err = idem.Begin(f.ctx, alice, "k1", "applyTask", "h1")
	wantErr(t, err, service.ErrIdempotencyConflict)
	// Keys are per user.
	if c, err := idem.Begin(f.ctx, bob, "k1", "startRun", "h2"); err != nil || c.Replay != nil {
		t.Fatalf("bob's key = %+v, %v", c, err)
	}

	// A released key starts over; an expired one too.
	c, err = idem.Begin(f.ctx, alice, "k2", "startRun", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if err := idem.Release(f.ctx, c); err != nil {
		t.Fatal(err)
	}
	if c, err = idem.Begin(f.ctx, alice, "k2", "startRun", "h3"); err != nil || c.Replay != nil {
		t.Fatalf("after release = %+v, %v", c, err)
	}
	if err := idem.Complete(f.ctx, c, 200, "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.NewUpdate().Model((*store.IdempotencyRecord)(nil)).Set("expires_at = ?", store.Now().Add(-time.Second)).
		Where("key = ?", "k2").Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	if c, err = idem.Begin(f.ctx, alice, "k2", "startRun", "h4"); err != nil || c.Replay != nil {
		t.Fatalf("after expiry = %+v, %v", c, err)
	}
}

func TestUpdateStream(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	allow := basicManifest()
	allow.Concurrency = service.ConcurrencyAllow
	mine := f.apply(alice, "mine", allow).Task.ID
	theirs := f.apply(bob, "theirs", allow).Task.ID

	s, err := f.runs.StreamUpdates(f.ctx, alice, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	f.start(bob, theirs)
	run := f.start(alice, mine)
	u, resync, err := s.Next(f.ctx, 5*time.Second)
	if err != nil || resync || u == nil || u.RunID != run.ID || u.Status != store.RunQueued || u.ProjectID != alice.PersonalProjectID {
		t.Fatalf("update = %+v, %v, %v", u, resync, err)
	}
	runner := f.runner("r1")
	f.claim(runner, 64)
	for {
		u, _, err = s.Next(f.ctx, 5*time.Second)
		if err != nil || u == nil {
			t.Fatalf("no preparing update: %v", err)
		}
		if u.RunID == run.ID && u.Status == store.RunPreparing {
			break
		}
		if u.ProjectID != alice.PersonalProjectID {
			t.Fatalf("alice saw bob's run: %+v", u)
		}
	}
	if u, _, err = s.Next(f.ctx, 20*time.Millisecond); u != nil || err != nil {
		t.Fatalf("idle stream = %+v, %v", u, err)
	}
	_, err = f.runs.StreamUpdates(f.ctx, alice, nil, &theirs)
	wantErr(t, err, service.ErrNotFound)
}
