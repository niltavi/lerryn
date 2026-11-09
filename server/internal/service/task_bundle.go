package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/store"
)

// Task bundle rules (API schemas TaskBundle and ImportTaskRequest).
const (
	// maxBundleVersions bounds the version history a bundle carries.
	maxBundleVersions = 50
	// maxBundleBytes keeps an exported bundle importable: importTask's
	// request body is limited to 4 MiB (httpapi maxRequestBytes), and the
	// request wraps the bundle with a few more bytes.
	maxBundleBytes = 4<<20 - 64<<10
	// bundleFileOverhead approximates the JSON around one file entry
	// besides its path and content.
	bundleFileOverhead = 64
)

// TaskBundle is a portable task (tech design 15.5): its name, the
// manifest and files of one or more versions and, optionally, its
// trigger definitions. It holds no runs, no credential values or
// bindings, no webhook tokens and no IDs of this deployment, so it can be
// imported into any project of any deployment.
type TaskBundle struct {
	Name       string
	ExportedAt time.Time
	// ActiveVersion is the source's active version number, 0 for none.
	ActiveVersion int
	// OmittedVersions counts earlier versions a history export left out
	// to stay within the limits.
	OmittedVersions int
	// Versions are oldest first; the last is the one exported.
	Versions []BundleVersion
	// Triggers is nil when the bundle has no trigger definitions, and
	// empty when the task had none.
	Triggers []TriggerInput
	// HideManualRun carries the task's console option. A bundle exported
	// before the option existed leaves it false, the default.
	HideManualRun bool
}

// BundleVersion is one version in a bundle.
type BundleVersion struct {
	// Seq is the version number in the source deployment.
	Seq       int
	CreatedAt time.Time
	Manifest  Manifest
	Files     []FileInput
}

// ExportTaskInput selects what to export.
type ExportTaskInput struct {
	// Version is the version to export; nil means the latest.
	Version *int
	// History adds the versions before Version.
	History bool
	// Triggers adds the trigger definitions.
	Triggers bool
}

// Export returns a task as a bundle. Anyone who can view the task may
// export it: the bundle holds nothing that getTaskVersion, getTaskFile
// and listTriggers do not already show them. Every export is audited.
func (t *Tasks) Export(ctx context.Context, p *auth.Principal, id uuid.UUID, in ExportTaskInput) (TaskBundle, error) {
	task, err := t.load(ctx, p, id, ActionViewTask)
	if err != nil {
		return TaskBundle{}, err
	}
	seq := task.LatestSeq
	if in.Version != nil {
		if *in.Version < 1 || *in.Version > task.LatestSeq {
			var v validation
			v.param("version", fmt.Sprintf("任务没有版本 %d，最新版本是 %d", *in.Version, task.LatestSeq))
			return TaskBundle{}, v.err()
		}
		seq = *in.Version
	}
	first := seq
	if in.History {
		first = max(1, seq-maxBundleVersions+1)
	}

	// Without history the earlier versions are not asked for, so none
	// counts as omitted.
	b := TaskBundle{Name: task.Name, ExportedAt: store.Now(), ActiveVersion: task.ActiveSeq, HideManualRun: task.ConsoleHideManualRun}
	if in.History {
		b.OmittedVersions = first - 1
	}
	size := 4 << 10 // name, times and the other fixed fields, generously
	if in.Triggers {
		var rows []store.Trigger
		if err := t.db.NewSelect().Model(&rows).Where("task_id = ?", id).Order("name").Scan(ctx); err != nil {
			return TaskBundle{}, fmt.Errorf("read triggers: %w", err)
		}
		b.Triggers = make([]TriggerInput, 0, len(rows))
		for _, row := range rows {
			ti, err := triggerDefinition(row)
			if err != nil {
				return TaskBundle{}, err
			}
			b.Triggers = append(b.Triggers, ti)
			size += len(row.Name) + len(row.Schedule) + len(row.Timezone) + len(row.Params) + 256
		}
	}

	// Newest first, so that when the history does not fit it is the
	// oldest versions that are left out; the exported version always is in.
	for s := seq; s >= first; s-- {
		bv, n, err := t.bundleVersion(ctx, id, s)
		if err != nil {
			return TaskBundle{}, err
		}
		if size+n > maxBundleBytes {
			if s == seq {
				var v validation
				v.param("version", "这个版本编码后超过 4 MiB，无法导出")
				return TaskBundle{}, v.err()
			}
			b.OmittedVersions += s - first + 1
			break
		}
		size += n
		b.Versions = append(b.Versions, bv)
	}
	slices.Reverse(b.Versions)

	kind, actor := actorOf(p)
	versions := make([]int, len(b.Versions))
	for i, v := range b.Versions {
		versions[i] = v.Seq
	}
	if err := writeAudit(ctx, t.db, auditEntry{
		actorKind: kind, actorID: actor,
		action: "task.export", resourceKind: "task", resourceID: task.ID.String(),
		result: resultSuccess,
		details: map[string]any{
			"project_id": task.ProjectID.String(), "name": task.Name, "versions": versions,
			"omitted_versions": b.OmittedVersions, "triggers": b.Triggers != nil,
		},
	}); err != nil {
		return TaskBundle{}, err
	}
	return b, nil
}

