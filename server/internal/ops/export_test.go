package ops

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

// TestExportImportRoundTrip moves a populated instance between a SQLite
// file and the database selected by LERRYN_TEST_DATABASE, in both
// directions. Under `make test-pg` this is SQLite to PostgreSQL and back.
func TestExportImportRoundTrip(t *testing.T) {
	newSQLite := func(t *testing.T) *store.DB {
		db := openSQLite(t, filepath.Join(t.TempDir(), "lerryn.db"))
		return db
	}
	newSelected := func(t *testing.T) *store.DB { return storetest.OpenEmpty(t) }
	tests := []struct {
		name     string
		src, dst func(*testing.T) *store.DB
	}{
		{"sqlite to selected", newSQLite, newSelected},
		{"selected to sqlite", newSelected, newSQLite},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			src := tt.src(t)
			if _, err := store.Migrate(ctx, src); err != nil {
				t.Fatal(err)
			}
			srcBlobs := newLocal(t, t.TempDir())
			content := seed(t, src, srcBlobs)
			want := readSnapshot(t, src)

			archive := filepath.Join(t.TempDir(), "export.tar.gz")
			m, err := Export(ctx, ExportOptions{DB: src, Blobs: srcBlobs, Output: archive})
			if err != nil {
				t.Fatalf("Export: %v", err)
			}
			if m.Blobs != 1 || len(m.MissingBlobs) != 0 {
				t.Fatalf("manifest blobs = %d, missing %v", m.Blobs, m.MissingBlobs)
			}
			for _, tbl := range m.Tables {
				if tbl.Name == "leases" || strings.HasPrefix(tbl.Name, "goose") {
					t.Errorf("exported excluded table %s", tbl.Name)
				}
			}

			dst := tt.dst(t) // empty, not migrated: Import prepares it
			dstBlobs := newLocal(t, t.TempDir())
			res, err := Import(ctx, ImportOptions{DB: dst, Blobs: dstBlobs, Input: archive})
			if err != nil {
				t.Fatalf("Import: %v", err)
			}
			if !reflect.DeepEqual(res.Tables, m.Tables) {
				t.Fatalf("imported tables %+v, exported %+v", res.Tables, m.Tables)
			}
			if err := store.CheckMigrations(ctx, dst); err != nil {
				t.Fatalf("target schema after import: %v", err)
			}
			if got := readSnapshot(t, dst); !reflect.DeepEqual(got, want) {
				t.Fatalf("imported rows differ:\n got %+v\nwant %+v", got, want)
			}
			srcCounts, err := TableCounts(ctx, src)
			if err != nil {
				t.Fatal(err)
			}
			dstCounts, err := TableCounts(ctx, dst)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(srcCounts, dstCounts) {
				t.Fatalf("row counts: source %v, target %v", srcCounts, dstCounts)
			}
			data, err := blob.ReadAll(ctx, dstBlobs, want.Files[0].BlobHash)
			if err != nil || string(data) != string(content) {
				t.Fatalf("imported blob = %q, %v", data, err)
			}

			// The imported database keeps working: timestamps still compare
			// and sort, so the queue query finds the queued run.
			var queued []store.Run
			if err := dst.NewSelect().Model(&queued).Where("status = ?", store.RunQueued).
				Where("queued_at > ?", want.Runs[0].QueuedAt).Scan(ctx); err != nil || len(queued) != 1 {
				t.Fatalf("queued runs after import = %d, %v", len(queued), err)
			}

			// Importing again would duplicate everything.
			_, err = Import(ctx, ImportOptions{DB: dst, Blobs: dstBlobs, Input: archive})
			if !errors.Is(err, ErrTargetHasData) {
				t.Fatalf("second Import: %v, want ErrTargetHasData", err)
			}
		})
	}
}

func TestExportRecordsMissingBlobs(t *testing.T) {
	ctx := context.Background()
	db := openSQLite(t, filepath.Join(t.TempDir(), "lerryn.db"))
	if _, err := store.Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	seed(t, db, newLocal(t, t.TempDir()))
	// Export against an empty store: the rows refer to an object it lacks.
	m, err := Export(ctx, ExportOptions{DB: db, Blobs: newLocal(t, t.TempDir()), Output: filepath.Join(t.TempDir(), "e.tar.gz")})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if m.Blobs != 0 || len(m.MissingBlobs) != 1 {
		t.Fatalf("blobs = %d, missing = %v", m.Blobs, m.MissingBlobs)
	}
}

func TestSortByReferences(t *testing.T) {
	tables := []dialect.Table{
		{Name: "runs", References: []string{"projects", "tasks"}},
		{Name: "projects", References: []string{"users"}},
		{Name: "tasks", References: []string{"projects", "tasks"}},
		{Name: "users"},
		{Name: "audit_events"},
		{Name: "leases_ref", References: []string{"leases"}},
	}
	sorted, err := sortByReferences(tables)
	if err != nil {
		t.Fatal(err)
	}
	pos := map[string]int{}
	for i, s := range sorted {
		pos[s.Name] = i
	}
	for _, edge := range [][2]string{{"users", "projects"}, {"projects", "tasks"}, {"tasks", "runs"}} {
		if pos[edge[0]] >= pos[edge[1]] {
			t.Errorf("%s must come before %s: %v", edge[0], edge[1], pos)
		}
	}
	if len(sorted) != len(tables) {
		t.Errorf("sorted %d tables, want %d", len(sorted), len(tables))
	}

	_, err = sortByReferences([]dialect.Table{{Name: "a", References: []string{"b"}}, {Name: "b", References: []string{"a"}}})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("cycle not reported: %v", err)
	}
}

