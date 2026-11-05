package service_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestApplyCreatesTaskAndVersions(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Description = "每天汇总"
	m.Params = map[string]any{"source": "crm"}

	first := f.apply(alice, "digest", m)
	if first.Task.Name != "digest" || first.Task.ProjectID != alice.PersonalProjectID || first.Task.LatestSeq != 1 || first.Task.Description != "每天汇总" {
		t.Fatalf("task = %+v", first.Task)
	}
	if first.Version.Version.Seq != 1 || first.Version.Manifest.Params["source"] != "crm" {
		t.Fatalf("version = %+v", first.Version)
	}
	file := first.Version.Files[0]
	content := []byte("print('hello')\n")
	if file.Path != "main.py" || file.Size != int64(len(content)) || file.BlobHash != blob.Sum(content) {
		t.Fatalf("file = %+v", file)
	}
	if got, err := blob.ReadAll(f.ctx, f.blobs, file.BlobHash); err != nil || string(got) != string(content) {
		t.Fatalf("blob = %q, %v", got, err)
	}

	m.Description = ""
	second := f.apply(alice, "digest", m,
		service.FileInput{Path: "src/util.py", Content: []byte("X = 1\n")},
		service.FileInput{Path: "main.py", Content: []byte("import src.util\n"), Executable: true},
	)
	if second.Task.ID != first.Task.ID || second.Task.LatestSeq != 2 || second.Version.Version.Seq != 2 || second.Task.Description != "" {
		t.Fatalf("second = %+v", second)
	}
	if second.Version.Version.TreeHash == first.Version.Version.TreeHash {
		t.Fatal("different trees share a tree hash")
	}

	got, err := f.tasks.Get(f.ctx, alice, first.Task.ID)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{}
	for _, file := range got.Version.Files {
		paths = append(paths, file.Path)
	}
	if got.Version.Version.Seq != 2 || !slices.Equal(paths, []string{"main.py", "src/util.py"}) || !got.Version.Files[0].Executable {
		t.Fatalf("get = %+v, files %v", got.Version.Version, got.Version.Files)
	}
	if got.Version.Manifest.Params["source"] != "crm" || got.Version.Manifest.Entrypoint[1] != "main.py" {
		t.Fatalf("manifest = %+v", got.Version.Manifest)
	}

	n, err := f.db.NewSelect().Model((*store.AuditEvent)(nil)).Where("action = ? AND resource_id = ?", "task.apply", first.Task.ID.String()).Count(f.ctx)
	if err != nil || n != 2 {
		t.Fatalf("task.apply audit events = %d, %v", n, err)
	}

	// The same tree in a new version hashes the same.
	third := f.apply(alice, "digest", m,
		service.FileInput{Path: "main.py", Content: []byte("import src.util\n"), Executable: true},
		service.FileInput{Path: "src/util.py", Content: []byte("X = 1\n")},
	)
	if third.Version.Version.TreeHash != second.Version.Version.TreeHash {
		t.Fatal("tree hash depends on file order")
	}
}

func TestApplyAcceptsLockfileAndLimits(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	m := basicManifest()
	m.Runtime.Lockfile = "uv.lock"
	cpu := 0.1
	m.Limits = &service.Limits{Timeout: "24h", CPU: &cpu, Memory: "64Mi"}
	m.Stages = []service.StageSpec{{ID: "fetch", Name: "获取"}, {ID: "save", Name: "保存", After: []string{"fetch"}}}
	m.Network, m.Concurrency = service.NetworkNone, service.ConcurrencyAllow
	f.apply(alice, "deps", m,
		service.FileInput{Path: "main.py", Content: []byte("import httpx\n")},
		service.FileInput{Path: "pyproject.toml", Content: []byte("[project]\nname = \"x\"\n")},
		service.FileInput{Path: "uv.lock", Content: []byte("version = 1\n")},
	)
}

func TestApplyAcceptsNodeAndShell(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	node := basicManifest()
	node.Runtime = service.RuntimeSpec{Language: "node", Version: "22", Lockfile: "package-lock.json"}
	node.Entrypoint = []string{"node", "main.ts"}
	f.apply(alice, "node-deps", node,
		service.FileInput{Path: "main.ts", Content: []byte("console.log(1)\n")},
		service.FileInput{Path: "package.json", Content: []byte(`{"name":"x"}`)},
		service.FileInput{Path: "package-lock.json", Content: []byte(`{"lockfileVersion":3}`)},
	)
	shell := basicManifest()
	shell.Runtime = service.RuntimeSpec{Language: "shell", Version: "5"}
	shell.Entrypoint = []string{"bash", "main.sh"}
	f.apply(alice, "shell", shell, service.FileInput{Path: "main.sh", Content: []byte("echo hi\n")})
}

