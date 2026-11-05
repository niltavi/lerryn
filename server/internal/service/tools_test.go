package service_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/niltavi/lerryn/server/internal/runproto"
	"github.com/niltavi/lerryn/server/internal/service"
	"github.com/niltavi/lerryn/server/internal/store"
)

func TestToolRegistry(t *testing.T) {
	f := newFixture(t)
	alice, bob, carol := f.user("alice"), f.user("bob"), f.user("carol")
	project := alice.PersonalProjectID
	f.grant(project, bob, service.RoleViewer)

	in := service.RegisterToolInput{Name: "crm-mcp", Kind: store.ToolKindMCP, Description: "CRM 查询", URL: "https://crm.example.com/mcp"}
	d, err := f.tasks.RegisterTool(f.ctx, alice, project, in)
	tool := d.Tool
	if err != nil || tool.Name != "crm-mcp" || tool.ProjectID != project || len(d.Files) != 0 || tool.TreeHash != "" {
		t.Fatalf("register = %+v, %v", d, err)
	}
	// Same name updates in place.
	in.URL = "https://crm2.example.com/mcp"
	again, err := f.tasks.RegisterTool(f.ctx, alice, project, in)
	if err != nil || again.Tool.ID != tool.ID || again.Tool.URL != in.URL {
		t.Fatalf("update = %+v, %v", again, err)
	}

	// A viewer sees the tool but cannot register; an outsider sees nothing.
	if list, err := f.tasks.ListTools(f.ctx, bob, nil); err != nil || len(list) != 1 {
		t.Fatalf("bob's list = %+v, %v", list, err)
	}
	_, err = f.tasks.RegisterTool(f.ctx, bob, project, in)
	wantErr(t, err, service.ErrPermissionDenied)
	_, err = f.tasks.RegisterTool(f.ctx, carol, project, in)
	wantErr(t, err, service.ErrNotFound)
	if list, err := f.tasks.ListTools(f.ctx, carol, nil); err != nil || len(list) != 0 {
		t.Fatalf("carol's list = %+v, %v", list, err)
	}
	wantErr(t, f.tasks.RemoveTool(f.ctx, carol, tool.ID), service.ErrNotFound)
	wantErr(t, f.tasks.RemoveTool(f.ctx, bob, tool.ID), service.ErrPermissionDenied)

	for name, bad := range map[string]service.RegisterToolInput{
		"name":            {Name: "CRM", Kind: "mcp", Description: "x", URL: "https://a.example"},
		"kind":            {Name: "a", Kind: "ftp", Description: "x", URL: "https://a.example"},
		"script with url": {Name: "a", Kind: "script", Description: "x", URL: "https://a.example", Files: mainPy()},
		"script no files": {Name: "a", Kind: "script", Description: "x"},
		"http with files": {Name: "a", Kind: "http", Description: "x", URL: "https://a.example", Files: mainPy()},
		"script bad path": {Name: "a", Kind: "script", Description: "x", Files: []service.FileInput{{Path: "../x", Content: []byte("x")}}},
		"description":     {Name: "a", Kind: "http", Description: " ", URL: "https://a.example"},
		"url scheme":      {Name: "a", Kind: "http", Description: "x", URL: "file:///etc/passwd"},
		"url credentials": {Name: "a", Kind: "http", Description: "x", URL: "https://user:pw@a.example"}, // #nosec G101 -- a rejected example
	} {
		_, err := f.tasks.RegisterTool(f.ctx, alice, project, bad)
		if fields, _ := fieldErrors(t, err); len(fields) == 0 {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// Manifests may reference only tools registered in the task's project;
// runs get the registered URLs, and a run whose tool was removed fails at
// claim instead of running without it.
func TestToolReferences(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	if _, err := f.tasks.RegisterTool(f.ctx, bob, bob.PersonalProjectID, service.RegisterToolInput{
		Name: "crm-mcp", Kind: "mcp", Description: "bob 的", URL: "https://bob.example.com"}); err != nil {
		t.Fatal(err)
	}
	m := basicManifest()
	m.Concurrency = service.ConcurrencyAllow
	m.Tools = []string{"crm-mcp"}
	task := f.apply(alice, "digest", m).Task.ID
	// Bob's tool of the same name does not count for alice's project.
	r, err := f.tasks.Validate(f.ctx, alice, task, 1)
	if err != nil || len(r.Errors) != 1 || r.Errors[0].Pointer != "#/manifest/tools/0" {
		t.Fatalf("before registering = %+v, %v", r.Errors, err)
	}

	tool, err := f.tasks.RegisterTool(f.ctx, alice, alice.PersonalProjectID, service.RegisterToolInput{
		Name: "crm-mcp", Kind: "mcp", Description: "CRM", URL: "https://crm.example.com/mcp"})
	if err != nil {
		t.Fatal(err)
	}
	if r, err = f.tasks.Validate(f.ctx, alice, task, 1); err != nil || len(r.Errors) != 0 {
		t.Fatalf("after registering = %+v, %v", r.Errors, err)
	}
	runner := f.runner("r1")
	run := f.start(alice, task)
	as := f.claim(runner, 1)
	if len(as) != 1 || len(as[0].Manifest.Tools) != 1 ||
		!reflect.DeepEqual(as[0].Manifest.Tools[0], runproto.Tool{Name: "crm-mcp", Kind: "mcp", URL: "https://crm.example.com/mcp"}) {
		t.Fatalf("assignment = %+v", as)
	}
	_ = run

	if err := f.tasks.RemoveTool(f.ctx, alice, tool.Tool.ID); err != nil {
		t.Fatal(err)
	}
	next := f.start(alice, task)
	if as := f.claim(runner, 1); len(as) != 0 {
		t.Fatalf("claimed a run whose tool is gone: %+v", as)
	}
	got := f.get(alice, next.ID).Run
	if got.Status != store.RunFailed || got.ErrorCode != "validation_failed" {
		t.Fatalf("run = %s %s %s", got.Status, got.ErrorCode, got.ErrorMessage)
	}
}

// A script tool is a file package: registering it stores its files, a run
// of a task that references it gets them with the tree hash, and
// registering it again replaces them for the next run.
func TestScriptTools(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.user("alice"), f.user("bob")
	project := alice.PersonalProjectID
	f.grant(project, bob, service.RoleViewer)
	files := []service.FileInput{
		{Path: "bin/fmt-price", Content: []byte("#!/bin/sh\necho \"$1 元\"\n"), Executable: true},
		{Path: "prices.py", Content: []byte("def fmt(x):\n    return f'{x} 元'\n")},
	}
	d, err := f.tasks.RegisterTool(f.ctx, alice, project, service.RegisterToolInput{
		Name: "price-lib", Kind: store.ToolKindScript, Description: "价格格式化：import prices 或运行 bin/fmt-price", Files: files,
	})
	if err != nil {
		t.Fatal(err)
	}
	if d.Tool.URL != "" || len(d.Tool.TreeHash) != 64 || len(d.Files) != 2 || d.Files[0].Path != "bin/fmt-price" ||
		!d.Files[0].Executable || d.Files[1].Size != int64(len(files[1].Content)) {
		t.Fatalf("registered = %+v", d)
	}
	list, err := f.tasks.ListTools(f.ctx, bob, &project)
	if err != nil || len(list) != 1 || len(list[0].Files) != 2 || list[0].Tool.TreeHash != d.Tool.TreeHash {
		t.Fatalf("viewer's list = %+v, %v", list, err)
	}

	// A script tool works without network; an MCP tool there is a
	// warning.
	if _, err := f.tasks.RegisterTool(f.ctx, alice, project, service.RegisterToolInput{
		Name: "crm", Kind: store.ToolKindMCP, Description: "CRM", URL: "https://crm.example.com/mcp"}); err != nil {
		t.Fatal(err)
	}
	m := basicManifest()
	m.Network = service.NetworkNone
	m.Concurrency = service.ConcurrencyAllow
	m.Tools = []string{"price-lib", "crm"}
	res, err := f.tasks.Apply(f.ctx, alice, service.ApplyTaskInput{ProjectID: project, Name: "digest", Manifest: m, Files: mainPy()})
	if err != nil {
		t.Fatal(err)
	}
	var warned []string
	for _, w := range res.Warnings {
		if w.Pointer == "#/manifest/tools/1" && strings.Contains(w.Message, "network") {
			warned = append(warned, w.Pointer)
		}
		if w.Pointer == "#/manifest/tools/0" {
			t.Errorf("script tool warned about: %+v", w)
		}
	}
	if len(warned) != 1 {
		t.Fatalf("warnings = %+v", res.Warnings)
	}
	task := res.Task.Task.ID
	if r, err := f.tasks.Validate(f.ctx, alice, task, 1); err != nil || len(r.Errors) != 0 {
		t.Fatalf("validate = %+v, %v", r.Errors, err)
	}

	runner := f.runner("r1")
	f.start(alice, task)
	as := f.claim(runner, 1)
	if len(as) != 1 || len(as[0].Manifest.Tools) != 2 {
		t.Fatalf("assignment = %+v", as)
	}
	got := as[0].Manifest.Tools[0]
	if got.Kind != "script" || got.TreeHash != d.Tool.TreeHash || len(got.Files) != 2 ||
		string(got.Files[0].Content) != string(files[0].Content) || !got.Files[0].Executable || got.Files[1].Executable {
		t.Fatalf("script tool in the assignment = %+v", got)
	}
	if mcp := as[0].Manifest.Tools[1]; mcp.Kind != "mcp" || len(mcp.Files) != 0 {
		t.Fatalf("mcp tool in the assignment = %+v", mcp)
	}

	// Registering again replaces the files for the next run.
	d2, err := f.tasks.RegisterTool(f.ctx, alice, project, service.RegisterToolInput{
		Name: "price-lib", Kind: store.ToolKindScript, Description: "价格格式化", Files: files[1:],
	})
	if err != nil || d2.Tool.ID != d.Tool.ID || d2.Tool.TreeHash == d.Tool.TreeHash || len(d2.Files) != 1 {
		t.Fatalf("re-register = %+v, %v", d2, err)
	}
	f.start(alice, task)
	if as := f.claim(runner, 1); len(as) != 1 || len(as[0].Manifest.Tools[0].Files) != 1 ||
		as[0].Manifest.Tools[0].TreeHash != d2.Tool.TreeHash {
		t.Fatalf("after re-registering = %+v", as)
	}

	// A damaged object store fails the run instead of mounting other code.
	if _, err := f.db.NewUpdate().Model((*store.ToolFile)(nil)).Set("blob_hash = ?", strings.Repeat("0", 64)).
		Where("tool_id = ?", d2.Tool.ID).Exec(f.ctx); err != nil {
		t.Fatal(err)
	}
	broken := f.start(alice, task)
	if as := f.claim(runner, 1); len(as) != 0 {
		t.Fatalf("claimed a run whose tool files are gone: %+v", as)
	}
	if r := f.get(alice, broken.ID).Run; r.Status != store.RunFailed || r.ErrorCode != "internal_error" {
		t.Fatalf("run = %s %s %s", r.Status, r.ErrorCode, r.ErrorMessage)
	}
	if f.stateRefCount(broken.ID) != 0 {
		t.Fatal("claim preparation failure retained its state input pin")
	}

	// Removing the tool removes its files.
	if err := f.tasks.RemoveTool(f.ctx, alice, d.Tool.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.NewSelect().Model((*store.ToolFile)(nil)).Count(f.ctx); err != nil || n != 0 {
		t.Fatalf("tool files left = %d, %v", n, err)
	}
}

// A tool registration carrying one of the caller's credential values is
// refused: it would reach every task of the project.
func TestToolRegistrationRejectsPlaintext(t *testing.T) {
	f := newPlaintextFixture(t)
	project := f.alice.PersonalProjectID
	_, err := f.tasks.RegisterTool(f.ctx, f.agent, project, service.RegisterToolInput{
		Name: "lib", Kind: store.ToolKindScript, Description: "x",
		Files: []service.FileInput{{Path: "lib.py", Content: []byte("TOKEN = '" + testSecret + "'\n")}},
	})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/files/0/content")
	_, err = f.tasks.RegisterTool(f.ctx, f.agent, project, service.RegisterToolInput{
		Name: "api", Kind: store.ToolKindHTTP, Description: "key " + testSecret, URL: "https://a.example/?k=" + testSecret,
	})
	rejected(t, err, []string{"LLM_API_KEY"}, "#/description", "#/url")
}

// A tool call the runner reports becomes a tool.call event whose payload
// is the API's ToolCallRecord, in the stage it was made in.
func TestToolCallEvents(t *testing.T) {
	f := newFixture(t)
	alice := f.user("alice")
	task := f.apply(alice, "digest", basicManifest()).Task.ID
	run := f.start(alice, task)
	runner := f.runner("r1")
	f.claim(runner, 1)
	if err := f.coord.Started(f.ctx, runner, run.ID, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	err := f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{
		{Seq: 1, Kind: runproto.EventToolCall, Stage: "fetch", ToolCall: &runproto.ToolCall{
			Tool: "crm", Method: "search", OK: true, DurationMillis: 12, ArgsSummary: `{"q":"acme"}`}},
		{Seq: 2, Kind: runproto.EventToolCall, ToolCall: &runproto.ToolCall{
			Tool: "crm", Method: "update", Error: "HTTP 503 " + strings.Repeat("x", 2000)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := f.runs.Events(f.ctx, alice, run.ID, service.EventsInput{Kinds: []string{"tool.call"}})
	if err != nil || len(page.Events) != 2 {
		t.Fatalf("events = %+v, %v", page.Events, err)
	}
	first, second := page.Events[0], page.Events[1]
	if first.Stage != "fetch" || first.Message != "crm search" ||
		first.Payload != `{"tool":"crm","method":"search","ok":true,"duration_ms":12,"args_summary":"{\"q\":\"acme\"}","error":null}` {
		t.Fatalf("first = %+v", first)
	}
	if !strings.Contains(second.Payload, `"ok":false`) || !strings.Contains(second.Payload, `"args_summary":null`) ||
		len(second.Payload) > 1300 {
		t.Fatalf("second = %s", second.Payload)
	}

	// A tool.call without its record is a protocol error.
	err = f.coord.AppendEvents(f.ctx, runner, run.ID, []runproto.Event{{Seq: 3, Kind: runproto.EventToolCall}})
	wantErr(t, err, service.ErrProtocol)
}
