package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/niltavi/lerryn/skills"
)

// The embedded skills are served anonymously, with their files, at the
// server's version.
func TestSkills(t *testing.T) {
	h := New(Options{PublicURL: "http://localhost:8080", Version: "v0.2.0"})
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assertMatchesSpec(t, req, rec)
		return rec
	}

	rec := get("/api/v1/skills")
	if rec.Code != http.StatusOK {
		t.Fatalf("listSkills = %d: %s", rec.Code, rec.Body)
	}
	var list SkillList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range list.Items {
		names = append(names, s.Name)
		if !slices.Contains(s.Files, "SKILL.md") {
			t.Errorf("%s files = %v", s.Name, s.Files)
		}
	}
	if list.Version != "v0.2.0" || !slices.Equal(names, skills.Names) {
		t.Fatalf("list = %+v", list)
	}

	rec = get("/api/v1/skills/lerryn-automation")
	if rec.Code != http.StatusOK {
		t.Fatalf("getSkill = %d: %s", rec.Code, rec.Body)
	}
	var s Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	want, _ := skills.ReadFile("lerryn-automation", "SKILL.md")
	var paths []string
	for _, f := range s.Files {
		paths = append(paths, f.Path)
		if f.Path == "SKILL.md" && f.Content != string(want) {
			t.Error("SKILL.md content differs from the embedded file")
		}
	}
	if s.Version != "v0.2.0" || s.Name != "lerryn-automation" || !slices.Equal(paths, list.Items[0].Files) {
		t.Fatalf("skill = %s %s %v", s.Name, s.Version, paths)
	}

	for _, name := range []string{"nope", "lerryn-automation..", "Lerryn-Automation"} {
		rec = get("/api/v1/skills/" + name)
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("getSkill(%q) = %d", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "description") {
			t.Errorf("getSkill(%q) leaked content", name)
		}
	}
}