func TestApplyValidation(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	tooMany := make([]service.FileInput, 101)
	for i := range tooMany {
		tooMany[i] = service.FileInput{Path: "f" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".py"}
	}
	cpu := func(v float64) *float64 { return &v }
	big := strings.Repeat("x", 600<<10)

	tests := []struct {
		name     string
		task     string
		manifest func(*service.Manifest)
		files    []service.FileInput
		want     string
	}{
		{"task name", "Bad_Name", nil, nil, "?taskName"},
		{"language", "", func(m *service.Manifest) { m.Runtime.Language = "ruby" }, nil, "#/manifest/runtime/language"},
		{"version", "", func(m *service.Manifest) { m.Runtime.Version = "3.11" }, nil, "#/manifest/runtime/version"},
		{"version of another runtime", "", func(m *service.Manifest) { m.Runtime.Language = "node" }, nil, "#/manifest/runtime/version"},
		{"lockfile name", "", func(m *service.Manifest) { m.Runtime.Lockfile = "poetry.lock" }, nil, "#/manifest/runtime/lockfile"},
		{"lockfile files", "", func(m *service.Manifest) { m.Runtime.Lockfile = "uv.lock" },
			[]service.FileInput{{Path: "main.py"}, {Path: "uv.lock"}}, "#/files"},
		{"npm lockfile on python", "", func(m *service.Manifest) { m.Runtime.Lockfile = "package-lock.json" }, nil, "#/manifest/runtime/lockfile"},
		{"uv lockfile on node", "", func(m *service.Manifest) {
			m.Runtime = service.RuntimeSpec{Language: "node", Version: "22", Lockfile: "uv.lock"}
		},
			nil, "#/manifest/runtime/lockfile"},
		{"npm lockfile files", "", func(m *service.Manifest) {
			m.Runtime = service.RuntimeSpec{Language: "node", Version: "22", Lockfile: "package-lock.json"}
		}, []service.FileInput{{Path: "main.ts"}, {Path: "package-lock.json"}}, "#/files"},
		{"lockfile on shell", "", func(m *service.Manifest) {
			m.Runtime = service.RuntimeSpec{Language: "shell", Version: "5", Lockfile: "uv.lock"}
		},
			nil, "#/manifest/runtime/lockfile"},
		{"shell version", "", func(m *service.Manifest) { m.Runtime = service.RuntimeSpec{Language: "shell", Version: "4"} }, nil, "#/manifest/runtime/version"},
		{"empty entrypoint", "", func(m *service.Manifest) { m.Entrypoint = nil }, nil, "#/manifest/entrypoint"},
		{"empty argument", "", func(m *service.Manifest) { m.Entrypoint = []string{"python", ""} }, nil, "#/manifest/entrypoint/1"},
		{"description", "", func(m *service.Manifest) { m.Description = strings.Repeat("说", 501) }, nil, "#/manifest/description"},
		{"timeout too long", "", func(m *service.Manifest) { m.Limits = &service.Limits{Timeout: "25h"} }, nil, "#/manifest/limits/timeout"},
		{"timeout format", "", func(m *service.Manifest) { m.Limits = &service.Limits{Timeout: "1d"} }, nil, "#/manifest/limits/timeout"},
		{"timeout overflow", "", func(m *service.Manifest) { m.Limits = &service.Limits{Timeout: "99999999999999999999h"} }, nil, "#/manifest/limits/timeout"},
		{"memory too small", "", func(m *service.Manifest) { m.Limits = &service.Limits{Memory: "63Mi"} }, nil, "#/manifest/limits/memory"},
		{"memory too large", "", func(m *service.Manifest) { m.Limits = &service.Limits{Memory: "17Gi"} }, nil, "#/manifest/limits/memory"},
		{"memory format", "", func(m *service.Manifest) { m.Limits = &service.Limits{Memory: "1G"} }, nil, "#/manifest/limits/memory"},
		{"cpu too small", "", func(m *service.Manifest) { m.Limits = &service.Limits{CPU: cpu(0.05)} }, nil, "#/manifest/limits/cpu"},
		{"cpu too large", "", func(m *service.Manifest) { m.Limits = &service.Limits{CPU: cpu(8.5)} }, nil, "#/manifest/limits/cpu"},
		{"network", "", func(m *service.Manifest) { m.Network = "intranet" }, nil, "#/manifest/network"},
		{"concurrency", "", func(m *service.Manifest) { m.Concurrency = "queue" }, nil, "#/manifest/concurrency"},
		{"params", "", func(m *service.Manifest) { m.Params = map[string]any{"blob": strings.Repeat("x", 16<<10)} }, nil, "#/manifest/params"},
		{"stage id", "", func(m *service.Manifest) { m.Stages = []service.StageSpec{{ID: "Fetch", Name: "x"}} }, nil, "#/manifest/stages/0/id"},
		{"stage name", "", func(m *service.Manifest) { m.Stages = []service.StageSpec{{ID: "fetch"}} }, nil, "#/manifest/stages/0/name"},
		{"duplicate stage", "", func(m *service.Manifest) {
			m.Stages = []service.StageSpec{{ID: "a", Name: "A"}, {ID: "a", Name: "A2"}}
		}, nil, "#/manifest/stages/1/id"},
		{"unknown after", "", func(m *service.Manifest) {
			m.Stages = []service.StageSpec{{ID: "a", Name: "A", After: []string{"missing"}}}
		}, nil, "#/manifest/stages/0/after/0"},
		{"self after", "", func(m *service.Manifest) {
			m.Stages = []service.StageSpec{{ID: "a", Name: "A", After: []string{"a"}}}
		}, nil, "#/manifest/stages/0/after/0"},
		{"cycle", "", func(m *service.Manifest) {
			m.Stages = []service.StageSpec{
				{ID: "a", Name: "A", After: []string{"c"}},
				{ID: "b", Name: "B", After: []string{"a"}},
				{ID: "c", Name: "C", After: []string{"b"}},
				{ID: "d", Name: "D"},
			}
		}, nil, "#/manifest/stages"},
		{"no files", "", nil, []service.FileInput{}, "#/files"},
		{"too many files", "", nil, tooMany, "#/files"},
		{"too large", "", nil, []service.FileInput{{Path: "a.py", Content: []byte(big)}, {Path: "b.py", Content: []byte(big)}}, "#/files"},
		{"absolute path", "", nil, []service.FileInput{{Path: "/main.py"}}, "#/files/0/path"},
		{"parent segment", "", nil, []service.FileInput{{Path: "main.py"}, {Path: "a/../main.py"}}, "#/files/1/path"},
		{"dot segment", "", nil, []service.FileInput{{Path: "./main.py"}}, "#/files/0/path"},
		{"empty segment", "", nil, []service.FileInput{{Path: "a//main.py"}}, "#/files/0/path"},
		{"trailing slash", "", nil, []service.FileInput{{Path: "src/"}}, "#/files/0/path"},
		{"backslash", "", nil, []service.FileInput{{Path: `src\main.py`}}, "#/files/0/path"},
		{"long path", "", nil, []service.FileInput{{Path: strings.Repeat("a", 256)}}, "#/files/0/path"},
		{"duplicate path", "", nil, []service.FileInput{{Path: "main.py"}, {Path: "main.py"}}, "#/files/1/path"},
		{"file and directory", "", nil, []service.FileInput{{Path: "src"}, {Path: "src/main.py"}}, "#/files/0/path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := basicManifest()
			if tt.manifest != nil {
				tt.manifest(&m)
			}
			files := tt.files
			if files == nil {
				files = mainPy()
			}
			name := tt.task
			if name == "" {
				name = "valid-name"
			}
			_, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: name, Manifest: m, Files: files})
			fields, content := fieldErrors(t, err)
			if _, ok := fields[tt.want]; !ok {
				t.Fatalf("no error at %s in %v", tt.want, fields)
			}
			if len(fields) != 1 {
				t.Errorf("want one error, got %v", fields)
			}
			// Only the path parameter is a format error; the rest are
			// content errors answered with 422.
			if wantContent := tt.want != "?taskName"; content != wantContent {
				t.Errorf("Content = %v, want %v", content, wantContent)
			}
		})
	}
	if n, err := f.db.NewSelect().Model((*store.Task)(nil)).Count(f.ctx); err != nil || n != 0 {
		t.Fatalf("invalid submissions stored %d tasks, %v", n, err)
	}
}

