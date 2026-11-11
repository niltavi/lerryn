package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Task file rules (API schema ApplyTaskRequest).
const (
	maxTaskFiles     = 100
	maxTaskBytes     = 1 << 20
	maxFilePathBytes = 255
)

// taskNamePattern matches the TaskName schema.
var taskNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// Tasks creates tasks and their versions, and activates, lists and
// archives them.
type Tasks struct {
	db    *store.DB
	authz *Authorizer
	blobs blob.Store
	log   *slog.Logger
	// requireTest reports whether a version needs a successful test run
	// before it can be activated (tech design 5.2).
	requireTest func(context.Context) (bool, error)
	// plaintext rejects submissions containing credential values; nil
	// skips the check.
	plaintext *Credentials
}

// WithPlaintextCheck rejects manifests, files and tags containing the
// value of a credential the caller owns or one bound to the task
// (tech design 11.7). It returns t for chaining.
func (t *Tasks) WithPlaintextCheck(c *Credentials) *Tasks {
	t.plaintext = c
	return t
}

// NewTasks returns the task service. File contents are kept in blobs.
// Activation requires a tested version until WithTestBeforeActivate says
// otherwise.
func NewTasks(db *store.DB, authz *Authorizer, blobs blob.Store, log *slog.Logger) *Tasks {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Tasks{
		db: db, authz: authz, blobs: blobs, log: log,
		requireTest: func(context.Context) (bool, error) { return true, nil },
	}
}

// WithTestBeforeActivate sets where the "test before activate" switch is
// read from, such as the deployment settings. It returns t for chaining.
func (t *Tasks) WithTestBeforeActivate(required func(context.Context) (bool, error)) *Tasks {
	t.requireTest = required
	return t
}

// FileInput is a submitted task file.
type FileInput struct {
	// Path is relative, separated by '/'.
	Path       string
	Content    []byte
	Executable bool
}

// ApplyTaskInput is a submission of a task version: the complete manifest
// and every file. Files not listed are not part of the new version.
type ApplyTaskInput struct {
	ProjectID uuid.UUID
	Name      string
	Manifest  Manifest
	Files     []FileInput
	// IfMatch is the latest version number the caller read. It is
	// required when the task exists; 0 asserts that it does not.
	IfMatch *int
	// DryRun validates and previews the submission without saving it.
	DryRun bool
}

// ApplyResult is the outcome of a submission.
type ApplyResult struct {
	DryRun  bool
	Created bool
	// Version is the number of the new version, or the one a dry run
	// would create.
	Version int
	// Task is the task after the submission; for a dry run, before it,
	// and nil when the task does not exist yet.
	Task *TaskDetail
	// Diff compares the submission with the active version, or the
	// latest when none is active; nil for a new task.
	Diff     *TaskDiff
	Warnings []Issue
}

// TaskDetail is a task with its latest version.
type TaskDetail struct {
	Task    store.Task
	Version VersionDetail
	Tags    []string
}

// VersionDetail is a task version with its decoded manifest and its
// files, sorted by path.
type VersionDetail struct {
	Version  store.TaskVersion
	Manifest Manifest
	Files    []store.TaskFile
}

