package service

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Action is something a principal does inside a project. Services check
// actions, never roles, so finer roles can be added later without touching
// the checks (tech design 12.2).
type Action string

// Project actions.
const (
	ActionViewTask      Action = "task.view"
	ActionViewLogs      Action = "logs.view"
	ActionControlRun    Action = "run.control"
	ActionModifyTask    Action = "task.modify"
	ActionRegisterTool  Action = "tool.register"
	ActionManageMembers Action = "project.manage_members"
	ActionManageProject Action = "project.manage"
)

// Project roles.
const (
	RoleViewer     = "viewer"
	RoleMaintainer = "maintainer"
	RoleOwner      = "owner"
)

// roleActions lists what each role may do. Each role includes the one
// before it.
var roleActions = map[string][]Action{
	RoleViewer:     {ActionViewTask, ActionViewLogs},
	RoleMaintainer: {ActionViewTask, ActionViewLogs, ActionControlRun, ActionModifyTask, ActionRegisterTool},
	RoleOwner:      {ActionViewTask, ActionViewLogs, ActionControlRun, ActionModifyTask, ActionRegisterTool, ActionManageMembers, ActionManageProject},
}

// ValidRole reports whether role names a project role.
func ValidRole(role string) bool {
	_, ok := roleActions[role]
	return ok
}

// Authorizer checks what principals may do in projects.
type Authorizer struct {
	db *store.DB
}

// NewAuthorizer returns an authorizer backed by db.
func NewAuthorizer(db *store.DB) *Authorizer {
	return &Authorizer{db: db}
}

// Require returns nil when p may perform action in the project. A principal
// that cannot even view the project gets ErrNotFound, so that the response
// does not reveal the project exists; one that can view it but may not
// perform the action gets ErrPermissionDenied, which wraps ErrForbidden.
//
// Platform admins get no implicit project access: administering accounts
// and runners does not grant use of other people's tasks or credentials.
func (a *Authorizer) Require(ctx context.Context, p *auth.Principal, projectID uuid.UUID, action Action) error {
	if p == nil {
		return ErrUnauthenticated
	}
	var b store.RoleBinding
	err := a.db.NewSelect().Model(&b).Column("role").
		Where("project_id = ? AND user_id = ?", projectID, p.UserID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	allowed := roleActions[b.Role]
	if !slices.Contains(allowed, ActionViewTask) {
		// An unknown role in the database grants nothing.
		return ErrNotFound
	}
	if !slices.Contains(allowed, action) {
		return ErrPermissionDenied
	}
	return nil
}
