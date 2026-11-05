package service

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

const maxProjectNameRunes = 64

// Projects implements projects, their members and the user lookup used to
// add members (tech design 12.2).
type Projects struct {
	db    *store.DB
	authz *Authorizer
	log   *slog.Logger
}

// NewProjects returns the project service.
func NewProjects(db *store.DB, authz *Authorizer, log *slog.Logger) *Projects {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Projects{db: db, authz: authz, log: log}
}

// ProjectView is a project as one member sees it.
type ProjectView struct {
	Project     store.Project
	Role        string
	Owner       store.User
	MemberCount int
}

// Member is a user's role in a project.
type Member struct {
	Binding store.RoleBinding
	User    store.User
}

// List returns the projects p belongs to: the personal project first, the
// others by name.
func (s *Projects) List(ctx context.Context, p *auth.Principal, limitParam *int, cursor string) ([]ProjectView, string, error) {
	var v validation
	limit := pageLimit(&v, limitParam)
	offset, err := decodeOffset(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	var bindings []store.RoleBinding
	if err := s.db.NewSelect().Model(&bindings).Where("user_id = ?", p.UserID).Scan(ctx); err != nil {
		return nil, "", err
	}
	roles := map[uuid.UUID]string{}
	ids := []uuid.UUID{}
	for _, b := range bindings {
		// An unknown role grants nothing, not even seeing the project.
		if ValidRole(b.Role) {
			roles[b.ProjectID] = b.Role
			ids = append(ids, b.ProjectID)
		}
	}
	if len(ids) == 0 {
		return []ProjectView{}, "", nil
	}
	var projects []store.Project
	if err := s.db.NewSelect().Model(&projects).Where("id IN (?)", bun.List(ids)).
		OrderExpr("is_personal DESC, " + s.db.Native().Bytewise("lower(name)") + " ASC, id ASC").
		Offset(int64(offset)).Limit(int64(limit) + 1).Scan(ctx); err != nil {
		return nil, "", err
	}
	next := ""
	if len(projects) > limit {
		projects = projects[:limit]
		next = encodeOffset(offset + limit)
	}
	views, err := s.views(ctx, s.db, projects, roles)
	return views, next, err
}

// views completes projects with the owner and the member count.
func (s *Projects) views(ctx context.Context, idb bun.IDB, projects []store.Project, roles map[uuid.UUID]string) ([]ProjectView, error) {
	out := make([]ProjectView, len(projects))
	if len(projects) == 0 {
		return out, nil
	}
	ids := make([]uuid.UUID, len(projects))
	owners := make([]uuid.UUID, len(projects))
	for i, pr := range projects {
		ids[i], owners[i] = pr.ID, pr.OwnerUserID
	}
	users, err := usersByID(ctx, idb, owners)
	if err != nil {
		return nil, err
	}
	var counts []struct {
		ProjectID uuid.UUID `bun:"project_id"`
		N         int       `bun:"n"`
	}
	if err := idb.NewSelect().Model((*store.RoleBinding)(nil)).
		ColumnExpr("project_id").ColumnExpr("count(*) AS n").
		Where("project_id IN (?)", bun.List(ids)).Group("project_id").Scan(ctx, &counts); err != nil {
		return nil, fmt.Errorf("count members: %w", err)
	}
	n := map[uuid.UUID]int{}
	for _, c := range counts {
		n[c.ProjectID] = c.N
	}
	for i, pr := range projects {
		out[i] = ProjectView{Project: pr, Role: roles[pr.ID], Owner: users[pr.OwnerUserID], MemberCount: n[pr.ID]}
	}
	return out, nil
}

// Get returns a project p belongs to; any other project is ErrNotFound.
func (s *Projects) Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (ProjectView, error) {
	if err := s.authz.Require(ctx, p, id, ActionViewTask); err != nil {
		return ProjectView{}, err
	}
	return s.view(ctx, s.db, p, id)
}

func (s *Projects) view(ctx context.Context, idb bun.IDB, p *auth.Principal, id uuid.UUID) (ProjectView, error) {
	var pr store.Project
	if err := idb.NewSelect().Model(&pr).Where("id = ?", id).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectView{}, ErrNotFound
		}
		return ProjectView{}, err
	}
	var b store.RoleBinding
	if err := idb.NewSelect().Model(&b).Where("project_id = ? AND user_id = ?", id, p.UserID).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectView{}, ErrNotFound
		}
		return ProjectView{}, err
	}
	views, err := s.views(ctx, idb, []store.Project{pr}, map[uuid.UUID]string{id: b.Role})
	if err != nil {
		return ProjectView{}, err
	}
	return views[0], nil
}

func checkProjectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	var v validation
	if n := utf8.RuneCountInString(name); n == 0 || n > maxProjectNameRunes {
		v.body("#/name", fmt.Sprintf("项目名称为 1 到 %d 个字符", maxProjectNameRunes))
	}
	return name, v.err()
}

// Create creates a shared project owned by p.
func (s *Projects) Create(ctx context.Context, p *auth.Principal, name string) (ProjectView, error) {
	name, err := checkProjectName(name)
	if err != nil {
		return ProjectView{}, err
	}
	var view ProjectView
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := store.Now()
		pr := store.Project{ID: store.NewID(), Name: name, OwnerUserID: p.UserID, CreatedAt: now, UpdatedAt: now}
		if _, err := tx.NewInsert().Model(&pr).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewInsert().Model(&store.RoleBinding{
			ID: store.NewID(), ProjectID: pr.ID, UserID: p.UserID, Role: RoleOwner, CreatedAt: now, UpdatedAt: now,
		}).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "project.create", resourceKind: "project", resourceID: pr.ID.String(),
			details: map[string]any{"name": name},
		}); err != nil {
			return err
		}
		var err error
		view, err = s.view(ctx, tx, p, pr.ID)
		return err
	})
	return view, err
}

// Rename changes a project's name. Only owners may.
func (s *Projects) Rename(ctx context.Context, p *auth.Principal, id uuid.UUID, name string) (ProjectView, error) {
	if err := s.authz.Require(ctx, p, id, ActionManageProject); err != nil {
		return ProjectView{}, err
	}
	name, err := checkProjectName(name)
	if err != nil {
		return ProjectView{}, err
	}
	var view ProjectView
	err = s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var before store.Project
		if err := tx.NewSelect().Model(&before).Where("id = ?", id).Scan(ctx); err != nil {
			return err
		}
		if _, err := tx.NewUpdate().Model((*store.Project)(nil)).
			Set("name = ?", name).Set("updated_at = ?", store.Now()).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "project.update", resourceKind: "project", resourceID: id.String(),
			details: map[string]any{"name_before": before.Name, "name": name},
		}); err != nil {
			return err
		}
		var err error
		view, err = s.view(ctx, tx, p, id)
		return err
	})
	return view, err
}

