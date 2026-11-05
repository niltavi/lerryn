package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Tasks is the task service the API calls. *service.Tasks implements it.
type Tasks interface {
	Apply(ctx context.Context, p *auth.Principal, in service.ApplyTaskInput) (service.ApplyResult, error)
	Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.TaskDetail, error)
	List(ctx context.Context, p *auth.Principal, in service.TaskListInput) (service.TaskPage, error)
	Archive(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.TaskDetail, error)
	SetActivation(ctx context.Context, p *auth.Principal, id uuid.UUID, active bool, seq *int) (service.TaskDetail, error)
	SetConsole(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.ConsoleOptions) (service.TaskDetail, error)
	Versions(ctx context.Context, p *auth.Principal, id uuid.UUID, limit *int, cursor string) (service.VersionPage, error)
	Version(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int) (service.VersionDetail, error)
	File(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int, path string) (service.FileContent, error)
	Diff(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int, base *int) (service.TaskDiff, error)
	Validate(ctx context.Context, p *auth.Principal, id uuid.UUID, seq int) (service.ValidationReport, error)
	// Portable task bundles (tech design 15.5).
	Export(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.ExportTaskInput) (service.TaskBundle, error)
	Import(ctx context.Context, p *auth.Principal, in service.ImportTaskInput) (service.ImportResult, error)
	// The project tool registry (tech design 7.8).
	ListTools(ctx context.Context, p *auth.Principal, projectID *uuid.UUID) ([]service.ToolDetail, error)
	RegisterTool(ctx context.Context, p *auth.Principal, projectID uuid.UUID, in service.RegisterToolInput) (service.ToolDetail, error)
	RemoveTool(ctx context.Context, p *auth.Principal, id uuid.UUID) error
}

// Runs is the run service the API calls. *service.Runs implements it.
type Runs interface {
	SetArchived(ctx context.Context, p *auth.Principal, id uuid.UUID, archived bool) (service.RunDetail, error)
	Start(ctx context.Context, p *auth.Principal, taskID uuid.UUID, in service.StartRunInput) (service.RunDetail, error)
	Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.RunDetail, error)
	Wait(ctx context.Context, p *auth.Principal, id uuid.UUID, wait time.Duration) (service.RunDetail, error)
	Cancel(ctx context.Context, p *auth.Principal, id uuid.UUID) (service.RunDetail, error)
	Logs(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.LogsInput) (service.LogPage, error)
	List(ctx context.Context, p *auth.Principal, in service.RunListInput) (service.RunPage, error)
	Rerun(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.RerunInput) (service.RunDetail, error)
	Events(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.EventsInput) (service.EventPage, error)
	FollowLogs(ctx context.Context, p *auth.Principal, id uuid.UUID, in service.FollowInput) (*service.LogFollower, error)
	DownloadLogs(ctx context.Context, p *auth.Principal, id uuid.UUID) (*service.LogDownload, error)
	Artifacts(ctx context.Context, p *auth.Principal, runID uuid.UUID, limit *int, cursor string) (service.ArtifactPage, error)
	OpenArtifact(ctx context.Context, p *auth.Principal, id uuid.UUID) (store.Artifact, io.ReadCloser, error)
	StreamUpdates(ctx context.Context, p *auth.Principal, project, task *uuid.UUID) (*service.UpdateStream, error)
	Overview(ctx context.Context, p *auth.Principal, project *uuid.UUID) (service.Overview, error)
	Calendar(ctx context.Context, p *auth.Principal, in service.CalendarInput) (service.Calendar, error)
}

var (
	errNoTasks = errors.New("httpapi: no task service configured")
	errNoRuns  = errors.New("httpapi: no run service configured")
)

