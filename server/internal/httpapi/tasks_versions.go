package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// ListTasks lists the tasks the caller can view.
func (h *handler) ListTasks(ctx context.Context, req ListTasksRequestObject) (ListTasksResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	in := service.TaskListInput{ProjectID: req.Params.ProjectID, Limit: req.Params.Limit}
	if s := req.Params.State; s != nil {
		in.State = string(*s)
	}
	if n := req.Params.Name; n != nil {
		in.Name = *n
	}
	if q := req.Params.Q; q != nil {
		in.Query = *q
	}
	if a := req.Params.Archived; a != nil {
		in.Archived = *a
	}
	if c := req.Params.Cursor; c != nil {
		in.Cursor = *c
	}
	page, err := h.tasks.List(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[ListTasksdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]TaskSummary, len(page.Tasks))
	for i, s := range page.Tasks {
		items[i] = TaskSummary{
			ID:            s.Task.ID,
			ProjectID:     s.Task.ProjectID,
			Name:          s.Task.Name,
			Description:   s.Task.Description,
			State:         taskState(s.Task),
			ActiveVersion: activeVersion(s.Task),
			LatestVersion: s.Task.LatestSeq,
			NextRunAt:     optionalTime(s.NextRunAt),
			ActiveRuns:    s.ActiveRuns,
			UpdatedAt:     s.Task.UpdatedAt,
			Links:         Links{Console: h.consoleLink("/tasks/" + s.Task.ID.String())},
		}
		if s.LastRun != nil {
			b := toRunBrief(*s.LastRun)
			items[i].LastRun = &b
		}
	}
	return ListTasks200JSONResponse{Items: items, NextCursor: optional(page.NextCursor)}, nil
}

