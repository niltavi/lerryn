package httpapi

import (
	"context"
	"fmt"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
)

// bundleFormatVersion is TaskBundle.format_version.
const bundleFormatVersion = 1

// ExportTask returns a task as a portable bundle.
func (h *handler) ExportTask(ctx context.Context, req ExportTaskRequestObject) (ExportTaskResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	in := service.ExportTaskInput{
		Version:  req.Params.Version,
		History:  req.Params.History != nil && *req.Params.History,
		Triggers: req.Params.Triggers == nil || *req.Params.Triggers,
	}
	b, err := h.tasks.Export(ctx, auth.PrincipalFrom(ctx), req.TaskID, in)
	if err != nil {
		return errorResponse[ExportTaskdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ExportTask200JSONResponse{
		Format:          TaskBundleFormatLerrynTaskBundle,
		FormatVersion:   bundleFormatVersion,
		Name:            b.Name,
		ExportedAt:      b.ExportedAt,
		ServerVersion:   h.server.Version,
		OmittedVersions: b.OmittedVersions,
		Versions:        make([]TaskBundleVersion, len(b.Versions)),
		Console:         &TaskConsole{ManualRun: !b.HideManualRun},
	}
	if b.ActiveVersion != 0 {
		out.ActiveVersion = &b.ActiveVersion
	}
	for i, v := range b.Versions {
		m, err := convertJSON[Manifest](v.Manifest)
		if err != nil {
			return nil, fmt.Errorf("convert manifest of version %d: %w", v.Seq, err)
		}
		files := make([]TaskFileInput, len(v.Files))
		for j, f := range v.Files {
			files[j] = TaskFileInput{Path: f.Path, Content: string(f.Content)}
			if f.Executable {
				files[j].Executable = new(true)
			}
		}
		out.Versions[i] = TaskBundleVersion{Seq: v.Seq, CreatedAt: v.CreatedAt, Manifest: m, Files: files}
	}
	if b.Triggers != nil {
		triggers := make([]TriggerInput, len(b.Triggers))
		for i, t := range b.Triggers {
			triggers[i] = toTriggerInput(t)
		}
		out.Triggers = &triggers
	}
	return out, nil
}

// ImportTask creates a task in a project from a bundle.
func (h *handler) ImportTask(ctx context.Context, req ImportTaskRequestObject) (ImportTaskResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	in := service.ImportTaskInput{ProjectID: req.ProjectID, DryRun: req.Params.DryRun != nil && *req.Params.DryRun}
	if body := req.Body; body != nil {
		if body.Name != nil {
			in.Name = *body.Name
		}
		b := body.Bundle
		in.Bundle = service.TaskBundle{Name: b.Name, ExportedAt: b.ExportedAt, OmittedVersions: b.OmittedVersions}
		if b.ActiveVersion != nil {
			in.Bundle.ActiveVersion = *b.ActiveVersion
		}
		if b.Console != nil {
			in.Bundle.HideManualRun = !b.Console.ManualRun
		}
		in.Bundle.Versions = make([]service.BundleVersion, len(b.Versions))
		for i, v := range b.Versions {
			m, err := convertJSON[service.Manifest](v.Manifest)
			if err != nil {
				return nil, fmt.Errorf("convert manifest: %w", err)
			}
			files := make([]service.FileInput, len(v.Files))
			for j, f := range v.Files {
				files[j] = service.FileInput{Path: f.Path, Content: []byte(f.Content), Executable: f.Executable != nil && *f.Executable}
			}
			in.Bundle.Versions[i] = service.BundleVersion{Seq: v.Seq, CreatedAt: v.CreatedAt, Manifest: m, Files: files}
		}
		if b.Triggers != nil {
			in.Bundle.Triggers = toServiceTriggers(*b.Triggers)
		}
	}
	res, err := h.tasks.Import(ctx, auth.PrincipalFrom(ctx), in)
	if err != nil {
		return errorResponse[ImportTaskdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ImportTask200JSONResponse{
		DryRun:      res.DryRun,
		Versions:    make([]ImportedVersion, len(res.SourceSeqs)),
		Triggers:    make([]Trigger, len(res.Triggers.Triggers)),
		Credentials: make([]TaskCredential, len(res.Credentials)),
		Warnings:    toIssues(res.Warnings),
	}
	for i, seq := range res.SourceSeqs {
		out.Versions[i] = ImportedVersion{SourceSeq: seq, Seq: i + 1}
	}
	for i, v := range res.Triggers.Triggers {
		out.Triggers[i] = h.toTrigger(v)
	}
	for i, c := range res.Credentials {
		out.Credentials[i] = TaskCredential{
			Name: c.Spec.Name, As: TaskCredentialAs(c.Spec.As), Optional: c.Spec.Optional, Status: TaskCredentialStatus(c.Status),
		}
	}
	if d := res.Task; d != nil {
		out.Task = &ImportedTask{
			ID: d.Task.ID, ProjectID: d.Task.ProjectID, Name: d.Task.Name, LatestVersion: d.Task.LatestSeq,
			Links: Links{Console: h.consoleLink("/tasks/" + d.Task.ID.String())},
		}
	}
	return out, nil
}

// toTriggerInput is the inverse of toServiceTriggers: the definition that
// recreates a trigger, with only the fields its type uses.
func toTriggerInput(t service.TriggerInput) TriggerInput {
	out := TriggerInput{Name: t.Name, Type: TriggerType(t.Type), Enabled: t.Enabled}
	if t.Schedule != "" {
		out.Schedule = &t.Schedule
	}
	if t.Timezone != "" {
		out.Timezone = &t.Timezone
	}
	if t.CatchUp != "" {
		c := CatchUp(t.CatchUp)
		out.CatchUp = &c
	}
	if t.MaxBodyBytes != nil {
		n := int(*t.MaxBodyBytes)
		out.MaxBodyBytes = &n
	}
	if t.Params != nil {
		params := t.Params
		out.Params = &params
	}
	return out
}