// Apply creates the task named in.Name in the project with its first
// version, or adds a new version to the existing task. A task that exists
// needs in.IfMatch equal to its latest version, so two sessions editing
// the same task cannot overwrite each other (tech design 9.3): the
// second gets a *VersionConflictError and reads the first one's change.
func (t *Tasks) Apply(ctx context.Context, p *auth.Principal, in ApplyTaskInput) (ApplyResult, error) {
	if err := t.authz.Require(ctx, p, in.ProjectID, ActionModifyTask); err != nil {
		return ApplyResult{}, err
	}
	var v validation
	if !taskNamePattern.MatchString(in.Name) {
		v.param("taskName", "任务名为 1 到 63 个字符，只能包含小写字母、数字和连字符，以字母或数字开头")
	}
	if in.IfMatch != nil && *in.IfMatch < 0 {
		v.param("If-Match", "版本号不能为负数")
	}
	paths := validateFiles(&v, in.Files)
	validateManifest(&v, &in.Manifest, paths)
	if in.Manifest.Workflow == nil {
		v.content("#/manifest/workflow", "提交任务需要流程说明，请按执行顺序写明每一步做什么")
	}
	if err := v.err(); err != nil {
		return ApplyResult{}, err
	}
	manifestJSON, err := json.Marshal(in.Manifest)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("encode manifest: %w", err)
	}

	existing, err := t.current(ctx, in.ProjectID, in.Name)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := checkIfMatch(existing, in.IfMatch); err != nil {
		return ApplyResult{}, err
	}
	if err := t.checkPlaintext(ctx, p, existing, manifestJSON, in.Files); err != nil {
		return ApplyResult{}, err
	}

	files := make([]store.TaskFile, len(in.Files))
	contents := map[string]string{}
	for i, f := range in.Files {
		files[i] = store.TaskFile{Path: f.Path, BlobHash: blob.Sum(f.Content), Size: int64(len(f.Content)), Executable: f.Executable}
		contents[f.Path] = string(f.Content)
	}
	slices.SortFunc(files, func(a, b store.TaskFile) int { return strings.Compare(a.Path, b.Path) })

	res := ApplyResult{DryRun: in.DryRun, Created: existing == nil, Version: 1}
	var taskID uuid.UUID
	if existing != nil {
		taskID = existing.ID
		res.Version = existing.LatestSeq + 1
		base := existing.LatestSeq
		if existing.ActiveSeq != 0 {
			base = existing.ActiveSeq
		}
		target := proposedVersion{manifest: in.Manifest, files: files, contents: contents}
		diff, err := t.diffAgainst(ctx, existing.ID, &base, target, res.Version)
		if err != nil {
			return ApplyResult{}, err
		}
		res.Diff = &diff
	}
	// A new version is never tested; that it must be before activation
	// is said by setTaskActivation, not repeated on every submission.
	errs, warns, err := t.check(ctx, in.ProjectID, taskID, in.Manifest, paths, true)
	if err != nil {
		return ApplyResult{}, err
	}
	res.Warnings = append(errs, warns...)

	if in.DryRun {
		if existing != nil {
			d, err := t.detail(ctx, *existing)
			if err != nil {
				return ApplyResult{}, err
			}
			res.Task = &d
		}
		return res, nil
	}

	// Contents go to the blob store before the transaction. A blob whose
	// transaction then fails is unreferenced, which is harmless; the
	// opposite order could commit a version whose content is missing.
	for _, f := range in.Files {
		if _, err := t.blobs.Put(ctx, bytes.NewReader(f.Content)); err != nil {
			return ApplyResult{}, fmt.Errorf("store file %s: %w", f.Path, err)
		}
	}
	client, err := clientName(ctx, t.db, p)
	if err != nil {
		return ApplyResult{}, err
	}
	expected := 0
	if existing != nil {
		expected = existing.LatestSeq
	}
	sub := submission{in: in, principal: p, manifest: string(manifestJSON), treeHash: treeHash(files), files: files, client: client, expected: expected}
	detail, restore, err := t.applyOnce(ctx, sub)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := restoreFileBlobs(ctx, t.blobs, restore, in.Files); err != nil {
		return ApplyResult{}, err
	}
	detail.Version.Manifest = in.Manifest
	res.Task = &detail
	res.Version = detail.Version.Version.Seq
	return res, nil
}

// current returns the unarchived task with the name in the project, nil
// if there is none.
func (t *Tasks) current(ctx context.Context, projectID uuid.UUID, name string) (*store.Task, error) {
	var task store.Task
	err := t.db.NewSelect().Model(&task).
		Where("project_id = ? AND name = ? AND archived_at IS NULL", projectID, name).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &task, nil
}

// checkIfMatch applies the If-Match rules of applyTask to the task as
// read: required for an existing task and equal to its latest version; 0
// or absent for a new one.
func checkIfMatch(existing *store.Task, ifMatch *int) error {
	switch {
	case existing == nil && ifMatch != nil && *ifMatch != 0:
		return &VersionConflictError{Detail: "任务不存在，可能已被归档；不带 If-Match 提交会建立新任务"}
	case existing == nil:
		return nil
	case ifMatch == nil:
		return ErrPreconditionRequired
	case *ifMatch != existing.LatestSeq:
		return &VersionConflictError{Current: existing.LatestSeq}
	}
	return nil
}

type submission struct {
	in        ApplyTaskInput
	principal *auth.Principal
	manifest  string
	treeHash  string
	files     []store.TaskFile
	client    string
	// expected is the latest version the submission builds on, 0 for a
	// new task.
	expected int
}

