package ops

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/schema"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

// Layout of an export archive: the manifest, then the objects, then one
// NDJSON file per table in foreign key order. Objects come before tables
// so that an import stores every object before any row refers to it.
// Internal archive names and JSON keys are a persisted format. Keep the
// original labels so backups and exports survive product renames.
const (
	exportFormat        = "waleng-export"
	exportFormatVersion = 1
	exportManifestName  = "waleng-export.json"
	exportBlobPrefix    = "blobs/sha256/"
	exportTablePrefix   = "tables/"
	exportTableSuffix   = ".ndjson"
)

// excludedTables are not exported. The goose version table belongs to
// the target's own migrations; leases are coordination state of running
// servers, such as the leader lease, and mean nothing elsewhere.
var excludedTables = []string{goose.DefaultTablename, "leases"}

// blobColumn names the columns that hold object store hashes in any
// table, such as task and tool files. Objects referenced by any such
// column are exported with the rows.
const blobColumn = "blob_hash"

// blobRef is a column of one table that names an object under another
// name than blob_hash. It matches the references the retention collector
// checks (service.blobReferenced), so that the export carries every
// object a live row needs.
type blobRef struct {
	table, column string
	// jsonField, when set, is the field of a JSON document in the column
	// that holds the hash, as in runs.log_archive.
	jsonField string
	// releasedBy, when set, is a column whose non-null value means
	// retention released the object: the row stays, the object may be
	// gone, and the export does not need it.
	releasedBy string
}

var blobRefs = []blobRef{
	{table: "artifacts", column: "blob_key", releasedBy: "expired_at"},
	{table: "runs", column: "log_blob"},
	// Logs archived before log_blob existed name their object only here.
	{table: "runs", column: "log_archive", jsonField: "blob", releasedBy: "logs_expired_at"},
	{table: "runs", column: "input_key"},
}

// Insert batches stay well below the statement size limits of both
// databases even for log chunks, which hold up to 256 KiB each.
const (
	insertBatchRows  = 200
	insertBatchBytes = 4 << 20
)

// ExportManifest describes an export archive.
type ExportManifest struct {
	Format         string        `json:"format"`
	FormatVersion  int           `json:"format_version"`
	CreatedAt      time.Time     `json:"created_at"`
	LerrynVersion  string        `json:"waleng_version"`
	SourceDatabase string        `json:"source_database"`
	SchemaVersion  int64         `json:"schema_version"`
	Tables         []ExportTable `json:"tables"`
	Blobs          int           `json:"blobs"`
	// MissingBlobs lists objects that rows refer to but the source object
	// store no longer had. The rows are exported anyway.
	MissingBlobs []string `json:"missing_blobs,omitempty"`
}

// ExportTable is one table of an export.
type ExportTable struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
}

// ExportOptions configures Export.
type ExportOptions struct {
	DB    *store.DB
	Blobs blob.Store
	// Output is the archive to create; it must not exist.
	Output string
}