// ApplyTask creates a task or a new version of it.
func (h *handler) ApplyTask(ctx context.Context, req ApplyTaskRequestObject) (ApplyTaskResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	in := service.ApplyTaskInput{ProjectID: req.ProjectID, Name: req.TaskName, DryRun: req.Params.DryRun != nil && *req.Params.DryRun}
	if m := req.Params.IfMatch; m != nil {
		// The contract allows the number with or without quotes and at
		// most 10 digits, so this parses unless it overflows an int.
		n, err := strconv.Atoi(strings.Trim(*m, `"`))
		if err != nil {
			return problemResponse[ApplyTaskdefaultApplicationProblemPlusJSONResponse](ctx, http.StatusBadRequest, ErrorCodeValidationFailed, "If-Match 不是有效的版本号"), nil
		}
		in.IfMatch = &n
	}
	if req.Body != nil {
		m, err := convertJSON[service.Manifest](req.Body.Manifest)
		if err != nil {
			return nil, fmt.Errorf("convert manifest: %w", err)
		}
		in.Manifest = m
		in.Files = make([]service.FileInput, len(req.Body.Files))
		for i, f := range req.Body.Files {
			in.Files[i] = service.FileInput{Path: f.Path, Content: []byte(f.Content), Executable: f.Executable != nil && *f.Executable}
		}
	}
	res, err := h.tasks.Apply(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[ApplyTaskdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ApplyTask200JSONResponse{
		DryRun:   res.DryRun,
		Created:  res.Created,
		Version:  res.Version,
		Warnings: toIssues(res.Warnings),
	}
	if res.Task != nil {
		t, err := h.toTask(*res.Task)
		if err != nil {
			return nil, err
		}
		out.Task = &t
	}
	if res.Diff != nil {
		d := toTaskDiff(*res.Diff)
		out.Diff = &d
	}
	return out, nil
}

// GetTask returns a task and its latest version.
func (h *handler) GetTask(ctx context.Context, req GetTaskRequestObject) (GetTaskResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	d, err := h.tasks.Get(ctx, auth.PrincipalFrom(ctx), req.TaskID)
	if err != nil {
		return errorResponse[GetTaskdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	t, err := h.toTask(d)
	if err != nil {
		return nil, err
	}
	return GetTask200JSONResponse(t), nil
}

// StartRun queues a run of a task version.
func (h *handler) StartRun(ctx context.Context, req StartRunRequestObject) (StartRunResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	var in service.StartRunInput
	if b := req.Body; b != nil {
		in.Version = b.Version
		if b.Params != nil {
			in.Params = *b.Params
			if in.Params == nil {
				in.Params = map[string]any{}
			}
		}
		in.Test = b.Test != nil && *b.Test
	}
	d, err := h.runs.Start(ctx, auth.PrincipalFrom(ctx), req.TaskID, in)
	if err != nil {
		return errorResponse[StartRundefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	run, err := h.toRun(d)
	if err != nil {
		return nil, err
	}
	return StartRun202JSONResponse(run), nil
}

// GetRun returns a run, optionally waiting for it to finish.
func (h *handler) GetRun(ctx context.Context, req GetRunRequestObject) (GetRunResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	p := auth.PrincipalFrom(ctx)
	var (
		d   service.RunDetail
		err error
	)
	if w := req.Params.Wait; w != nil && *w != 0 {
		// Clamped only so the conversion cannot overflow; the service
		// still rejects anything outside 0 to 60 seconds.
		d, err = h.runs.Wait(ctx, p, req.RunID, time.Duration(min(max(*w, -1), 3600))*time.Second)
	} else {
		d, err = h.runs.Get(ctx, p, req.RunID)
	}
	if err != nil {
		return errorResponse[GetRundefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	run, err := h.toRun(d)
	if err != nil {
		return nil, err
	}
	return GetRun200JSONResponse(run), nil
}

// CancelRun requests a run be stopped.
func (h *handler) CancelRun(ctx context.Context, req CancelRunRequestObject) (CancelRunResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	d, err := h.runs.Cancel(ctx, auth.PrincipalFrom(ctx), req.RunID)
	if err != nil {
		return errorResponse[CancelRundefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	run, err := h.toRun(d)
	if err != nil {
		return nil, err
	}
	return CancelRun202JSONResponse(run), nil
}

// ReadRunLogs returns a page of a run's log.
func (h *handler) ReadRunLogs(ctx context.Context, req ReadRunLogsRequestObject) (ReadRunLogsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	in := service.LogsInput{Limit: req.Params.Limit, Tail: req.Params.Tail}
	if req.Params.Cursor != nil {
		in.Cursor = *req.Params.Cursor
	}
	page, err := h.runs.Logs(ctx, auth.PrincipalFrom(ctx), req.RunID, in)
	if err != nil {
		return errorResponse[ReadRunLogsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	lines := make([]LogLine, len(page.Lines))
	for i, l := range page.Lines {
		lines[i] = LogLine{Seq: l.Seq, TS: l.TS, Stream: LogLineStream(l.Stream), Stage: optional(l.Stage), Text: l.Text}
	}
	return ReadRunLogs200JSONResponse{
		Lines:        lines,
		NextCursor:   page.NextCursor,
		Complete:     page.Complete,
		DroppedLines: page.DroppedLines,
	}, nil
}

func (h *handler) toTask(d service.TaskDetail) (Task, error) {
	latest, err := toTaskVersion(d.Version)
	if err != nil {
		return Task{}, fmt.Errorf("task %s: %w", d.Task.ID, err)
	}
	return Task{
		ID:            d.Task.ID,
		ProjectID:     d.Task.ProjectID,
		Name:          d.Task.Name,
		Description:   d.Task.Description,
		State:         taskState(d.Task),
		ActiveVersion: activeVersion(d.Task),
		LatestVersion: latest,
		Console:       TaskConsole{ManualRun: !d.Task.ConsoleHideManualRun},
		CreatedBy:     d.Task.CreatedBy,
		CreatedAt:     d.Task.CreatedAt,
		UpdatedAt:     d.Task.UpdatedAt,
		ArchivedAt:    optionalTime(d.Task.ArchivedAt),
		Links:         Links{Console: h.consoleLink("/tasks/" + d.Task.ID.String())},
	}, nil
}

func (h *handler) toRun(d service.RunDetail) (Run, error) {
	r := d.Run
	params := map[string]any{}
	if r.Params != "" {
		if err := json.Unmarshal([]byte(r.Params), &params); err != nil {
			return Run{}, fmt.Errorf("decode params of run %s: %w", r.ID, err)
		}
	}
	out := Run{
		ID:        r.ID,
		TaskID:    r.TaskID,
		ProjectID: r.ProjectID,
		Version:   r.VersionSeq,
		Status:    RunStatus(r.Status),
		Test:      r.IsTest,
		Trigger: RunTrigger{
			Kind:        TriggerKind(r.TriggerKind),
			TriggerID:   optionalUUID(r.TriggerID),
			ScheduledAt: optionalTime(r.ScheduledAt),
			UserID:      optionalUUID(r.CreatedBy),
			Client:      optional(r.Client),
		},
		Params: params,
		// The columns default to 1; a zero only comes from a model that
		// was never written.
		Attempt:         max(r.Attempt, 1),
		MaxAttempts:     max(r.MaxAttempts, 1),
		RetryOf:         optionalUUID(r.RetryOf),
		RerunOf:         optionalUUID(r.RerunOf),
		SkipReason:      optionalEnum[SkipReason](r.SkipReason),
		QueuedAt:        r.QueuedAt,
		ClaimedAt:       optionalTime(r.ClaimedAt),
		StartedAt:       optionalTime(r.StartedAt),
		FinishedAt:      optionalTime(r.FinishedAt),
		ExitCode:        r.ExitCode,
		Stages:          make([]RunStage, len(d.Stages)),
		CancelRequested: !r.CancelRequestedAt.IsZero(),
		Environment:     optional(r.EnvID),
		LogLines:        r.LogLines,
		DroppedLogLines: r.DroppedLines,
		LogsExpiredAt:   optionalTime(r.LogsExpiredAt),
		ArchivedAt:      optionalTime(r.ArchivedAt),
		Links:           Links{Console: h.consoleLink("/runs/" + r.ID.String())},
	}
	if !out.Trigger.Kind.Valid() {
		out.Trigger.Kind = TriggerKindManual
	}
	if !r.CancelRequestedAt.IsZero() {
		out.CancelledBy = &Canceller{
			UserID:      optionalUUID(r.CancelRequestedBy),
			Client:      optional(r.CancelClient),
			RequestedAt: r.CancelRequestedAt,
		}
	}
	if r.RunnerID != "" {
		// Runs claimed before runner_name was recorded name the runner by
		// its ID.
		name := r.RunnerName
		if name == "" {
			name = r.RunnerID
		}
		out.Runner = &RunnerRef{ID: r.RunnerID, Name: name}
	}
	if r.ErrorCode != "" {
		code := ErrorCode(r.ErrorCode)
		if !code.Valid() {
			// The coordinator records contract codes only; this keeps a
			// stray value from breaking clients that switch on the enum.
			code = ErrorCodeInternalError
		}
		out.Error = &RunError{Code: code, Stage: optional(r.ErrorStage), Message: r.ErrorMessage}
	}
	for i, s := range d.Stages {
		out.Stages[i] = RunStage{
			ID:         s.ID,
			Name:       s.Name,
			Declared:   s.Declared,
			After:      s.After,
			Status:     RunStageStatus(s.Status),
			StartedAt:  optionalTime(s.StartedAt),
			FinishedAt: optionalTime(s.FinishedAt),
			Message:    optional(s.Message),
		}
	}
	if res := d.Result; res != nil {
		out.Result = &RunResult{Status: RunResultStatus(res.Status), Summary: res.Summary, Data: rawJSON(res.Data)}
	}
	return out, nil
}

// rawJSON passes stored JSON through as it is, so large numbers keep
// their precision; empty and null become null.
func rawJSON(b []byte) *any {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	var v any = json.RawMessage(b)
	return &v
}

func optionalUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

// convertJSON converts between an API type and the service type with the
// same JSON shape, such as Manifest. The API schema is the definition of
// that shape, so converting through it keeps the two from drifting apart
// field by field.
func convertJSON[T any](v any) (T, error) {
	var out T
	b, err := json.Marshal(v)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(b, &out)
	return out, err
}
