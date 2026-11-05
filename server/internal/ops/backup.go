package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/niltavi/lerryn/server/internal/buildinfo"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/dialect"
)

// Layout of a backup archive. The manifest comes first so that a restore
// can check it before extracting anything.
// Internal archive names and JSON keys are a persisted format. Keep the
// original labels so backups and exports survive product renames.
const (
	backupFormat        = "waleng-backup"
	backupFormatVersion = 1
	backupManifestName  = "waleng-backup.json"
	backupDatabaseName  = "database/waleng.db"
	backupStoragePrefix = "storage"
)

// ErrNotSQLite is returned by Backup and Restore for a PostgreSQL
// database, which is backed up with the operator's PostgreSQL tooling.
var ErrNotSQLite = errors.New("lerryn backup and restore handle SQLite only; back up PostgreSQL with pg_dump or your existing PostgreSQL backup, or use lerryn export")

// ErrTargetNotEmpty is returned by Restore when the database or storage
// it would write already holds data and Force is not set.
var ErrTargetNotEmpty = errors.New("restore target already holds data")

// ErrOverlappingTargets is returned before Restore changes a database or
// object store whose target paths would be moved along with one another.
var ErrOverlappingTargets = errors.New("restore database and object store paths overlap")

// BackupManifest describes a backup archive.
type BackupManifest struct {
	Format        string    `json:"format"`
	FormatVersion int       `json:"format_version"`
	CreatedAt     time.Time `json:"created_at"`
	LerrynVersion string    `json:"waleng_version"`
	Database      string    `json:"database"`
	SchemaVersion int64     `json:"schema_version"`
	// StorageIncluded is false when the deployment's object store is not
	// the local disk, which the operator then backs up separately.
	StorageIncluded bool `json:"storage_included"`
}

// BackupOptions configures Backup.
type BackupOptions struct {
	// DB is the open SQLite database to copy. The server may keep
	// running and writing meanwhile.
	DB *store.DB
	// StoragePath is the local object store root (storage.path), or empty
	// when the object store is not local.
	StoragePath string
	// Output is the archive to create; it must not exist.
	Output string
}

// BackupResult reports what Backup wrote.
type BackupResult struct {
	Manifest     BackupManifest
	StorageFiles int
}

// Backup writes a gzip-compressed tar archive with a consistent copy of
// the SQLite database and the local object store.
//
// The database is copied first, with VACUUM INTO, which runs as one read
// transaction and does not block the server's writers. The object store
// is copied afterwards while a renewable database lease protects it from
// retention's collector. Only object deletion pauses; the server keeps
// writing. The copied database's references are checked against the files
// actually archived, and losing the lease fails the backup. Only objects
// referenced by the snapshot are included; temporary or unrelated files,
// including an output archive inside storage, are never copied. The master
// key is deliberately not included (tech design 15.5).
func Backup(ctx context.Context, opts BackupOptions) (res BackupResult, err error) {
	lite, ok := opts.DB.Native().(dialect.SQLite)
	if !ok {
		return res, ErrNotSQLite
	}
	var pin *store.BlobBackupPin
	if opts.StoragePath != "" {
		pin, err = opts.DB.PinBlobs(ctx)
		if err != nil {
			return res, err
		}
		defer func() { _ = pin.Close() }()
		ctx = pin.Context()
	}
	current, _, err := store.SchemaVersion(ctx, opts.DB)
	if err != nil {
		return res, err
	}

	// Stage the copy next to the output, on the same file system, so a
	// large database does not land in a small /tmp.
	if err := os.MkdirAll(filepath.Dir(opts.Output), dirMode); err != nil {
		return res, fmt.Errorf("create output directory: %w", err)
	}
	stage, err := os.MkdirTemp(filepath.Dir(opts.Output), ".lerryn-backup-*")
	if err != nil {
		return res, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	dbCopy := filepath.Join(stage, "lerryn.db")
	if err := lite.VacuumInto(ctx, opts.DB, dbCopy); err != nil {
		return res, err
	}
	var hashes map[string]struct{}
	if opts.StoragePath != "" {
		hashes, err = backupBlobHashes(ctx, dbCopy)
		if err != nil {
			return res, err
		}
	}

	res.Manifest = BackupManifest{
		Format:          backupFormat,
		FormatVersion:   backupFormatVersion,
		CreatedAt:       time.Now().UTC().Truncate(time.Second),
		LerrynVersion:   buildinfo.Version,
		Database:        dialect.SQLiteName,
		SchemaVersion:   current,
		StorageIncluded: opts.StoragePath != "",
	}
	manifest, err := json.MarshalIndent(res.Manifest, "", "  ")
	if err != nil {
		return res, err
	}

	a, err := createArchive(opts.Output)
	if err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			a.abort()
		}
	}()
	if err := a.addBytes(backupManifestName, manifest); err != nil {
		return res, err
	}
	if err := a.addFile(backupDatabaseName, dbCopy); err != nil {
		return res, err
	}
	if opts.StoragePath != "" {
		res.StorageFiles, err = backupObjects(ctx, a, opts.StoragePath, hashes)
		if err != nil {
			return res, err
		}
		if err := pin.Check(); err != nil {
			return res, err
		}
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, a.close()
}

