package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// taskClient is an apiClient with the task and run services, plus the
// coordinator to play the runner.
type taskClient struct {
	*apiClient
	coord       *service.Coordinator
	db          *store.DB
	streamsDone chan struct{}
}

func newTaskClient(t *testing.T) *taskClient {
	t.Helper()
	return newTaskClientWith(t, false)
}

// newTaskClientWith builds the client; idempotent adds the idempotency
// record store.
func newTaskClientWith(t *testing.T, idempotent bool) *taskClient {
	t.Helper()
	db := storetest.Open(t)
	blobs, err := blob.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	authz := service.NewAuthorizer(db)
	runs := service.NewRuns(db, authz, nil).WithBlobs(blobs)
	done := make(chan struct{})
	opts := Options{
		PublicURL:   testOrigin,
		Version:     "dev",
		Accounts:    service.NewAccounts(db, nil),
		Tasks:       service.NewTasks(db, authz, blobs, nil),
		Runs:        runs,
		StreamsDone: done,
	}
	if idempotent {
		opts.Idempotency = service.NewIdempotency(db, nil)
	}
	return &taskClient{apiClient: &apiClient{t: t, h: New(opts)}, coord: service.NewCoordinator(db, blobs, runs, nil), db: db, streamsDone: done}
}

// agent registers a user and returns an access token with the given
// scopes and the user's personal project.
func (c *taskClient) agent(username string, scopes ...string) (string, uuid.UUID) {
	c.t.Helper()
	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": username, "password": "correct horse"})
	c.want(rec, http.StatusCreated, "")
	me := decode[Me](c.t, rec)
	cookie := sessionCookie(c.t, rec)
	if len(scopes) == 0 {
		scopes = []string{"read", "write", "execute"}
	}
	rec = c.call(http.MethodPost, "/api/v1/me/tokens", map[string]any{"name": "agent", "scopes": scopes},
		"Cookie", cookie, "Origin", testOrigin)
	c.want(rec, http.StatusCreated, "")
	return "Bearer " + decode[CreatedToken](c.t, rec).Token, me.PersonalProjectID
}

func (c *taskClient) as(token, method, path string, body any) *httptest.ResponseRecorder {
	c.t.Helper()
	return c.call(method, path, body, "Authorization", token)
}

func digestRequest() map[string]any {
	return map[string]any{
		"manifest": map[string]any{
			"description": "汇总",
			"runtime":     map[string]any{"language": "python", "version": "3.12"},
			"entrypoint":  []string{"python", "main.py"},
			"workflow": []map[string]string{
				{"title": "读取线索", "description": "读取 CRM 中的新线索。"},
				{"title": "生成摘要", "description": "汇总线索并保存摘要。"},
			},
			"params": map[string]any{"source": "crm"},
			"stages": []map[string]any{
				{"id": "fetch", "name": "获取"},
				{"id": "save", "name": "保存", "after": []string{"fetch"}},
			},
			"limits": map[string]any{"timeout": "5m", "cpu": 0.5, "memory": "256Mi"},
		},
		"files": []map[string]any{
			{"path": "main.py", "content": "print('hi')\n"},
			{"path": "run.sh", "content": "#!/bin/sh\n", "executable": true},
		},
	}
}

func TestTaskTitle(t *testing.T) {
	c := newTaskClient(t)
	token, project := c.agent("alice")
	path := "/api/v1/projects/" + project.String() + "/tasks/digest"
	body := digestRequest()
	manifest := body["manifest"].(map[string]any)
	manifest["title"] = "CRM 费用日报"
	rec := c.as(token, http.MethodPut, path, body)
	c.want(rec, http.StatusOK, "")
	task := appliedTask(t, rec)
	if task.Title == nil || *task.Title != "CRM 费用日报" || task.Name != "digest" {
		t.Fatalf("created task = %+v", task)
	}

	manifest["title"] = "GitHub 费用日报"
	rec = c.call(http.MethodPut, path, body, "Authorization", token, "If-Match", "1")
	c.want(rec, http.StatusOK, "")
	detailPath := "/api/v1/tasks/" + task.ID.String()
	rec = c.as(token, http.MethodGet, detailPath, nil)
	c.want(rec, http.StatusOK, "")
	detail := decode[Task](t, rec)
	if detail.Title == nil || *detail.Title != "GitHub 费用日报" ||
		detail.LatestVersion.Manifest.Title == nil || *detail.LatestVersion.Manifest.Title != *detail.Title {
		t.Fatalf("task detail = %+v", detail)
	}

	searchPath := "/api/v1/tasks?q=" + url.QueryEscape("github 费用")
	rec = c.as(token, http.MethodGet, searchPath, nil)
	c.want(rec, http.StatusOK, "")
	list := decode[TaskSummaryPage](t, rec)
	if len(list.Items) != 1 || list.Items[0].ID != task.ID || list.Items[0].Title == nil || *list.Items[0].Title != *detail.Title {
		t.Fatalf("title search = %+v", list)
	}

	delete(manifest, "title")
	rec = c.call(http.MethodPut, path, body, "Authorization", token, "If-Match", "2")
	c.want(rec, http.StatusOK, "")
	if cleared := appliedTask(t, rec); cleared.Title != nil || cleared.ID != task.ID {
		t.Fatalf("cleared task = %+v", cleared)
	}
	rec = c.as(token, http.MethodGet, searchPath, nil)
	c.want(rec, http.StatusOK, "")
	if list := decode[TaskSummaryPage](t, rec); len(list.Items) != 0 {
		t.Fatalf("title search after clearing = %+v", list)
	}
}

