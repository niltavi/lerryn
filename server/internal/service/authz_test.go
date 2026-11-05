package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestAuthorizer(t *testing.T) {
	a, db := newAccounts(t)
	ctx := context.Background()
	owner, _ := register(t, a, "owner") // first account: also a platform admin
	members := map[string]*auth.Principal{}
	for _, role := range []string{service.RoleViewer, service.RoleMaintainer, "unknown-role"} {
		acct, _ := register(t, a, "u-"+role)
		if _, err := db.NewInsert().Model(&store.RoleBinding{
			ID: store.NewID(), ProjectID: owner.PersonalProjectID, UserID: acct.User.ID, Role: role,
			CreatedAt: store.Now(), UpdatedAt: store.Now(),
		}).Exec(ctx); err != nil {
			t.Fatal(err)
		}
		members[role] = &auth.Principal{UserID: acct.User.ID}
	}
	stranger, _ := register(t, a, "stranger")
	members["owner"] = &auth.Principal{UserID: owner.User.ID}
	members["stranger"] = &auth.Principal{UserID: stranger.User.ID}

	z := service.NewAuthorizer(db)
	tests := []struct {
		who    string
		action service.Action
		want   error
	}{
		{"owner", service.ActionManageMembers, nil},
		{service.RoleMaintainer, service.ActionModifyTask, nil},
		{service.RoleMaintainer, service.ActionManageMembers, service.ErrForbidden},
		{service.RoleViewer, service.ActionViewLogs, nil},
		{service.RoleViewer, service.ActionControlRun, service.ErrForbidden},
		{"unknown-role", service.ActionViewTask, service.ErrNotFound},
		{"stranger", service.ActionViewTask, service.ErrNotFound},
	}
	for _, tt := range tests {
		if err := z.Require(ctx, members[tt.who], owner.PersonalProjectID, tt.action); !errors.Is(err, tt.want) {
			t.Errorf("%s %s: err = %v, want %v", tt.who, tt.action, err, tt.want)
		}
	}

	// Admins get no implicit access to other people's projects.
	strangerProject := stranger.PersonalProjectID
	if err := z.Require(ctx, members["owner"], strangerProject, service.ActionViewTask); !errors.Is(err, service.ErrNotFound) {
		t.Errorf("admin reached another user's project: %v", err)
	}
	if err := z.Require(ctx, nil, uuid.New(), service.ActionViewTask); !errors.Is(err, service.ErrUnauthenticated) {
		t.Errorf("anonymous: %v", err)
	}
	if !service.ValidRole(service.RoleOwner) || service.ValidRole("admin") {
		t.Error("ValidRole")
	}
}