// Of concurrent submissions built on the same version, exactly one
// succeeds and the others learn which version they lost to (A12).
func TestApplyConcurrentSubmissions(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	const writers = 8
	race := func(ifMatch *int) ([]service.ApplyResult, []error) {
		var wg sync.WaitGroup
		results := make([]service.ApplyResult, writers)
		errs := make([]error, writers)
		for i := range writers {
			wg.Go(func() {
				results[i], errs[i] = f.tasks.Apply(context.Background(), alice, service.ApplyTaskInput{
					ProjectID: alice.PersonalProjectID, Name: "racy", Manifest: basicManifest(), IfMatch: ifMatch,
					Files: []service.FileInput{{Path: "main.py", Content: []byte(strconv.Itoa(i))}},
				})
			})
		}
		wg.Wait()
		return results, errs
	}
	winner := func(results []service.ApplyResult, errs []error, wantVersion int) service.ApplyResult {
		t.Helper()
		var won []service.ApplyResult
		for i, err := range errs {
			var conflict *service.VersionConflictError
			switch {
			case err == nil:
				won = append(won, results[i])
			case errors.As(err, &conflict):
				if conflict.Current != wantVersion {
					t.Errorf("conflict names version %d, want %d", conflict.Current, wantVersion)
				}
			case errors.Is(err, service.ErrPreconditionRequired) && wantVersion == 1:
				// A creator that read the task after the winner committed
				// is told to name the version it builds on.
			default:
				t.Errorf("submission %d: %v", i, err)
			}
		}
		if len(won) != 1 || won[0].Version != wantVersion {
			t.Fatalf("winners = %+v, want one with version %d", won, wantVersion)
		}
		return won[0]
	}

	results, errs := race(nil)
	created := winner(results, errs, 1)
	if !created.Created {
		t.Fatalf("first version = %+v", created)
	}
	one := 1
	results, errs = race(&one)
	second := winner(results, errs, 2)
	if second.Task.Task.ID != created.Task.Task.ID {
		t.Fatal("concurrent submissions created two tasks")
	}
	got, err := f.tasks.Get(f.ctx, alice, created.Task.Task.ID)
	if err != nil || got.Task.LatestSeq != 2 {
		t.Fatalf("latest = %d, %v", got.Task.LatestSeq, err)
	}
}

