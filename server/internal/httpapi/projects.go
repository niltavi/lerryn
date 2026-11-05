package httpapi

import (
	"context"
	"errors"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

var errNoProjects = errors.New("httpapi: no project service configured")

// ListProjects lists the projects the caller belongs to.
func (h *handler) ListProjects(ctx context.Context, req ListProjectsRequestObject) (ListProjectsResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	views, next, err := h.projects.List(ctx, auth.PrincipalFrom(ctx), req.Params.Limit, deref(req.Params.Cursor))
	if err != nil {
		return errorResponse[ListProjectsdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ListProjects200JSONResponse{Items: make([]Project, len(views)), NextCursor: optional(next)}
	for i, v := range views {
		out.Items[i] = h.toProject(v)
	}
	return out, nil
}

// CreateProject creates a shared project owned by the caller.
func (h *handler) CreateProject(ctx context.Context, req CreateProjectRequestObject) (CreateProjectResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	v, err := h.projects.Create(ctx, auth.PrincipalFrom(ctx), req.Body.Name)
	if err != nil {
		return errorResponse[CreateProjectdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return CreateProject201JSONResponse(h.toProject(v)), nil
}

// GetProject returns a project the caller belongs to.
func (h *handler) GetProject(ctx context.Context, req GetProjectRequestObject) (GetProjectResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	v, err := h.projects.Get(ctx, auth.PrincipalFrom(ctx), req.ProjectID)
	if err != nil {
		return errorResponse[GetProjectdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return GetProject200JSONResponse(h.toProject(v)), nil
}

// UpdateProject renames a project.
func (h *handler) UpdateProject(ctx context.Context, req UpdateProjectRequestObject) (UpdateProjectResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	v, err := h.projects.Rename(ctx, auth.PrincipalFrom(ctx), req.ProjectID, req.Body.Name)
	if err != nil {
		return errorResponse[UpdateProjectdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return UpdateProject200JSONResponse(h.toProject(v)), nil
}

// ListMembers lists the members of a project.
func (h *handler) ListMembers(ctx context.Context, req ListMembersRequestObject) (ListMembersResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	members, next, err := h.projects.Members(ctx, auth.PrincipalFrom(ctx), req.ProjectID, req.Params.Limit, deref(req.Params.Cursor))
	if err != nil {
		return errorResponse[ListMembersdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := ListMembers200JSONResponse{Items: make([]Member, len(members)), NextCursor: optional(next)}
	for i, m := range members {
		out.Items[i] = toMember(m)
	}
	return out, nil
}

// SetMember adds a user to a project or changes their role.
func (h *handler) SetMember(ctx context.Context, req SetMemberRequestObject) (SetMemberResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	m, err := h.projects.SetMember(ctx, auth.PrincipalFrom(ctx), req.ProjectID, req.UserID, string(req.Body.Role))
	if err != nil {
		return errorResponse[SetMemberdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return SetMember200JSONResponse(toMember(m)), nil
}

// RemoveMember removes a user from a project.
func (h *handler) RemoveMember(ctx context.Context, req RemoveMemberRequestObject) (RemoveMemberResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	if err := h.projects.RemoveMember(ctx, auth.PrincipalFrom(ctx), req.ProjectID, req.UserID); err != nil {
		return errorResponse[RemoveMemberdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	return RemoveMember204Response{}, nil
}

// SearchUsers finds active users by the start of their name.
func (h *handler) SearchUsers(ctx context.Context, req SearchUsersRequestObject) (SearchUsersResponseObject, error) {
	if h.projects == nil {
		return nil, errNoProjects
	}
	users, next, err := h.projects.SearchUsers(ctx, auth.PrincipalFrom(ctx), req.Params.Q, req.Params.Limit, deref(req.Params.Cursor))
	if err != nil {
		return errorResponse[SearchUsersdefaultApplicationProblemPlusJSONResponse](ctx, err)
	}
	out := SearchUsers200JSONResponse{Items: make([]UserRef, len(users)), NextCursor: optional(next)}
	for i, u := range users {
		out.Items[i] = toUserRef(u)
	}
	return out, nil
}

func (h *handler) toProject(v service.ProjectView) Project {
	return Project{
		ID:          v.Project.ID,
		Name:        v.Project.Name,
		IsPersonal:  v.Project.IsPersonal,
		Role:        ProjectRole(v.Role),
		Owner:       toUserRef(v.Owner),
		MemberCount: v.MemberCount,
		CreatedAt:   v.Project.CreatedAt,
		UpdatedAt:   v.Project.UpdatedAt,
		Links:       Links{Console: h.consoleLink("/projects/" + v.Project.ID.String())},
	}
}

func toMember(m service.Member) Member {
	return Member{
		User:      toUserRef(m.User),
		Role:      ProjectRole(m.Binding.Role),
		Disabled:  !m.User.DisabledAt.IsZero(),
		CreatedAt: m.Binding.CreatedAt,
		UpdatedAt: m.Binding.UpdatedAt,
	}
}

func toUserRef(u store.User) UserRef {
	return UserRef{ID: u.ID, Username: u.Username, DisplayName: optional(u.DisplayName)}
}
