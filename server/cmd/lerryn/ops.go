package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/config"
	"github.com/niltavi/lerryn/server/internal/ops"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Maintenance commands of tech design 15.5. Each reads the deployment
// configuration from --config like the server roles do.

func newBackupCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "backup",
		Short: "Back up a SQLite deployment: database and local object store",
		Long: "Write a consistent copy of the SQLite database and the local object store to a\n" +
			"gzip-compressed tar archive. Safe to run while the server is running. The master\n" +
			"key is not included; keep it separately. For PostgreSQL use pg_dump, or lerryn export.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := loadFromFlags(cmd, "backup")
			if err != nil {
				return err
			}
			if output == "" {
				output = "lerryn-backup-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
			}
			return runBackup(cmd.Context(), cfg, log, output, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "archive to create (default lerryn-backup-<time>.tar.gz)")
	return cmd
}

func runBackup(ctx context.Context, cfg config.Config, log *slog.Logger, output string, out io.Writer) error {
	if cfg.Database.Driver != config.DatabaseSQLite {
		return ops.ErrNotSQLite
	}
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	res, err := ops.Backup(ctx, ops.BackupOptions{DB: db, StoragePath: localStoragePath(cfg), Output: output})
	if err != nil {
		return err
	}
	log.InfoContext(ctx, "backup written", "output", output, "schema_version", res.Manifest.SchemaVersion, "storage_files", res.StorageFiles)
	_, err = fmt.Fprintf(out, "wrote %s: database at schema version %d, %d object store file(s)\n"+
		"the master key (%s) is not in the backup; keep a copy separately\n",
		output, res.Manifest.SchemaVersion, res.StorageFiles, cfg.Secrets.MasterKeyFile)
	if !res.Manifest.StorageIncluded {
		_, err = fmt.Fprintf(out, "storage.driver is %s: back up the object store with its own tooling\n", cfg.Storage.Driver)
	}
	return err
}

func newRestoreCmd() *cobra.Command {
	var (
		input string
		force bool
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a SQLite deployment from a lerryn backup",
		Long: "Replace the configured SQLite database and local object store with the contents\n" +
			"of an archive written by lerryn backup. Stop the server first. Existing data is\n" +
			"refused unless --force is given, and is then renamed with a .pre-restore suffix,\n" +
			"not deleted. Put the master key of the backed-up instance in place as well.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := loadFromFlags(cmd, "restore")
			if err != nil {
				return err
			}
			res, err := ops.Restore(cmd.Context(), ops.RestoreOptions{
				Input:       input,
				Database:    cfg.Database,
				StoragePath: localStoragePath(cfg),
				Force:       force,
			})
			if err != nil {
				return err
			}
			log.InfoContext(cmd.Context(), "backup restored", "input", input, "schema_version", res.Manifest.SchemaVersion, "storage_files", res.StorageFiles)
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "restored %s (taken %s by lerryn %s): schema version %d, %d object store file(s)\n",
				input, res.Manifest.CreatedAt.Format(time.RFC3339), res.Manifest.LerrynVersion, res.Manifest.SchemaVersion, res.StorageFiles); err != nil {
				return err
			}
			for _, p := range res.MovedAside {
				if _, err := fmt.Fprintf(out, "previous data kept at %s\n", p); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", "", "archive written by lerryn backup (required)")
	cmd.Flags().BoolVar(&force, "force", false, "replace existing data, keeping it under a .pre-restore name")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func newExportCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export the whole instance to a portable archive",
		Long: "Write every table and every referenced object to an archive that lerryn import\n" +
			"loads into an empty SQLite or PostgreSQL database. Credentials stay encrypted;\n" +
			"the target needs the same master key. Stop the server first when the export is\n" +
			"meant to replace this instance, or writes after the export are lost.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := loadFromFlags(cmd, "export")
			if err != nil {
				return err
			}
			if output == "" {
				output = "lerryn-export-" + time.Now().UTC().Format("20060102T150405Z") + ".tar.gz"
			}
			db, bs, closeDB, err := openData(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			m, err := ops.Export(cmd.Context(), ops.ExportOptions{DB: db, Blobs: bs, Output: output})
			if err != nil {
				return err
			}
			log.InfoContext(cmd.Context(), "export written", "output", output, "tables", len(m.Tables), "blobs", m.Blobs, "missing_blobs", len(m.MissingBlobs))
			return printExport(cmd.OutOrStdout(), output, m)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "archive to create (default lerryn-export-<time>.tar.gz)")
	return cmd
}

func newImportCmd() *cobra.Command {
	var input string
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Import an export archive into an empty database",
		Long: "Load an archive written by lerryn export into the configured database, which must\n" +
			"be empty, and the configured object store. Do not start the server on the new\n" +
			"configuration before importing. Row counts are verified before the import commits.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := loadFromFlags(cmd, "import")
			if err != nil {
				return err
			}
			db, bs, closeDB, err := openData(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer closeDB()
			res, err := ops.Import(cmd.Context(), ops.ImportOptions{DB: db, Blobs: bs, Input: input})
			if err != nil {
				return err
			}
			log.InfoContext(cmd.Context(), "export imported", "input", input, "database", cfg.Database.Driver, "tables", len(res.Tables), "blobs", res.Blobs, "migrations", len(res.Migrated))
			return printImport(cmd.OutOrStdout(), cfg, res)
		},
	}
	cmd.Flags().StringVarP(&input, "input", "i", "", "archive written by lerryn export (required)")
	_ = cmd.MarkFlagRequired("input")
	return cmd
}