// bundleVersion reads version seq with its file contents and returns it
// with its approximate size in a JSON bundle.
func (t *Tasks) bundleVersion(ctx context.Context, taskID uuid.UUID, seq int) (BundleVersion, int, error) {
	v, err := t.version(ctx, taskID, seq)
	if err != nil {
		return BundleVersion{}, 0, err
	}
	bv := BundleVersion{Seq: seq, CreatedAt: v.Version.CreatedAt, Manifest: v.Manifest, Files: make([]FileInput, len(v.Files))}
	size := len(v.Version.Manifest) + 128
	for i, f := range v.Files {
		content, err := blob.ReadAll(ctx, t.blobs, f.BlobHash)
		if err != nil {
			return BundleVersion{}, 0, fmt.Errorf("read %s of version %d: %w", f.Path, seq, err)
		}
		bv.Files[i] = FileInput{Path: f.Path, Content: content, Executable: f.Executable}
		// JSON escaping can grow content several times (control and HTML
		// characters), so measure the encoding rather than the bytes.
		enc, err := json.Marshal(string(content))
		if err != nil {
			return BundleVersion{}, 0, fmt.Errorf("encode %s: %w", f.Path, err)
		}
		size += len(enc) + len(f.Path) + bundleFileOverhead
	}
	return bv, size, nil
}

// triggerDefinition turns a stored trigger into the input that recreates
// it, without its ID, token or schedule state.
func triggerDefinition(row store.Trigger) (TriggerInput, error) {
	enabled := row.Enabled
	ti := TriggerInput{Name: row.Name, Type: row.Type, Enabled: &enabled, CatchUp: row.CatchUp}
	switch row.Type {
	case store.TriggerTypeCron:
		ti.Schedule, ti.Timezone = row.Schedule, row.Timezone
	case store.TriggerTypeWebhook:
		n := row.MaxBodyBytes
		ti.MaxBodyBytes = &n
		// catch_up only means something for cron.
		ti.CatchUp = ""
	}
	if row.Params != "" {
		if err := json.Unmarshal([]byte(row.Params), &ti.Params); err != nil {
			return TriggerInput{}, fmt.Errorf("decode params of trigger %s: %w", row.ID, err)
		}
		if ti.Params == nil {
			ti.Params = map[string]any{}
		}
	}
	return ti, nil
}

// ImportTaskInput imports a bundle into a project as a new task.
type ImportTaskInput struct {
	ProjectID uuid.UUID
	Bundle    TaskBundle
	// Name overrides the bundle's task name when not empty.
	Name string
	// DryRun validates and previews the import without saving it.
	DryRun bool
}

// ImportResult is the outcome of an import.
type ImportResult struct {
	DryRun bool
	// Task is the new task with its latest version, nil for a dry run.
	Task *TaskDetail
	// SourceSeqs are the bundle's version numbers; the new task numbers
	// them 1, 2, ... in this order.
	SourceSeqs []int
	// Triggers are the triggers created, with the new webhook tokens.
	Triggers TriggerSet
	// Credentials are the references of the latest version, all unbound.
	Credentials []TaskCredential
	Warnings    []Issue
}

