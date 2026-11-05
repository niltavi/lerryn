package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// newM2Client is a taskClient that also remembers idempotency keys.
func newM2Client(t *testing.T) *taskClient {
	t.Helper()
	return newTaskClientWith(t, true)
}

// playRun claims a queued run as runner-1 and ends it with status.
func (c *taskClient) playRun(runID uuid.UUID, status string, lines int, events ...runproto.Event) {
	c.t.Helper()
	ctx := context.Background()
	if err := c.coord.Register(ctx, runproto.Registration{ID: "runner-1", Name: "builder-1", Slots: 8}); err != nil {
		c.t.Fatal(err)
	}
	assignments, err := c.coord.Claim(ctx, "runner-1", 8)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.coord.Started(ctx, "runner-1", runID, "", time.Now()); err != nil {
		c.t.Fatal(err)
	}
	if lines > 0 {
		batch := make([]runproto.LogLine, lines)
		for i := range batch {
			batch[i] = runproto.LogLine{Seq: int64(i + 1), TS: time.Now(), Stream: runproto.StreamStdout, Stage: "fetch", Text: "line"}
		}
		if err := c.coord.AppendLogs(ctx, "runner-1", runID, batch, runproto.Dropped{}); err != nil {
			c.t.Fatal(err)
		}
	}
	if len(events) > 0 {
		if err := c.coord.AppendEvents(ctx, "runner-1", runID, events); err != nil {
			c.t.Fatal(err)
		}
	}
	code := 0
	if status == runproto.StatusFailed {
		code = 2
	}
	for _, assignment := range assignments {
		if assignment.RunID == runID && assignment.HasState && status == runproto.StatusCompleted {
			snapshot, err := c.coord.GetState(ctx, "runner-1", runID)
			if err != nil {
				c.t.Fatal(err)
			}
			if err := c.coord.PutState(ctx, "runner-1", runID, snapshot); err != nil {
				c.t.Fatal(err)
			}
		}
	}
	if err := c.coord.Finish(ctx, "runner-1", runID, runproto.Finish{Status: status, ExitCode: &code, ErrorStage: "fetch"}); err != nil {
		c.t.Fatal(err)
	}
}