func newMigrateDBCmd() *cobra.Command {
	var (
		toConfig    string
		workDir     string
		keepArchive bool
	)
	cmd := &cobra.Command{
		Use:   "migrate-db",
		Short: "Move the instance to another database, such as SQLite to PostgreSQL",
		Long: "Export the instance described by --config and import it into the empty database\n" +
			"and object store described by --to-config, then compare the row count of every\n" +
			"table on both sides. Stop the server first. The source is not modified.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			src, log, err := loadFromFlags(cmd, "migrate-db")
			if err != nil {
				return err
			}
			dst, err := config.Load(toConfig)
			if err != nil {
				return fmt.Errorf("--to-config: %w", err)
			}
			return runMigrateDB(cmd.Context(), src, dst, log, workDir, keepArchive, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&toConfig, "to-config", "", "config file of the target deployment (required)")
	cmd.Flags().StringVar(&workDir, "work-dir", "", "directory for the intermediate archive (default: the system temporary directory)")
	cmd.Flags().BoolVar(&keepArchive, "keep-archive", false, "keep the intermediate archive after a successful move")
	_ = cmd.MarkFlagRequired("to-config")
	return cmd
}

func runMigrateDB(ctx context.Context, src, dst config.Config, log *slog.Logger, workDir string, keepArchive bool, out io.Writer) error {
	if sameDatabase(src.Database, dst.Database) {
		return errors.New("--config and --to-config point at the same database")
	}
	srcDB, srcBlobs, closeSrc, err := openData(ctx, src)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer closeSrc()
	if err := store.CheckMigrations(ctx, srcDB); err != nil {
		return fmt.Errorf("source schema: %w (run lerryn migrate on the source first)", err)
	}
	dstDB, dstBlobs, closeDst, err := openData(ctx, dst)
	if err != nil {
		return fmt.Errorf("target: %w", err)
	}
	defer closeDst()

	dir, err := os.MkdirTemp(workDir, "lerryn-migrate-db-*")
	if err != nil {
		return err
	}
	archive := filepath.Join(dir, "export.tar.gz")
	defer func() {
		if !keepArchive {
			_ = os.RemoveAll(dir)
		}
	}()

	m, err := ops.Export(ctx, ops.ExportOptions{DB: srcDB, Blobs: srcBlobs, Output: archive})
	if err != nil {
		return fmt.Errorf("export source: %w", err)
	}
	log.InfoContext(ctx, "source exported", "tables", len(m.Tables), "blobs", m.Blobs)
	res, err := ops.Import(ctx, ops.ImportOptions{DB: dstDB, Blobs: dstBlobs, Input: archive})
	if err != nil {
		return fmt.Errorf("import into target: %w", err)
	}

	// Import already matched the manifest; comparing with the live source
	// also catches a server that kept writing during the move.
	srcCounts, err := ops.TableCounts(ctx, srcDB)
	if err != nil {
		return fmt.Errorf("count source rows: %w", err)
	}
	dstCounts, err := ops.TableCounts(ctx, dstDB)
	if err != nil {
		return fmt.Errorf("count target rows: %w", err)
	}
	mismatch, err := printCounts(out, src.Database.Driver, dst.Database.Driver, srcCounts, dstCounts)
	if err != nil {
		return err
	}
	if len(m.MissingBlobs) > 0 {
		if _, err := fmt.Fprintf(out, "%d object(s) referenced by the source were already missing from its object store\n", len(m.MissingBlobs)); err != nil {
			return err
		}
	}
	if mismatch {
		return errors.New("row counts differ between source and target; was the source server still running?")
	}
	log.InfoContext(ctx, "database moved", "from", src.Database.Driver, "to", dst.Database.Driver, "tables", len(res.Tables), "blobs", res.Blobs)
	if keepArchive {
		_, err = fmt.Fprintf(out, "intermediate archive kept at %s\n", archive)
		return err
	}
	_, err = fmt.Fprintln(out, "row counts match; point the servers at the new configuration")
	return err
}

func sameDatabase(a, b config.Database) bool {
	if a.Driver != b.Driver {
		return false
	}
	if a.Driver == config.DatabaseSQLite {
		pa, errA := ops.SQLitePath(a.DSN)
		pb, errB := ops.SQLitePath(b.DSN)
		if errA != nil || errB != nil {
			return false
		}
		if pa == pb {
			return true
		}
		infoA, errA := os.Stat(pa)
		infoB, errB := os.Stat(pb)
		return errA == nil && errB == nil && os.SameFile(infoA, infoB)
	}
	return a.DSN == b.DSN
}

// openData opens the configured database and object store for an
// offline maintenance command. The returned function closes the database.
func openData(ctx context.Context, cfg config.Config) (*store.DB, blob.Store, func(), error) {
	bs, err := openBlobStore(ctx, cfg.Storage)
	if err != nil {
		return nil, nil, nil, err
	}
	db, err := store.Open(ctx, cfg.Database)
	if err != nil {
		return nil, nil, nil, err
	}
	return db, bs, func() { _ = db.Close() }, nil
}

// localStoragePath returns storage.path for the local object store and
// "" for any other.
func localStoragePath(cfg config.Config) string {
	if cfg.Storage.Driver == config.StorageLocal {
		return cfg.Storage.Path
	}
	return ""
}

// loadFromFlags reads the configuration named by the root command's
// --config flag and builds the logger, as the migrate command does.
func loadFromFlags(cmd *cobra.Command, role string) (config.Config, *slog.Logger, error) {
	configPath, err := cmd.Flags().GetString("config")
	if err != nil {
		return config.Config{}, nil, err
	}
	cfg, log, err := (&rootOptions{configPath: configPath}).load()
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, log.With("role", role), nil
}

func printExport(out io.Writer, output string, m ops.ExportManifest) error {
	var rows int64
	for _, t := range m.Tables {
		rows += t.Rows
	}
	if _, err := fmt.Fprintf(out, "wrote %s: %d table(s), %d row(s), %d object(s), schema version %d\n",
		output, len(m.Tables), rows, m.Blobs, m.SchemaVersion); err != nil {
		return err
	}
	if len(m.MissingBlobs) > 0 {
		_, err := fmt.Fprintf(out, "%d referenced object(s) were missing from the object store and are not in the archive\n", len(m.MissingBlobs))
		return err
	}
	return nil
}

func printImport(out io.Writer, cfg config.Config, res ops.ImportResult) error {
	var rows int64
	for _, t := range res.Tables {
		rows += t.Rows
	}
	_, err := fmt.Fprintf(out, "imported %d table(s), %d row(s), %d object(s) into %s; %d later migration(s) applied\n",
		len(res.Tables), rows, res.Blobs, cfg.Database.Driver, len(res.Migrated))
	return err
}

// printCounts writes a table of row counts and reports whether any
// table differs.
func printCounts(out io.Writer, srcName, dstName string, src, dst map[string]int64) (bool, error) {
	names := make([]string, 0, len(src))
	for n := range src {
		names = append(names, n)
	}
	for n := range dst {
		if _, ok := src[n]; !ok {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "table\t%s\t%s\t\n", srcName, dstName)
	mismatch := false
	for _, n := range names {
		mark := ""
		if src[n] != dst[n] {
			mark, mismatch = "differs", true
		}
		_, _ = fmt.Fprintf(w, "%s\t%d\t%d\t%s\n", n, src[n], dst[n], mark)
	}
	return mismatch, w.Flush()
}