func TestTaskEndpoints(t *testing.T) {
	c := newTaskClient(t)
	alice, project := c.agent("alice")
	bob, _ := c.agent("bob")
	path := "/api/v1/projects/" + project.String() + "/tasks/digest"

	rec := c.as(alice, http.MethodPut, path, digestRequest())
	c.want(rec, http.StatusOK, "")
	task := appliedTask(t, rec)
	v := task.LatestVersion
	if task.Name != "digest" || task.ProjectID != project || task.Description != "汇总" || v.Seq != 1 {
		t.Fatalf("task = %+v", task)
	}
	if task.Links.Console != testOrigin+"/tasks/"+task.ID.String() {
		t.Errorf("links.console = %s", task.Links.Console)
	}
	if len(v.Files) != 2 || v.Files[0].Path != "main.py" || v.Files[0].Sha256 != blob.Sum([]byte("print('hi')\n")) || !v.Files[1].Executable {
		t.Fatalf("files = %+v", v.Files)
	}
	if v.Manifest.Limits == nil || *v.Manifest.Limits.CPU != 0.5 || v.Manifest.Stages == nil || len(*v.Manifest.Stages) != 2 {
		t.Fatalf("manifest = %+v", v.Manifest)
	}

	rec = c.call(http.MethodPut, path, digestRequest(), "Authorization", alice, "If-Match", "1")
	c.want(rec, http.StatusOK, "")
	if got := appliedTask(t, rec); got.ID != task.ID || got.LatestVersion.Seq != 2 {
		t.Fatalf("second version = %+v", got)
	}
	rec = c.as(alice, http.MethodGet, "/api/v1/tasks/"+task.ID.String(), nil)
	c.want(rec, http.StatusOK, "")
	got := decode[Task](t, rec)
	if got.LatestVersion.Seq != 2 {
		t.Fatalf("get = %+v", got)
	}
	workflow := got.LatestVersion.Manifest.Workflow
	if workflow == nil || len(*workflow) != 2 || (*workflow)[0].Title != "读取线索" ||
		(*workflow)[1].Description != "汇总线索并保存摘要。" {
		t.Fatalf("stored workflow = %+v", workflow)
	}

	// Invisible to others, and the same answer as for a missing task.
	c.want(c.as(bob, http.MethodGet, "/api/v1/tasks/"+task.ID.String(), nil), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(alice, http.MethodGet, "/api/v1/tasks/"+uuid.NewString(), nil), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(bob, http.MethodPut, path, digestRequest()), http.StatusNotFound, ErrorCodeNotFound)

	// Requests that break the contract are 400 and point into the body,
	// before any handler runs.
	bad := digestRequest()
	m := bad["manifest"].(map[string]any)
	m["entrypoint"] = []string{}
	m["surprise"] = true
	rec = c.as(alice, http.MethodPut, path, bad)
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	wantPointers(t, rec, "#/manifest/entrypoint", "#/manifest")

	// Content errors the contract cannot express are 422.
	bad = digestRequest()
	m = bad["manifest"].(map[string]any)
	m["stages"] = []map[string]any{{"id": "a", "name": "A", "after": []string{"b"}}, {"id": "b", "name": "B", "after": []string{"a"}}}
	bad["files"] = []map[string]any{{"path": "../main.py", "content": ""}}
	rec = c.as(alice, http.MethodPut, path, bad)
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	wantPointers(t, rec, "#/manifest/stages", "#/files/0/path")
	// A malformed task name is a parameter error.
	rec = c.as(alice, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/Bad_Name", digestRequest())
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	if errs := *decode[Problem](t, rec).Errors; len(errs) != 1 || errs[0].Parameter == nil || *errs[0].Parameter != "taskName" {
		t.Fatalf("errors = %+v", errs)
	}
	// A token without the write scope cannot submit.
	reader, _ := c.agent("reader", "read")
	c.want(c.as(reader, http.MethodPut, path, digestRequest()), http.StatusForbidden, ErrorCodeInsufficientScope)
}

func TestApplyRequiresWorkflow(t *testing.T) {
	c := newTaskClient(t)
	token, project := c.agent("alice")
	path := "/api/v1/projects/" + project.String() + "/tasks/digest"

	omitted := digestRequest()
	delete(omitted["manifest"].(map[string]any), "workflow")
	rec := c.as(token, http.MethodPut, path+"?dry_run=true", omitted)
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	wantPointers(t, rec, "#/manifest/workflow")

	blank := digestRequest()
	blank["manifest"].(map[string]any)["workflow"] = []map[string]string{{"title": "读取线索", "description": "　 "}}
	rec = c.as(token, http.MethodPut, path, blank)
	c.want(rec, http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	wantPointers(t, rec, "#/manifest/workflow/0/description")
}

func TestRunEndpoints(t *testing.T) {
	c := newTaskClient(t)
	alice, project := c.agent("alice")
	bob, _ := c.agent("bob")
	rec := c.as(alice, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/digest", digestRequest())
	c.want(rec, http.StatusOK, "")
	task := appliedTask(t, rec)
	runsPath := "/api/v1/tasks/" + task.ID.String() + "/runs"

	rec = c.as(alice, http.MethodPost, runsPath, map[string]any{"params": map[string]any{"source": "erp"}, "test": true})
	c.want(rec, http.StatusAccepted, "")
	run := decode[Run](t, rec)
	if run.Status != RunStatusQueued || run.Version != 1 || !run.Test || run.Error != nil || run.Result != nil || run.CancelRequested {
		t.Fatalf("run = %+v", run)
	}
	if run.Links.Console != testOrigin+"/runs/"+run.ID.String() || len(run.Stages) != 2 || run.Stages[0].Status != RunStageStatusPending {
		t.Fatalf("run = %+v", run)
	}
	// The graph comes from the run itself: every declared stage carries its
	// dependencies, as an array even when it has none.
	if !run.Stages[0].Declared || run.Stages[0].After == nil || len(run.Stages[0].After) != 0 ||
		!run.Stages[1].Declared || len(run.Stages[1].After) != 1 || run.Stages[1].After[0] != run.Stages[0].ID {
		t.Fatalf("stages = %+v", run.Stages)
	}
	if !strings.Contains(rec.Body.String(), `"after":[]`) {
		t.Fatalf("stage without dependencies must encode after as []: %s", rec.Body.String())
	}
	runPath := "/api/v1/runs/" + run.ID.String()

	// The manifest forbids overlap by default.
	rec = c.as(alice, http.MethodPost, runsPath, nil)
	c.want(rec, http.StatusConflict, ErrorCodeRunInProgress)
	if p := decode[Problem](t, rec); p.NextActions == nil || len(*p.NextActions) != 2 || p.Detail == nil || !strings.Contains(*p.Detail, run.ID.String()) {
		t.Fatalf("problem = %+v", p)
	}
	c.want(c.as(alice, http.MethodPost, runsPath, map[string]any{"version": 9}), http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	c.want(c.as(bob, http.MethodPost, runsPath, nil), http.StatusNotFound, ErrorCodeNotFound)

	rec = c.as(alice, http.MethodGet, runPath, nil)
	c.want(rec, http.StatusOK, "")
	c.want(c.as(alice, http.MethodGet, runPath+"?wait=61", nil), http.StatusBadRequest, ErrorCodeValidationFailed)
	c.want(c.as(bob, http.MethodGet, runPath, nil), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(alice, http.MethodGet, "/api/v1/runs/"+uuid.NewString(), nil), http.StatusNotFound, ErrorCodeNotFound)

	// Play the runner: claim, start, log, report, fail.
	ctx := context.Background()
	if err := c.coord.Register(ctx, runproto.Registration{ID: "runner-1", Slots: 1}); err != nil {
		t.Fatal(err)
	}
	assigned, err := c.coord.Claim(ctx, "runner-1", 1)
	if err != nil || len(assigned) != 1 || string(assigned[0].Params) != `{"source":"erp"}` || assigned[0].Manifest.MilliCPUs != 500 {
		t.Fatalf("claim = %+v, %v", assigned, err)
	}
	if err := c.coord.Started(ctx, "runner-1", run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := c.coord.AppendLogs(ctx, "runner-1", run.ID, []runproto.LogLine{
		{Seq: 1, TS: now, Stream: runproto.StreamSystem, Text: "preparing"},
		{Seq: 2, TS: now, Stream: runproto.StreamStdout, Stage: "fetch", Text: "fetched 3"},
		{Seq: 3, TS: now, Stream: runproto.StreamStderr, Stage: "save", Text: "Traceback"},
	}, runproto.Dropped{Lines: 4, Bytes: 40}); err != nil {
		t.Fatal(err)
	}
	if err := c.coord.AppendEvents(ctx, "runner-1", run.ID, []runproto.Event{
		{Seq: 1, TS: now, Kind: runproto.EventStageStart, Stage: "fetch"},
		{Seq: 2, TS: now, Kind: runproto.EventStageDone, Stage: "fetch", Message: "3 条"},
		{Seq: 3, TS: now, Kind: runproto.EventStageStart, Stage: "save"},
		{Seq: 4, TS: now, Kind: runproto.EventResult, Result: &runproto.Result{Status: "partial", Summary: "存了一半", Data: json.RawMessage(`{"saved":[1,2]}`)}},
	}); err != nil {
		t.Fatal(err)
	}

	// A waiting reader returns as soon as the run ends.
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, runPath+"?wait=30", nil)
		req.Header.Set("Authorization", alice)
		rec := httptest.NewRecorder()
		c.h.ServeHTTP(rec, req)
		done <- rec
	}()
	time.Sleep(50 * time.Millisecond)
	exit := 2
	if err := c.coord.Finish(ctx, "runner-1", run.ID, runproto.Finish{Status: runproto.StatusFailed, ExitCode: &exit, ErrorStage: "save"}); err != nil {
		t.Fatal(err)
	}
	select {
	case rec = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("wait did not return when the run ended")
	}
	c.want(rec, http.StatusOK, "")
	// Validate the waited response against the spec too.
	rec = c.as(alice, http.MethodGet, runPath+"?wait=60", nil)
	c.want(rec, http.StatusOK, "")
	got := decode[Run](t, rec)
	if got.Status != RunStatusFailed || got.ExitCode == nil || *got.ExitCode != 2 || got.FinishedAt == nil || got.StartedAt == nil {
		t.Fatalf("run = %+v", got)
	}
	if got.Error == nil || got.Error.Code != ErrorCodeScriptError || got.Error.Stage == nil || *got.Error.Stage != "save" || got.Error.Message == "" {
		t.Fatalf("error = %+v", got.Error)
	}
	if got.Stages[0].Status != RunStageStatusDone || got.Stages[0].Message == nil || *got.Stages[0].Message != "3 条" || got.Stages[1].Status != RunStageStatusRunning {
		t.Fatalf("stages = %+v", got.Stages)
	}
	if got.Result == nil || got.Result.Status != RunResultStatusPartial || got.Result.Summary != "存了一半" {
		t.Fatalf("result = %+v", got.Result)
	}
	if data, _ := json.Marshal(got.Result.Data); string(data) != `{"saved":[1,2]}` {
		t.Fatalf("result data = %s", data)
	}

	// Logs.
	logsPath := runPath + "/logs"
	rec = c.as(alice, http.MethodGet, logsPath, nil)
	c.want(rec, http.StatusOK, "")
	page := decode[LogPage](t, rec)
	if len(page.Lines) != 3 || !page.Complete || page.DroppedLines != 4 || page.Lines[0].Stream != LogLineStreamSystem || page.Lines[0].Stage != nil || *page.Lines[1].Stage != "fetch" {
		t.Fatalf("logs = %+v", page)
	}
	rec = c.as(alice, http.MethodGet, logsPath+"?limit=2", nil)
	c.want(rec, http.StatusOK, "")
	page = decode[LogPage](t, rec)
	if len(page.Lines) != 2 || page.Complete {
		t.Fatalf("first page = %+v", page)
	}
	rec = c.as(alice, http.MethodGet, logsPath+"?cursor="+page.NextCursor, nil)
	c.want(rec, http.StatusOK, "")
	if page = decode[LogPage](t, rec); len(page.Lines) != 1 || page.Lines[0].Seq != 3 || !page.Complete {
		t.Fatalf("second page = %+v", page)
	}
	rec = c.as(alice, http.MethodGet, logsPath+"?tail=1", nil)
	c.want(rec, http.StatusOK, "")
	if page = decode[LogPage](t, rec); len(page.Lines) != 1 || page.Lines[0].Text != "Traceback" {
		t.Fatalf("tail = %+v", page)
	}
	rec = c.as(alice, http.MethodGet, logsPath+"?tail=1&cursor="+page.NextCursor, nil)
	c.want(rec, http.StatusBadRequest, ErrorCodeValidationFailed)
	c.want(c.as(alice, http.MethodGet, logsPath+"?cursor=bogus", nil), http.StatusBadRequest, ErrorCodeValidationFailed)
	c.want(c.as(bob, http.MethodGet, logsPath, nil), http.StatusNotFound, ErrorCodeNotFound)

	// Cancel: an ended run stays as it is; a queued run is cancelled.
	rec = c.as(alice, http.MethodPost, runPath+"/cancel", nil)
	c.want(rec, http.StatusAccepted, "")
	if got := decode[Run](t, rec); got.Status != RunStatusFailed {
		t.Fatalf("cancel of an ended run = %s", got.Status)
	}
	rec = c.as(alice, http.MethodPost, runsPath, map[string]any{})
	c.want(rec, http.StatusAccepted, "")
	queued := decode[Run](t, rec)
	if queued.Test {
		t.Fatal("runs are not test runs by default")
	}
	c.want(c.as(bob, http.MethodPost, "/api/v1/runs/"+queued.ID.String()+"/cancel", nil), http.StatusNotFound, ErrorCodeNotFound)
	rec = c.as(alice, http.MethodPost, "/api/v1/runs/"+queued.ID.String()+"/cancel", nil)
	c.want(rec, http.StatusAccepted, "")
	if got := decode[Run](t, rec); got.Status != RunStatusCancelled || !got.CancelRequested || got.FinishedAt == nil {
		t.Fatalf("cancelled run = %+v", got)
	}

	// Starting and cancelling need the execute scope.
	reader, _ := c.agent("reader", "read", "write")
	c.want(c.as(reader, http.MethodPost, runsPath, nil), http.StatusForbidden, ErrorCodeInsufficientScope)
}

func TestTaskOperationsWithoutServices(t *testing.T) {
	c := newAPIClient(t)
	rec := c.call(http.MethodPost, "/api/v1/auth/register", map[string]any{"username": "alice", "password": "correct horse"})
	cookie := sessionCookie(t, rec)
	c.want(c.call(http.MethodGet, "/api/v1/tasks/"+uuid.NewString(), nil, "Cookie", cookie), http.StatusInternalServerError, ErrorCodeInternalError)
	c.want(c.call(http.MethodGet, "/api/v1/runs/"+uuid.NewString(), nil, "Cookie", cookie), http.StatusInternalServerError, ErrorCodeInternalError)
}

// wantPointers checks that a problem has a field error at each pointer.
func wantPointers(t *testing.T, rec *httptest.ResponseRecorder, want ...string) {
	t.Helper()
	pointers := map[string]bool{}
	if errs := decode[Problem](t, rec).Errors; errs != nil {
		for _, e := range *errs {
			if e.Pointer != nil {
				pointers[*e.Pointer] = true
			}
		}
	}
	for _, w := range want {
		if !pointers[w] {
			t.Errorf("no error at %s in %v", w, pointers)
		}
	}
}

// appliedTask decodes an applyTask response and returns the task in it.
func appliedTask(t *testing.T, rec *httptest.ResponseRecorder) Task {
	t.Helper()
	res := decode[ApplyTaskResult](t, rec)
	if res.DryRun || res.Task == nil {
		t.Fatalf("apply result = %+v", res)
	}
	if res.Version != res.Task.LatestVersion.Seq || res.Created != (res.Version == 1) {
		t.Fatalf("apply result version %d, created %v, task version %d", res.Version, res.Created, res.Task.LatestVersion.Seq)
	}
	return *res.Task
}
