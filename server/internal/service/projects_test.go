package service_test

import (
	"testing"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func newProjects(e *accountsEnv) *service.Projects {
	return service.NewProjects(e.db, service.NewAuthorizer(e.db), nil)
}

func TestProjectsAndMembers(t *testing.T) {
	e := newAccountsEnv(t)
	ps := newProjects(e)
	alice, _ := e.user("alice")
	bob, _ := e.user("bob")
	carol, _ := e.user("carol")

	shared, err := ps.Create(e.ctx, alice, "  数据组 ")
	if err != nil {
		t.Fatal(err)
	}
	if shared.Project.Name != "数据组" || shared.Role != service.RoleOwner || shared.MemberCount != 1 ||
		shared.Owner.ID != alice.UserID || shared.Project.IsPersonal {
		t.Fatalf("created = %+v", shared)
	}
	id := shared.Project.ID

	// Non-members cannot see the project at all.
	_, err = ps.Get(e.ctx, bob, id)
	wantErr(t, err, service.ErrNotFound)
	_, err = ps.SetMember(e.ctx, bob, id, bob.UserID, service.RoleOwner)
	wantErr(t, err, service.ErrNotFound)

	if _, err := ps.SetMember(e.ctx, alice, id, bob.UserID, service.RoleViewer); err != nil {
		t.Fatal(err)
	}
	// A viewer sees the project but cannot manage it.
	if v, err := ps.Get(e.ctx, bob, id); err != nil || v.Role != service.RoleViewer || v.MemberCount != 2 {
		t.Fatalf("bob's view = %+v, %v", v, err)
	}
	_, err = ps.SetMember(e.ctx, bob, id, carol.UserID, service.RoleViewer)
	wantErr(t, err, service.ErrPermissionDenied)
	_, err = ps.Rename(e.ctx, bob, id, "x")
	wantErr(t, err, service.ErrPermissionDenied)
	wantErr(t, ps.RemoveMember(e.ctx, bob, id, alice.UserID), service.ErrPermissionDenied)

	// Projects are listed personal first, then by name.
	if _, err := ps.Create(e.ctx, bob, "alpha"); err != nil {
		t.Fatal(err)
	}
	list, _, err := ps.List(e.ctx, bob, nil, "")
	if err != nil || len(list) != 3 || !list[0].Project.IsPersonal || list[1].Project.Name != "alpha" || list[2].Project.ID != id {
		t.Fatalf("bob's projects = %+v, %v", list, err)
	}
	page, next, err := ps.List(e.ctx, bob, new(2), "")
	if err != nil || len(page) != 2 || next == "" {
		t.Fatalf("page = %d %q %v", len(page), next, err)
	}
	if rest, next, _ := ps.List(e.ctx, bob, new(2), next); len(rest) != 1 || next != "" {
		t.Fatalf("second page = %d %q", len(rest), next)
	}

	// The last owner can be neither demoted nor removed.
	_, err = ps.SetMember(e.ctx, alice, id, alice.UserID, service.RoleMaintainer)
	wantErr(t, err, service.ErrLastOwner)
	wantErr(t, ps.RemoveMember(e.ctx, alice, id, alice.UserID), service.ErrLastOwner)
	if _, err := ps.SetMember(e.ctx, alice, id, bob.UserID, service.RoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := ps.SetMember(e.ctx, alice, id, alice.UserID, service.RoleMaintainer); err != nil {
		t.Fatalf("demote with another owner: %v", err)
	}

	members, _, err := ps.Members(e.ctx, alice, id, nil, "")
	if err != nil || len(members) != 2 || members[0].User.Username != "alice" || members[0].Binding.Role != service.RoleMaintainer ||
		members[1].Binding.Role != service.RoleOwner {
		t.Fatalf("members = %+v, %v", members, err)
	}

	// Members may leave on their own.
	if err := ps.RemoveMember(e.ctx, alice, id, alice.UserID); err != nil {
		t.Fatal(err)
	}
	_, err = ps.Get(e.ctx, alice, id)
	wantErr(t, err, service.ErrNotFound)

	_, err = ps.SetMember(e.ctx, bob, id, store.NewID(), service.RoleViewer)
	wantErr(t, err, service.ErrNotFound)
	_, err = ps.SetMember(e.ctx, bob, id, carol.UserID, "admin")
	wantValidation(t, err, "#/role")
	if v, err := ps.Rename(e.ctx, bob, id, "数据二组"); err != nil || v.Project.Name != "数据二组" {
		t.Fatalf("rename = %+v, %v", v, err)
	}
	_, err = ps.Create(e.ctx, bob, "   ")
	wantValidation(t, err, "#/name")
}

func TestPersonalProjectHasNoOtherMembers(t *testing.T) {
	e := newAccountsEnv(t)
	ps := newProjects(e)
	alice, _ := e.user("alice")
	bob, _ := e.user("bob")
	_, err := ps.SetMember(e.ctx, alice, alice.PersonalProjectID, bob.UserID, service.RoleViewer)
	wantValidation(t, err, "userId")
	wantErr(t, ps.RemoveMember(e.ctx, alice, alice.PersonalProjectID, alice.UserID), service.ErrLastOwner)
}

func TestSearchUsers(t *testing.T) {
	e := newAccountsEnv(t)
	ps := newProjects(e)
	admin, _ := e.user("admin")
	e.user("alice")
	al, _ := e.user("al_x")
	e.user("bob")
	if _, err := e.accounts.UpdateMe(e.ctx, al, service.UpdateMeInput{DisplayName: new("Alba")}); err != nil {
		t.Fatal(err)
	}
	bobAcct, _ := e.user("bobby")
	if _, err := e.admin.UpdateUser(e.ctx, admin, bobAcct.UserID, service.AdminUpdateUserInput{Disabled: new(true)}); err != nil {
		t.Fatal(err)
	}

	names := func(q string) []string {
		users, _, err := ps.SearchUsers(e.ctx, admin, q, nil, "")
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, u := range users {
			out = append(out, u.Username)
		}
		return out
	}
	if got := names("AL"); len(got) != 2 || got[0] != "al_x" || got[1] != "alice" {
		t.Errorf("AL = %v", got)
	}
	if got := names("alb"); len(got) != 1 || got[0] != "al_x" {
		t.Errorf("display name prefix = %v", got)
	}
	// _ is literal, not a wildcard; disabled users are not found.
	if got := names("al_"); len(got) != 1 {
		t.Errorf("al_ = %v", got)
	}
	if got := names("bob"); len(got) != 1 || got[0] != "bob" {
		t.Errorf("bob = %v", got)
	}
}