// RestoreOptions configures Restore.
type RestoreOptions struct {
	// Input is the archive written by Backup.
	Input string
	// Database is the SQLite database to restore into (database.dsn).
	Database config.Database
	// StoragePath is the local object store root to restore into. It is
	// required when the archive includes the object store.
	StoragePath string
	// Force replaces existing data. The existing database and object
	// store are kept next to the originals with a .pre-restore suffix.
	Force bool
}

// RestoreResult reports what Restore did.
type RestoreResult struct {
	Manifest     BackupManifest
	StorageFiles int
	// MovedAside lists the paths existing data was renamed to.
	MovedAside []string
}

// Restore replaces the SQLite database and the local object store with
// the contents of a backup archive. The server must be stopped. Nothing
// is changed until the whole archive has been extracted and the database
// opens and is not newer than this binary; the old data is then moved
// aside, not deleted.
func Restore(ctx context.Context, opts RestoreOptions) (res RestoreResult, err error) {
	return restoreWithRename(ctx, opts, os.Rename)
}

func restoreWithRename(ctx context.Context, opts RestoreOptions, rename func(string, string) error) (res RestoreResult, err error) {
	if opts.Database.Driver != config.DatabaseSQLite {
		return res, ErrNotSQLite
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	dbPath, err := SQLitePath(opts.Database.DSN)
	if err != nil {
		return res, err
	}
	ar, err := openArchive(ctx, opts.Input)
	if err != nil {
		return res, err
	}
	defer ar.close()
	res.Manifest, err = readBackupManifest(ar)
	if err != nil {
		return res, err
	}
	storagePath := ""
	if res.Manifest.StorageIncluded {
		if opts.StoragePath == "" {
			return res, errors.New("the backup includes the local object store, but storage.driver is not local")
		}
		storagePath = filepath.Clean(opts.StoragePath)
	}
	if storagePath != "" {
		if err := checkRestorePaths(dbPath, storagePath); err != nil {
			return res, err
		}
	}
	if !opts.Force {
		if err := checkRestoreTarget(dbPath, storagePath); err != nil {
			return res, err
		}
	}

	preserveStaging := false
	var createdParents []string
	defer func() {
		if err != nil && !preserveStaging {
			for i := len(createdParents) - 1; i >= 0; i-- {
				_ = os.Remove(createdParents[i]) // remove only empty directories this restore created
			}
		}
	}()
	created, err := makeRestoreParents(filepath.Dir(dbPath))
	createdParents = append(createdParents, created...)
	if err != nil {
		return res, err
	}
	dbStage, err := os.MkdirTemp(filepath.Dir(dbPath), ".lerryn-restore-*")
	if err != nil {
		return res, fmt.Errorf("create staging directory: %w", err)
	}
	defer func() {
		if !preserveStaging {
			_ = os.RemoveAll(dbStage)
		}
	}()
	var storageStage string
	if storagePath != "" {
		// Staged next to the target, so the final rename stays on one
		// file system.
		created, err := makeRestoreParents(filepath.Dir(storagePath))
		createdParents = append(createdParents, created...)
		if err != nil {
			return res, err
		}
		storageStage, err = os.MkdirTemp(filepath.Dir(storagePath), ".lerryn-restore-storage-*")
		if err != nil {
			return res, fmt.Errorf("create staging directory: %w", err)
		}
		defer func() {
			if !preserveStaging {
				_ = os.RemoveAll(storageStage)
			}
		}()
	}
	// New parents make formerly missing aliases observable through file
	// identity. Recheck before extracting files or moving existing data.
	if storagePath != "" {
		if err := checkRestorePaths(dbPath, storagePath); err != nil {
			return res, err
		}
	}

	stagedDB := filepath.Join(dbStage, "lerryn.db")
	haveDB := false
	for {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		name, hdr, r, err := ar.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return res, err
		}
		switch {
		case name == backupDatabaseName:
			if err := writeFileFrom(stagedDB, r, hdr.Size); err != nil {
				return res, fmt.Errorf("extract database: %w", err)
			}
			haveDB = true
		case strings.HasPrefix(name, backupStoragePrefix+"/") && storageStage != "":
			rel := strings.TrimPrefix(name, backupStoragePrefix+"/")
			if err := writeFileFrom(filepath.Join(storageStage, filepath.FromSlash(rel)), r, hdr.Size); err != nil {
				return res, fmt.Errorf("extract %s: %w", name, err)
			}
			res.StorageFiles++
		default:
			return res, fmt.Errorf("unexpected archive entry %q", name)
		}
	}
	if !haveDB {
		return res, errors.New("the backup has no database")
	}
	if err := checkRestoredDatabase(ctx, stagedDB); err != nil {
		return res, err
	}
	// The input may live in a target directory. Finish reading and close
	// it before moving that directory, including on systems that prevent
	// renaming an open file.
	ar.close()

	targets := []restoreTarget{
		{path: dbPath, staged: stagedDB},
		{path: dbPath + "-wal"},
		{path: dbPath + "-shm"},
	}
	if storageStage != "" {
		// MkdirTemp creates 0700; the store's group may read (blob.Local).
		if err := os.Chmod(storageStage, dirMode); err != nil {
			return res, err
		}
		targets = append(targets, restoreTarget{path: storagePath, staged: storageStage})
	}
	res.MovedAside, err = swapRestoreTargets(targets, rename)
	if errors.Is(err, errRestoreRollback) {
		preserveStaging = true
		stages := dbStage
		if storageStage != "" {
			stages += ", " + storageStage
		}
		err = fmt.Errorf("%w; restore staging retained at %s", err, stages)
	}
	return res, err
}