// Export writes every table of the database and every object its rows
// refer to into a portable archive (tech design 15.5). All tables are
// read in one snapshot transaction, so the export is consistent even if
// the server is running; stop it anyway when the export is meant to
// replace the instance, or later writes are lost.
//
// Values are written in a form both databases accept: booleans as JSON
// booleans or 0 and 1, times as UTC text in Bun's layout, UUIDs as text.
// Credential ciphertext is exported as stored; it stays readable only
// with the instance's master key, which is not part of the archive.
func Export(ctx context.Context, opts ExportOptions) (m ExportManifest, err error) {
	if err := os.MkdirAll(filepath.Dir(opts.Output), dirMode); err != nil {
		return m, fmt.Errorf("create output directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(opts.Output), ".lerryn-export-*")
	if err != nil {
		return m, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	m = ExportManifest{
		Format:         exportFormat,
		FormatVersion:  exportFormatVersion,
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		LerrynVersion:  buildinfo.Version,
		SourceDatabase: opts.DB.Native().Name(),
	}
	if m.SchemaVersion, _, err = store.SchemaVersion(ctx, opts.DB); err != nil {
		return m, err
	}

	hashes := map[string]struct{}{}
	tx, err := opts.DB.BeginTx(ctx, opts.DB.Native().SnapshotTxOptions())
	if err != nil {
		return m, fmt.Errorf("begin snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	tables, err := exportedTables(ctx, opts.DB.Native(), tx)
	if err != nil {
		return m, err
	}
	for _, t := range tables {
		n, err := dumpTable(ctx, tx, t, filepath.Join(stage, t.Name+exportTableSuffix), hashes)
		if err != nil {
			return m, err
		}
		m.Tables = append(m.Tables, ExportTable{Name: t.Name, Columns: columnNames(t), Rows: n})
	}
	_ = tx.Rollback()

	sorted := make([]string, 0, len(hashes))
	for h := range hashes {
		sorted = append(sorted, h)
	}
	sort.Strings(sorted)
	blobDir := filepath.Join(stage, "blobs")
	if err := os.Mkdir(blobDir, dirMode); err != nil {
		return m, err
	}
	var present []string
	for _, h := range sorted {
		ok, err := spoolBlob(ctx, opts.Blobs, h, filepath.Join(blobDir, h))
		if err != nil {
			return m, err
		}
		if !ok {
			m.MissingBlobs = append(m.MissingBlobs, h)
			continue
		}
		present = append(present, h)
	}
	m.Blobs = len(present)

	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return m, err
	}
	a, err := createArchive(opts.Output)
	if err != nil {
		return m, err
	}
	defer func() {
		if err != nil {
			a.abort()
		}
	}()
	if err := a.addBytes(exportManifestName, manifest); err != nil {
		return m, err
	}
	for _, h := range present {
		if err := a.addFile(exportBlobPrefix+h, filepath.Join(blobDir, h)); err != nil {
			return m, err
		}
	}
	for _, t := range m.Tables {
		if err := a.addFile(exportTablePrefix+t.Name+exportTableSuffix, filepath.Join(stage, t.Name+exportTableSuffix)); err != nil {
			return m, err
		}
	}
	return m, a.close()
}

// exportedTables returns the tables to export in an order in which every
// table comes after the tables it references.
func exportedTables(ctx context.Context, in dialect.Introspector, db bun.IDB) ([]dialect.Table, error) {
	all, err := in.Tables(ctx, db)
	if err != nil {
		return nil, err
	}
	var tables []dialect.Table
	for _, t := range all {
		if !slices.Contains(excludedTables, t.Name) {
			tables = append(tables, t)
		}
	}
	return sortByReferences(tables)
}

// sortByReferences orders tables so that referenced tables come first,
// keeping name order among independent tables. A table may reference
// itself; its rows are then inserted in primary key order, which for
// UUIDv7 keys is creation order.
func sortByReferences(tables []dialect.Table) ([]dialect.Table, error) {
	byName := map[string]dialect.Table{}
	for _, t := range tables {
		byName[t.Name] = t
	}
	var (
		sorted []dialect.Table
		state  = map[string]int{} // 1 visiting, 2 done
		visit  func(name string, chain []string) error
	)
	visit = func(name string, chain []string) error {
		switch state[name] {
		case 2:
			return nil
		case 1:
			return fmt.Errorf("foreign keys form a cycle: %s", strings.Join(append(chain, name), " -> "))
		}
		t, ok := byName[name]
		if !ok {
			return nil // an excluded table such as leases
		}
		state[name] = 1
		for _, ref := range t.References {
			if ref == name {
				continue
			}
			if err := visit(ref, append(chain, name)); err != nil {
				return err
			}
		}
		state[name] = 2
		sorted = append(sorted, t)
		return nil
	}
	for _, t := range tables {
		if err := visit(t.Name, nil); err != nil {
			return nil, err
		}
	}
	return sorted, nil
}

func columnNames(t dialect.Table) []string {
	names := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		names[i] = c.Name
	}
	return names
}

// dumpTable writes every row of t to dst as one JSON array per line, in
// primary key order, and collects the object hashes the rows refer to.
func dumpTable(ctx context.Context, tx bun.Tx, t dialect.Table, dst string, hashes map[string]struct{}) (int64, error) {
	gen := schema.NewQueryGen(tx.Dialect())
	q := []byte("SELECT ")
	for i, c := range t.Columns {
		if i > 0 {
			q = append(q, ", "...)
		}
		q = gen.AppendIdent(q, c.Name)
	}
	q = append(q, " FROM "...)
	q = gen.AppendIdent(q, t.Name)
	q = append(q, " ORDER BY 1"...)

	// The underlying *sql.Tx runs the statement as written: it holds
	// quoted identifiers only, nothing for Bun to format.
	rows, err := tx.Tx.QueryContext(ctx, string(q))
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", t.Name, err)
	}
	defer func() { _ = rows.Close() }()

	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) // #nosec G304 -- inside the staging directory
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)

	refs := tableBlobRefs(t)
	vals := make([]any, len(t.Columns))
	ptrs := make([]any, len(t.Columns))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	out := make([]any, len(t.Columns))
	var n int64
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return n, fmt.Errorf("read %s: %w", t.Name, err)
		}
		for i, v := range vals {
			out[i], err = encodeValue(v)
			if err != nil {
				return n, fmt.Errorf("export %s.%s: %w", t.Name, t.Columns[i].Name, err)
			}
		}
		for _, ref := range refs {
			if h := ref.hash(out); h != "" {
				hashes[h] = struct{}{}
			}
		}
		if err := enc.Encode(out); err != nil {
			return n, err
		}
		n++
	}
	if err := rows.Err(); err != nil {
		return n, fmt.Errorf("read %s: %w", t.Name, err)
	}
	if err := w.Flush(); err != nil {
		return n, err
	}
	return n, f.Sync()
}