// ArchiveTask archives a task: its triggers stop and it can no longer run or change.
func (h *handler) ArchiveTask(ctx context.Context, req ArchiveTaskRequestObject) (ArchiveTaskResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	d, err := h.tasks.Archive(ctx, auth.PrincipalFrom(ctx), req.TaskID)
	if err != nil {
		return errorResponse[ArchiveTaskdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	t, err := h.toTask(d)
	if err != nil {
		return nil, err
	}
	return ArchiveTask200JSONResponse(t), nil
}

// SetTaskActivation activates a task version or pauses the task.
func (h *handler) SetTaskActivation(ctx context.Context, req SetTaskActivationRequestObject) (SetTaskActivationResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	var (
		active  bool
		version *int
	)
	if req.Body != nil {
		active, version = req.Body.Active, req.Body.Version
	}
	d, err := h.tasks.SetActivation(ctx, auth.PrincipalFrom(ctx), req.TaskID, active, version)
	if err != nil {
		return errorResponse[SetTaskActivationdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	t, err := h.toTask(d)
	if err != nil {
		return nil, err
	}
	return SetTaskActivation200JSONResponse(t), nil
}

// SetTaskConsole changes how the console shows a task.
func (h *handler) SetTaskConsole(ctx context.Context, req SetTaskConsoleRequestObject) (SetTaskConsoleResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	var in service.ConsoleOptions
	if req.Body != nil {
		in.ManualRun = req.Body.ManualRun
	}
	d, err := h.tasks.SetConsole(ctx, auth.PrincipalFrom(ctx), req.TaskID, in)
	if err != nil {
		return errorResponse[SetTaskConsoledefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	t, err := h.toTask(d)
	if err != nil {
		return nil, err
	}
	return SetTaskConsole200JSONResponse(t), nil
}

// ListTaskVersions lists the versions of a task, newest first.
func (h *handler) ListTaskVersions(ctx context.Context, req ListTaskVersionsRequestObject) (ListTaskVersionsResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	cursor := ""
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	page, err := h.tasks.Versions(ctx, auth.PrincipalFrom(ctx), req.TaskID, req.Params.Limit, cursor)
	if err != nil {
		return errorResponse[ListTaskVersionsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]TaskVersionSummary, len(page.Versions))
	for i, v := range page.Versions {
		rt, err := convertJSON[RuntimeSpec](v.Manifest.Runtime)
		if err != nil {
			return nil, fmt.Errorf("convert runtime of version %d: %w", v.Version.Seq, err)
		}
		items[i] = TaskVersionSummary{
			Seq:         v.Version.Seq,
			CreatedAt:   v.Version.CreatedAt,
			CreatedBy:   v.Version.CreatedBy,
			Client:      optional(v.Version.Client),
			TestedAt:    optionalTime(v.Version.TestedAt),
			Active:      v.Active,
			Description: v.Manifest.Description,
			Runtime:     rt,
			FileCount:   v.FileCount,
			TreeHash:    v.Version.TreeHash,
		}
	}
	return ListTaskVersions200JSONResponse{Items: items, NextCursor: optional(page.NextCursor)}, nil
}

// GetTaskVersion returns one version of a task.
func (h *handler) GetTaskVersion(ctx context.Context, req GetTaskVersionRequestObject) (GetTaskVersionResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	v, err := h.tasks.Version(ctx, auth.PrincipalFrom(ctx), req.TaskID, req.Seq)
	if err != nil {
		return errorResponse[GetTaskVersiondefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out, err := toTaskVersion(v)
	if err != nil {
		return nil, err
	}
	return GetTaskVersion200JSONResponse(out), nil
}

// GetTaskFile returns the content of one file of a task version.
func (h *handler) GetTaskFile(ctx context.Context, req GetTaskFileRequestObject) (GetTaskFileResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	f, err := h.tasks.File(ctx, auth.PrincipalFrom(ctx), req.TaskID, req.Seq, req.Params.Path)
	if err != nil {
		return errorResponse[GetTaskFiledefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetTaskFile200JSONResponse{
		Path: f.File.Path, Size: f.File.Size, Sha256: f.File.BlobHash, Executable: f.File.Executable, Content: f.Content,
	}, nil
}

// DiffTaskVersion returns the differences between two versions of a task.
func (h *handler) DiffTaskVersion(ctx context.Context, req DiffTaskVersionRequestObject) (DiffTaskVersionResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	d, err := h.tasks.Diff(ctx, auth.PrincipalFrom(ctx), req.TaskID, req.Seq, req.Params.Base)
	if err != nil {
		return errorResponse[DiffTaskVersiondefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return DiffTaskVersion200JSONResponse(toTaskDiff(d)), nil
}

// ValidateTaskVersion checks whether a version can run and be activated now.
func (h *handler) ValidateTaskVersion(ctx context.Context, req ValidateTaskVersionRequestObject) (ValidateTaskVersionResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	r, err := h.tasks.Validate(ctx, auth.PrincipalFrom(ctx), req.TaskID, req.Seq)
	if err != nil {
		return errorResponse[ValidateTaskVersiondefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return ValidateTaskVersion200JSONResponse{
		Seq:         r.Seq,
		Valid:       len(r.Errors) == 0,
		Activatable: r.Activatable,
		Errors:      toIssues(r.Errors),
		Warnings:    toIssues(r.Warnings),
	}, nil
}

func taskState(t store.Task) TaskState {
	if t.State == store.TaskActive {
		return TaskStateActive
	}
	return TaskStateInactive
}

func activeVersion(t store.Task) *int {
	if t.ActiveSeq == 0 {
		return nil
	}
	return &t.ActiveSeq
}

func toTaskVersion(d service.VersionDetail) (TaskVersion, error) {
	m, err := convertJSON[Manifest](d.Manifest)
	if err != nil {
		return TaskVersion{}, fmt.Errorf("convert manifest of version %s: %w", d.Version.ID, err)
	}
	files := make([]TaskFile, len(d.Files))
	for i, f := range d.Files {
		files[i] = TaskFile{Path: f.Path, Size: f.Size, Sha256: f.BlobHash, Executable: f.Executable}
	}
	v := d.Version
	return TaskVersion{
		Seq:       v.Seq,
		CreatedAt: v.CreatedAt,
		CreatedBy: v.CreatedBy,
		Client:    optional(v.Client),
		TestedAt:  optionalTime(v.TestedAt),
		TreeHash:  v.TreeHash,
		Manifest:  m,
		Files:     files,
	}, nil
}

func toTaskDiff(d service.TaskDiff) TaskDiff {
	files := make([]FileDiff, len(d.Files))
	for i, f := range d.Files {
		files[i] = FileDiff{
			Path: f.Path, Change: FileDiffChange(f.Change), Additions: f.Additions, Deletions: f.Deletions,
			Patch: optional(f.Patch),
		}
	}
	return TaskDiff{
		Base: d.Base, Target: d.Target, ManifestPatch: optional(d.ManifestPatch), Files: files, Truncated: d.Truncated,
	}
}

func toIssues(issues []service.Issue) []ValidationIssue {
	out := make([]ValidationIssue, len(issues))
	for i, is := range issues {
		code := ErrorCode(is.Code)
		if !code.Valid() {
			code = ErrorCodeValidationFailed
		}
		out[i] = ValidationIssue{Code: code, Pointer: optional(is.Pointer), Message: is.Message}
	}
	return out
}

func toRunBrief(r store.Run) RunBrief {
	b := RunBrief{
		ID:         r.ID,
		Status:     RunStatus(r.Status),
		Test:       r.IsTest,
		Version:    r.VersionSeq,
		QueuedAt:   r.QueuedAt,
		StartedAt:  optionalTime(r.StartedAt),
		FinishedAt: optionalTime(r.FinishedAt),
	}
	if r.ErrorCode != "" {
		code := ErrorCode(r.ErrorCode)
		if !code.Valid() {
			code = ErrorCodeInternalError
		}
		b.ErrorCode = &code
	}
	b.ResultStatus = resultStatus(r.Result)
	return b
}

// resultStatus reads the status of a stored result, nil without one.
func resultStatus(result string) *RunResultStatus {
	s, _ := resultFields(result)
	return s
}

func resultFields(result string) (*RunResultStatus, *string) {
	if result == "" {
		return nil, nil
	}
	var r struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal([]byte(result), &r); err != nil {
		return nil, nil
	}
	return optionalEnum[RunResultStatus](r.Status), &r.Summary
}

// lifecycleProblem maps the errors of the task lifecycle and of
// idempotency keys that errorResponse does not know to a problem.
func lifecycleProblem(ctx context.Context, err error) (Problem, bool) {
	var (
		status int
		code   ErrorCode
		detail string
		next   []NextAction
	)
	switch {
	case errors.Is(err, service.ErrVersionNotTested):
		status, code, detail = http.StatusConflict, ErrorCodeVersionNotTested, "这个版本还没有成功的测试运行，部署要求激活前测试"
		next = []NextAction{{Action: "run_start", Hint: "带 test 为 true 运行这个版本，确认结果后再激活"}}
	case errors.Is(err, service.ErrTaskArchived):
		status, code, detail = http.StatusConflict, ErrorCodeTaskArchived, "任务已归档，不能再运行或修改"
		next = []NextAction{{Action: "task_apply", Hint: "用同一个任务名重新提交，会建立一个新任务"}}
	case errors.Is(err, service.ErrManualRunDisabled):
		status, code, detail = http.StatusConflict, ErrorCodeManualRunDisabled, "这个任务关闭了控制台的手动执行"
		next = []NextAction{{Action: "setTaskConsole", Hint: "需要从控制台运行时，请 Agent 把 manual_run 设为 true"}}
	case errors.Is(err, service.ErrIdempotencyInProgress):
		status, code, detail = http.StatusConflict, ErrorCodeIdempotencyConflict, "使用这个幂等键的第一个请求还在处理中，稍后用同一个键重试"
	case errors.Is(err, service.ErrExpired):
		status, code, detail = http.StatusGone, ErrorCodeExpired, "内容已按部署的保留策略清理，运行记录仍在"
		next = []NextAction{{Action: "run_rerun", Hint: "需要这些内容时重新运行；同一个运行的日志或产物不会再出现，不必重试读取"}}
	default:
		return Problem{}, false
	}
	p := buildProblem(ctx, status, code, detail)
	if next != nil {
		p.NextActions = &next
	}
	return p, true
}
