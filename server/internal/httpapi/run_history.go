package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// sseHeartbeat is how often an idle event stream sends a comment, so
// proxies keep the connection and clients notice a dead one.
const sseHeartbeat = 15 * time.Second

// ListRuns lists the runs the caller can view.
func (h *handler) ListRuns(ctx context.Context, req ListRunsRequestObject) (ListRunsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	pr := req.Params
	in := service.RunListInput{
		ProjectID: pr.ProjectID, TaskID: pr.TaskID, Test: pr.Test, RerunOf: pr.RerunOf,
		Since: pr.Since, Until: pr.Until, Limit: pr.Limit,
	}
	if pr.Archived != nil {
		in.Archived = *pr.Archived
	}
	if pr.ExcludeArchivedTasks != nil {
		in.ExcludeArchivedTasks = *pr.ExcludeArchivedTasks
	}
	if pr.Status != nil {
		for _, s := range *pr.Status {
			in.Statuses = append(in.Statuses, string(s))
		}
	}
	if pr.Trigger != nil {
		in.Trigger = string(*pr.Trigger)
	}
	if pr.RunnerID != nil {
		in.RunnerID = *pr.RunnerID
	}
	if pr.Cursor != nil {
		in.Cursor = *pr.Cursor
	}
	page, err := h.runs.List(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[ListRunsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]RunSummary, len(page.Runs))
	for i, it := range page.Runs {
		r := it.Run
		status, summary := resultFields(r.Result)
		items[i] = RunSummary{
			ID:            r.ID,
			ArchivedAt:    optionalTime(r.ArchivedAt),
			Task:          TaskRef{ID: r.TaskID, Name: it.TaskName, ProjectID: r.ProjectID},
			Version:       r.VersionSeq,
			Status:        RunStatus(r.Status),
			Test:          r.IsTest,
			Trigger:       runTrigger(r),
			Attempt:       max(r.Attempt, 1),
			RetryOf:       optionalUUID(r.RetryOf),
			RerunOf:       optionalUUID(r.RerunOf),
			SkipReason:    optionalEnum[SkipReason](r.SkipReason),
			QueuedAt:      r.QueuedAt,
			StartedAt:     optionalTime(r.StartedAt),
			FinishedAt:    optionalTime(r.FinishedAt),
			Error:         runError(r),
			ResultStatus:  status,
			ResultSummary: summary,
			CurrentStage:  optional(it.CurrentStage),
			Links:         Links{Console: h.consoleLink("/runs/" + r.ID.String())},
		}
	}
	return ListRuns200JSONResponse{Items: items, NextCursor: optional(page.NextCursor)}, nil
}

