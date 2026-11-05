package runner

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/runtimes/sdk"
	"github.com/niltavi/lerryn/server/internal/blob"
	"github.com/niltavi/lerryn/server/internal/runnerhttp"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
	"github.com/niltavi/lerryn/server/internal/store/storetest"
)

func TestSDKResultBurstCompletes(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	files, err := sdk.Files(sdk.Node)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	module := filepath.Join(dir, "sbx.mjs")
	eventsFile := filepath.Join(dir, "events.ndjson")
	for _, f := range files {
		if f.Path == "node_modules/@lerryn/sbx/index.js" {
			if err := os.WriteFile(module, f.Data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	script := `
import {pathToFileURL} from "node:url";
const {result} = await import(pathToFileURL(process.argv[1]).href);
process.env.LERRYN_EVENTS_FILE = process.argv[2];
for (let i = 0; i < 300; i++) {
  result("ok", "checkpoint " + i, "a".repeat(65534));
}
`
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, module, eventsFile) // #nosec G204 -- a fixed test script and the embedded SDK
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Node SDK: %v %s", err, out)
	}
	eventBytes, err := os.ReadFile(eventsFile) // #nosec G304 -- a temp file written by the embedded SDK
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(eventBytes), []byte{'\n'})
	if len(lines) != 300 {
		t.Fatal(len(lines))
	}
	t.Logf("real Node SDK: %d events, %d bytes", len(lines), len(eventBytes))
	for _, remote := range []bool{false, true} {
		name := "in-process"
		if remote {
			name = "HTTP"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			db := storetest.Open(t)
			blobs, err := blob.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			authz := service.NewAuthorizer(db)
			runs := service.NewRuns(db, authz, nil)
			coord := service.NewCoordinator(db, blobs, runs, nil)
			accounts := service.NewAccounts(db, nil)
			_, sess, err := accounts.Register(ctx, service.RegisterInput{Username: "alice", Password: "correct horse"})
			if err != nil {
				t.Fatal(err)
			}
			p, err := accounts.Authenticate(ctx, sess.Token)
			if err != nil {
				t.Fatal(err)
			}
			tasks := service.NewTasks(db, authz, blobs, nil)
			td, err := tasks.Apply(ctx, p, service.ApplyTaskInput{ProjectID: p.PersonalProjectID, Name: "burst", Manifest: service.Manifest{Runtime: service.RuntimeSpec{Language: "node", Version: "22"}, Entrypoint: []string{"node", "main.js"}, Concurrency: service.ConcurrencyAllow}, Files: []service.FileInput{{Path: "main.js", Content: []byte("// SDK burst\n")}}})
			if err != nil {
				t.Fatal(err)
			}
			rd, err := runs.Start(ctx, p, td.Task.Task.ID, service.StartRunInput{})
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t, func(o *Options) { o.NodeImage = "node:test" })
			var peer runproto.Coordinator = coord
			if remote {
				runners := service.NewRunners(db, coord, nil).WithStaticRegistrationToken("registration")
				mux := http.NewServeMux()
				mux.Handle(runnerhttp.BasePath, runnerhttp.NewHandler(runnerhttp.Options{Coordinator: coord, Runners: runners}))
				srv := httptest.NewServer(mux)
				t.Cleanup(srv.Close)
				client, err := runnerhttp.NewClient(runnerhttp.ClientOptions{ServerURL: srv.URL, RegistrationToken: "registration", CredentialFile: filepath.Join(t.TempDir(), "credential")})
				if err != nil {
					t.Fatal(err)
				}
				peer = client
			}
			if err := peer.Register(ctx, runproto.Registration{ID: h.r.opts.ID, Slots: 1}); err != nil {
				t.Fatal(err)
			}
			aa, err := peer.Claim(ctx, h.r.opts.ID, 1)
			if err != nil || len(aa) != 1 {
				t.Fatalf("claim %+v %v", aa, err)
			}
			// Exercise the final flush with a backlog rather than relying on timer interleaving.
			h.r.coord = peer
			// This large-payload test checks delivery, not latency under load.
			// An oversized request still returns ErrRejected immediately.
			h.r.reportTimeout = time.Minute
			h.r.eventPollInterval = time.Hour
			h.r.logFlushInterval = time.Hour
			h.ex.behave = func(c *fakeContainer) {
				for _, line := range lines {
					c.event(string(line))
				}
				if err := os.WriteFile(filepath.Join(c.mountSource(outMount), "report.txt"), []byte("all checkpoints delivered"), 0o600); err != nil {
					panic(err)
				}
				c.exit(0, false)
			}
			if err := h.r.ensureLayout(); err != nil {
				t.Fatal(err)
			}
			h.r.execute(ctx, h.r.track(aa[0]))
			detail, err := runs.Get(ctx, p, rd.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			var run store.Run
			if err := db.NewSelect().Model(&run).Where("id = ?", rd.Run.ID).Scan(ctx); err != nil {
				t.Fatal(err)
			}
			if detail.Run.Status != store.RunCompleted || run.RunnerEventSeq != 300 || detail.Result == nil || detail.Result.Summary != "checkpoint 299" {
				t.Fatalf("successful SDK task failed to finish: status=%s uploaded=%d/300; trace=%s", detail.Run.Status, run.RunnerEventSeq, strings.Join(h.log.snapshot(), ";"))
			}
			artifacts, err := runs.Artifacts(ctx, p, rd.Run.ID, nil, "")
			if err != nil || len(artifacts.Artifacts) != 1 || artifacts.Artifacts[0].Path != "report.txt" {
				t.Fatalf("artifacts=%+v, %v", artifacts, err)
			}
		})
	}
}