// Import creates a task from a bundle in the target project, with the
// caller as its creator. The bundle's versions become versions 1 to n,
// each validated as applyTask validates a submission, credential
// plaintext check included; none is tested, so the test-before-activate
// rule applies to them as to any new version. Credential references stay
// names with no binding, and webhook triggers get new tokens. The task
// starts inactive. A task of the same name in the project is a
// *VersionConflictError, as applyTask with If-Match 0 would be.
func (t *Tasks) Import(ctx context.Context, p *auth.Principal, in ImportTaskInput) (ImportResult, error) {
	if err := t.authz.Require(ctx, p, in.ProjectID, ActionModifyTask); err != nil {
		return ImportResult{}, err
	}
	name := in.Name
	namePointer := "#/name"
	if name == "" {
		name, namePointer = in.Bundle.Name, "#/bundle/name"
	}

	var v validation
	if !taskNamePattern.MatchString(name) {
		v.content(namePointer, "任务名为 1 到 63 个字符，只能包含小写字母、数字和连字符，以字母或数字开头")
	}
	versions := in.Bundle.Versions
	switch {
	case len(versions) == 0:
		v.content("#/bundle/versions", "任务包中至少要有一个版本")
	case len(versions) > maxBundleVersions:
		v.content("#/bundle/versions", fmt.Sprintf("任务包中最多 %d 个版本", maxBundleVersions))
		return ImportResult{}, v.err()
	}
	pathSets := make([]map[string]bool, len(versions))
	for i := range versions {
		bv := &versions[i]
		at := fmt.Sprintf("#/bundle/versions/%d", i)
		if i > 0 && bv.Seq <= versions[i-1].Seq {
			v.content(at+"/seq", "版本按版本号从旧到新排列，版本号不能重复")
		}
		// The same rules as applyTask, reported at the version's place in
		// the bundle.
		var one validation
		pathSets[i] = validateFiles(&one, bv.Files)
		validateManifest(&one, &bv.Manifest, pathSets[i])
		for _, f := range one.fields {
			v.content(at+strings.TrimPrefix(f.Pointer, "#"), f.Detail)
		}
	}
	if in.Bundle.Triggers != nil {
		if err := validateTriggers(in.Bundle.Triggers); err != nil {
			verr, ok := errors.AsType[*ValidationError](err)
			if !ok {
				return ImportResult{}, err
			}
			for _, f := range verr.Fields {
				v.content("#/bundle"+strings.TrimPrefix(f.Pointer, "#"), f.Detail)
			}
		}
	}
	if err := v.err(); err != nil {
		return ImportResult{}, err
	}

	existing, err := t.current(ctx, in.ProjectID, name)
	if err != nil {
		return ImportResult{}, err
	}
	if existing != nil {
		return ImportResult{}, &VersionConflictError{Detail: fmt.Sprintf("项目中已有名为 %s 的任务；用 name 换一个任务名导入", name)}
	}
	if err := t.checkBundlePlaintext(ctx, p, in.Bundle); err != nil {
		return ImportResult{}, err
	}

	manifests := make([]string, len(versions))
	files := make([][]store.TaskFile, len(versions))
	res := ImportResult{DryRun: in.DryRun, SourceSeqs: make([]int, len(versions))}
	for i, bv := range versions {
		b, err := json.Marshal(bv.Manifest)
		if err != nil {
			return ImportResult{}, fmt.Errorf("encode manifest: %w", err)
		}
		manifests[i] = string(b)
		files[i] = make([]store.TaskFile, len(bv.Files))
		for j, f := range bv.Files {
			files[i][j] = store.TaskFile{Path: f.Path, BlobHash: blob.Sum(f.Content), Size: int64(len(f.Content)), Executable: f.Executable}
		}
		slices.SortFunc(files[i], func(a, b store.TaskFile) int { return strings.Compare(a.Path, b.Path) })
		res.SourceSeqs[i] = bv.Seq
	}

	// What the new task will need before it runs is what applyTask warns
	// about for its latest version; the task does not exist yet, so no
	// credential is bound.
	last := len(versions) - 1
	latest := versions[last].Manifest
	errs, warns, err := t.check(ctx, in.ProjectID, uuid.Nil, latest, pathSets[last], true)
	if err != nil {
		return ImportResult{}, err
	}
	for _, is := range append(errs, warns...) {
		if strings.HasPrefix(is.Pointer, "#/") {
			is.Pointer = fmt.Sprintf("#/bundle/versions/%d%s", last, strings.TrimPrefix(is.Pointer, "#"))
		}
		res.Warnings = append(res.Warnings, is)
	}
	for _, spec := range latest.Credentials {
		spec.As = spec.as()
		res.Credentials = append(res.Credentials, TaskCredential{Spec: spec, Status: TaskCredentialMissing})
	}

	now := store.Now()
	task := store.Task{
		ID:          store.NewID(),
		ProjectID:   in.ProjectID,
		Name:        name,
		Title:       latest.Title,
		Description: latest.Description,
		LatestSeq:   len(versions),
		CreatedBy:   p.UserID,
		CreatedAt:   now,
		UpdatedAt:   now,
		State:       store.TaskInactive,

		ConsoleHideManualRun: in.Bundle.HideManualRun,
	}
	triggers := in.Bundle.Triggers
	if triggers == nil {
		triggers = []TriggerInput{}
	}
	plan, err := planTriggers(task, nil, triggers, p.UserID, now)
	if err != nil {
		return ImportResult{}, err
	}
	if len(plan.rows) > 0 {
		task.TriggersRevision = 1
	}
	view := (&Triggers{log: t.log}).view
	if in.DryRun {
		res.Triggers = view(task, plan.rows, nil, true, now)
		return res, nil
	}

	// Contents go to the blob store first, as in Apply.
	for _, bv := range versions {
		for _, f := range bv.Files {
			if _, err := t.blobs.Put(ctx, bytes.NewReader(f.Content)); err != nil {
				return ImportResult{}, fmt.Errorf("store file %s: %w", f.Path, err)
			}
		}
	}
	client, err := clientName(ctx, t.db, p)
	if err != nil {
		return ImportResult{}, err
	}
	var detail TaskDetail
	var restore []string
	tokens := map[uuid.UUID]string{}
	err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		clear(tokens)
		if _, err := tx.NewInsert().Model(&task).Exec(ctx); err != nil {
			if store.IsUniqueViolation(err) {
				return &VersionConflictError{Detail: fmt.Sprintf("项目中已有名为 %s 的任务；用 name 换一个任务名导入", name)}
			}
			return err
		}
		for i := range versions {
			version := store.TaskVersion{
				ID:        store.NewID(),
				TaskID:    task.ID,
				Seq:       i + 1,
				Manifest:  manifests[i],
				TreeHash:  treeHash(files[i]),
				CreatedBy: p.UserID,
				CreatedAt: now,
				Client:    client,
			}
			if _, err := tx.NewInsert().Model(&version).Exec(ctx); err != nil {
				return err
			}
			rows := slices.Clone(files[i])
			for j := range rows {
				rows[j].ID = store.NewID()
				rows[j].VersionID = version.ID
			}
			if _, err := tx.NewInsert().Model(&rows).Exec(ctx); err != nil {
				return err
			}
			if i == last {
				detail = TaskDetail{Task: task, Version: VersionDetail{Version: version, Manifest: latest, Files: rows}}
			}
		}
		for i := range plan.rows {
			row := &plan.rows[i]
			if row.Type == store.TriggerTypeWebhook {
				token, hash, err := auth.NewToken(auth.TriggerTokenPrefix)
				if err != nil {
					return err
				}
				row.TokenHash, row.TokenPrefix = hash, auth.DisplayPrefix(token)
				tokens[row.ID] = token
			}
			if _, err := tx.NewInsert().Model(row).Exec(ctx); err != nil {
				return fmt.Errorf("insert trigger %s: %w", row.Name, err)
			}
		}
		credentials := make([]string, len(latest.Credentials))
		for i, c := range latest.Credentials {
			credentials[i] = c.Name
		}
		kind, actor := actorOf(p)
		if err := writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "task.import", resourceKind: "task", resourceID: task.ID.String(),
			result: resultSuccess,
			details: map[string]any{
				"project_id": task.ProjectID.String(), "name": task.Name, "source_name": in.Bundle.Name,
				"versions": len(versions), "source_versions": res.SourceSeqs, "triggers": plan.names(plan.created),
				"credentials": credentials,
			},
		}); err != nil {
			return err
		}
		// Last in the transaction, so that the collector cannot delete
		// an object stored above before these rows reference it.
		var hashes []string
		for _, fs := range files {
			hashes = append(hashes, fileHashes(fs)...)
		}
		var err error
		restore, err = claimBlobs(ctx, tx, hashes)
		return err
	})
	if err != nil {
		return ImportResult{}, err
	}
	var all []FileInput
	for _, bv := range versions {
		all = append(all, bv.Files...)
	}
	if err := restoreFileBlobs(ctx, t.blobs, restore, all); err != nil {
		return ImportResult{}, err
	}
	res.Task = &detail
	res.Triggers = view(task, plan.rows, tokens, false, now)
	return res, nil
}

