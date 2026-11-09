package service_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestTaskTagPermissions(t *testing.T) {
	f := newFixture(t)
	owner, maintainer, viewer, stranger := f.user("owner"), f.user("maintainer"), f.user("viewer"), f.user("stranger")
	f.grant(owner.PersonalProjectID, maintainer, service.RoleMaintainer)
	f.grant(owner.PersonalProjectID, viewer, service.RoleViewer)
	task := f.apply(owner, "digest", basicManifest())
	id := task.Task.ID

	for _, tc := range []struct {
		name string
		p    *auth.Principal
		want error
	}{
		{"stranger", stranger, service.ErrNotFound},
		{"viewer", viewer, service.ErrPermissionDenied},
		{"maintainer", maintainer, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.tasks.SetTags(f.ctx, tc.p, id, []string{"日报"}); !errors.Is(err, tc.want) {
				t.Fatalf("SetTags = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := f.tasks.Archive(f.ctx, owner, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.tasks.SetTags(f.ctx, owner, id, []string{}); !errors.Is(err, service.ErrTaskArchived) {
		t.Fatalf("SetTags on archived task = %v", err)
	}
	archived, err := f.tasks.Get(f.ctx, viewer, id)
	if err != nil || !slices.Equal(archived.Tags, []string{"日报"}) {
		t.Fatalf("archived tags = %v, %v", archived.Tags, err)
	}
	audits, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).
		Where("action = ? AND resource_id = ?", "task.tags", id.String()).Count(f.ctx)
	if err != nil || audits != 1 {
		t.Fatalf("task.tags audits = %d, %v", audits, err)
	}
}

func TestTaskTagsValidateAndSurviveNewVersions(t *testing.T) {
	f := newFixture(t)
	owner := f.user("owner")
	task := f.apply(owner, "digest", basicManifest())
	id := task.Task.ID
	tag := strings.Repeat("中", 40)
	want := []string{"CRM", tag}
	if _, err := f.tasks.SetTags(f.ctx, owner, id, want); err != nil {
		t.Fatal(err)
	}
	for _, tags := range [][]string{{""}, {" CRM"}, {"A\u0000B"}, {"A\u2028B"}, {"a", "a"}, {tag + "中"}, make([]string, 21)} {
		var verr *service.ValidationError
		if _, err := f.tasks.SetTags(f.ctx, owner, id, tags); !errors.As(err, &verr) || !verr.Content {
			t.Fatalf("tags %q = %v, want content validation error", tags, err)
		}
	}
	caseMismatch := "crm"
	page, err := f.tasks.List(f.ctx, owner, service.TaskListInput{Tag: &caseMismatch})
	if err != nil || len(page.Tasks) != 0 {
		t.Fatalf("case-sensitive filter = %+v, %v", page, err)
	}
	second := f.apply(owner, "digest", basicManifest())
	if !slices.Equal(second.Tags, want) || second.Task.LatestSeq != 2 {
		t.Fatalf("tags = %v, version = %d", second.Tags, second.Task.LatestSeq)
	}
}

func TestTaskTagsRejectCredentialValues(t *testing.T) {
	f := newPlaintextFixture(t)
	_, err := f.tasks.SetTags(f.ctx, f.agent, f.task.Task.ID, []string{testSecret})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/tags/0")
	got, err := f.tasks.Get(f.ctx, f.alice, f.task.Task.ID)
	if err != nil || len(got.Tags) != 0 {
		t.Fatalf("reading task after rejection returned %d tags: %v", len(got.Tags), err)
	}
}
