package service_test

import (
	"testing"

	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/service"
)

// An import whose file content the collector deletes between storing it
// and committing the version stores it again (blobgc.go).
func TestImportRacesCollector(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", allowOverlap())
	runner := f.runner("r1")
	run := f.start(alice, task.Task.ID)
	f.claim(runner, 1)
	content := "print('shared with an imported version')\n"
	old := f.addArtifact(runner, run.ID, "copy.py", content)
	f.finishWithLines(runner, run.ID, 1)
	f.age(run.ID, 10*day)

	hooked := &hookStore{Local: f.blobs, hash: old.BlobKey}
	ret := service.NewRetention(f.db, hooked, service.RetentionPolicy{Artifacts: 7 * day}, nil)
	collected := 0
	hooked.hook = func() { collected = f.cleanup(ret).Objects }

	tasks := service.NewTasks(f.db, service.NewAuthorizer(f.db), hooked, nil)
	res, err := tasks.Import(f.ctx, alice, service.ImportTaskInput{ProjectID: alice.PersonalProjectID, Bundle: service.TaskBundle{
		Name: "imported", Versions: []service.BundleVersion{{Seq: 1, Manifest: basicManifest(), Files: []service.FileInput{{Path: "main.py", Content: []byte(content)}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if collected != 1 {
		t.Fatalf("the collector deleted %d objects during the import; the test did not interleave", collected)
	}
	if got, err := blob.ReadAll(f.ctx, f.blobs, res.Task.Version.Files[0].BlobHash); err != nil || string(got) != content {
		t.Fatalf("imported file content = %q, %v", got, err)
	}
}