// checkBundlePlaintext rejects a bundle whose manifests, files or trigger
// params contain the value of a credential the caller owns, as applyTask
// and setTriggers do. The task is new, so no other credential is bound
// to it.
func (t *Tasks) checkBundlePlaintext(ctx context.Context, p *auth.Principal, b TaskBundle) error {
	if t.plaintext == nil {
		return nil
	}
	var texts []ScanText
	for i, bv := range b.Versions {
		manifest, err := json.Marshal(bv.Manifest)
		if err != nil {
			return fmt.Errorf("encode manifest: %w", err)
		}
		more, err := JSONScanTexts(fmt.Sprintf("#/bundle/versions/%d/manifest", i), manifest)
		if err != nil {
			return err
		}
		texts = append(texts, more...)
		for j, f := range bv.Files {
			texts = append(texts, ScanText{Base: fmt.Sprintf("#/bundle/versions/%d/files/%d/content", i, j), Text: string(f.Content)})
		}
	}
	for i, tr := range b.Triggers {
		if tr.Params == nil {
			continue
		}
		params, err := json.Marshal(tr.Params)
		if err != nil {
			return fmt.Errorf("encode trigger params: %w", err)
		}
		more, err := JSONScanTexts(fmt.Sprintf("#/bundle/triggers/%d/params", i), params)
		if err != nil {
			return err
		}
		texts = append(texts, more...)
	}
	return t.plaintext.CheckPlaintext(ctx, p, uuid.Nil, texts)
}
