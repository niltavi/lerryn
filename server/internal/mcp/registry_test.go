package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

var update = flag.Bool("update", false, "rewrite golden files")

func loadTestRegistry(t *testing.T) *Registry {
	t.Helper()
	spec, err := os.ReadFile(filepath.Join("testdata", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := Load(context.Background(), spec)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

// TestRegistryGolden pins the tool list derived from the test spec. In the
// runtime-built design this golden file is what a reviewer reads instead of
// generated registration code; run go test -update to refresh it.
func TestRegistryGolden(t *testing.T) {
	reg := loadTestRegistry(t)
	type goldenTool struct {
		Name        string         `json:"name"`
		Title       string         `json:"title"`
		OperationID string         `json:"operation_id"`
		Kind        OperationKind  `json:"kind"`
		Method      string         `json:"method"`
		Path        string         `json:"path"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"input_schema"`
		Annotations any            `json:"annotations"`
	}
	var tools []goldenTool
	for _, tl := range reg.Tools() {
		tools = append(tools, goldenTool{
			Name: tl.Name, Title: tl.Title, OperationID: tl.OperationID, Kind: tl.Kind,
			Method: tl.Method, Path: tl.Path, Description: tl.Description,
			InputSchema: tl.InputSchema, Annotations: tl.Annotations,
		})
	}
	got, err := json.MarshalIndent(tools, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	const golden = "testdata/tools.golden.json"
	if *update {
		if err := os.WriteFile(golden, got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tool list differs from %s; run go test -update if the change is intended\n%s", golden, got)
	}
}

func TestRegistryContents(t *testing.T) {
	reg := loadTestRegistry(t)
	if n := len(reg.Tools()); n != 2 {
		t.Fatalf("got %d tools, want 2 (login has no x-mcp)", n)
	}
	start, ok := reg.Tool("run_start")
	if !ok {
		t.Fatal("run_start missing")
	}
	if start.Path != "/api/v1/tasks/{taskId}/runs" {
		t.Errorf("path = %q, want server base path prefixed", start.Path)
	}
	props := start.InputSchema["properties"].(map[string]any)
	for _, arg := range []string{"task_id", "wait", "idempotency_key", "test", "inputs", "reason"} {
		if _, ok := props[arg]; !ok {
			t.Errorf("run_start: argument %q missing", arg)
		}
	}
	if _, ok := props["id"]; ok {
		t.Error("run_start: readOnly body property id must not be an argument")
	}
	if got := start.InputSchema["required"]; !equalJSON(got, []string{"task_id"}) {
		t.Errorf("run_start required = %v, want [task_id]", got)
	}
	// Type arrays from OpenAPI 3.1 survive the conversion.
	if got := props["reason"].(map[string]any)["type"]; !equalJSON(got, []string{"string", "null"}) {
		t.Errorf("reason type = %v", got)
	}

	get, _ := reg.Tool("run_get")
	runID := get.InputSchema["properties"].(map[string]any)["run_id"].(map[string]any)
	if runID["description"] != "运行 ID" || runID["pattern"] == nil {
		t.Errorf("run_id from a $ref parameter lost description or pattern: %v", runID)
	}
	if _, ok := reg.Tool("login"); ok {
		t.Error("login must not be exposed")
	}
}

func equalJSON(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestLoadErrors(t *testing.T) {
	const head = `openapi: 3.1.0
info: {title: t, version: "1"}
paths:
`
	op := func(body string) string { return head + body }
	tests := []struct {
		name string
		spec string
		want string
	}{
		{
			name: "openapi 3.0 is rejected",
			spec: strings.Replace(op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      responses: {'200': {description: ok}}\n"), "3.1.0", "3.0.3", 1),
			want: "only 3.1",
		},
		{
			name: "missing operation kind",
			spec: op("  /a:\n    get:\n      operationId: a\n      responses: {'200': {description: ok}}\n"),
			want: "missing x-operation-kind",
		},
		{
			name: "unknown operation kind",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: write\n      responses: {'200': {description: ok}}\n"),
			want: "want read, mutate or execute",
		},
		{
			name: "kind contradicts method",
			spec: op("  /a:\n    delete:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: a_delete, description: d}\n      responses: {'204': {description: ok}}\n"),
			want: "contradicts",
		},
		{
			name: "unknown x-mcp field",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: a_get, summary: d}\n      responses: {'200': {description: ok}}\n"),
			want: "unknown field",
		},
		{
			name: "bad tool name",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: getA, description: d}\n      responses: {'200': {description: ok}}\n"),
			want: "want snake_case",
		},
		{
			name: "missing description",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: a_get}\n      responses: {'200': {description: ok}}\n"),
			want: "description is required",
		},
		{
			name: "fields alongside expose false",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: false, tool: a_get}\n      responses: {'200': {description: ok}}\n"),
			want: "no other fields",
		},
		{
			name: "duplicate tool name",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: same, description: d}\n      responses: {'200': {description: ok}}\n" +
				"  /b:\n    get:\n      operationId: b\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: same, description: d}\n      responses: {'200': {description: ok}}\n"),
			want: "declared by both",
		},
		{
			name: "argument collision between parameter and body",
			spec: op("  /a/{id}:\n    put:\n      operationId: a\n      x-operation-kind: mutate\n      x-mcp: {expose: true, tool: a_put, description: d}\n" +
				"      parameters: [{name: id, in: path, required: true, schema: {type: string}}]\n" +
				"      requestBody: {content: {application/json: {schema: {type: object, properties: {id: {type: string}}}}}}\n" +
				"      responses: {'200': {description: ok}}\n"),
			want: "declared twice",
		},
		{
			name: "cookie parameter",
			spec: op("  /a:\n    get:\n      operationId: a\n      x-operation-kind: read\n      x-mcp: {expose: true, tool: a_get, description: d}\n" +
				"      parameters: [{name: sid, in: cookie, schema: {type: string}}]\n      responses: {'200': {description: ok}}\n"),
			want: "not supported",
		},
		{
			name: "recursive schema",
			spec: op("  /a:\n    post:\n      operationId: a\n      x-operation-kind: mutate\n      x-mcp: {expose: true, tool: a_post, description: d}\n" +
				"      requestBody: {content: {application/json: {schema: {$ref: '#/components/schemas/Node'}}}}\n      responses: {'200': {description: ok}}\n" +
				"components:\n  schemas:\n    Node: {type: object, properties: {children: {type: array, items: {$ref: '#/components/schemas/Node'}}}}\n"),
			want: "recursive",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(context.Background(), []byte(tt.spec))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}

func TestLoadNotExposed(t *testing.T) {
	spec := `openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /a:
    get:
      operationId: a
      x-operation-kind: read
      x-mcp: {expose: false}
      responses: {'200': {description: ok}}
`
	reg, err := Load(context.Background(), []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Tools()) != 0 {
		t.Fatalf("expose: false must not register a tool, got %d", len(reg.Tools()))
	}
}

func TestLoadTitleAndHints(t *testing.T) {
	spec := `openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /triggers:
    put:
      operationId: setTriggers
      summary: Set triggers
      x-operation-kind: mutate
      x-mcp:
        expose: true
        tool: trigger_set
        title: 设置触发器
        description: Replace the task's triggers.
        hints: {destructive: true}
      responses: {'204': {description: ok}}
`
	reg, err := Load(context.Background(), []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	tool, ok := reg.Tool("trigger_set")
	if !ok {
		t.Fatal("trigger_set not registered")
	}
	if tool.Title != "设置触发器" {
		t.Errorf("title = %q, want the x-mcp title over the summary", tool.Title)
	}
	if !*tool.Annotations.DestructiveHint || !tool.Annotations.IdempotentHint {
		t.Errorf("hints not applied: destructive=%v idempotent=%v", *tool.Annotations.DestructiveHint, tool.Annotations.IdempotentHint)
	}
}

func TestNestedBody(t *testing.T) {
	// A composed body cannot be flattened, so it travels as one "body"
	// argument.
	spec := `openapi: 3.1.0
info: {title: t, version: "1"}
paths:
  /triggers:
    put:
      operationId: setTriggers
      x-operation-kind: mutate
      x-mcp: {expose: true, tool: trigger_set, description: d}
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: array
              items:
                oneOf:
                  - {type: object, properties: {cron: {type: string}}}
                  - {type: object, properties: {webhook: {type: boolean}}}
      responses: {'200': {description: ok}}
`
	reg, err := Load(context.Background(), []byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	tl, _ := reg.Tool("trigger_set")
	if !tl.bodyNested {
		t.Fatal("array body must be nested")
	}
	if got := tl.InputSchema["required"]; !equalJSON(got, []string{"body"}) {
		t.Fatalf("required = %v", got)
	}
}

// BenchmarkLoad measures building the registry at startup for a spec about
// the size of the full v1 API: the test spec plus 60 copies of startRun, each
// exposed as a tool.
func BenchmarkLoad(b *testing.B) {
	data, err := os.ReadFile("testdata/openapi.yaml")
	if err != nil {
		b.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		b.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	tmpl, _ := json.Marshal(paths["/tasks/{taskId}/runs"])
	for i := range 60 {
		var item map[string]any
		_ = json.Unmarshal(tmpl, &item)
		op := item["post"].(map[string]any)
		op["operationId"] = fmt.Sprintf("startRun%d", i)
		op["x-mcp"].(map[string]any)["name"] = fmt.Sprintf("run_start_%d", i)
		paths[fmt.Sprintf("/bench%d/tasks/{taskId}/runs", i)] = item
	}
	spec, err := json.Marshal(doc)
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		reg, err := Load(context.Background(), spec)
		if err != nil {
			b.Fatal(err)
		}
		if len(reg.Tools()) != 62 {
			b.Fatalf("got %d tools", len(reg.Tools()))
		}
	}
}

func TestSnakeCase(t *testing.T) {
	tests := map[string]string{
		"runId":           "run_id",
		"Idempotency-Key": "idempotency_key",
		"If-Match":        "if_match",
		"dry_run":         "dry_run",
		"taskID":          "task_id",
		"limit":           "limit",
		"v2Name":          "v2_name",
	}
	for in, want := range tests {
		if got := snakeCase(in); got != want {
			t.Errorf("snakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
