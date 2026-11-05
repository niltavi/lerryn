package runnerhttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/runtimes/sdk"
	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

const resultRunnerID = "result-runner"

func resultRun(t *testing.T, remote bool) (*env, runproto.Coordinator, uuid.UUID) {
	t.Helper()
	e := newEnv(t)
	coord := runproto.Coordinator(e.coord)
	if remote {
		coord = e.client(t.TempDir(), staticToken)
	}
	if err := coord.Register(e.ctx, runproto.Registration{ID: resultRunnerID, Slots: 1}); err != nil {
		t.Fatal(err)
	}
	id := e.startRun()
	assignments, err := coord.Claim(e.ctx, resultRunnerID, 1)
	if err != nil || len(assignments) != 1 || assignments[0].RunID != id {
		t.Fatalf("claim = %+v, %v", assignments, err)
	}
	if err := coord.Started(e.ctx, resultRunnerID, id, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	return e, coord, id
}

func TestResultDataLimits(t *testing.T) {
	const limit = 64 << 10
	for _, remote := range []bool{false, true} {
		name := "in-process"
		if remote {
			name = "HTTP"
		}
		t.Run(name, func(t *testing.T) {
			e, coord, id := resultRun(t, remote)
			var accepted int64
			for _, tt := range []struct {
				name     string
				data     string
				rejected bool
			}{
				{name: "minimum", data: `0`},
				{name: "one below", data: `"` + strings.Repeat("<", limit-3) + `"`},
				{name: "at limit", data: `"` + strings.Repeat("<", limit-2) + `"`},
				{name: "over limit", data: `"` + strings.Repeat("<", limit-1) + `"`, rejected: true},
				{name: "existing escapes at limit", data: `"` + strings.Repeat(`\u003c`, (limit-2)/6) + `xx"`},
				{name: "existing escapes over limit", data: `"` + strings.Repeat(`\u003c`, (limit-2)/6) + `xxx"`, rejected: true},
				{name: "escaped line separator at limit", data: `"` + strings.Repeat(`\u2028`, (limit-2)/6) + `xx"`},
				{name: "escaped line separator over limit", data: `"` + strings.Repeat(`\u2028`, (limit-2)/6) + `xxx"`, rejected: true},
				{name: "escaped paragraph separator at limit", data: `"` + strings.Repeat(`\u2029`, (limit-2)/6) + `xx"`},
				{name: "escaped paragraph separator over limit", data: `"` + strings.Repeat(`\u2029`, (limit-2)/6) + `xxx"`, rejected: true},
				{name: "valid after rejection", data: `{"ok":true}`},
			} {
				t.Run(tt.name, func(t *testing.T) {
					err := coord.AppendEvents(e.ctx, resultRunnerID, id, []runproto.Event{{
						Seq: accepted + 1, Kind: runproto.EventResult,
						Result: &runproto.Result{Status: "ok", Summary: tt.name, Data: json.RawMessage(tt.data)},
					}})
					if tt.rejected {
						want := service.ErrProtocol
						if remote {
							want = runproto.ErrRejected
						}
						if !errors.Is(err, want) {
							t.Fatalf("%d-byte result = %v, want %v", len(tt.data), err, want)
						}
					} else {
						if err != nil {
							t.Fatalf("legal %d-byte result rejected: %v", len(tt.data), err)
						}
						accepted++
					}
					var run store.Run
					if err := e.db.NewSelect().Model(&run).Where("id = ?", id).Scan(e.ctx); err != nil {
						t.Fatal(err)
					}
					if run.RunnerEventSeq != accepted {
						t.Fatalf("accepted event sequence = %d, want %d", run.RunnerEventSeq, accepted)
					}
				})
			}
		})
	}
}

func nodeSDKResults(t *testing.T) []runproto.Event {
	t.Helper()
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
	for _, f := range files {
		if f.Path == "node_modules/@lerryn/sbx/index.js" {
			if err := os.WriteFile(module, f.Data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	path := filepath.Join(dir, "events.ndjson")
	script := `
import assert from "node:assert/strict";
import {pathToFileURL} from "node:url";
const {result} = await import(pathToFileURL(process.argv[1]).href);
process.env.LERRYN_EVENTS_FILE = process.argv[2];
result("ok", "minimum", 0);
result("ok", "scraped HTML", {html: "<p>hello</p>".repeat(3000)});
result("ok", "below limit", "<".repeat(65533));
result("ok", "HTML at limit", "<".repeat(65534));
result("ok", "line separator at limit", "\u2028".repeat(21844) + "xx");
result("ok", "paragraph separator at limit", "\u2029".repeat(21844) + "xx");
assert.throws(() => result("ok", "over limit", "<".repeat(65535)), RangeError);
`
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "-e", script, module, path) // #nosec G204 -- a test script and an embedded SDK
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Node SDK: %v, %s", err, out)
	}
	data, err := os.ReadFile(path) // #nosec G304 -- a temp file created by this test's SDK process
	if err != nil {
		t.Fatal(err)
	}
	var events []runproto.Event
	for _, line := range bytes.Split(bytes.TrimSpace(data), []byte{'\n'}) {
		var event struct {
			TS                    time.Time `json:"ts"`
			Kind, Status, Summary string
			Data                  json.RawMessage
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, runproto.Event{
			Seq: int64(len(events) + 1), TS: event.TS, Kind: event.Kind,
			Result: &runproto.Result{Status: event.Status, Summary: event.Summary, Data: event.Data},
		})
	}
	if len(events) != 6 || len(events[3].Result.Data) != 64<<10 || len(events[4].Result.Data) != 64<<10 || len(events[5].Result.Data) != 64<<10 {
		t.Fatal("Node SDK fixture no longer covers the exact result data limits")
	}
	return events
}

func sameJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal(got, &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(want, &b); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("result JSON changed: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func TestNodeSDKResultsComplete(t *testing.T) {
	events := nodeSDKResults(t)
	for _, remote := range []bool{false, true} {
		name := "in-process"
		if remote {
			name = "HTTP"
		}
		t.Run(name, func(t *testing.T) {
			e, coord, id := resultRun(t, remote)
			if err := coord.AppendEvents(e.ctx, resultRunnerID, id, events); err != nil {
				t.Fatal(err)
			}
			artifact := "SDK result delivered"
			if err := coord.AddArtifact(e.ctx, resultRunnerID, id, runproto.Artifact{
				Path: "report.txt", Size: int64(len(artifact)), Content: strings.NewReader(artifact),
			}); err != nil {
				t.Fatal(err)
			}
			zero := 0
			if err := coord.PutState(e.ctx, resultRunnerID, id, runproto.StateSnapshot{Version: runproto.StateFormatVersion}); err != nil {
				t.Fatal(err)
			}
			if err := coord.Finish(e.ctx, resultRunnerID, id, runproto.Finish{Status: runproto.StatusCompleted, ExitCode: &zero, FinishedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			detail, err := e.runs.Get(e.ctx, e.user, id)
			if err != nil || detail.Run.Status != store.RunCompleted || detail.Result == nil || detail.Result.Summary != events[len(events)-1].Result.Summary {
				t.Fatalf("run did not complete with its SDK result: %+v, %v", detail, err)
			}
			sameJSON(t, detail.Result.Data, events[len(events)-1].Result.Data)
			var rows []store.RunEvent
			if err := e.db.NewSelect().Model(&rows).Where("run_id = ? AND kind = ?", id, runproto.EventResult).Order("seq").Scan(e.ctx); err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(events) {
				t.Fatalf("stored %d of %d SDK result events", len(rows), len(events))
			}
			for i, row := range rows {
				var result service.Result
				if err := json.Unmarshal([]byte(row.Payload), &result); err != nil {
					t.Fatal(err)
				}
				sameJSON(t, result.Data, events[i].Result.Data)
			}
			artifacts, err := e.runs.Artifacts(e.ctx, e.user, id, nil, "")
			if err != nil || len(artifacts.Artifacts) != 1 || artifacts.Artifacts[0].Path != "report.txt" || artifacts.Artifacts[0].Size != int64(len(artifact)) {
				t.Fatalf("run artifacts = %+v, %v", artifacts, err)
			}
		})
	}
}
