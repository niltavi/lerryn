package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// The project tool registry (tech design 7.8): HTTP APIs and external MCP
// servers a maintainer registers, and script tools, file packages an
// agent registers, so the project's tasks can reference them by name.
// HTTP and MCP tools hand the task an address; a script tool's files are
// mounted read-only into the run.

const (
	maxToolDescriptionRunes = 2000
	maxToolURLBytes         = 2048
)

var toolNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// RegisterToolInput is a tool to register or update. URL is for http and
// mcp tools, Files for script tools.
type RegisterToolInput struct {
	Name, Kind, Description, URL string
	Files                        []FileInput
}

// ToolDetail is a registered tool with the files of a script tool,
// sorted by path.
type ToolDetail struct {
	Tool  store.Tool
	Files []store.ToolFile
}

// ListTools returns the tools of the projects p can view, or of one
// project, by project and name.
func (t *Tasks) ListTools(ctx context.Context, p *auth.Principal, projectID *uuid.UUID) ([]ToolDetail, error) {
	projects, err := t.authz.Visible(ctx, p, ActionViewTask, projectID)
	if err != nil {
		return nil, err
	}
	out := []ToolDetail{}
	if len(projects) == 0 {
		return out, nil
	}
	var rows []store.Tool
	if err := t.db.NewSelect().Model(&rows).Where("project_id IN (?)", bun.List(projects)).
		Order("project_id", "name").Scan(ctx); err != nil {
		return nil, fmt.Errorf("list tools: %w", err)
	}
	files, err := toolFiles(ctx, t.db, rows)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		out = append(out, ToolDetail{Tool: r, Files: files[r.ID]})
	}
	return out, nil
}

// toolFiles returns the files of the script tools among rows, by tool.
func toolFiles(ctx context.Context, idb bun.IDB, rows []store.Tool) (map[uuid.UUID][]store.ToolFile, error) {
	var ids []uuid.UUID
	for _, r := range rows {
		if r.Kind == store.ToolKindScript {
			ids = append(ids, r.ID)
		}
	}
	out := map[uuid.UUID][]store.ToolFile{}
	if len(ids) == 0 {
		return out, nil
	}
	var files []store.ToolFile
	if err := idb.NewSelect().Model(&files).Where("tool_id IN (?)", bun.List(ids)).Order("tool_id", "path").Scan(ctx); err != nil {
		return nil, fmt.Errorf("read tool files: %w", err)
	}
	for _, f := range files {
		out[f.ToolID] = append(out[f.ToolID], f)
	}
	return out, nil
}

// RegisterTool registers a tool in the project, or updates the tool of
// the same name. A script tool's files replace the previous ones.
func (t *Tasks) RegisterTool(ctx context.Context, p *auth.Principal, projectID uuid.UUID, in RegisterToolInput) (ToolDetail, error) {
	if err := t.authz.Require(ctx, p, projectID, ActionRegisterTool); err != nil {
		return ToolDetail{}, err
	}
	in.Description = strings.TrimSpace(in.Description)
	var v validation
	if !toolNamePattern.MatchString(in.Name) {
		v.body("#/name", "工具名为 1 到 63 个小写字母、数字或连字符，以字母或数字开头")
	}
	if n := utf8.RuneCountInString(in.Description); n == 0 || n > maxToolDescriptionRunes {
		v.body("#/description", fmt.Sprintf("说明为 1 到 %d 个字符", maxToolDescriptionRunes))
	}
	switch in.Kind {
	case store.ToolKindHTTP, store.ToolKindMCP:
		if u, err := url.Parse(in.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
			u.User != nil || len(in.URL) > maxToolURLBytes {
			// Credentials in the URL would reach every run and the API; they
			// belong in the credential store.
			v.body("#/url", "地址必须是 http 或 https 的绝对地址，不能包含用户名和密码")
		}
		if len(in.Files) > 0 {
			v.content("#/files", "只有 script 工具可以带文件")
		}
	case store.ToolKindScript:
		if in.URL != "" {
			v.content("#/url", "script 工具没有地址，文件挂载在 /sbx/tools/<名称>")
		}
		validateFiles(&v, in.Files)
	default:
		v.body("#/kind", "工具类型只能是 http、mcp 或 script")
	}
	if err := v.err(); err != nil {
		return ToolDetail{}, err
	}
	if err := t.checkToolPlaintext(ctx, p, in); err != nil {
		return ToolDetail{}, err
	}

	files := make([]store.ToolFile, len(in.Files))
	for i, f := range in.Files {
		files[i] = store.ToolFile{Path: f.Path, BlobHash: blob.Sum(f.Content), Size: int64(len(f.Content)), Executable: f.Executable}
	}
	slices.SortFunc(files, func(a, b store.ToolFile) int { return strings.Compare(a.Path, b.Path) })
	tree := ""
	if in.Kind == store.ToolKindScript {
		// The same digest as a task version's, so the two read alike.
		asTask := make([]store.TaskFile, len(files))
		for i, f := range files {
			asTask[i] = store.TaskFile{Path: f.Path, BlobHash: f.BlobHash, Executable: f.Executable}
		}
		tree = treeHash(asTask)
	}
	// Contents go to the blob store first, as for task versions: an
	// unreferenced blob is harmless, a referenced missing one is not.
	for _, f := range in.Files {
		if _, err := t.blobs.Put(ctx, bytes.NewReader(f.Content)); err != nil {
			return ToolDetail{}, fmt.Errorf("store tool file %s: %w", f.Path, err)
		}
	}
	var out ToolDetail
	var restore []string
	// Two registrations of a new name can race; the loser finds the row
	// on its second attempt and updates it.
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		if err = t.registerOnce(ctx, p, projectID, in, tree, files, &out, &restore); !errors.Is(err, errAppendConflict) {
			break
		}
	}
	if err != nil {
		return ToolDetail{}, err
	}
	if err := restoreFileBlobs(ctx, t.blobs, restore, in.Files); err != nil {
		return ToolDetail{}, err
	}
	return out, nil
}