// spoolBlob verifies an object's content while copying it to dst. Only
// missing objects are tolerated; corrupt bytes must not become an export
// that a later import cannot read.
func spoolBlob(ctx context.Context, bs blob.Store, hash, dst string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	rc, err := bs.Get(ctx, hash)
	if err != nil && ctx.Err() != nil {
		return false, errors.Join(err, ctx.Err())
	}
	if errors.Is(err, blob.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, fileMode) // #nosec G304 -- inside the staging directory; the name is a validated hash
	if err != nil {
		return false, errors.Join(err, rc.Close(), ctx.Err())
	}
	digest := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, digest), archiveContextReader{ctx: ctx, r: rc})
	if err = errors.Join(err, rc.Close(), f.Close(), ctx.Err()); err != nil {
		return false, fmt.Errorf("copy object %s: %w", hash, err)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != hash {
		return false, fmt.Errorf("object %s has content hash %s", hash, actual)
	}
	return true, nil
}

// ImportOptions configures Import.
type ImportOptions struct {
	// DB is the target database. Every table except the excluded ones
	// must be empty; missing migrations are applied.
	DB    *store.DB
	Blobs blob.Store
	Input string
}

// ImportResult reports what Import loaded.
type ImportResult struct {
	Manifest ExportManifest
	// Tables are the row counts read back from the target after loading,
	// which match the manifest.
	Tables []ExportTable
	Blobs  int
	// Migrated lists the migrations applied after loading, when the
	// export came from an older schema.
	Migrated []store.AppliedMigration
}

// ErrTargetHasData is returned by Import when a target table is not
// empty.
var ErrTargetHasData = errors.New("target database is not empty")