func TestValueEncoding(t *testing.T) {
	boolCol := dialect.Column{Name: "is_admin", Type: "boolean"}
	textCol := dialect.Column{Name: "name", Type: "text"}
	tests := []struct {
		name   string
		raw    any
		col    dialect.Column
		sqlite bool
		want   any
	}{
		{"sqlite int to pg boolean", jsonNumber("1"), boolCol, false, true},
		{"sqlite zero to pg boolean", jsonNumber("0"), boolCol, false, false},
		{"pg boolean to sqlite", true, textCol, true, int64(1)},
		{"pg false to sqlite", false, textCol, true, int64(0)},
		{"integer", jsonNumber("42"), textCol, false, int64(42)},
		{"null", nil, boolCol, false, nil},
		{"text", "x", textCol, true, "x"},
		{"bytes", map[string]any{bytesKey: "aGk="}, textCol, false, []byte("hi")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodeValue(tt.raw, tt.col, tt.sqlite)
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("decodeValue = %#v, %v; want %#v", got, err, tt.want)
			}
		})
	}
	if _, err := decodeValue("yes", boolCol, false); err == nil {
		t.Error("text accepted for a boolean column")
	}
	if _, err := encodeValue(struct{}{}); err == nil {
		t.Error("unknown type accepted")
	}
}

func jsonNumber(s string) json.Number { return json.Number(s) }

// Objects named by run and artifact columns travel with the export:
// archived logs (log_blob, and log_archive for logs archived before
// log_blob existed), webhook inputs and live artifacts. An expired
// artifact's object is not needed and not reported missing.
func TestExportImportRunObjects(t *testing.T) {
	ctx := context.Background()
	src := storetest.Open(t)
	srcBlobs := newLocal(t, t.TempDir())
	seed(t, src, srcBlobs)
	var runs []store.Run
	if err := src.NewSelect().Model(&runs).Order("queued_at").Scan(ctx); err != nil || len(runs) != 2 {
		t.Fatalf("seeded runs = %d, %v", len(runs), err)
	}
	done, queued := runs[0], runs[1]

	put := func(content string) string {
		t.Helper()
		h, err := srcBlobs.Put(ctx, strings.NewReader(content))
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	log, legacyLog, input, artifact := put("archived log"), put("log archived before log_blob"), put(`{"webhook":"body"}`), put("report.csv")
	expired := strings.Repeat("e", 64) // retention deleted the object
	legacy := store.Run{ID: store.NewID(), ProjectID: done.ProjectID, TaskID: done.TaskID, VersionID: done.VersionID, VersionSeq: 1,
		Status: store.RunCompleted, TriggerKind: "manual", Params: "{}", QueuedAt: done.QueuedAt.Add(-time.Hour),
		FinishedAt: done.QueuedAt, LogArchive: `{"blob":"` + legacyLog + `","lines":1,"frames":[]}`}
	if _, err := src.NewInsert().Model(&legacy).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := src.NewUpdate().Model((*store.Run)(nil)).Set("log_blob = ?", log).
		Set("log_archive = ?", `{"blob":"`+log+`","lines":2,"frames":[]}`).Where("id = ?", done.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := src.NewUpdate().Model((*store.Run)(nil)).Set("input_key = ?", input).Where("id = ?", queued.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	now := store.Now()
	for _, a := range []store.Artifact{
		{ID: store.NewID(), RunID: done.ID, Path: "report.csv", Size: 10, ContentType: "text/csv", SHA256: artifact, BlobKey: artifact, CreatedAt: now},
		{ID: store.NewID(), RunID: done.ID, Path: "old.csv", Size: 10, ContentType: "text/csv", SHA256: expired, BlobKey: expired, CreatedAt: now, ExpiredAt: now},
	} {
		if _, err := src.NewInsert().Model(&a).Exec(ctx); err != nil {
			t.Fatal(err)
		}
	}

	archive := filepath.Join(t.TempDir(), "export.tar.gz")
	m, err := Export(ctx, ExportOptions{DB: src, Blobs: srcBlobs, Output: archive})
	if err != nil {
		t.Fatal(err)
	}
	if m.Blobs != 5 || len(m.MissingBlobs) != 0 {
		t.Fatalf("manifest blobs = %d, missing %v; want the task file, two logs, the input and the live artifact", m.Blobs, m.MissingBlobs)
	}
	dst := openSQLite(t, filepath.Join(t.TempDir(), "target.db"))
	dstBlobs := newLocal(t, t.TempDir())
	if _, err := Import(ctx, ImportOptions{DB: dst, Blobs: dstBlobs, Input: archive}); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]string{log: "archived log", legacyLog: "log archived before log_blob", input: `{"webhook":"body"}`, artifact: "report.csv"} {
		if got, err := blob.ReadAll(ctx, dstBlobs, hash); err != nil || string(got) != want {
			t.Errorf("object %s on the target = %q, %v", hash[:8], got, err)
		}
	}
}