var errRestoreRollback = errors.New("restore rollback incomplete")

type restoreTarget struct {
	path, staged string
	saved        string
	installed    bool
}

// swapRestoreTargets preserves every old target before installing any
// replacement. Every move stays under its target's parent, so database and
// object storage may reside on different file systems.
func swapRestoreTargets(targets []restoreTarget, rename func(string, string) error) ([]string, error) {
	suffix := ".pre-restore-" + time.Now().UTC().Format("20060102T150405Z")
	var containers []string
	rollback := func(cause error) ([]string, error) {
		var failures []error
		for i := len(targets) - 1; i >= 0; i-- {
			target := &targets[i]
			if target.installed {
				if err := renameRestoreTarget(target.path, target.staged, rename); err != nil {
					failures = append(failures, fmt.Errorf("return restored target %s to staging: %w", target.path, err))
					continue
				}
				target.installed = false
			}
			if target.saved != "" {
				if err := renameRestoreTarget(target.saved, target.path, rename); err != nil {
					failures = append(failures, fmt.Errorf("restore previous target from %s: %w", target.saved, err))
					continue
				}
				target.saved = ""
			}
		}
		if len(failures) > 0 {
			var remaining []string
			for _, target := range targets {
				if target.saved != "" {
					remaining = append(remaining, target.saved)
				}
			}
			return remaining, errors.Join(cause, errRestoreRollback, errors.Join(failures...))
		}
		for _, dir := range containers {
			if err := os.Remove(dir); err != nil {
				cause = errors.Join(cause, fmt.Errorf("remove empty restore preservation directory %s: %w", dir, err))
			}
		}
		return nil, cause
	}
	for i := range targets {
		saved, err := moveAsideWithRename(targets[i].path, suffix, rename)
		if err != nil {
			return rollback(err)
		}
		targets[i].saved = saved
		if saved != "" {
			containers = append(containers, filepath.Dir(saved))
		}
	}
	for i := range targets {
		target := &targets[i]
		if target.staged == "" {
			continue
		}
		if err := renameRestoreTarget(target.staged, target.path, rename); err != nil {
			return rollback(fmt.Errorf("install restored target %s: %w", target.path, err))
		}
		target.installed = true
	}
	var saved []string
	for _, target := range targets {
		if target.saved != "" {
			saved = append(saved, target.saved)
		}
	}
	return saved, nil
}

