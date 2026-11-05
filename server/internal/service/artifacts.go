package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/niltavi/lerryn/server/internal/auth"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/store"
)

// ErrExpired means a log or artifact was removed by retention (tech
// design 6.6). The API answers 410 expired rather than an empty result.
var ErrExpired = errors.New("removed by the retention policy")

// Artifact previews (API Artifact.preview).
const (
	PreviewText = "text"
	PreviewJSON = "json"
	PreviewNone = "none"
)

// sniffBytes is how much of an artifact is read to guess its type, the
// amount http.DetectContentType looks at.
const sniffBytes = 512

// extensionTypes maps common file extensions to media types. A fixed
// table rather than mime.TypeByExtension, whose answer depends on the
// host's mime.types file.
var extensionTypes = map[string]string{
	".txt": "text/plain", ".log": "text/plain", ".md": "text/markdown", ".csv": "text/csv", ".tsv": "text/tab-separated-values",
	".json": "application/json", ".ndjson": "application/x-ndjson", ".yaml": "application/yaml", ".yml": "application/yaml",
	".xml": "application/xml", ".html": "text/html", ".htm": "text/html", ".svg": "image/svg+xml",
	".js": "text/javascript", ".mjs": "text/javascript", ".py": "text/x-python", ".sh": "application/x-sh",
	".pdf": "application/pdf", ".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif",
	".webp": "image/webp", ".zip": "application/zip", ".gz": "application/gzip", ".tar": "application/x-tar",
	".xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
}

// detectContentType guesses an artifact's media type from its extension,
// then from its first bytes.
func detectContentType(name string, head []byte) string {
	if t, ok := extensionTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	t, _, err := mime.ParseMediaType(http.DetectContentType(head))
	if err != nil {
		return "application/octet-stream"
	}
	return t
}

// ArtifactPreview says whether the console may show an artifact inline.
// HTML, SVG, scripts and binaries never are: the content is the task's
// and is not sanitised.
func ArtifactPreview(contentType string) string {
	switch contentType {
	case "application/json", "application/x-ndjson":
		return PreviewJSON
	case "text/plain", "text/markdown", "text/csv", "text/tab-separated-values", "application/yaml":
		return PreviewText
	}
	return PreviewNone
}

// headBuffer keeps the first sniffBytes written to it.
type headBuffer struct{ b []byte }

func (h *headBuffer) Write(p []byte) (int, error) {
	if room := sniffBytes - len(h.b); room > 0 {
		h.b = append(h.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// AddArtifact implements runproto.Coordinator. The content is stored in
// the object store under its hash; the artifact row and an artifact event
// commit together.
func (c *Coordinator) AddArtifact(ctx context.Context, runnerID string, runID uuid.UUID, a runproto.Artifact) error {
	if detail := checkFilePath(a.Path); detail != "" {
		return fmt.Errorf("%w: artifact path %q of run %s: %s", ErrProtocol, a.Path, runID, detail)
	}
	if a.Size < 0 || a.Size > runproto.MaxArtifactBytes {
		return fmt.Errorf("%w: artifact %s of run %s is %d bytes, over the %d byte limit", ErrProtocol, a.Path, runID, a.Size, runproto.MaxArtifactBytes)
	}
	// Check ownership before reading what may be a large body.
	if _, err := ownedRun(ctx, c.db, runnerID, runID); err != nil {
		return err
	}
	head := &headBuffer{}
	body := &countingReader{r: io.LimitReader(a.Content, a.Size+1)}
	hash, err := c.blobs.Put(ctx, io.TeeReader(body, head))
	if err != nil {
		return fmt.Errorf("store artifact %s of run %s: %w", a.Path, runID, err)
	}
	if body.n != a.Size {
		return fmt.Errorf("%w: artifact %s of run %s announced %d bytes and sent %d or more", ErrProtocol, a.Path, runID, a.Size, body.n)
	}
	contentType := detectContentType(a.Path, head.b)
	if ArtifactPreview(contentType) != PreviewNone && !utf8.Valid(trimPartialRune(head.b)) {
		contentType = "application/octet-stream"
	}

	var restore []string
	err = c.retry(ctx, func(ctx context.Context, tx bun.Tx) error {
		restore = nil
		if _, err := ownedRun(ctx, tx, runnerID, runID); err != nil {
			return err
		}
		var existing []store.Artifact
		if err := tx.NewSelect().Model(&existing).Column("id", "path", "size", "sha256").Where("run_id = ?", runID).Scan(ctx); err != nil {
			return err
		}
		total := a.Size
		var same *store.Artifact
		for i, e := range existing {
			if e.Path == a.Path {
				same = &existing[i]
				continue
			}
			total += e.Size
		}
		if same == nil && len(existing) >= runproto.MaxArtifacts {
			return fmt.Errorf("%w: run %s already has %d artifacts", ErrProtocol, runID, len(existing))
		}
		if total > runproto.MaxRunArtifactBytes {
			return fmt.Errorf("%w: artifacts of run %s would total %d bytes, over the %d byte limit", ErrProtocol, runID, total, runproto.MaxRunArtifactBytes)
		}
		now := store.Now()
		if same != nil {
			if same.SHA256 == hash {
				return nil
			}
			if _, err := tx.NewUpdate().Model((*store.Artifact)(nil)).
				Set("size = ?", a.Size).Set("content_type = ?", contentType).
				Set("sha256 = ?", hash).Set("blob_key = ?", hash).Set("created_at = ?", now).
				Where("id = ?", same.ID).Exec(ctx); err != nil {
				return err
			}
			restore, err = claimBlobs(ctx, tx, []string{hash})
			return err
		}
		row := store.Artifact{
			ID: store.NewID(), RunID: runID, Path: a.Path, Size: a.Size, ContentType: contentType,
			SHA256: hash, BlobKey: hash, CreatedAt: now,
		}
		if _, err := tx.NewInsert().Model(&row).Exec(ctx); err != nil {
			return err
		}
		if err := recordRunEvent(ctx, tx, runID, store.EventArtifact, "",
			map[string]string{"artifact_id": row.ID.String(), "path": row.Path}, now); err != nil {
			return err
		}
		restore, err = claimBlobs(ctx, tx, []string{hash})
		return err
	})
	if err != nil || len(restore) == 0 {
		return err
	}
	// Retention deleted this content after it was stored above. The body
	// is consumed, so the upload fails; the runner sends it again, which
	// stores it again, and the row already names it.
	missing, err := restoreBlobs(ctx, c.blobs, restore, func(string) io.Reader { return nil })
	if err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("artifact %s of run %s: its content was removed by retention while it was stored; send it again", a.Path, runID)
	}
	return nil
}

// trimPartialRune drops a UTF-8 sequence cut off at the end of b.
func trimPartialRune(b []byte) []byte {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				return b[:i]
			}
			break
		}
	}
	return b
}