// checkToolPlaintext rejects a registration whose description, address or
// files contain one of the caller's credential values: every task of the
// project would receive them.
func (t *Tasks) checkToolPlaintext(ctx context.Context, p *auth.Principal, in RegisterToolInput) error {
	if t.plaintext == nil {
		return nil
	}
	texts := []ScanText{{Base: "#/description", Text: in.Description}, {Base: "#/url", Text: in.URL}}
	for i, f := range in.Files {
		texts = append(texts, ScanText{Base: fmt.Sprintf("#/files/%d/content", i), Text: string(f.Content)})
	}
	return t.plaintext.CheckPlaintext(ctx, p, uuid.Nil, texts)
}

func (t *Tasks) registerOnce(ctx context.Context, p *auth.Principal, projectID uuid.UUID, in RegisterToolInput,
	tree string, files []store.ToolFile, out *ToolDetail, restore *[]string) error {
	return t.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := store.Now()
		var tool store.Tool
		err := tx.NewSelect().Model(&tool).Where("project_id = ? AND name = ?", projectID, in.Name).Scan(ctx)
		created := errors.Is(err, sql.ErrNoRows)
		switch {
		case created:
			tool = store.Tool{ID: store.NewID(), ProjectID: projectID, Name: in.Name, CreatedBy: p.UserID, CreatedAt: now}
		case err != nil:
			return err
		}
		tool.Kind, tool.Description, tool.URL, tool.TreeHash, tool.UpdatedAt = in.Kind, in.Description, in.URL, tree, now
		if created {
			_, err = tx.NewInsert().Model(&tool).Exec(ctx)
		} else {
			_, err = tx.NewUpdate().Model(&tool).WherePK().Exec(ctx)
		}
		if err != nil {
			if store.IsUniqueViolation(err) {
				return errAppendConflict
			}
			return err
		}
		if _, err := tx.NewDelete().Model((*store.ToolFile)(nil)).Where("tool_id = ?", tool.ID).Exec(ctx); err != nil {
			return err
		}
		rows := make([]store.ToolFile, len(files))
		for i, f := range files {
			f.ID, f.ToolID = store.NewID(), tool.ID
			rows[i] = f
		}
		if len(rows) > 0 {
			if _, err := tx.NewInsert().Model(&rows).Exec(ctx); err != nil {
				return err
			}
		}
		*out = ToolDetail{Tool: tool, Files: rows}
		hashes := make([]string, len(rows))
		for i, f := range rows {
			hashes[i] = f.BlobHash
		}
		if *restore, err = claimBlobs(ctx, tx, hashes); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		details := map[string]any{"project_id": projectID.String(), "name": tool.Name, "kind": tool.Kind, "created": created}
		if tree != "" {
			details["tree_hash"] = tree
		}
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "tool.register", resourceKind: "tool", resourceID: tool.ID.String(),
			details: details,
		})
	})
}