// SetRunArchive changes list visibility and durable log retention.
func (h *handler) SetRunArchive(ctx context.Context, req SetRunArchiveRequestObject) (SetRunArchiveResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	if req.Body == nil {
		return problemResponse[SetRunArchivedefaultApplicationProblemPlusJSONResponse](ctx, http.StatusBadRequest, ErrorCodeValidationFailed, "需要提供 archived 字段"), nil
	}
	d, err := h.runs.SetArchived(ctx, auth.PrincipalFrom(ctx), req.RunID, req.Body.Archived)
	if err != nil {
		return errorResponse[SetRunArchivedefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	run, err := h.toRun(d)
	if err != nil {
		return nil, err
	}
	return SetRunArchive200JSONResponse(run), nil
}

// RerunRun starts a new run modelled on a finished one.
func (h *handler) RerunRun(ctx context.Context, req RerunRunRequestObject) (RerunRunResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	var in service.RerunInput
	if b := req.Body; b != nil {
		in.Version, in.Test = b.Version, b.Test
		if b.Params != nil {
			in.Params = *b.Params
			if in.Params == nil {
				in.Params = map[string]any{}
			}
		}
	}
	d, err := h.runs.Rerun(ctx, auth.PrincipalFrom(ctx), req.RunID, in)
	if err != nil {
		return errorResponse[RerunRundefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	run, err := h.toRun(d)
	if err != nil {
		return nil, err
	}
	return RerunRun202JSONResponse(run), nil
}

// ListRunEvents returns a page of a run's events.
func (h *handler) ListRunEvents(ctx context.Context, req ListRunEventsRequestObject) (ListRunEventsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	in := service.EventsInput{Limit: req.Params.Limit}
	if c := req.Params.Cursor; c != nil {
		in.Cursor = *c
	}
	if k := req.Params.Kind; k != nil {
		for _, kind := range *k {
			in.Kinds = append(in.Kinds, string(kind))
		}
	}
	page, err := h.runs.Events(ctx, auth.PrincipalFrom(ctx), req.RunID, in)
	if err != nil {
		return errorResponse[ListRunEventsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	events := make([]RunEvent, len(page.Events))
	for i, e := range page.Events {
		events[i] = RunEvent{
			Seq: e.Seq, TS: e.TS, Kind: RunEventKind(e.Kind), Stage: optional(e.Stage), Message: optional(e.Message),
		}
		events[i].Data = rawJSON([]byte(e.Payload))
	}
	return ListRunEvents200JSONResponse{Events: events, NextCursor: page.NextCursor, Complete: page.Complete}, nil
}

// StreamRunLogs streams a run's log as server-sent events.
func (h *handler) StreamRunLogs(ctx context.Context, req StreamRunLogsRequestObject) (StreamRunLogsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	in := service.FollowInput{Tail: req.Params.Tail}
	if c := req.Params.Cursor; c != nil {
		in.Cursor = *c
	}
	if id := req.Params.LastEventID; id != nil {
		n, err := strconv.ParseInt(*id, 10, 64)
		if err != nil {
			p := buildProblem(ctx, http.StatusBadRequest, ErrorCodeValidationFailed, "Last-Event-ID 不是有效的行号")
			p.Errors = &[]FieldError{{Detail: "行号超出范围", Header: new("Last-Event-ID")}}
			return StreamRunLogsdefaultApplicationProblemPlusJSONResponse{Body: p, StatusCode: p.Status}, nil
		}
		in.LastEventID = &n
	}
	f, err := h.runs.FollowLogs(ctx, auth.PrincipalFrom(ctx), req.RunID, in)
	if err != nil {
		return errorResponse[StreamRunLogsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return logStream{ctx: ctx, f: f, done: h.streamsDone, log: h.log}, nil
}

// DownloadRunLogs returns a run's whole log as a text attachment.
func (h *handler) DownloadRunLogs(ctx context.Context, req DownloadRunLogsRequestObject) (DownloadRunLogsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	d, err := h.runs.DownloadLogs(ctx, auth.PrincipalFrom(ctx), req.RunID)
	if err != nil {
		return errorResponse[DownloadRunLogsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return logDownload{ctx: ctx, d: d, name: req.RunID.String() + ".log", log: h.log}, nil
}

// ListArtifacts lists the artifacts of a run.
func (h *handler) ListArtifacts(ctx context.Context, req ListArtifactsRequestObject) (ListArtifactsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	cursor := ""
	if req.Params.Cursor != nil {
		cursor = *req.Params.Cursor
	}
	page, err := h.runs.Artifacts(ctx, auth.PrincipalFrom(ctx), req.RunID, req.Params.Limit, cursor)
	if err != nil {
		return errorResponse[ListArtifactsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]Artifact, len(page.Artifacts))
	for i, a := range page.Artifacts {
		items[i] = Artifact{
			ID: a.ID, RunID: a.RunID, Path: a.Path, Title: optional(a.Title), Size: a.Size,
			ContentType: a.ContentType, Sha256: a.SHA256, Preview: ArtifactPreview(service.ArtifactPreview(a.ContentType)),
			CreatedAt: a.CreatedAt, ExpiredAt: optionalTime(a.ExpiredAt),
			Links: ArtifactLinks{Download: h.consoleLink(BasePath + "artifacts/" + a.ID.String() + "/download")},
		}
	}
	return ListArtifacts200JSONResponse{Items: items, NextCursor: optional(page.NextCursor)}, nil
}

// DownloadArtifact returns the content of an artifact.
func (h *handler) DownloadArtifact(ctx context.Context, req DownloadArtifactRequestObject) (DownloadArtifactResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	a, content, err := h.runs.OpenArtifact(ctx, auth.PrincipalFrom(ctx), req.ArtifactID)
	if err != nil {
		return errorResponse[DownloadArtifactdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	inline := req.Params.Inline != nil && *req.Params.Inline && service.ArtifactPreview(a.ContentType) != service.PreviewNone
	return artifactDownload{a: a, content: content, inline: inline}, nil
}

// StreamEvents streams run updates the caller can view as server-sent events.
func (h *handler) StreamEvents(ctx context.Context, req StreamEventsRequestObject) (StreamEventsResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	s, err := h.runs.StreamUpdates(ctx, auth.PrincipalFrom(ctx), req.Params.ProjectID, req.Params.TaskID)
	if err != nil {
		return errorResponse[StreamEventsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return updateStream{ctx: ctx, s: s, done: h.streamsDone, log: h.log}, nil
}

func runTrigger(r store.Run) RunTrigger {
	t := RunTrigger{
		Kind:        TriggerKind(r.TriggerKind),
		TriggerID:   optionalUUID(r.TriggerID),
		ScheduledAt: optionalTime(r.ScheduledAt),
		UserID:      optionalUUID(r.CreatedBy),
		Client:      optional(r.Client),
	}
	if !t.Kind.Valid() {
		t.Kind = TriggerKindManual
	}
	return t
}

func runError(r store.Run) *RunError {
	if r.ErrorCode == "" {
		return nil
	}
	code := ErrorCode(r.ErrorCode)
	if !code.Valid() {
		// The coordinator records contract codes only; this keeps a
		// stray value from breaking clients that switch on the enum.
		code = ErrorCodeInternalError
	}
	return &RunError{Code: code, Stage: optional(r.ErrorStage), Message: r.ErrorMessage}
}

// sse writes server-sent events.
type sse struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func startSSE(w http.ResponseWriter) *sse {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Reverse proxies such as nginx buffer responses unless told not to.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	s := &sse{w: w, rc: http.NewResponseController(w)}
	_ = s.rc.Flush()
	return s
}

// event writes one event; id is omitted when empty. data is encoded as
// JSON, which never contains a newline.
func (s *sse) event(name, id string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		return err
	}
	if id != "" {
		if _, err := fmt.Fprintf(s.w, "id: %s\n", id); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, b)
	return err
}

func (s *sse) comment(text string) error {
	_, err := fmt.Fprintf(s.w, ": %s\n\n", text)
	return err
}

func (s *sse) flush() error { return s.rc.Flush() }

// streamContext ends with the request or when the server starts shutting
// down, so open streams do not hold up a graceful stop.
func streamContext(ctx context.Context, done <-chan struct{}) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// expectedStreamEnd reports whether a stream error is the caller losing
// access (the run is no longer visible, or the credential was revoked,
// expired or its user disabled) rather than a failure worth logging.
func expectedStreamEnd(err error) bool {
	return errors.Is(err, service.ErrNotFound) || errors.Is(err, service.ErrUnauthenticated)
}

// logStream is the hand-written body of streamRunLogs (api/README.md).
type logStream struct {
	ctx  context.Context
	f    *service.LogFollower
	done <-chan struct{}
	log  *slog.Logger
}

func (s logStream) VisitStreamRunLogsResponse(w http.ResponseWriter) error {
	ctx, cancel := streamContext(s.ctx, s.done)
	defer cancel()
	out := startSSE(w)
	for {
		b, err := s.f.Next(ctx, sseHeartbeat)
		if err != nil {
			if ctx.Err() == nil && !expectedStreamEnd(err) && !errors.Is(err, service.ErrExpired) {
				s.log.ErrorContext(ctx, "log stream failed", "error", err)
			}
			// Headers are sent; the client reconnects with Last-Event-ID.
			return nil
		}
		for _, l := range b.Lines {
			line := LogLine{Seq: l.Seq, TS: l.TS, Stream: LogLineStream(l.Stream), Stage: optional(l.Stage), Text: l.Text}
			if err := out.event("line", strconv.FormatInt(l.Seq, 10), line); err != nil {
				return nil
			}
		}
		if b.DroppedLines > 0 {
			if err := out.event("truncated", "", map[string]int64{"dropped_lines": b.DroppedLines}); err != nil {
				return nil
			}
		}
		if e := b.End; e != nil {
			_ = out.event("end", "", LogStreamEnd{Status: RunStatus(e.Status), LastSeq: e.LastSeq, DroppedLines: e.DroppedLines})
			_ = out.flush()
			return nil
		}
		if len(b.Lines) == 0 && b.DroppedLines == 0 {
			if err := out.comment("heartbeat"); err != nil {
				return nil
			}
		}
		if err := out.flush(); err != nil {
			return nil
		}
	}
}

// updateStream is the hand-written body of streamEvents.
type updateStream struct {
	ctx  context.Context
	s    *service.UpdateStream
	done <-chan struct{}
	log  *slog.Logger
}

func (u updateStream) VisitStreamEventsResponse(w http.ResponseWriter) error {
	defer u.s.Close()
	ctx, cancel := streamContext(u.ctx, u.done)
	defer cancel()
	out := startSSE(w)
	for {
		next, resync, err := u.s.Next(ctx, sseHeartbeat)
		switch {
		case err != nil:
			if ctx.Err() == nil && !expectedStreamEnd(err) {
				u.log.ErrorContext(ctx, "event stream failed", "error", err)
			}
			return nil
		case resync:
			err = out.event("resync", "", struct{}{})
		case next != nil:
			err = out.event("run", "", RunUpdate{
				RunID: next.RunID, TaskID: next.TaskID, ProjectID: next.ProjectID, Status: RunStatus(next.Status),
				CurrentStage: optional(next.CurrentStage), ResultStatus: optionalEnum[RunResultStatus](next.ResultStatus), TS: next.TS,
			})
		default:
			err = out.comment("heartbeat")
		}
		if err != nil || out.flush() != nil {
			return nil
		}
	}
}

// logDownload is the body of downloadRunLogs, streamed in batches.
type logDownload struct {
	ctx  context.Context
	d    *service.LogDownload
	name string
	log  *slog.Logger
}

func (d logDownload) VisitDownloadRunLogsResponse(w http.ResponseWriter) error {
	h := w.Header()
	h.Set("Content-Type", "text/plain; charset=utf-8")
	h.Set("Content-Disposition", contentDisposition("attachment", d.name))
	h.Set("X-Content-Type-Options", "nosniff")
	// The log is the task's own output, like an artifact: if a browser
	// ever renders it, nothing in it may run or load.
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.WriteHeader(http.StatusOK)
	if err := d.d.WriteTo(d.ctx, w); err != nil && d.ctx.Err() == nil {
		// The status is sent; a cut-off download is all that can be done.
		d.log.ErrorContext(d.ctx, "write log download", "file", d.name, "error", err)
	}
	return nil
}

// artifactDownload is the body of downloadArtifact. Artifacts are the
// task's output and are not sanitised, so the browser is told never to
// guess a type or run anything in them.
type artifactDownload struct {
	a       store.Artifact
	content io.ReadCloser
	inline  bool
}

func (d artifactDownload) VisitDownloadArtifactResponse(w http.ResponseWriter) error {
	defer func() { _ = d.content.Close() }()
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Length", strconv.FormatInt(d.a.Size, 10))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	disposition := "attachment"
	if d.inline {
		disposition = "inline"
	}
	h.Set("Content-Disposition", contentDisposition(disposition, path.Base(d.a.Path)))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, d.content)
	return nil
}

// contentDisposition builds the header with an ASCII fallback name and
// the exact UTF-8 name (RFC 6266).
func contentDisposition(kind, name string) string {
	fallback := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, fallback, url.PathEscape(name))
}