// Import loads an export archive into an empty database of either
// dialect and the target object store. The target is first migrated to
// the export's schema version, the rows are loaded in one transaction
// whose row counts must match the manifest, and the remaining
// migrations, if any, are applied afterwards.
func Import(ctx context.Context, opts ImportOptions) (res ImportResult, err error) {
	ar, err := openArchive(ctx, opts.Input)
	if err != nil {
		return res, err
	}
	defer ar.close()
	res.Manifest, err = readExportManifest(ar)
	if err != nil {
		return res, err
	}
	m := res.Manifest

	latest, err := store.LatestVersion(ctx, opts.DB)
	if err != nil {
		return res, err
	}
	if m.SchemaVersion > latest {
		return res, fmt.Errorf("the export's schema version %d is newer than this binary supports (%d); import it with a newer lerryn", m.SchemaVersion, latest)
	}
	if err := store.MigrateTo(ctx, opts.DB, m.SchemaVersion); err != nil {
		return res, fmt.Errorf("prepare target schema: %w", err)
	}

	native := opts.DB.Native()
	targetTables, err := native.Tables(ctx, opts.DB)
	if err != nil {
		return res, err
	}
	target := map[string]dialect.Table{}
	for _, t := range targetTables {
		if slices.Contains(excludedTables, t.Name) {
			continue
		}
		target[t.Name] = t
		n, err := opts.DB.NewSelect().Table(t.Name).Count(ctx)
		if err != nil {
			return res, fmt.Errorf("count %s: %w", t.Name, err)
		}
		if n > 0 {
			return res, fmt.Errorf("%w: table %s has %d rows; import into a new database", ErrTargetHasData, t.Name, n)
		}
	}
	plans := map[string]insertPlan{}
	for _, et := range m.Tables {
		t, ok := target[et.Name]
		if !ok {
			return res, fmt.Errorf("the export has table %s, which the target schema lacks", et.Name)
		}
		p, err := planInsert(t, et.Columns, native.Name() == dialect.SQLiteName)
		if err != nil {
			return res, err
		}
		plans[et.Name] = p
	}

	// Objects first, outside the transaction: an object nobody refers to
	// is harmless, a row whose object is missing is not.
	name, hdr, r, err := ar.next()
	for err == nil && strings.HasPrefix(name, exportBlobPrefix) {
		if err := importBlob(ctx, opts.Blobs, strings.TrimPrefix(name, exportBlobPrefix), r, hdr.Size); err != nil {
			return res, err
		}
		res.Blobs++
		name, hdr, r, err = ar.next()
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return res, err
	}
	if res.Blobs != m.Blobs {
		return res, fmt.Errorf("the archive holds %d objects, its manifest lists %d", res.Blobs, m.Blobs)
	}

	err = opts.DB.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		res.Tables = nil
		loaded := map[string]bool{}
		for name != "" {
			tableName, ok := strings.CutPrefix(name, exportTablePrefix)
			tableName, ok2 := strings.CutSuffix(tableName, exportTableSuffix)
			if !ok || !ok2 {
				return fmt.Errorf("unexpected archive entry %q", name)
			}
			p, known := plans[tableName]
			if !known || loaded[tableName] {
				return fmt.Errorf("unexpected archive entry %q", name)
			}
			if err := loadTable(ctx, tx, p, r); err != nil {
				return err
			}
			loaded[tableName] = true
			var err error
			name, _, r, err = ar.next()
			if errors.Is(err, io.EOF) {
				name = ""
			} else if err != nil {
				return err
			}
		}
		for _, et := range m.Tables {
			if !loaded[et.Name] {
				return fmt.Errorf("the archive lacks the rows of table %s", et.Name)
			}
			n, err := tx.NewSelect().Table(et.Name).Count(ctx)
			if err != nil {
				return fmt.Errorf("count %s: %w", et.Name, err)
			}
			if n != et.Rows {
				return fmt.Errorf("table %s: loaded %d rows, the manifest lists %d", et.Name, n, et.Rows)
			}
			res.Tables = append(res.Tables, ExportTable{Name: et.Name, Columns: et.Columns, Rows: n})
		}
		return nil
	})
	if err != nil {
		return res, fmt.Errorf("import rows: %w", err)
	}

	mig, err := store.Migrate(ctx, opts.DB)
	res.Migrated = mig.Applied
	if err != nil {
		return res, fmt.Errorf("migrate after import: %w", err)
	}
	return res, nil
}

