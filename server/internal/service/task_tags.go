package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/store"
)

const maxTaskTags = 20

func validTaskTag(tag string) bool {
	return tag != "" && tag == strings.TrimSpace(tag) && utf8.ValidString(tag) &&
		utf8.RuneCountInString(tag) <= 40 &&
		!strings.ContainsFunc(tag, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })
}

// SetTags replaces a task's classification tags independently of its versions.
func (t *Tasks) SetTags(ctx context.Context, p *auth.Principal, id uuid.UUID, tags []string) ([]string, error) {
	task, err := t.load(ctx, p, id, ActionModifyTask)
	if err != nil {
		return nil, err
	}
	if !task.ArchivedAt.IsZero() {
		return nil, ErrTaskArchived
	}
	var v validation
	if len(tags) > maxTaskTags {
		v.content("#/tags", "每个任务最多可添加 20 个标签")
		return nil, v.err()
	}
	seen := make(map[string]bool, len(tags))
	texts := make([]ScanText, len(tags))
	for i, tag := range tags {
		pointer := fmt.Sprintf("#/tags/%d", i)
		switch {
		case !validTaskTag(tag):
			v.content(pointer, "标签须为 1 至 40 个字符，不能有首尾空白、控制字符或换行")
		case seen[tag]:
			v.content(pointer, "标签不能重复")
		}
		seen[tag] = true
		texts[i] = ScanText{Base: pointer, Text: tag}
	}
	if err := v.err(); err != nil {
		return nil, err
	}
	if err := t.plaintext.CheckPlaintext(ctx, p, id, texts); err != nil {
		return nil, err
	}

	err = t.db.WriteTx(ctx, func(ctx context.Context, tx bun.Tx) error {
		// The guarded update also serializes tag writers with one another
		// and with task archival on PostgreSQL.
		var locked uuid.UUID
		err := tx.NewUpdate().Model((*store.Task)(nil)).
			Set("updated_at = ?", store.Now()).
			Where("id = ? AND archived_at IS NULL", id).Returning("id").Scan(ctx, &locked)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTaskArchived
		}
		if err != nil {
			return fmt.Errorf("update tagged task: %w", err)
		}
		if err := store.ReplaceTaskTags(ctx, tx, id, tags); err != nil {
			return err
		}
		kind, actor := actorOf(p)
		return writeAudit(ctx, tx, auditEntry{
			actorKind: kind, actorID: actor,
			action: "task.tags", resourceKind: "task", resourceID: id.String(),
			result:  resultSuccess,
			details: map[string]any{"project_id": task.ProjectID.String(), "tag_count": len(tags)},
		})
	})
	if err != nil {
		return nil, err
	}
	sorted := append([]string{}, tags...)
	slices.Sort(sorted)
	return sorted, nil
}
