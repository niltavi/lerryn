package store

import (
	"context"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

type taskTag struct {
	bun.BaseModel `bun:"table:task_tags"`

	TaskID uuid.UUID `bun:"task_id,pk"`
	Tag    string    `bun:"tag,pk"`
}

// TaskTags reads the tags of a batch of tasks. Sorting here gives both
// database dialects the same order regardless of the database collation.
func TaskTags(ctx context.Context, db bun.IDB, ids []uuid.UUID) (map[uuid.UUID][]string, error) {
	tags := make(map[uuid.UUID][]string, len(ids))
	if len(ids) == 0 {
		return tags, nil
	}
	var rows []taskTag
	if err := db.NewSelect().Model(&rows).Where("task_id IN (?)", bun.List(ids)).Scan(ctx); err != nil {
		return nil, fmt.Errorf("read task tags: %w", err)
	}
	for _, row := range rows {
		tags[row.TaskID] = append(tags[row.TaskID], row.Tag)
	}
	for _, names := range tags {
		slices.Sort(names)
	}
	return tags, nil
}

// ReplaceTaskTags replaces a task's tags in the transaction holding its
// task row lock, so replacements cannot interleave.
func ReplaceTaskTags(ctx context.Context, tx bun.Tx, id uuid.UUID, tags []string) error {
	if _, err := tx.NewDelete().Model((*taskTag)(nil)).Where("task_id = ?", id).Exec(ctx); err != nil {
		return fmt.Errorf("delete task tags: %w", err)
	}
	if len(tags) == 0 {
		return nil
	}
	rows := make([]taskTag, len(tags))
	for i, tag := range tags {
		rows[i] = taskTag{TaskID: id, Tag: tag}
	}
	if _, err := tx.NewInsert().Model(&rows).Exec(ctx); err != nil {
		return fmt.Errorf("insert task tags: %w", err)
	}
	return nil
}