// ArtifactPage is a page of a run's artifacts, by path.
type ArtifactPage struct {
	Artifacts  []store.Artifact
	NextCursor string
}

const artifactCursor = "a"

// Artifacts lists the artifacts of a run by path, including expired ones.
func (r *Runs) Artifacts(ctx context.Context, p *auth.Principal, runID uuid.UUID, limit *int, cursor string) (ArtifactPage, error) {
	var v validation
	size := pageSize(&v, limit)
	after := ""
	if cursor != "" {
		parts, err := decodePageCursor(cursor, artifactCursor, 1)
		if err != nil {
			v.param("cursor", "游标无效，请使用上一页响应中的 next_cursor")
		} else {
			after = parts[0]
		}
	}
	if err := v.err(); err != nil {
		return ArtifactPage{}, err
	}
	if _, err := r.load(ctx, p, runID, ActionViewLogs); err != nil {
		return ArtifactPage{}, err
	}
	var rows []store.Artifact
	q := r.db.NewSelect().Model(&rows).Where("run_id = ?", runID)
	if after != "" {
		q = q.Where("path > ?", after)
	}
	if err := q.Order("path").Limit(int64(size + 1)).Scan(ctx); err != nil {
		return ArtifactPage{}, fmt.Errorf("list artifacts: %w", err)
	}
	var page ArtifactPage
	if len(rows) > size {
		rows = rows[:size]
		page.NextCursor = encodePageCursor(artifactCursor, rows[size-1].Path)
	}
	page.Artifacts = rows
	return page, nil
}

// OpenArtifact returns an artifact and its content; the caller closes
// the reader. An expired artifact is ErrExpired.
func (r *Runs) OpenArtifact(ctx context.Context, p *auth.Principal, id uuid.UUID) (store.Artifact, io.ReadCloser, error) {
	var a store.Artifact
	err := r.db.NewSelect().Model(&a).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Artifact{}, nil, ErrNotFound
	}
	if err != nil {
		return store.Artifact{}, nil, err
	}
	if _, err := r.load(ctx, p, a.RunID, ActionViewLogs); err != nil {
		return store.Artifact{}, nil, err
	}
	if !a.ExpiredAt.IsZero() {
		return store.Artifact{}, nil, ErrExpired
	}
	if r.blobs == nil {
		return store.Artifact{}, nil, errors.New("artifact downloads need the object store; call Runs.WithBlobs")
	}
	content, err := r.blobs.Get(ctx, a.BlobKey)
	if err != nil {
		return store.Artifact{}, nil, fmt.Errorf("open artifact %s: %w", id, err)
	}
	return a, content, nil
}
