package service_test

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestRunArchiveKeepsCompleteDatabaseLogs(t *testing.T) {
	for _, offloaded := range []bool{false, true} {
		t.Run(map[bool]string{false: "chunks", true: "object-store"}[offloaded], func(t *testing.T) {
			f := newFixture(t)
			f.runs.WithLogArchive(f.blobs)
			alice := f.user("alice")
			task := f.apply(alice, "digest", allowOverlap()).Task
			runner := f.runner("runner")
			kept, ordinary, latest := f.start(alice, task.ID), f.start(alice, task.ID), f.start(alice, task.ID)
			f.claim(runner, 3)
			artifact := f.addArtifact(runner, kept.ID, "kept.txt", "preserved artifact")
			f.finishWithLines(runner, kept.ID, 2500)
			f.finishWithLines(runner, ordinary.ID, 5)
			f.finishWithLines(runner, latest.ID, 0)
			if offloaded {
				if _, err := f.runs.ArchiveLogs(f.ctx); err != nil {
					t.Fatal(err)
				}
				if f.chunkCount(kept.ID) != 0 {
					t.Fatal("test log was not offloaded")
				}
			}
			before, err := f.runs.Logs(f.ctx, alice, kept.ID, service.LogsInput{Tail: new(3)})
			if err != nil {
				t.Fatal(err)
			}
			inFlight, err := f.runs.DownloadLogs(f.ctx, alice, kept.ID)
			if err != nil {
				t.Fatal(err)
			}
			previousBlob := f.row(kept.ID).LogBlob
			stream, err := f.runs.StreamUpdates(f.ctx, alice, nil, &task.ID)
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			archived, err := f.runs.SetArchived(f.ctx, alice, kept.ID, true)
			if err != nil {
				t.Fatal(err)
			}
			nextUpdate(t, stream, kept.ID, store.RunCompleted)
			if archived.Run.ArchivedAt.IsZero() || archived.Run.LogArchive != "" || archived.Run.LogBlob != "" || f.chunkCount(kept.ID) != 3 {
				t.Fatalf("archived run is not backed by database chunks: %+v", archived.Run)
			}
			if offloaded {
				if err := f.blobs.Delete(f.ctx, previousBlob); err != nil {
					t.Fatal(err)
				}
			}
			var inFlightData bytes.Buffer
			if err := inFlight.WriteTo(f.ctx, &inFlightData); err != nil || strings.Count(inFlightData.String(), "\n") != 2500 {
				t.Fatalf("an in-flight download lost the moved log: %v", err)
			}
			repeated, err := f.runs.SetArchived(f.ctx, alice, kept.ID, true)
			if err != nil || !repeated.Run.ArchivedAt.Equal(archived.Run.ArchivedAt) || len(f.auditRecords("run.archive")) != 1 {
				t.Fatalf("repeated archive changed the record: %v", err)
			}
			// A new service with no object store must still page and download
			// the entire archived log, including records across chunk borders.
			reader := service.NewRuns(f.db, service.NewAuthorizer(f.db), nil)
			after, err := reader.Logs(f.ctx, alice, kept.ID, service.LogsInput{Tail: new(3)})
			if err != nil || len(after.Lines) != len(before.Lines) || after.Lines[2] != before.Lines[2] {
				t.Fatalf("database log differs after archiving: %v", err)
			}
			cursor, count := "", 0
			for {
				page, err := reader.Logs(f.ctx, alice, kept.ID, service.LogsInput{Cursor: cursor, Limit: new(333)})
				if err != nil {
					t.Fatal(err)
				}
				count += len(page.Lines)
				if page.Complete {
					break
				}
				if len(page.Lines) == 0 {
					t.Fatal("paging stopped early")
				}
				cursor = page.NextCursor
			}
			if count != 2500 {
				t.Fatalf("read %d lines", count)
			}
			download, err := reader.DownloadLogs(f.ctx, alice, kept.ID)
			if err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			if err := download.WriteTo(f.ctx, &output); err != nil || strings.Count(output.String(), "\n") != 2500 {
				t.Fatalf("download lost lines: %v", err)
			}
			page, err := f.runs.List(f.ctx, alice, service.RunListInput{})
			if err != nil || len(page.Runs) != 2 {
				t.Fatalf("default list: %+v, %v", page, err)
			}
			page, err = f.runs.List(f.ctx, alice, service.RunListInput{Archived: true})
			if err != nil || len(page.Runs) != 1 || page.Runs[0].Run.ID != kept.ID {
				t.Fatalf("archive list: %+v, %v", page, err)
			}
			f.age(kept.ID, 10*day)
			f.age(ordinary.ID, 10*day)
			retention := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{Runs: day, Logs: day, Artifacts: day, LogsMaxBytes: 1, ArtifactsMaxBytes: 1}, nil)
			f.cleanup(retention)
			if f.chunkCount(kept.ID) != 3 || !f.row(kept.ID).LogsExpiredAt.IsZero() || !f.has(artifact.BlobKey) {
				t.Fatal("retention removed manually archived content")
			}
			if _, err := f.runs.ArchiveLogs(f.ctx); err != nil {
				t.Fatal(err)
			}
			if f.chunkCount(kept.ID) != 3 {
				t.Fatal("background offloading removed database archive")
			}
			if _, err := f.tasks.Archive(f.ctx, alice, task.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := reader.Logs(f.ctx, alice, kept.ID, service.LogsInput{}); err != nil {
				t.Fatalf("task deletion hid history: %v", err)
			}
			restored, err := f.runs.SetArchived(f.ctx, alice, kept.ID, false)
			if err != nil || !restored.Run.ArchivedAt.IsZero() {
				t.Fatalf("restore: %v", err)
			}
			f.cleanup(retention)
			if _, err := f.runs.Get(f.ctx, alice, kept.ID); !errors.Is(err, service.ErrNotFound) {
				t.Fatalf("restored record did not follow retention: %v", err)
			}
		})
	}
}