// applyOnce writes one version. The next version number is taken with an
// increment of latest_seq guarded by the expected value, so of two
// submissions built on the same version exactly one succeeds; the other
// gets a *VersionConflictError naming the version it lost to.
func (t *Tasks) applyOnce(ctx context.Context, sub submission) (TaskDetail, []string, error) {
	var detail TaskDetail
	var restore []string
	err := t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		now := store.Now()
		var task store.Task
		created := sub.expected == 0
		if created {
			task = store.Task{
				ID:          store.NewID(),
				ProjectID:   sub.in.ProjectID,
				Name:        sub.in.Name,
				Title:       sub.in.Manifest.Title,
				Description: sub.in.Manifest.Description,
				LatestSeq:   1,
				CreatedBy:   sub.principal.UserID,
				CreatedAt:   now,
				UpdatedAt:   now,
				State:       store.TaskInactive,
			}
			if _, err := tx.NewInsert().Model(&task).Exec(ctx); err != nil {
				if store.IsUniqueViolation(err) {
					return t.lostRace(ctx, sub)
				}
				return err
			}
		} else {
			err := tx.NewUpdate().Model(&task).
				Set("latest_seq = latest_seq + 1").
				Set("title = ?", sub.in.Manifest.Title).
				Set("description = ?", sub.in.Manifest.Description).
				Set("updated_at = ?", now).
				Where("project_id = ? AND name = ? AND archived_at IS NULL AND latest_seq = ?", sub.in.ProjectID, sub.in.Name, sub.expected).
				Returning("*").
				Scan(ctx)
			if errors.Is(err, sql.ErrNoRows) {
				return t.lostRace(ctx, sub)
			}
			if err != nil {
				return err
			}
		}

		version := store.TaskVersion{
			ID:        store.NewID(),
			TaskID:    task.ID,
			Seq:       task.LatestSeq,
			Manifest:  sub.manifest,
			TreeHash:  sub.treeHash,
			CreatedBy: sub.principal.UserID,
			CreatedAt: now,
			Client:    sub.client,
		}
		if _, err := tx.NewInsert().Model(&version).Exec(ctx); err != nil {
			return err
		}
		files := slices.Clone(sub.files)
		for i := range files {
			files[i].ID = store.NewID()
			files[i].VersionID = version.ID
		}
		if _, err := tx.NewInsert().Model(&files).Exec(ctx); err != nil {
			return err
		}

		kind, actor := actorOf(sub.principal)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "task.apply", resourceKind: "task", resourceID: task.ID.String(),
			result: resultSuccess,
			details: map[string]any{
				"project_id": task.ProjectID.String(), "name": task.Name, "version": version.Seq,
				"created": created, "files": len(files), "tree_hash": version.TreeHash,
			},
		}); err != nil {
			return err
		}
		detail = TaskDetail{Task: task, Version: VersionDetail{Version: version, Files: files}}
		if !created {
			tags, err := store.TaskTags(ctx, tx, []uuid.UUID{task.ID})
			if err != nil {
				return err
			}
			detail.Tags = tags[task.ID]
		}
		var err error
		restore, err = claimBlobs(ctx, tx, fileHashes(files))
		return err
	})
	return detail, restore, err
}

// lostRace explains a guarded write that matched nothing: another
// submission created the task or took the next version first. It reads
// outside the transaction, which is about to roll back.
func (t *Tasks) lostRace(ctx context.Context, sub submission) error {
	now, err := t.current(ctx, sub.in.ProjectID, sub.in.Name)
	if err != nil {
		return err
	}
	if now == nil {
		return &VersionConflictError{Detail: "任务已被归档；重新读取后再提交"}
	}
	return &VersionConflictError{Current: now.LatestSeq}
}

// detail loads the latest version of a task with its files.
func (t *Tasks) detail(ctx context.Context, task store.Task) (TaskDetail, error) {
	version, err := t.version(ctx, task.ID, task.LatestSeq)
	if err != nil {
		return TaskDetail{}, err
	}
	tags, err := store.TaskTags(ctx, t.db, []uuid.UUID{task.ID})
	if err != nil {
		return TaskDetail{}, err
	}
	return TaskDetail{Task: task, Version: version, Tags: tags[task.ID]}, nil
}

// version loads a version of a task with its files. A missing version is
// ErrNotFound.
func (t *Tasks) version(ctx context.Context, taskID uuid.UUID, seq int) (VersionDetail, error) {
	version, err := loadVersion(ctx, t.db, taskID, seq)
	if errors.Is(err, sql.ErrNoRows) {
		return VersionDetail{}, ErrNotFound
	}
	if err != nil {
		return VersionDetail{}, err
	}
	var files []store.TaskFile
	if err := t.db.NewSelect().Model(&files).Where("version_id = ?", version.Version.ID).Order("path").Scan(ctx); err != nil {
		return VersionDetail{}, err
	}
	version.Files = files
	return version, nil
}

// Get returns a task and its latest version. A task the principal may not
// see is ErrNotFound.
func (t *Tasks) Get(ctx context.Context, p *auth.Principal, id uuid.UUID) (TaskDetail, error) {
	task, err := t.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return TaskDetail{}, err
	}
	return t.detail(ctx, task)
}