func (c *taskClient) apply(token string, project uuid.UUID, name string, body map[string]any, ifMatch string, query string) *httptest.ResponseRecorder {
	c.t.Helper()
	headers := []string{"Authorization", token}
	if ifMatch != "" {
		headers = append(headers, "If-Match", ifMatch)
	}
	return c.call(http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/"+name+query, body, headers...)
}

// TestAgentLifecycle walks the A01 path over HTTP: dry run, submit, test,
// validate, activate, with every response checked against the spec.
func TestAgentLifecycle(t *testing.T) {
	c := newM2Client(t)
	alice, project := c.agent("alice")
	req := digestRequest()

	rec := c.apply(alice, project, "digest", req, "", "?dry_run=true")
	c.want(rec, http.StatusOK, "")
	preview := decode[ApplyTaskResult](t, rec)
	if !preview.DryRun || !preview.Created || preview.Task != nil || preview.Diff != nil || len(preview.Warnings) == 0 {
		t.Fatalf("preview = %+v", preview)
	}
	rec = c.apply(alice, project, "digest", req, "", "")
	c.want(rec, http.StatusOK, "")
	task := appliedTask(t, rec)
	if task.LatestVersion.Client == nil || *task.LatestVersion.Client != "agent" {
		t.Fatalf("client = %v", task.LatestVersion.Client)
	}
	taskPath := "/api/v1/tasks/" + task.ID.String()

	// Activation before a test is refused with the next step.
	rec = c.as(alice, http.MethodPost, taskPath+"/activation", map[string]any{"active": true})
	c.want(rec, http.StatusConflict, ErrorCodeVersionNotTested)
	if p := decode[Problem](t, rec); p.NextActions == nil || (*p.NextActions)[0].Action != "run_start" {
		t.Fatalf("problem = %+v", p)
	}
	rec = c.as(alice, http.MethodPost, taskPath+"/versions/1/validate", nil)
	c.want(rec, http.StatusOK, "")
	if r := decode[ValidationReport](t, rec); !r.Valid || r.Activatable {
		t.Fatalf("report = %+v", r)
	}

	rec = c.as(alice, http.MethodPost, taskPath+"/runs", map[string]any{"test": true})
	c.want(rec, http.StatusAccepted, "")
	run := decode[Run](t, rec)
	c.playRun(run.ID, runproto.StatusCompleted, 3)
	rec = c.as(alice, http.MethodGet, "/api/v1/runs/"+run.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	if got := decode[Run](t, rec); got.Runner == nil || got.Runner.Name != "builder-1" || got.ClaimedAt == nil {
		t.Fatalf("runner = %+v, claimed %v", got.Runner, got.ClaimedAt)
	}

	rec = c.as(alice, http.MethodPost, taskPath+"/versions/1/validate", nil)
	c.want(rec, http.StatusOK, "")
	if r := decode[ValidationReport](t, rec); !r.Activatable {
		t.Fatalf("report after the test = %+v", r)
	}
	rec = c.as(alice, http.MethodPost, taskPath+"/activation", map[string]any{"active": true, "version": 1})
	c.want(rec, http.StatusOK, "")
	if got := decode[Task](t, rec); got.State != TaskStateActive || got.ActiveVersion == nil || *got.ActiveVersion != 1 || got.LatestVersion.TestedAt == nil {
		t.Fatalf("activated = %+v", got)
	}

	// Lists, versions, files and diffs.
	rec = c.as(alice, http.MethodGet, "/api/v1/tasks?name=digest&project_id="+project.String(), nil)
	c.want(rec, http.StatusOK, "")
	if page := decode[TaskSummaryPage](t, rec); len(page.Items) != 1 || page.Items[0].State != TaskStateActive || page.Items[0].LastRun != nil {
		t.Fatalf("tasks = %+v", page)
	}
	body := digestRequest()
	body["files"] = []map[string]any{{"path": "main.py", "content": "print('bye')\n"}}
	rec = c.apply(alice, project, "digest", body, `"1"`, "")
	c.want(rec, http.StatusOK, "")
	if res := decode[ApplyTaskResult](t, rec); res.Diff == nil || res.Diff.Base == nil || *res.Diff.Base != 1 || len(res.Diff.Files) != 2 {
		t.Fatalf("apply result diff = %+v", res.Diff)
	}
	rec = c.as(alice, http.MethodGet, taskPath+"/versions?limit=1", nil)
	c.want(rec, http.StatusOK, "")
	versions := decode[TaskVersionPage](t, rec)
	if len(versions.Items) != 1 || versions.Items[0].Seq != 2 || versions.NextCursor == nil || versions.Items[0].Active {
		t.Fatalf("versions = %+v", versions)
	}
	rec = c.as(alice, http.MethodGet, taskPath+"/versions?cursor="+*versions.NextCursor, nil)
	c.want(rec, http.StatusOK, "")
	if v := decode[TaskVersionPage](t, rec); len(v.Items) != 1 || !v.Items[0].Active || v.Items[0].FileCount != 2 {
		t.Fatalf("second page = %+v", v)
	}
	rec = c.as(alice, http.MethodGet, taskPath+"/versions/2", nil)
	c.want(rec, http.StatusOK, "")
	rec = c.as(alice, http.MethodGet, taskPath+"/versions/1/file?path=main.py", nil)
	c.want(rec, http.StatusOK, "")
	if f := decode[TaskFileContent](t, rec); f.Content != "print('hi')\n" {
		t.Fatalf("file = %+v", f)
	}
	c.want(c.as(alice, http.MethodGet, taskPath+"/versions/1/file?path=nope.py", nil), http.StatusNotFound, ErrorCodeNotFound)
	rec = c.as(alice, http.MethodGet, taskPath+"/versions/2/diff", nil)
	c.want(rec, http.StatusOK, "")
	if d := decode[TaskDiff](t, rec); d.Base == nil || *d.Base != 1 || d.ManifestPatch != nil {
		t.Fatalf("diff = %+v", d)
	}

	// The console option changes without a new version.
	rec = c.as(alice, http.MethodPut, taskPath+"/console", map[string]any{"manual_run": false})
	c.want(rec, http.StatusOK, "")
	if got := decode[Task](t, rec); got.Console.ManualRun || got.LatestVersion.Seq != 2 {
		t.Fatalf("console = %+v, latest = %d", got.Console, got.LatestVersion.Seq)
	}
	c.want(c.as(alice, http.MethodPut, taskPath+"/console", map[string]any{"manual_run": "no"}), http.StatusBadRequest, ErrorCodeValidationFailed)

	// Pause, archive, and the archived task refuses runs.
	c.want(c.as(alice, http.MethodPost, taskPath+"/activation", map[string]any{"active": false}), http.StatusOK, "")
	rec = c.as(alice, http.MethodDelete, taskPath, nil)
	c.want(rec, http.StatusOK, "")
	if got := decode[Task](t, rec); got.ArchivedAt == nil || got.State != TaskStateInactive {
		t.Fatalf("archived = %+v", got)
	}
	c.want(c.as(alice, http.MethodPost, taskPath+"/runs", nil), http.StatusConflict, ErrorCodeTaskArchived)

	// Strangers see none of it.
	bob, _ := c.agent("bob")
	for _, path := range []string{taskPath + "/versions", taskPath + "/versions/1", taskPath + "/versions/1/diff", taskPath + "/versions/1/file?path=main.py"} {
		c.want(c.as(bob, http.MethodGet, path, nil), http.StatusNotFound, ErrorCodeNotFound)
	}
	c.want(c.as(bob, http.MethodPost, taskPath+"/activation", map[string]any{"active": false}), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(bob, http.MethodGet, "/api/v1/tasks?project_id="+project.String(), nil), http.StatusNotFound, ErrorCodeNotFound)
}

// TestVersionConflict is A12 over HTTP: of two sessions editing the same
// version, the second is refused with the current version and its run
// keeps the version it started with.
func TestVersionConflict(t *testing.T) {
	c := newM2Client(t)
	alice, project := c.agent("alice")
	c.want(c.apply(alice, project, "digest", digestRequest(), "", ""), http.StatusOK, "")

	rec := c.apply(alice, project, "digest", digestRequest(), "", "")
	c.want(rec, http.StatusPreconditionRequired, ErrorCodePreconditionRequired)
	c.want(c.apply(alice, project, "digest", digestRequest(), "1", ""), http.StatusOK, "")
	rec = c.apply(alice, project, "digest", digestRequest(), "1", "")
	c.want(rec, http.StatusPreconditionFailed, ErrorCodeVersionConflict)
	p := decode[Problem](t, rec)
	if p.CurrentVersion == nil || *p.CurrentVersion != 2 || p.Detail == nil {
		t.Fatalf("conflict = %+v", p)
	}
	// A dry run checks the precondition too.
	c.want(c.apply(alice, project, "digest", digestRequest(), "1", "?dry_run=true"), http.StatusPreconditionFailed, ErrorCodeVersionConflict)
}

func TestIdempotentRequests(t *testing.T) {
	c := newM2Client(t)
	alice, project := c.agent("alice")
	bob, _ := c.agent("bob")
	rec := c.call(http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", digestRequest(),
		"Authorization", alice, "Idempotency-Key", "create-1")
	c.want(rec, http.StatusOK, "")
	task := appliedTask(t, rec)
	// Same key and request: the first response, and no second version.
	rec = c.call(http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", digestRequest(),
		"Authorization", alice, "Idempotency-Key", "create-1")
	c.want(rec, http.StatusOK, "")
	if rec.Header().Get(ReplayedHeader) != "true" || appliedTask(t, rec).LatestVersion.Seq != 1 {
		t.Fatalf("replay = %s %s", rec.Header(), rec.Body)
	}

	runs := "/api/v1/tasks/" + task.ID.String() + "/runs"
	start := func(token, key string, body any) *httptest.ResponseRecorder {
		return c.call(http.MethodPost, runs, body, "Authorization", token, "Idempotency-Key", key)
	}
	first := start(alice, "run-1", map[string]any{"test": true})
	c.want(first, http.StatusAccepted, "")
	again := start(alice, "run-1", map[string]any{"test": true})
	c.want(again, http.StatusAccepted, "")
	if decode[Run](t, first).ID != decode[Run](t, again).ID {
		t.Fatal("a retried start created a second run")
	}
	// The same request formatted differently is still the same request.
	reformatted := httptest.NewRequest(http.MethodPost, runs, strings.NewReader("{ \"test\" :true }"))
	reformatted.Header.Set("Content-Type", "application/json")
	reformatted.Header.Set("Authorization", alice)
	reformatted.Header.Set("Idempotency-Key", "run-1")
	rr := httptest.NewRecorder()
	c.h.ServeHTTP(rr, reformatted)
	if rr.Code != http.StatusAccepted || rr.Header().Get(ReplayedHeader) != "true" {
		t.Fatalf("reformatted retry = %d %s", rr.Code, rr.Body)
	}
	c.want(start(alice, "run-1", map[string]any{"test": false}), http.StatusConflict, ErrorCodeIdempotencyConflict)
	// Errors are remembered as well: the run is still active.
	busy := start(alice, "run-2", nil)
	c.want(busy, http.StatusConflict, ErrorCodeRunInProgress)
	c.want(start(alice, "run-2", nil), http.StatusConflict, ErrorCodeRunInProgress)
	// Another user's identical key is theirs; they cannot see the task.
	c.want(start(bob, "run-1", map[string]any{"test": true}), http.StatusNotFound, ErrorCodeNotFound)

	n, err := c.db.NewSelect().Table("runs").Count(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("runs = %d, %v", n, err)
	}
}

func TestRunHistoryEndpoints(t *testing.T) {
	c := newM2Client(t)
	alice, project := c.agent("alice")
	rec := c.apply(alice, project, "digest", digestRequest(), "", "")
	task := appliedTask(t, rec)
	runs := "/api/v1/tasks/" + task.ID.String() + "/runs"
	rec = c.as(alice, http.MethodPost, runs, map[string]any{"params": map[string]any{"source": "erp"}})
	c.want(rec, http.StatusAccepted, "")
	run := decode[Run](t, rec)
	c.playRun(run.ID, runproto.StatusFailed, 5,
		runproto.Event{Seq: 1, Kind: runproto.EventStageStart, Stage: "fetch"},
		runproto.Event{Seq: 2, Kind: runproto.EventStageFail, Stage: "fetch", Message: "timeout talking to crm"},
	)
	runPath := "/api/v1/runs/" + run.ID.String()

	// A11: the failure, its stage and log, then a linked rerun.
	rec = c.as(alice, http.MethodGet, "/api/v1/runs?status=failed&task_id="+task.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	page := decode[RunPage](t, rec)
	if len(page.Items) != 1 || page.Items[0].Error == nil || page.Items[0].Error.Code != ErrorCodeScriptError || page.Items[0].Task.Name != "digest" {
		t.Fatalf("runs = %+v", page)
	}
	rec = c.as(alice, http.MethodGet, runPath+"/events?kind=status&kind=stage.fail", nil)
	c.want(rec, http.StatusOK, "")
	events := decode[RunEventPage](t, rec)
	if !events.Complete || len(events.Events) != 5 || events.Events[4].Kind != RunEventKindStatus {
		t.Fatalf("events = %+v", events)
	}
	if data, _ := json.Marshal(events.Events[4].Data); string(data) != `{"from":"running","to":"failed"}` {
		t.Fatalf("last status data = %s", data)
	}
	rec = c.as(alice, http.MethodPost, runPath+"/rerun", map[string]any{"test": true})
	c.want(rec, http.StatusAccepted, "")
	rerun := decode[Run](t, rec)
	if rerun.RerunOf == nil || *rerun.RerunOf != run.ID || rerun.Params["source"] != "erp" || !rerun.Test {
		t.Fatalf("rerun = %+v", rerun)
	}
	c.want(c.as(alice, http.MethodPost, "/api/v1/runs/"+rerun.ID.String()+"/rerun", nil), http.StatusConflict, ErrorCodeRunInProgress)
	rec = c.as(alice, http.MethodGet, "/api/v1/runs?rerun_of="+run.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	if p := decode[RunPage](t, rec); len(p.Items) != 1 || p.Items[0].ID != rerun.ID || p.Items[0].CurrentStage != nil {
		t.Fatalf("reruns = %+v", p)
	}

	// The log as a download and as a stream that resumes after a line.
	rec = c.as(alice, http.MethodGet, runPath+"/logs/download", nil)
	c.want(rec, http.StatusOK, "")
	if n := strings.Count(rec.Body.String(), " stdout line\n"); n != 5 || !strings.Contains(rec.Header().Get("Content-Disposition"), run.ID.String()+".log") {
		t.Fatalf("download = %s %q", rec.Header(), rec.Body)
	}
	// The log is task output, so a browser must not run or load anything
	// from it, as with artifacts.
	if h := rec.Header(); h.Get("Content-Security-Policy") != "default-src 'none'; sandbox" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download headers = %s", h)
	}
	rec = c.call(http.MethodGet, runPath+"/logs/stream", nil, "Authorization", alice, "Last-Event-ID", "3")
	c.want(rec, http.StatusOK, "")
	frames := sseFrames(t, rec.Body.String())
	if len(frames) != 3 || frames[0].id != "4" || frames[1].id != "5" || frames[2].event != "end" {
		t.Fatalf("frames = %+v", frames)
	}
	var end LogStreamEnd
	if err := json.Unmarshal([]byte(frames[2].data), &end); err != nil || end.Status != RunStatusFailed || end.LastSeq != 5 {
		t.Fatalf("end = %+v, %v", end, err)
	}
	rec = c.as(alice, http.MethodGet, runPath+"/logs/stream?tail=1", nil)
	c.want(rec, http.StatusOK, "")
	if frames = sseFrames(t, rec.Body.String()); len(frames) != 2 || frames[0].id != "5" {
		t.Fatalf("tail frames = %+v", frames)
	}
	c.want(c.call(http.MethodGet, runPath+"/logs/stream", nil, "Authorization", alice, "Last-Event-ID", "9999999999999999999"),
		http.StatusBadRequest, ErrorCodeValidationFailed)

	// Artifacts.
	if err := c.coord.AddArtifact(context.Background(), "runner-1", rerun.ID, runproto.Artifact{}); err == nil {
		t.Fatal("an unclaimed run took an artifact")
	}
	c.playRunWithArtifact(rerun.ID, "out/report.json", `{"leads": 3}`)
	rec = c.as(alice, http.MethodGet, "/api/v1/runs/"+rerun.ID.String()+"/artifacts", nil)
	c.want(rec, http.StatusOK, "")
	arts := decode[ArtifactPage](t, rec)
	if len(arts.Items) != 1 || arts.Items[0].Preview != ArtifactPreviewJSON || arts.Items[0].Path != "out/report.json" {
		t.Fatalf("artifacts = %+v", arts)
	}
	download := strings.TrimPrefix(arts.Items[0].Links.Download, testOrigin)
	rec = c.as(alice, http.MethodGet, download+"?inline=true", nil)
	c.want(rec, http.StatusOK, "")
	if rec.Body.String() != `{"leads": 3}` || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline;") || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("download = %s %q", rec.Header(), rec.Body)
	}

	bob, _ := c.agent("bob")
	for _, path := range []string{runPath + "/events", runPath + "/logs/download", runPath + "/logs/stream", runPath + "/artifacts", download} {
		c.want(c.as(bob, http.MethodGet, path, nil), http.StatusNotFound, ErrorCodeNotFound)
	}
	c.want(c.as(bob, http.MethodPost, runPath+"/rerun", nil), http.StatusNotFound, ErrorCodeNotFound)

	// The overview sees the failure; the calendar sees both runs.
	rec = c.as(alice, http.MethodGet, "/api/v1/overview", nil)
	c.want(rec, http.StatusOK, "")
	o := decode[Overview](t, rec)
	if o.Counts.Tasks != 1 || len(o.Attention) != 1 || o.Attention[0].Kind != AttentionKindRunFailed || o.Attention[0].Link != testOrigin+"/runs/"+run.ID.String() {
		t.Fatalf("overview = %+v", o)
	}
	rec = c.as(alice, http.MethodGet, "/api/v1/overview/calendar", nil)
	c.want(rec, http.StatusOK, "")
	if cal := decode[Calendar](t, rec); len(cal.Items) != 1 || cal.Items[0].Kind != CalendarItemKindRun {
		t.Fatalf("calendar = %+v", cal)
	}
}

// playRunWithArtifact claims the run, uploads one artifact and completes it.
func (c *taskClient) playRunWithArtifact(runID uuid.UUID, path, content string) {
	c.t.Helper()
	ctx := context.Background()
	assignments, err := c.coord.Claim(ctx, "runner-1", 8)
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.coord.Started(ctx, "runner-1", runID, "", time.Now()); err != nil {
		c.t.Fatal(err)
	}
	if err := c.coord.AddArtifact(ctx, "runner-1", runID, runproto.Artifact{Path: path, Size: int64(len(content)), Content: strings.NewReader(content)}); err != nil {
		c.t.Fatal(err)
	}
	zero := 0
	for _, assignment := range assignments {
		if assignment.RunID == runID && assignment.HasState {
			snapshot, err := c.coord.GetState(ctx, "runner-1", runID)
			if err != nil {
				c.t.Fatal(err)
			}
			if err := c.coord.PutState(ctx, "runner-1", runID, snapshot); err != nil {
				c.t.Fatal(err)
			}
		}
	}
	if err := c.coord.Finish(ctx, "runner-1", runID, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero}); err != nil {
		c.t.Fatal(err)
	}
}

// TestEventStream reads streamEvents over a real connection: updates of
// visible runs arrive, others do not, and shutdown ends the stream.
func TestEventStream(t *testing.T) {
	c := newM2Client(t)
	alice, project := c.agent("alice")
	bob, bobProject := c.agent("bob")
	mine := appliedTask(t, c.apply(alice, project, "mine", digestRequest(), "", ""))
	theirs := appliedTask(t, c.apply(bob, bobProject, "theirs", digestRequest(), "", ""))

	srv := httptest.NewServer(c.h)
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/events/stream", nil)
	req.Header.Set("Authorization", alice)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header)
	}
	c.want(c.as(bob, http.MethodPost, "/api/v1/tasks/"+theirs.ID.String()+"/runs", nil), http.StatusAccepted, "")
	rec := c.as(alice, http.MethodPost, "/api/v1/tasks/"+mine.ID.String()+"/runs", nil)
	c.want(rec, http.StatusAccepted, "")
	run := decode[Run](t, rec)

	frames := make(chan sseFrame, 10)
	go readFrames(resp, frames)
	select {
	case f := <-frames:
		var u RunUpdate
		if err := json.Unmarshal([]byte(f.data), &u); err != nil || f.event != "run" || u.RunID != run.ID || u.Status != RunStatusQueued {
			t.Fatalf("first frame = %+v (%v)", f, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no update arrived")
	}
	close(c.streamsDone)
	select {
	case f, ok := <-frames:
		if ok {
			t.Fatalf("unexpected frame after shutdown: %+v", f)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the stream did not end on shutdown")
	}
}

type sseFrame struct{ id, event, data string }

func sseFrames(t *testing.T, body string) []sseFrame {
	t.Helper()
	var frames []sseFrame
	for block := range strings.SplitSeq(body, "\n\n") {
		var f sseFrame
		for line := range strings.SplitSeq(block, "\n") {
			switch {
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if f.event != "" {
			frames = append(frames, f)
		}
	}
	return frames
}

func readFrames(resp *http.Response, out chan<- sseFrame) {
	defer close(out)
	sc := bufio.NewScanner(resp.Body)
	var f sseFrame
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if f.event != "" {
				out <- f
			}
			f = sseFrame{}
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}