func TestRunArchiveGuards(t *testing.T) {
	f := newFixture(t)
	alice, viewer, outsider := f.user("alice"), f.user("viewer"), f.user("outsider")
	f.grant(alice.PersonalProjectID, viewer, service.RoleViewer)
	task := f.apply(alice, "digest", basicManifest()).Task
	run := f.start(alice, task.ID)
	for _, tc := range []struct {
		name   string
		caller string
		want   error
	}{
		{"viewer", "viewer", service.ErrPermissionDenied}, {"outsider", "outsider", service.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := viewer
			if tc.caller == "outsider" {
				caller = outsider
			}
			_, err := f.runs.SetArchived(f.ctx, caller, run.ID, true)
			wantErr(t, err, tc.want)
		})
	}
	_, err := f.runs.SetArchived(f.ctx, alice, run.ID, true)
	fields, _ := fieldErrors(t, err)
	if fields["#/archived"] == "" {
		t.Fatalf("active run guard: %v", err)
	}
	runner := f.runner("runner")
	f.claim(runner, 1)
	f.finishWithLines(runner, run.ID, 0)
	if _, err := f.db.NewUpdate().Model((*store.Run)(nil)).Set("logs_expired_at = ?", store.Now()).Where("id = ?", run.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	_, err = f.runs.SetArchived(f.ctx, alice, run.ID, true)
	wantErr(t, err, service.ErrExpired)
	if !f.row(run.ID).ArchivedAt.IsZero() {
		t.Fatal("an incomplete log was marked archived")
	}
}

func TestRunArchiveConcurrentOffloadingAndRetention(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(map[bool]string{false: "offload", true: "retention"}[cleanup], func(t *testing.T) {
			f := newFixture(t)
			f.runs.WithLogArchive(f.blobs)
			alice := f.user("alice")
			task := f.apply(alice, "digest", allowOverlap()).Task
			runner := f.runner("runner")
			run, latest := f.start(alice, task.ID), f.start(alice, task.ID)
			f.claim(runner, 2)
			f.finishWithLines(runner, run.ID, 1500)
			f.finishWithLines(runner, latest.ID, 0)
			f.age(run.ID, 10*day)
			retention := service.NewRetention(f.db, f.blobs, service.RetentionPolicy{Runs: day, Logs: day}, nil)
			var archiveErr error
			var wg sync.WaitGroup
			wg.Go(func() { _, archiveErr = f.runs.SetArchived(f.ctx, alice, run.ID, true) })
			wg.Go(func() {
				if cleanup {
					if _, err := retention.Cleanup(f.ctx); err != nil {
						t.Error(err)
					}
				} else if _, err := f.runs.ArchiveLogs(f.ctx); err != nil {
					t.Error(err)
				}
			})
			wg.Wait()
			if archiveErr != nil {
				if !cleanup || (!errors.Is(archiveErr, service.ErrExpired) && !errors.Is(archiveErr, service.ErrNotFound)) {
					t.Fatal(archiveErr)
				}
				return
			}
			f.cleanup(retention)
			if f.row(run.ID).ArchivedAt.IsZero() || f.chunkCount(run.ID) != 2 {
				t.Fatal("a committed archive was lost")
			}
		})
	}
}