// renameRestoreTarget refuses an unexpectedly occupied destination. The
// restore runs offline; an existing path belongs to another operation and
// must not be overwritten during installation or rollback.
func renameRestoreTarget(from, to string, rename func(string, string) error) error {
	if _, err := os.Lstat(to); err == nil {
		return fmt.Errorf("restore destination %s already exists", to)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check restore destination %s: %w", to, err)
	}
	return rename(from, to)
}

// makeRestoreParents reports newly created directories in creation order,
// so a rejected restore can remove its empty parents after staging cleanup.
func makeRestoreParents(dir string) ([]string, error) {
	var missing []string
	for current := dir; ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		missing = append(missing, current)
		if filepath.Dir(current) == current {
			return nil, fmt.Errorf("directory %s has no existing ancestor", dir)
		}
	}
	created := make([]string, len(missing))
	for i, path := range missing {
		created[len(missing)-1-i] = path
	}
	return created, os.MkdirAll(dir, dirMode)
}

// checkRestorePaths keeps each rename independent. Check both lexical
// paths and existing symlink targets: either can make one target contain
// another, and the database's WAL and SHM paths are replaced too.
func checkRestorePaths(dbPath, storagePath string) error {
	dbAbs, err := filepath.Abs(dbPath)
	if err != nil {
		return fmt.Errorf("resolve restore database path: %w", err)
	}
	storageAbs, err := filepath.Abs(storagePath)
	if err != nil {
		return fmt.Errorf("resolve restore storage path: %w", err)
	}
	overlaps := func(a, b string) bool {
		inside := func(parent, child string) bool {
			rel, err := filepath.Rel(parent, child)
			return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		return inside(a, b) || inside(b, a)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if overlaps(dbAbs+suffix, storageAbs) {
			return fmt.Errorf("%w: %s and %s", ErrOverlappingTargets, dbPath+suffix, storagePath)
		}
		for _, pair := range [][2]string{{storageAbs, dbAbs + suffix}, {dbAbs + suffix, storageAbs}} {
			same, err := sameRestoreAncestor(pair[0], pair[1])
			if err != nil {
				return fmt.Errorf("check restore target ancestors: %w", err)
			}
			if same {
				return fmt.Errorf("%w: %s and %s", ErrOverlappingTargets, dbPath+suffix, storagePath)
			}
		}
	}
	storageAliases, err := restorePathAliases(storageAbs)
	if err != nil {
		return fmt.Errorf("resolve restore storage path: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		dbAliases, err := restorePathAliases(dbAbs + suffix)
		if err != nil {
			return fmt.Errorf("resolve restore database path: %w", err)
		}
		for _, db := range dbAliases {
			for _, storage := range storageAliases {
				if overlaps(db, storage) {
					return fmt.Errorf("%w: %s and %s", ErrOverlappingTargets, dbPath+suffix, storagePath)
				}
			}
		}
	}
	return nil
}

// sameRestoreAncestor catches filesystem aliases that EvalSymlinks does
// not normalize, such as different path case on an insensitive volume.
// File identity keeps distinct case-sensitive directories independent.
func sameRestoreAncestor(parent, child string) (bool, error) {
	parentInfo, err := os.Stat(parent)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for {
		info, err := os.Stat(child)
		if err == nil && os.SameFile(parentInfo, info) {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		previous := child
		child = filepath.Dir(child)
		if child == previous {
			return false, nil
		}
	}
}

func restorePathAliases(abs string) ([]string, error) {
	parent, err := resolveRestorePath(filepath.Dir(abs))
	if err != nil {
		return nil, err
	}
	resolved, err := resolveRestorePath(abs)
	if err != nil {
		return nil, err
	}
	// Rename moves a final symlink itself, while paths below that symlink
	// follow its target. Both locations matter to an overlapping layout.
	return []string{abs, filepath.Join(parent, filepath.Base(abs)), resolved}, nil
}

// resolveRestorePath resolves existing ancestors without requiring a new
// restore target to exist yet. A dangling symlink is rejected.
func resolveRestorePath(abs string) (string, error) {
	missing := ""
	for {
		if _, err := os.Lstat(abs); err == nil {
			resolved, err := filepath.EvalSymlinks(abs)
			if err != nil {
				return "", err
			}
			return filepath.Join(resolved, missing), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("path %s has no existing ancestor", abs)
		}
		missing = filepath.Join(filepath.Base(abs), missing)
		abs = parent
	}
}

func readBackupManifest(ar *archiveReader) (BackupManifest, error) {
	var m BackupManifest
	name, _, r, err := ar.next()
	if err != nil {
		return m, fmt.Errorf("read backup manifest: %w", err)
	}
	if name != backupManifestName {
		return m, fmt.Errorf("not a lerryn backup: first entry is %q, want %q", name, backupManifestName)
	}
	if err := json.NewDecoder(r).Decode(&m); err != nil {
		return m, fmt.Errorf("read backup manifest: %w", err)
	}
	if m.Format != backupFormat {
		return m, fmt.Errorf("not a lerryn backup: format %q", m.Format)
	}
	if m.FormatVersion != backupFormatVersion {
		return m, fmt.Errorf("backup format version %d is not supported by this binary (want %d)", m.FormatVersion, backupFormatVersion)
	}
	return m, nil
}

// checkRestoredDatabase opens the staged database and refuses one whose
// schema is newer than this binary. An older schema is fine: the server
// migrates it on start.
func checkRestoredDatabase(ctx context.Context, path string) error {
	// Decoding a configured URI can put ? or # in the staging parents.
	// Encode this filesystem path so the driver does not parse it again.
	dsn := "file:" + (&url.URL{Path: filepath.ToSlash(path)}).EscapedPath()
	db, err := store.Open(ctx, config.Database{Driver: config.DatabaseSQLite, DSN: dsn})
	if err != nil {
		return fmt.Errorf("open restored database: %w", err)
	}
	defer func() { _ = db.Close() }()
	current, latest, err := store.SchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("check restored database: %w", err)
	}
	if current > latest {
		return fmt.Errorf("the backup's schema version %d is newer than this binary supports (%d); restore it with the lerryn version that wrote it", current, latest)
	}
	return nil
}

func checkRestoreTarget(dbPath, storagePath string) error {
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%w: %s exists; stop the server and pass --force to replace it", ErrTargetNotEmpty, p)
		}
	}
	if storagePath == "" {
		return nil
	}
	nonEmpty, err := treeHasFiles(storagePath)
	if err != nil {
		return err
	}
	if nonEmpty {
		return fmt.Errorf("%w: %s holds files; stop the server and pass --force to replace it", ErrTargetNotEmpty, storagePath)
	}
	return nil
}