// RemoveTool removes a tool. Versions that reference it stop validating
// and their runs fail in preparation.
func (t *Tasks) RemoveTool(ctx context.Context, p *auth.Principal, id uuid.UUID) error {
	var tool store.Tool
	if err := t.db.NewSelect().Model(&tool).Where("id = ?", id).Scan(ctx); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := t.authz.Require(ctx, p, tool.ProjectID, ActionRegisterTool); err != nil {
		return err
	}
	return t.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// Files first: SQLite enforces the cascade only with foreign keys
		// on, and the order is right either way.
		if _, err := tx.NewDelete().Model((*store.ToolFile)(nil)).Where("tool_id = ?", id).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewDelete().Model((*store.Tool)(nil)).Where("id = ?", id).Exec(ctx); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "tool.remove", resourceKind: "tool", resourceID: id.String(),
			details: map[string]any{"project_id": tool.ProjectID.String(), "name": tool.Name},
		})
	})
}

// projectTools returns the tools of a project by name.
func projectTools(ctx context.Context, idb bun.IDB, projectID uuid.UUID, names []string) (map[string]store.Tool, error) {
	out := map[string]store.Tool{}
	if len(names) == 0 || projectID == uuid.Nil {
		return out, nil
	}
	var rows []store.Tool
	if err := idb.NewSelect().Model(&rows).Where("project_id = ? AND name IN (?)", projectID, bun.List(names)).
		Scan(ctx); err != nil {
		return nil, fmt.Errorf("read tools: %w", err)
	}
	for _, r := range rows {
		out[r.Name] = r
	}
	return out, nil
}

// toolIssues reports the manifest's tool references that the project has
// not registered, as errors, and HTTP or MCP tools a task without network
// cannot reach, as warnings.
func toolIssues(names []string, network string, registered map[string]store.Tool) (errs, warns []Issue) {
	for i, name := range names {
		pointer := fmt.Sprintf("#/manifest/tools/%d", i)
		r, ok := registered[name]
		switch {
		case !ok:
			errs = append(errs, Issue{Code: codeValidationFailed, Pointer: pointer,
				Message: fmt.Sprintf("工具 %s 没有在项目中登记：先用 registerTool 登记，或从清单的 tools 中去掉", name)})
		case r.Kind != store.ToolKindScript && network == NetworkNone:
			warns = append(warns, Issue{Code: codeValidationFailed, Pointer: pointer,
				Message: fmt.Sprintf("工具 %s 是 %s 工具，清单的 network 为 none，任务连不到它；需要调用时把 network 改为 full", name, r.Kind)})
		}
	}
	return errs, warns
}

// scriptFiles are the files of a run's script tool, Tool indexing the
// run's tools, whose contents loadToolContents reads.
type scriptFiles struct {
	Tool  int
	Files []store.ToolFile
}

// runTools resolves the manifest's tool references for a run. A tool no
// longer registered is returned as missing, naming it. Script tools come
// with the files to load; loadToolContents reads them outside the claim
// transaction.
func runTools(ctx context.Context, idb bun.IDB, names []string, registered map[string]store.Tool) (
	tools []runproto.Tool, pending []scriptFiles, missing, err error) {
	tools = make([]runproto.Tool, 0, len(names))
	var scripts []store.Tool
	for _, name := range names {
		r, ok := registered[name]
		if !ok {
			return nil, nil, fmt.Errorf("工具 %s 已不在项目中登记", name), nil
		}
		if r.Kind == store.ToolKindScript {
			scripts = append(scripts, r)
		}
		tools = append(tools, runproto.Tool{Name: r.Name, Kind: r.Kind, URL: r.URL, TreeHash: r.TreeHash})
	}
	files, err := toolFiles(ctx, idb, scripts)
	if err != nil {
		return nil, nil, nil, err
	}
	for i, name := range names {
		if r := registered[name]; r.Kind == store.ToolKindScript {
			pending = append(pending, scriptFiles{Tool: i, Files: files[r.ID]})
		}
	}
	return tools, pending, nil, nil
}

// loadToolContents reads the pending script tool files into tools,
// checked against their hashes, so a damaged blob store fails the run
// instead of mounting different code.
func loadToolContents(ctx context.Context, blobs blob.Store, tools []runproto.Tool, pending []scriptFiles) error {
	for _, p := range pending {
		t := &tools[p.Tool]
		t.Files = make([]runproto.File, 0, len(p.Files))
		for _, f := range p.Files {
			content, err := blob.ReadAll(ctx, blobs, f.BlobHash)
			if err != nil {
				return fmt.Errorf("file %s of tool %s: %w", f.Path, t.Name, err)
			}
			if blob.Sum(content) != f.BlobHash {
				return fmt.Errorf("file %s of tool %s: content does not match its hash %s", f.Path, t.Name, f.BlobHash)
			}
			t.Files = append(t.Files, runproto.File{Path: f.Path, Content: content, Executable: f.Executable})
		}
	}
	return nil
}