// Members returns the members of a project, by username. Every member may
// list them.
func (s *Projects) Members(ctx context.Context, p *auth.Principal, id uuid.UUID, limitParam *int, cursor string) ([]Member, string, error) {
	if err := s.authz.Require(ctx, p, id, ActionViewTask); err != nil {
		return nil, "", err
	}
	var v validation
	limit := pageLimit(&v, limitParam)
	offset, err := decodeOffset(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	var users []store.User
	if err := s.db.NewSelect().Model(&users).
		Where("id IN (?)", s.db.NewSelect().Model((*store.RoleBinding)(nil)).Column("user_id").Where("project_id = ?", id)).
		OrderExpr(s.db.Native().Bytewise("lower(username)") + " ASC, id ASC").Offset(int64(offset)).Limit(int64(limit) + 1).Scan(ctx); err != nil {
		return nil, "", err
	}
	next := ""
	if len(users) > limit {
		users = users[:limit]
		next = encodeOffset(offset + limit)
	}
	out := make([]Member, 0, len(users))
	if len(users) == 0 {
		return out, next, nil
	}
	ids := make([]uuid.UUID, len(users))
	for i, u := range users {
		ids[i] = u.ID
	}
	var bindings []store.RoleBinding
	if err := s.db.NewSelect().Model(&bindings).
		Where("project_id = ? AND user_id IN (?)", id, bun.List(ids)).Scan(ctx); err != nil {
		return nil, "", err
	}
	byUser := map[uuid.UUID]store.RoleBinding{}
	for _, b := range bindings {
		byUser[b.UserID] = b
	}
	for _, u := range users {
		out = append(out, Member{Binding: byUser[u.ID], User: u})
	}
	return out, next, nil
}

// SetMember adds a user to a project or changes their role. Only owners
// may. A personal project has no members but its owner, and the last owner
// cannot be demoted (ErrLastOwner).
func (s *Projects) SetMember(ctx context.Context, p *auth.Principal, projectID, userID uuid.UUID, role string) (Member, error) {
	if err := s.authz.Require(ctx, p, projectID, ActionManageMembers); err != nil {
		return Member{}, err
	}
	if !ValidRole(role) {
		var v validation
		v.body("#/role", "角色只能是 viewer、maintainer 或 owner")
		return Member{}, v.err()
	}
	var m Member
	err := s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		pr, err := lockProject(ctx, tx, projectID)
		if err != nil {
			return err
		}
		var u store.User
		if err := tx.NewSelect().Model(&u).Where("id = ?", userID).Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if pr.IsPersonal && userID != pr.OwnerUserID {
			return &ValidationError{Content: true, Fields: []FieldError{{Parameter: "userId", Detail: "个人项目不能加入其他成员，请建立共享项目"}}}
		}
		now := store.Now()
		var b store.RoleBinding
		err = tx.NewSelect().Model(&b).Where("project_id = ? AND user_id = ?", projectID, userID).Scan(ctx)
		before := ""
		switch {
		case errors.Is(err, sql.ErrNoRows):
			if !u.DisabledAt.IsZero() {
				// Disabled accounts cannot be found when adding members
				// either; they keep roles they already had.
				return ErrNotFound
			}
			b = store.RoleBinding{ID: store.NewID(), ProjectID: projectID, UserID: userID, Role: role, CreatedAt: now, UpdatedAt: now}
			if _, err := tx.NewInsert().Model(&b).Exec(ctx); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			before = b.Role
			if before == RoleOwner && role != RoleOwner {
				if err := requireAnotherOwner(ctx, tx, projectID, userID); err != nil {
					return err
				}
			}
			if before != role {
				b.Role, b.UpdatedAt = role, now
				if _, err := tx.NewUpdate().Model((*store.RoleBinding)(nil)).
					Set("role = ?", role).Set("updated_at = ?", now).Where("id = ?", b.ID).Exec(ctx); err != nil {
					return err
				}
			}
		}
		m = Member{Binding: b, User: u}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "project.set_member", resourceKind: "project", resourceID: projectID.String(),
			details: map[string]any{"user_id": userID.String(), "username": u.Username, "role_before": nullable(before), "role": role},
		})
	})
	return m, err
}

// RemoveMember removes a user from a project. Owners may remove anyone and
// every member may remove themselves, but not the last owner
// (ErrLastOwner). Credentials the user granted to the project's tasks stay
// theirs, and so do the grants.
func (s *Projects) RemoveMember(ctx context.Context, p *auth.Principal, projectID, userID uuid.UUID) error {
	action := ActionManageMembers
	if userID == p.UserID {
		action = ActionViewTask
	}
	if err := s.authz.Require(ctx, p, projectID, action); err != nil {
		return err
	}
	return s.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if _, err := lockProject(ctx, tx, projectID); err != nil {
			return err
		}
		var b store.RoleBinding
		if err := tx.NewSelect().Model(&b).Where("project_id = ? AND user_id = ?", projectID, userID).Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if b.Role == RoleOwner {
			if err := requireAnotherOwner(ctx, tx, projectID, userID); err != nil {
				return err
			}
		}
		if _, err := tx.NewDelete().Model((*store.RoleBinding)(nil)).Where("id = ?", b.ID).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "project.remove_member", resourceKind: "project", resourceID: projectID.String(),
			details: map[string]any{"user_id": userID.String(), "role_before": b.Role},
		})
	})
}