// treeHasFiles reports whether a regular file exists below root. Empty
// directories, as a server creates on first start, do not count.
func treeHasFiles(root string) (bool, error) {
	found := errors.New("found")
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			return found
		}
		return nil
	})
	switch {
	case errors.Is(err, found):
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// moveAside preserves p in an exclusively created sibling directory.
// Distinct restores never rename over a previous preserved target.
func moveAside(p, suffix string) (string, error) {
	return moveAsideWithRename(p, suffix, os.Rename)
}

func moveAsideWithRename(p, suffix string, rename func(string, string) error) (string, error) {
	if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", fmt.Errorf("check existing restore target %s: %w", p, err)
	}
	dir, err := os.MkdirTemp(filepath.Dir(p), filepath.Base(p)+suffix+"-*")
	if err != nil {
		return "", fmt.Errorf("reserve preservation directory for %s: %w", p, err)
	}
	if err := os.Chmod(dir, dirMode); err != nil {
		_ = os.Remove(dir)
		return "", fmt.Errorf("set preservation directory permissions: %w", err)
	}
	dst := filepath.Join(dir, filepath.Base(p))
	if err := renameRestoreTarget(p, dst, rename); err != nil {
		_ = os.Remove(dir)
		return "", fmt.Errorf("move existing %s aside: %w", p, err)
	}
	return dst, nil
}

