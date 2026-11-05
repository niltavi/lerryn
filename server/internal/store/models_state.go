package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// TaskState names the last complete snapshot published by a successful
// run. Its object is shared through the content-addressed blob store.
type TaskState struct {
	bun.BaseModel `bun:"table:task_states"`

	TaskID    uuid.UUID `bun:"task_id,pk,nullzero"`
	BlobHash  string    `bun:"blob_hash,notnull"`
	Size      int64     `bun:"size,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

// Run state reference roles.
const (
	RunStateInput  = "input"
	RunStateOutput = "output"
)

// RunStateRef pins the input snapshot or an output candidate of an active
// run. An input row with no hash represents an initially empty state.
// Output candidates become publishable only after their object is ready.
type RunStateRef struct {
	bun.BaseModel `bun:"table:run_state_refs"`

	RunID    uuid.UUID `bun:"run_id,pk,nullzero"`
	Role     string    `bun:"role,pk"`
	BlobHash string    `bun:"blob_hash,nullzero"`
	Size     int64     `bun:"size,notnull"`
	Ready    bool      `bun:"ready,notnull"`
}
