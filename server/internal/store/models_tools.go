package store

import (
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// Tool kinds (tech design 7.8).
const (
	ToolKindHTTP = "http"
	ToolKindMCP  = "mcp"
	// ToolKindScript is a file package, mounted read-only into the runs
	// of tasks that reference it.
	ToolKindScript = "script"
)

// Tool is a tool registered in a project: an HTTP API, an external MCP
// server or a script package, that the project's tasks may reference in
// their manifest.
type Tool struct {
	bun.BaseModel `bun:"table:tools"`

	ID        uuid.UUID `bun:"id,pk,nullzero"`
	ProjectID uuid.UUID `bun:"project_id,notnull"`
	// Name is unique within the project.
	Name        string `bun:"name,notnull"`
	Kind        string `bun:"kind,notnull"`
	Description string `bun:"description,notnull"`
	// URL is empty for script tools.
	URL string `bun:"url,notnull"`
	// TreeHash is the hex SHA-256 over a script tool's sorted file list,
	// empty for other kinds.
	TreeHash  string    `bun:"tree_hash,nullzero"`
	CreatedBy uuid.UUID `bun:"created_by,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
	UpdatedAt time.Time `bun:"updated_at,notnull"`
}

// ToolFile is a file of a script tool. The content lives in the blob
// store under BlobHash.
type ToolFile struct {
	bun.BaseModel `bun:"table:tool_files"`

	ID         uuid.UUID `bun:"id,pk,nullzero"`
	ToolID     uuid.UUID `bun:"tool_id,notnull"`
	Path       string    `bun:"path,notnull"`
	BlobHash   string    `bun:"blob_hash,notnull"`
	Size       int64     `bun:"size,notnull"`
	Executable bool      `bun:"executable,notnull"`
}
