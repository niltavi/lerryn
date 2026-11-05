package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// GetOverview returns the data of the console overview board.
func (h *handler) GetOverview(ctx context.Context, req GetOverviewRequestObject) (GetOverviewResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	o, err := h.runs.Overview(ctx, auth.PrincipalFrom(ctx), req.Params.ProjectID)
	if err != nil {
		return errorResponse[GetOverviewdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := GetOverview200JSONResponse{
		GeneratedAt: o.GeneratedAt,
		Counts: OverviewCounts{
			Tasks: o.Counts.Tasks, ActiveTasks: o.Counts.ActiveTasks, Attention: o.Counts.Attention,
			Running: o.Counts.Running, Queued: o.Counts.Queued,
		},
		Attention: make([]AttentionItem, len(o.Attention)),
		Active:    make([]ActiveRun, len(o.Active)),
		Upcoming:  make([]UpcomingFire, len(o.Upcoming)),
	}
	for i, a := range o.Attention {
		item := AttentionItem{
			Kind:                AttentionKind(a.Kind),
			At:                  a.At,
			RunID:               optionalUUID(a.RunID),
			Stage:               optional(a.Stage),
			Summary:             a.Summary,
			CredentialRequestID: optionalUUID(a.CredentialRequestID),
			CredentialID:        optionalUUID(a.CredentialID),
			RunnerID:            optional(a.RunnerID),
			DueAt:               optionalTime(a.DueAt),
			Link:                h.consoleLink(a.Path),
		}
		if a.Task != nil {
			item.Task = &TaskRef{ID: a.Task.ID, Name: a.Task.Name, ProjectID: a.Task.ProjectID}
		}
		out.Attention[i] = item
	}
	for i, it := range o.Active {
		r := it.Run
		out.Active[i] = ActiveRun{
			RunID:        r.ID,
			Task:         TaskRef{ID: r.TaskID, Name: it.TaskName, ProjectID: r.ProjectID},
			Status:       RunStatus(r.Status),
			Test:         r.IsTest,
			Trigger:      runTrigger(r).Kind,
			QueuedAt:     r.QueuedAt,
			StartedAt:    optionalTime(r.StartedAt),
			CurrentStage: optional(it.CurrentStage),
			Links:        Links{Console: h.consoleLink("/runs/" + r.ID.String())},
		}
	}
	for i, f := range o.Upcoming {
		out.Upcoming[i] = UpcomingFire{
			Task:         TaskRef{ID: f.Task.ID, Name: f.Task.Name, ProjectID: f.Task.ProjectID},
			TriggerID:    f.TriggerID,
			At:           f.At,
			ScheduleText: f.ScheduleText,
		}
	}
	return out, nil
}

// GetRunCalendar returns the runs and planned fires in a time window.
func (h *handler) GetRunCalendar(ctx context.Context, req GetRunCalendarRequestObject) (GetRunCalendarResponseObject, error) {
	if h.runs == nil {
		return nil, errNoRuns
	}
	cal, err := h.runs.Calendar(ctx, auth.PrincipalFrom(ctx), service.CalendarInput{
		ProjectID: req.Params.ProjectID, TaskID: req.Params.TaskID, From: req.Params.From, To: req.Params.To,
	})
	if err != nil {
		return errorResponse[GetRunCalendardefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]CalendarItem, len(cal.Items))
	for i, it := range cal.Items {
		item := CalendarItem{
			Kind:  CalendarItemKindPlanned,
			Task:  TaskRef{ID: it.Task.ID, Name: it.Task.Name, ProjectID: it.Task.ProjectID},
			Start: it.Start,
		}
		if it.TriggerID != uuid.Nil {
			item.TriggerID, item.ScheduleText = &it.TriggerID, &it.ScheduleText
		}
		if it.RunID != uuid.Nil {
			status := RunStatus(it.Status)
			item.Kind, item.RunID, item.Status, item.End = CalendarItemKindRun, &it.RunID, &status, optionalTime(it.End)
		}
		items[i] = item
	}
	return GetRunCalendar200JSONResponse{From: cal.From, To: cal.To, Items: items, Truncated: cal.Truncated}, nil
}