func TestTaskAccess(t *testing.T) {
	f := newFixture(t)
	alice, bob, viewer := f.user("alice"), f.user("bob"), f.user("viewer")
	f.grant(alice.PersonalProjectID, viewer, service.RoleViewer)
	task := f.apply(alice, "private", basicManifest())

	_, err := f.tasks.Get(f.ctx, bob, task.Task.ID)
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.Get(f.ctx, alice, store.NewID())
	wantErr(t, err, service.ErrNotFound)
	_, err = f.tasks.Apply(f.ctx, bob, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "private", Manifest: basicManifest(), Files: mainPy()})
	wantErr(t, err, service.ErrNotFound)
	// The permission check comes before validation, so an outsider learns
	// nothing from a bad submission either.
	_, err = f.tasks.Apply(f.ctx, bob, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "Bad"})
	wantErr(t, err, service.ErrNotFound)

	if _, err := f.tasks.Get(f.ctx, viewer, task.Task.ID); err != nil {
		t.Fatalf("viewer get: %v", err)
	}
	_, err = f.tasks.Apply(f.ctx, viewer, service.ApplyTaskInput{ProjectID: alice.PersonalProjectID, Name: "private", Manifest: basicManifest(), Files: mainPy()})
	wantErr(t, err, service.ErrForbidden)
}

func TestRunSpecDefaults(t *testing.T) {
	m := basicManifest()
	spec, err := m.RunSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Timeout.String() != "10m0s" || spec.MilliCPUs != 1000 || spec.MemoryBytes != 1<<30 || spec.Network != "full" {
		t.Fatalf("defaults = %+v", spec)
	}
	cpu := 0.25
	m.Limits = &service.Limits{Timeout: "90s", CPU: &cpu, Memory: "512Mi"}
	m.Network = service.NetworkNone
	m.Runtime.Lockfile = "uv.lock"
	m.Stages = []service.StageSpec{{ID: "a", Name: "A"}, {ID: "b", Name: "B", After: []string{"a"}}}
	spec, err = m.RunSpec()
	if err != nil {
		t.Fatal(err)
	}
	if spec.Timeout.String() != "1m30s" || spec.MilliCPUs != 250 || spec.MemoryBytes != 512<<20 || spec.Network != "none" ||
		spec.Runtime.Lockfile != "uv.lock" || len(spec.Stages) != 2 || spec.Stages[1].After[0] != "a" {
		t.Fatalf("spec = %+v", spec)
	}
}
