package httpapi

import (
	"context"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

// ListTools implements listTools.
func (h *handler) ListTools(ctx context.Context, req ListToolsRequestObject) (ListToolsResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	tools, err := h.tasks.ListTools(ctx, auth.PrincipalFrom(ctx), req.Params.ProjectID)
	if err != nil {
		return errorResponse[ListToolsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	items := make([]Tool, len(tools))
	for i, t := range tools {
		items[i] = toTool(t)
	}
	return ListTools200JSONResponse{Items: items}, nil
}

// RegisterTool implements registerTool.
func (h *handler) RegisterTool(ctx context.Context, req RegisterToolRequestObject) (RegisterToolResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	b := req.Body
	in := service.RegisterToolInput{Name: b.Name, Kind: string(b.Kind), Description: b.Description}
	if b.URL != nil {
		in.URL = *b.URL
	}
	if b.Files != nil {
		in.Files = make([]service.FileInput, len(*b.Files))
		for i, f := range *b.Files {
			in.Files[i] = service.FileInput{Path: f.Path, Content: []byte(f.Content), Executable: f.Executable != nil && *f.Executable}
		}
	}
	t, err := h.tasks.RegisterTool(ctx, auth.PrincipalFrom(ctx), req.ProjectID, in)
	if err != nil {
		return errorResponse[RegisterTooldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RegisterTool200JSONResponse(toTool(t)), nil
}

// RemoveTool implements removeTool.
func (h *handler) RemoveTool(ctx context.Context, req RemoveToolRequestObject) (RemoveToolResponseObject, error) {
	if h.tasks == nil {
		return nil, errNoTasks
	}
	if err := h.tasks.RemoveTool(ctx, auth.PrincipalFrom(ctx), req.ToolID); err != nil {
		return errorResponse[RemoveTooldefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RemoveTool204Response{}, nil
}

func toTool(d service.ToolDetail) Tool {
	t := d.Tool
	out := Tool{
		ID: &t.ID, ProjectID: &t.ProjectID, Name: t.Name, Kind: ToolKind(t.Kind),
		Description: t.Description, URL: t.URL, CreatedAt: &t.CreatedAt, UpdatedAt: &t.UpdatedAt,
	}
	if t.Kind == store.ToolKindScript {
		files := make([]TaskFile, len(d.Files))
		for i, f := range d.Files {
			files[i] = TaskFile{Path: f.Path, Size: f.Size, Sha256: f.BlobHash, Executable: f.Executable}
		}
		out.Files, out.TreeHash = &files, &t.TreeHash
	}
	return out
}
