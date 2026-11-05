package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

type rejectingArtifactCoordinator struct {
	*fakeCoordinator
	rejected map[string]bool
}

func (c *rejectingArtifactCoordinator) AddArtifact(ctx context.Context, runnerID string, runID uuid.UUID, a runproto.Artifact) error {
	if c.rejected[a.Path] {
		return fmt.Errorf("%w: artifact %s was rejected", runproto.ErrRejected, a.Path)
	}
	return c.fakeCoordinator.AddArtifact(ctx, runnerID, runID, a)
}

func TestArtifactUploadSummary(t *testing.T) {
	files := map[string]string{"a.txt": "hi", "b.txt": "world"}
	for _, tt := range []struct {
		name     string
		files    map[string]string
		rejected map[string]bool
		want     map[string]string
		count    int
		bytes    int64
	}{
		{name: "all succeed", files: files, want: map[string]string{"a.txt": "hi", "b.txt": "world"}, count: 2, bytes: 7},
		{name: "one fails", files: files, rejected: map[string]bool{"a.txt": true}, want: map[string]string{"b.txt": "world"}, count: 1, bytes: 5},
		{name: "all fail", files: files, rejected: map[string]bool{"a.txt": true, "b.txt": true}},
		{name: "empty file succeeds", files: map[string]string{"empty.txt": ""}, want: map[string]string{"empty.txt": ""}, count: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			h := newHarness(t, func(opts *Options) { opts.Logger = slog.New(slog.NewJSONHandler(&output, nil)) })
			h.r.coord = &rejectingArtifactCoordinator{fakeCoordinator: h.coord, rejected: tt.rejected}
			h.ex.behave = func(c *fakeContainer) {
				for path, content := range tt.files {
					if err := os.WriteFile(filepath.Join(c.mountSource(outMount), path), []byte(content), 0o600); err != nil {
						t.Error(err)
					}
				}
				c.exit(0, false)
			}
			assignment := newAssignment(pyManifest())
			h.coord.enqueue(assignment, mainPy)
			stop := h.start()
			if len(tt.rejected) == 0 {
				finish := h.coord.waitFinish(t, assignment.RunID, testTimeout)
				if finish.Status != runproto.StatusCompleted {
					t.Fatalf("Finish=%+v", finish)
				}
			} else {
				waitFor(t, testTimeout, "failed report cleanup", func() bool { return h.ex.wasRemoved("c1") && h.r.activeCount() == 0 })
				if finish, ok := h.coord.finish(assignment.RunID); ok {
					t.Fatalf("failed artifacts must not produce a terminal report: %+v", finish)
				}
			}
			stop()
			h.coord.checkProtocol(t)
			if got := h.coord.runArtifacts(assignment.RunID); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("stored artifacts=%v, want %v", got, tt.want)
			}

			summaries := 0
			warnings := map[string]bool{}
			decoder := json.NewDecoder(bytes.NewReader(output.Bytes()))
			for {
				var entry struct {
					Level   string `json:"level"`
					Message string `json:"msg"`
					Count   int    `json:"count"`
					Bytes   int64  `json:"bytes"`
					Path    string `json:"path"`
				}
				err := decoder.Decode(&entry)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if entry.Message == "artifacts uploaded" {
					summaries++
					if tt.count == 0 {
						t.Error("all uploads failed but a successful-upload summary was logged")
					}
					if entry.Level != "INFO" || entry.Count != tt.count || entry.Bytes != tt.bytes {
						t.Errorf("summary=%+v, want count=%d bytes=%d", entry, tt.count, tt.bytes)
					}
				}
				if entry.Message == "upload artifact" && entry.Level == "WARN" {
					warnings[entry.Path] = true
				}
			}
			wantSummaries := 0
			if tt.count > 0 {
				wantSummaries = 1
			}
			if summaries != wantSummaries {
				t.Errorf("upload summaries=%d, want %d", summaries, wantSummaries)
			}
			logs := strings.Join(h.coord.logTexts(assignment.RunID), "\n")
			for path := range tt.rejected {
				if !warnings[path] || !strings.Contains(logs, "could not upload artifact "+path) {
					t.Errorf("failed artifact %s lost its runner or task warning", path)
				}
			}
			if len(warnings) != len(tt.rejected) {
				t.Errorf("artifact warning count=%d, want %d", len(warnings), len(tt.rejected))
			}
		})
	}
}