func readExportManifest(ar *archiveReader) (ExportManifest, error) {
	var m ExportManifest
	name, _, r, err := ar.next()
	if err != nil {
		return m, fmt.Errorf("read export manifest: %w", err)
	}
	if name != exportManifestName {
		return m, fmt.Errorf("not a lerryn export: first entry is %q, want %q", name, exportManifestName)
	}
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return m, fmt.Errorf("read export manifest: %w", err)
	}
	if m.Format != exportFormat {
		return m, fmt.Errorf("not a lerryn export: format %q", m.Format)
	}
	if m.FormatVersion != exportFormatVersion {
		return m, fmt.Errorf("export format version %d is not supported by this binary (want %d)", m.FormatVersion, exportFormatVersion)
	}
	return m, nil
}

func importBlob(ctx context.Context, bs blob.Store, hash string, r io.Reader, size int64) error {
	if !blob.ValidHash(hash) {
		return fmt.Errorf("archive object %q: %w", hash, blob.ErrInvalidHash)
	}
	if size < 0 || size > maxEntryBytes {
		return fmt.Errorf("archive object %s: size %d out of range", hash, size)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	input := &io.LimitedReader{R: archiveContextReader{ctx: ctx, r: r}, N: size}
	got, err := bs.Put(ctx, input)
	if err != nil {
		return fmt.Errorf("store object %s: %w", hash, err)
	}
	if input.N != 0 {
		return fmt.Errorf("archive object %s is short: read %d of %d bytes", hash, size-input.N, size)
	}
	if got != hash {
		return fmt.Errorf("archive object %s is corrupt: its content hashes to %s", hash, got)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Put may reuse an existing object without reading its stored bytes.
	// Verify what the imported rows will actually reference before any
	// rows commit, without replacing an object another instance may use.
	stored, err := bs.Get(ctx, hash)
	if err != nil {
		return fmt.Errorf("open stored object %s: %w", hash, err)
	}
	digest := sha256.New()
	n, err := io.Copy(digest, io.LimitReader(archiveContextReader{ctx: ctx, r: stored}, size+1))
	if err = errors.Join(err, stored.Close()); err != nil {
		return fmt.Errorf("verify stored object %s: %w", hash, err)
	}
	if n != size {
		return fmt.Errorf("stored object %s has size %d, expected %d", hash, n, size)
	}
	if actual := hex.EncodeToString(digest.Sum(nil)); actual != hash {
		return fmt.Errorf("stored object %s has content hash %s", hash, actual)
	}
	return ctx.Err()
}

// insertPlan is how the rows of one exported table are written into the
// matching target table.
type insertPlan struct {
	table   string
	columns []dialect.Column
	sqlite  bool
}

func planInsert(t dialect.Table, exported []string, sqlite bool) (insertPlan, error) {
	p := insertPlan{table: t.Name, sqlite: sqlite}
	byName := map[string]dialect.Column{}
	for _, c := range t.Columns {
		byName[c.Name] = c
	}
	for _, name := range exported {
		c, ok := byName[name]
		if !ok {
			return p, fmt.Errorf("the export has column %s.%s, which the target schema lacks", t.Name, name)
		}
		p.columns = append(p.columns, c)
	}
	if len(p.columns) == 0 {
		return p, fmt.Errorf("the export lists no columns for table %s", t.Name)
	}
	return p, nil
}

// loadTable reads NDJSON rows from r and inserts them in batches.
func loadTable(ctx context.Context, tx bun.Tx, p insertPlan, r io.Reader) error {
	gen := schema.NewQueryGen(tx.Dialect())
	prefix := append([]byte(nil), "INSERT INTO "...)
	prefix = gen.AppendIdent(prefix, p.table)
	prefix = append(prefix, " ("...)
	for i, c := range p.columns {
		if i > 0 {
			prefix = append(prefix, ", "...)
		}
		prefix = gen.AppendIdent(prefix, c.Name)
	}
	prefix = append(prefix, ") VALUES "...)

	dec := json.NewDecoder(bufio.NewReaderSize(r, 1<<20))
	dec.UseNumber()
	var (
		stmt  []byte
		batch int
		line  int64
	)
	flush := func() error {
		if batch == 0 {
			return nil
		}
		// The statement is complete SQL text with every value formatted
		// by Bun's dialect; the underlying *sql.Tx runs it as is, so
		// question marks inside values are not taken for placeholders.
		if _, err := tx.Tx.ExecContext(ctx, string(stmt)); err != nil {
			return fmt.Errorf("insert into %s near row %d: %w", p.table, line, err)
		}
		stmt, batch = stmt[:0], 0
		return nil
	}
	for {
		var row []any
		err := dec.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			return fmt.Errorf("read %s row %d: %w", p.table, line, err)
		}
		if len(row) != len(p.columns) {
			return fmt.Errorf("read %s row %d: %d values for %d columns", p.table, line, len(row), len(p.columns))
		}
		if batch == 0 {
			stmt = append(stmt, prefix...)
		} else {
			stmt = append(stmt, ", "...)
		}
		stmt = append(stmt, '(')
		for i, raw := range row {
			v, err := decodeValue(raw, p.columns[i], p.sqlite)
			if err != nil {
				return fmt.Errorf("read %s.%s row %d: %w", p.table, p.columns[i].Name, line, err)
			}
			if i > 0 {
				stmt = append(stmt, ", "...)
			}
			stmt = gen.AppendQuery(stmt, "?", v)
		}
		stmt = append(stmt, ')')
		batch++
		if batch >= insertBatchRows || len(stmt) >= insertBatchBytes {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

// TableCounts returns the row count of every exported table, for
// comparing a source and a target after a migration.
func TableCounts(ctx context.Context, db *store.DB) (map[string]int64, error) {
	tables, err := exportedTables(ctx, db.Native(), db)
	if err != nil {
		return nil, err
	}
	counts := map[string]int64{}
	for _, t := range tables {
		n, err := db.NewSelect().Table(t.Name).Count(ctx)
		if err != nil {
			return nil, fmt.Errorf("count %s: %w", t.Name, err)
		}
		counts[t.Name] = n
	}
	return counts, nil
}

// columnRef is a blobRef resolved to column positions of a table.
type columnRef struct {
	column, releasedBy int // releasedBy is -1 when there is none
	jsonField          string
}

// tableBlobRefs returns the columns of t that name objects.
func tableBlobRefs(t dialect.Table) []columnRef {
	index := func(name string) int {
		return slices.IndexFunc(t.Columns, func(c dialect.Column) bool { return c.Name == name })
	}
	var out []columnRef
	if i := index(blobColumn); i >= 0 {
		out = append(out, columnRef{column: i, releasedBy: -1})
	}
	for _, r := range blobRefs {
		if r.table != t.Name {
			continue
		}
		i := index(r.column)
		if i < 0 {
			continue // an older schema without the column
		}
		ref := columnRef{column: i, releasedBy: -1, jsonField: r.jsonField}
		if r.releasedBy != "" {
			ref.releasedBy = index(r.releasedBy)
		}
		out = append(out, ref)
	}
	return out
}

// hash returns the object a row names in the column, or "" when it names
// none or retention released it.
func (r columnRef) hash(row []any) string {
	if r.releasedBy >= 0 && row[r.releasedBy] != nil {
		return ""
	}
	s, ok := row[r.column].(string)
	if !ok {
		return ""
	}
	if r.jsonField != "" {
		var doc map[string]any
		if json.Unmarshal([]byte(s), &doc) != nil {
			return ""
		}
		s, _ = doc[r.jsonField].(string)
	}
	if !blob.ValidHash(s) {
		return ""
	}
	return s
}