// SQLitePath returns the file path of a SQLite DSN as accepted by
// database.dsn: a path or a file: URI, optionally with parameters.
func SQLitePath(dsn string) (string, error) {
	p, rawQuery, _ := strings.Cut(dsn, "?")
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return "", fmt.Errorf("parse SQLite parameters: %w", err)
	}
	// modernc selects the VFS even for ordinary filenames or parameters
	// after a URI fragment. memdb never opens the disk path we would restore.
	for _, vfs := range q["vfs"] {
		if vfs == "memdb" || strings.ContainsRune(vfs, 0) {
			return "", fmt.Errorf("database.dsn %q does not select a disk SQLite VFS", dsn)
		}
	}
	if strings.HasPrefix(p, "file:") {
		// The driver preserves file: URIs for SQLite, which decodes their
		// path and ignores fragments. Plain filenames keep literal % and #.
		withoutFragment, _, hasFragment := strings.Cut(p, "#")
		if !hasFragment {
			for _, mode := range q["mode"] {
				if mode != "ro" && mode != "rw" && mode != "rwc" {
					return "", fmt.Errorf("database.dsn %q does not select a disk SQLite mode", dsn)
				}
			}
		}
		u, err := url.Parse(withoutFragment)
		if err != nil {
			return "", fmt.Errorf("parse SQLite file URI: %w", err)
		}
		if u.User != nil || (u.Host != "" && u.Host != "localhost") {
			return "", fmt.Errorf("database.dsn %q does not name a local SQLite file", dsn)
		}
		p = u.Path
		if u.Opaque != "" {
			p, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return "", fmt.Errorf("decode SQLite file URI: %w", err)
			}
		}
	}
	if p == "" || p == ":memory:" || strings.HasPrefix(p, "//") {
		return "", fmt.Errorf("database.dsn %q does not name a SQLite file", dsn)
	}
	if strings.ContainsRune(p, 0) {
		return "", fmt.Errorf("database.dsn %q contains a zero byte", dsn)
	}
	return cleanSQLitePath(filepath.FromSlash(p))
}

// cleanSQLitePath preserves the filesystem traversal order before removing
// parent components. SQLite follows a symlink before processing a subsequent
// ".."; a lexical Clean can otherwise select a different restore target.
func cleanSQLitePath(p string) (string, error) {
	separator := string(filepath.Separator)
	hasParent := false
	for _, component := range strings.Split(p, separator) {
		if component == ".." {
			hasParent = true
			break
		}
	}
	if !hasParent {
		return filepath.Clean(p), nil
	}
	// Leave the final component in place: restoring through a file symlink
	// has always preserved that link itself, and overlap checks also inspect
	// its target. Only its parents must be resolved before Dir/Join clean them.
	parent, leaf := ".", p
	if i := strings.LastIndex(p, separator); i >= 0 {
		parent, leaf = p[:i], p[i+1:]
		if parent == "" {
			parent = separator
		}
	}
	links := 0
	resolved, err := resolveSQLiteParents(parent, &links)
	if err != nil {
		return "", fmt.Errorf("resolve SQLite database parents: %w", err)
	}
	return filepath.Join(resolved, leaf), nil
}

// SQLite's Unix VFS permits 201 symlink expansions. The actual-driver
// boundary is checked by TestRestoreSQLiteTraversalSymlinkLimit.
const maxSQLitePathSymlinks = 201

func resolveSQLiteParents(p string, links *int) (string, error) {
	separator := string(filepath.Separator)
	volume := filepath.VolumeName(p)
	rest := strings.TrimPrefix(p, volume)
	current := volume
	if strings.HasPrefix(rest, separator) {
		current += separator
	}
	for _, component := range strings.Split(rest, separator) {
		switch component {
		case "", ".":
			continue
		case "..":
			current = filepath.Join(current, "..")
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			// A restore may create new parents. SQLite also allows a missing
			// component to be canceled by a subsequent "..".
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		*links++
		if *links > maxSQLitePathSymlinks {
			return "", errors.New("too many symbolic links in SQLite database path")
		}
		target, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			// Join would discard target's parent components too early.
			target = filepath.Dir(current) + separator + target
		}
		current, err = resolveSQLiteParents(target, links)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(current); err != nil {
			return "", fmt.Errorf("resolve symbolic link %s: %w", target, err)
		}
	}
	if current == "" {
		current = "."
	}
	return current, nil
}