// load reads a task and checks that p may perform action on it.
func (t *Tasks) load(ctx context.Context, p *auth.Principal, id uuid.UUID, action Action) (store.Task, error) {
	var task store.Task
	err := t.db.NewSelect().Model(&task).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Task{}, ErrNotFound
	}
	if err != nil {
		return store.Task{}, err
	}
	if err := t.authz.Require(ctx, p, task.ProjectID, action); err != nil {
		return store.Task{}, err
	}
	return task, nil
}

// loadVersion reads version seq of a task and decodes its manifest. A
// missing version is sql.ErrNoRows.
func loadVersion(ctx context.Context, idb bun.IDB, taskID uuid.UUID, seq int) (VersionDetail, error) {
	var version store.TaskVersion
	if err := idb.NewSelect().Model(&version).Where("task_id = ? AND seq = ?", taskID, seq).Scan(ctx); err != nil {
		return VersionDetail{}, fmt.Errorf("version %d of task %s: %w", seq, taskID, err)
	}
	m, err := decodeManifest(version.Manifest)
	if err != nil {
		return VersionDetail{}, fmt.Errorf("version %s: %w", version.ID, err)
	}
	return VersionDetail{Version: version, Manifest: m}, nil
}

// validateFiles checks the submitted file list and returns the set of
// paths.
func validateFiles(v *validation, files []FileInput) map[string]bool {
	paths := map[string]bool{}
	switch {
	case len(files) == 0:
		v.content("#/files", "至少需要一个文件")
	case len(files) > maxTaskFiles:
		v.content("#/files", fmt.Sprintf("最多 %d 个文件", maxTaskFiles))
		return paths
	}
	total := 0
	dirs := map[string]bool{}
	for i, f := range files {
		total += len(f.Content)
		where := fmt.Sprintf("#/files/%d/path", i)
		if detail := checkFilePath(f.Path); detail != "" {
			v.content(where, detail)
			continue
		}
		if paths[f.Path] {
			v.content(where, fmt.Sprintf("路径 %s 重复", f.Path))
			continue
		}
		paths[f.Path] = true
		for dir := f.Path; ; {
			slash := strings.LastIndexByte(dir, '/')
			if slash < 0 {
				break
			}
			dir = dir[:slash]
			dirs[dir] = true
		}
	}
	for i, f := range files {
		// One name cannot be a file and a directory when the version is
		// laid out on disk.
		if dirs[f.Path] && paths[f.Path] {
			v.content(fmt.Sprintf("#/files/%d/path", i), fmt.Sprintf("%s 同时被用作文件和目录", f.Path))
		}
	}
	if total > maxTaskBytes {
		v.content("#/files", "文件总大小不能超过 1 MiB")
	}
	return paths
}

// checkFilePath returns why path is not an acceptable task file path, or
// "" when it is.
func checkFilePath(path string) string {
	switch {
	case path == "" || len(path) > maxFilePathBytes:
		return fmt.Sprintf("路径为 1 到 %d 字节", maxFilePathBytes)
	case strings.HasPrefix(path, "/"):
		return "路径必须是相对路径，不能以 / 开头"
	case strings.ContainsRune(path, 0):
		return "路径不能包含 NUL 字符"
	case strings.ContainsRune(path, '\\'):
		return "路径用 / 分隔，不能包含反斜杠"
	}
	for seg := range strings.SplitSeq(path, "/") {
		switch seg {
		case "":
			return "路径不能包含空段，也不能以 / 结尾"
		case ".", "..":
			return "路径不能包含 . 或 .. 段"
		}
	}
	return ""
}

// treeHash identifies a file tree: the hex SHA-256 over the files sorted
// by path, one line each of path, executable bit and content hash. Paths
// contain no NUL, so the encoding is unambiguous.
func treeHash(sorted []store.TaskFile) string {
	h := sha256.New()
	for _, f := range sorted {
		mode := "-"
		if f.Executable {
			mode = "x"
		}
		_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s\n", f.Path, mode, f.BlobHash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkPlaintext rejects a submission whose manifest or files contain a
// credential value. A dry run is checked too: the value is already in the
// request, and the agent should learn before it builds on it.
func (t *Tasks) checkPlaintext(ctx context.Context, p *auth.Principal, existing *store.Task, manifest []byte, files []FileInput) error {
	if t.plaintext == nil {
		return nil
	}
	texts, err := JSONScanTexts("#/manifest", manifest)
	if err != nil {
		return err
	}
	for i, f := range files {
		texts = append(texts, ScanText{Base: fmt.Sprintf("#/files/%d/content", i), Text: string(f.Content)})
	}
	taskID := uuid.Nil
	if existing != nil {
		taskID = existing.ID
	}
	return t.plaintext.CheckPlaintext(ctx, p, taskID, texts)
}