// lockProject loads a project and takes its row lock for the rest of the
// transaction. Member changes of one project then run one at a time on
// PostgreSQL, so two owners demoting each other cannot both pass the
// last-owner check; SQLite serialises all writers anyway. A plain update
// is used because SELECT ... FOR UPDATE does not exist in SQLite.
func lockProject(ctx context.Context, tx bun.Tx, id uuid.UUID) (store.Project, error) {
	res, err := tx.NewUpdate().Model((*store.Project)(nil)).Set("updated_at = updated_at").Where("id = ?", id).Exec(ctx)
	if err != nil {
		return store.Project{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.Project{}, ErrNotFound
	}
	var pr store.Project
	err = tx.NewSelect().Model(&pr).Where("id = ?", id).Scan(ctx)
	return pr, err
}

// requireAnotherOwner returns ErrLastOwner unless the project has an owner
// other than userID.
func requireAnotherOwner(ctx context.Context, tx bun.IDB, projectID, userID uuid.UUID) error {
	others, err := tx.NewSelect().Model((*store.RoleBinding)(nil)).
		Where("project_id = ? AND role = ? AND user_id <> ?", projectID, RoleOwner, userID).Count(ctx)
	if err != nil {
		return err
	}
	if others == 0 {
		return ErrLastOwner
	}
	return nil
}

// SearchUsers finds enabled users whose username or display name starts
// with q, case-insensitively, for adding members. Any signed-in user may
// search; only the username and display name are returned.
func (s *Projects) SearchUsers(ctx context.Context, p *auth.Principal, q string, limitParam *int, cursor string) ([]store.User, string, error) {
	if p == nil {
		return nil, "", ErrUnauthenticated
	}
	var v validation
	limit := pageLimit(&v, limitParam)
	offset, err := decodeOffset(cursor)
	if err != nil {
		v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
	}
	q = strings.TrimSpace(q)
	if q == "" {
		v.param("q", "请输入要查找的名字开头")
	}
	if err := v.err(); err != nil {
		return nil, "", err
	}
	pattern := likeEscape(strings.ToLower(q)) + "%"
	var users []store.User
	if err := s.db.NewSelect().Model(&users).Column("id", "username", "display_name").
		Where("disabled_at IS NULL").
		WhereGroup(" AND ", func(sq *bun.SelectQuery) *bun.SelectQuery {
			return sq.Where(`lower(username) LIKE ? ESCAPE '\'`, pattern).
				WhereOr(`lower(display_name) LIKE ? ESCAPE '\'`, pattern)
		}).
		OrderExpr(s.db.Native().Bytewise("lower(username)") + " ASC, id ASC").Offset(int64(offset)).Limit(int64(limit) + 1).Scan(ctx); err != nil {
		return nil, "", err
	}
	next := ""
	if len(users) > limit {
		users = users[:limit]
		next = encodeOffset(offset + limit)
	}
	return users, next, nil
}

// likeEscape escapes the LIKE wildcards in s for ESCAPE '\'.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func usersByID(ctx context.Context, idb bun.IDB, ids []uuid.UUID) (map[uuid.UUID]store.User, error) {
	out := map[uuid.UUID]store.User{}
	if len(ids) == 0 {
		return out, nil
	}
	var users []store.User
	if err := idb.NewSelect().Model(&users).Where("id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("load users: %w", err)
	}
	for _, u := range users {
		out[u.ID] = u
	}
	return out, nil
}

// Offset cursors page lists that are sorted by name rather than by ID.
// They are opaque to clients like the ID cursors.
func encodeOffset(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte("o" + strconv.Itoa(n)))
}

func decodeOffset(s string) (int, error) {
	if s == "" {
		return 0, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) < 2 || b[0] != 'o' {
		return 0, errors.New("invalid cursor")
	}
	n, err := strconv.Atoi(string(b[1:]))
	if err != nil || n < 0 {
		return 0, errors.New("invalid cursor")
	}
	return n, nil
}
