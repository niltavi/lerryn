package goserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// Tests named TestKnownIssue* pin generator behaviour that is wrong or
// surprising. They are recorded in docs/spikes/s2-openapi-codegen.md; when
// one starts failing after an oapi-codegen upgrade, re-check the finding.

func TestNullableRequiredFieldsEncodeAsNull(t *testing.T) {
	run := Run{Id: "run_1", Status: Queued, Links: Links{Console: "http://localhost:8080/runs/run_1"}}
	got := encode(t, run)
	for _, field := range []string{"finished_at", "exit_code", "error", "labels"} {
		v, ok := got[field]
		if !ok {
			t.Errorf("%s: missing, want explicit null", field)
			continue
		}
		if v != nil {
			t.Errorf("%s = %v, want null", field, v)
		}
	}
	for _, field := range []string{"attempt", "principal", "priority"} {
		if _, ok := got[field]; ok {
			t.Errorf("%s: optional nil field should be omitted", field)
		}
	}
}

func TestNullableRefDecodes(t *testing.T) {
	var run Run
	body := `{"id":"r","status":"failed","finished_at":"2026-11-02T08:00:00Z","exit_code":1,` +
		`"error":{"code":"not_found","stage":null},"principal":{"user_id":"u"},"labels":["a"],"links":{"console":"x"}}`
	if err := json.Unmarshal([]byte(body), &run); err != nil {
		t.Fatal(err)
	}
	if run.Error == nil || run.Error.Code != ErrorCodeNotFound || run.Error.Stage != nil {
		t.Errorf("error = %+v", run.Error)
	}
	if run.Principal == nil || run.Principal.UserId != "u" {
		t.Errorf("principal = %+v", run.Principal)
	}
	if run.FinishedAt == nil || run.FinishedAt.Year() != 2026 {
		t.Errorf("finished_at = %v", run.FinishedAt)
	}
}

// With the default pointer mapping an optional nullable field cannot tell
// "absent" from "null". output-options.nullable-type switches these fields
// to nullable.Nullable[T], which keeps the three states apart.
func TestKnownIssueOptionalNullableLosesNull(t *testing.T) {
	var absent, explicitNull Run
	if err := json.Unmarshal([]byte(`{}`), &absent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"attempt":null}`), &explicitNull); err != nil {
		t.Fatal(err)
	}
	if absent.Attempt != nil || explicitNull.Attempt != nil {
		t.Fatal("expected both to decode to nil")
	}
}

func TestEnumViaOneOfConst(t *testing.T) {
	// Branches with a title become typed constants with Valid().
	for _, c := range []ErrorCode{ErrorCodeValidationFailed, ErrorCodeVersionConflict, ErrorCodeNotFound} {
		if !c.Valid() {
			t.Errorf("%q should be valid", c)
		}
	}
	if ErrorCode("bogus").Valid() {
		t.Error("unknown code reported as valid")
	}
	// Without per-branch titles the schema degrades to a plain string
	// alias: no constants and no Valid(). The assignment below only
	// compiles because StageState is `= string`.
	var s StageState = "anything"
	_ = s
}

// A nullable enum written as `type: [string, "null"]` with `null` in the
// enum list yields a bogus constant for the null member.
func TestKnownIssueNullableEnumNullConstant(t *testing.T) {
	if !LessThannil.Valid() || string(LessThannil) != "<nil>" {
		t.Fatalf("LessThannil = %q; the generator may have fixed the null enum member", LessThannil)
	}
}

func TestDiscriminatorRoundTrip(t *testing.T) {
	var body SetTriggersJSONBody
	in := `{"triggers":[{"type":"cron","schedule":"0 8 * * 1-5","timezone":"Asia/Shanghai"},{"type":"webhook","max_body":1048576}]}`
	if err := json.Unmarshal([]byte(in), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Triggers) != 2 {
		t.Fatalf("got %d triggers", len(body.Triggers))
	}
	first, err := body.Triggers[0].ValueByDiscriminator()
	if err != nil {
		t.Fatal(err)
	}
	cron, ok := first.(CronTrigger)
	if !ok || cron.Timezone != "Asia/Shanghai" {
		t.Fatalf("first = %#v", first)
	}
	second, err := body.Triggers[1].ValueByDiscriminator()
	if err != nil {
		t.Fatal(err)
	}
	if hook, ok := second.(WebhookTrigger); !ok || hook.MaxBody == nil || *hook.MaxBody != 1048576 {
		t.Fatalf("second = %#v", second)
	}

	// From* fills in the discriminator value.
	var tr Trigger
	if err := tr.FromCronTrigger(CronTrigger{Schedule: "@daily", Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	if d, _ := tr.Discriminator(); d != "cron" {
		t.Errorf("discriminator = %q", d)
	}

	var unknown Trigger
	if err := json.Unmarshal([]byte(`{"type":"manual"}`), &unknown); err != nil {
		t.Fatal(err)
	}
	if _, err := unknown.ValueByDiscriminator(); err == nil {
		t.Error("unknown discriminator should be an error")
	}
}

func TestWebhookPayloadTypes(t *testing.T) {
	// Needs output-options.skip-prune: schemas referenced only from
	// `webhooks` are pruned otherwise and the package does not compile.
	ev := RunFinishedEvent{Event: RunFinished, Run: Run{Id: "r"}}
	if got := encode(t, ev)["event"]; got != "run.finished" {
		t.Errorf("event = %v", got)
	}
	var _ WebhookReceiverInterface = webhookReceiver{}
}

func TestProblemResponses(t *testing.T) {
	problem := Problem{
		Type:      "urn:lerryn:error:not_found",
		Title:     "Not found",
		Status:    http.StatusNotFound,
		Code:      ErrorCodeNotFound,
		RequestId: "req_1",
	}
	problem.Set("current_version", 3)

	tests := []struct {
		name        string
		resp        GetRunResponseObject
		wantStatus  int
		wantExtraOK bool
	}{
		{
			name:        "default response keeps additional properties",
			resp:        GetRundefaultApplicationProblemPlusJSONResponse{Body: problem, StatusCode: http.StatusServiceUnavailable},
			wantStatus:  http.StatusServiceUnavailable,
			wantExtraOK: true,
		},
		{
			// Known issue: a status-specific response that $refs a shared
			// response component is a defined type over Problem, which drops
			// Problem's MarshalJSON, so additional properties are lost.
			name:        "status response via shared component drops additional properties",
			resp:        GetRun404ApplicationProblemPlusJSONResponse{ProblemApplicationProblemPlusJSONResponse(problem)},
			wantStatus:  http.StatusNotFound,
			wantExtraOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := Handler(NewStrictHandler(stubServer{getRun: tt.resp}, nil))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run_1", nil))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q", ct)
			}
			var got map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["code"] != "not_found" || got["request_id"] != "req_1" {
				t.Errorf("body = %v", got)
			}
			if _, ok := got["current_version"]; ok != tt.wantExtraOK {
				t.Errorf("current_version present = %v, want %v", ok, tt.wantExtraOK)
			}
		})
	}
}

// A `default` response carries its own StatusCode. Leaving it at zero makes
// the generated Visit method call WriteHeader(0), which panics in net/http.
func TestKnownIssueDefaultResponseZeroStatusPanics(t *testing.T) {
	resp := GetRundefaultApplicationProblemPlusJSONResponse{Body: Problem{Code: ErrorCodeNotFound, Status: http.StatusNotFound}}
	defer func() {
		if recover() == nil {
			t.Fatal("expected WriteHeader(0) to panic; the generator may now default the status")
		}
	}()
	_ = resp.VisitGetRunResponse(httptest.NewRecorder())
}

// The strict server binds parameters and decodes JSON, but does not check
// the body against the schema: required fields, const and discriminator
// values all pass through to the handler.
func TestKnownIssueStrictServerSkipsSchemaValidation(t *testing.T) {
	var got SetTriggersRequestObject
	srv := stubServer{setTriggers: func(req SetTriggersRequestObject) { got = req }}
	h := Handler(NewStrictHandler(srv, nil))

	req := httptest.NewRequest(http.MethodPut, "/api/v1/tasks/t1/triggers",
		strings.NewReader(`{"triggers":[{"type":"manual"}]}`))
	req.Header.Set("If-Match", "3")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got.Body == nil || len(got.Body.Triggers) != 1 {
		t.Fatalf("handler did not receive the body: %+v", got)
	}

	// Required parameters, on the other hand, are enforced by the wrapper.
	req = httptest.NewRequest(http.MethodPut, "/api/v1/tasks/t1/triggers", strings.NewReader(`{"triggers":[]}`))
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing If-Match: status = %d", rec.Code)
	}
}

func TestEmbeddedSpecKeeps31Features(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q", spec.OpenAPI)
	}
	if spec.Webhooks["run.finished"] == nil {
		t.Error("webhooks lost in the embedded spec")
	}
	op := spec.Paths.Find("/api/v1/runs/{runId}").Get
	if op.Extensions["x-operation-kind"] != "read" {
		t.Errorf("x-operation-kind = %v", op.Extensions["x-operation-kind"])
	}
	mcp, ok := op.Extensions["x-mcp"].(map[string]any)
	if !ok || mcp["tool"] != "run_get" {
		t.Errorf("x-mcp = %#v", op.Extensions["x-mcp"])
	}
	finished := spec.Components.Schemas["Run"].Value.Properties["finished_at"].Value
	if !slices.Contains(finished.Type.Slice(), "null") {
		t.Errorf("finished_at type = %v, want it to include null", finished.Type.Slice())
	}
}

// kin-openapi's request validator, which oapi-codegen's net/http
// middleware is built on, understands the 3.1 constructs used here.
func TestKinOpenAPIValidatesRequests(t *testing.T) {
	spec, err := GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	// The embedded spec carries an absolute server URL; validate paths only.
	spec.Servers = nil
	router, err := legacy.NewRouter(spec)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "valid cron", body: `{"triggers":[{"type":"cron","schedule":"@daily","timezone":"UTC"}]}`},
		{name: "valid webhook with null max_body", body: `{"triggers":[{"type":"webhook","max_body":null}]}`},
		{name: "unknown discriminator", body: `{"triggers":[{"type":"manual"}]}`, wantErr: true},
		{name: "cron missing timezone", body: `{"triggers":[{"type":"cron","schedule":"@daily"}]}`, wantErr: true},
		{name: "wrong type for nullable integer", body: `{"triggers":[{"type":"webhook","max_body":"big"}]}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/api/v1/tasks/t1/triggers", bytes.NewReader([]byte(tt.body)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("If-Match", "3")
			req.Header.Set("Authorization", "Bearer x")
			route, params, err := router.FindRoute(req)
			if err != nil {
				t.Fatal(err)
			}
			err = openapi3filter.ValidateRequest(context.Background(), &openapi3filter.RequestValidationInput{
				Request:    req,
				PathParams: params,
				Route:      route,
				Options:    &openapi3filter.Options{AuthenticationFunc: openapi3filter.NoopAuthenticationFunc},
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

type stubServer struct {
	getRun      GetRunResponseObject
	setTriggers func(SetTriggersRequestObject)
}

func (s stubServer) ListRuns(context.Context, ListRunsRequestObject) (ListRunsResponseObject, error) {
	return ListRuns200JSONResponse{Items: []Run{}}, nil
}

func (s stubServer) GetRun(context.Context, GetRunRequestObject) (GetRunResponseObject, error) {
	if s.getRun == nil {
		return nil, errors.New("no response configured")
	}
	return s.getRun, nil
}

func (s stubServer) SetTriggers(_ context.Context, req SetTriggersRequestObject) (SetTriggersResponseObject, error) {
	if s.setTriggers != nil {
		s.setTriggers(req)
	}
	return SetTriggers200JSONResponse{Triggers: []Trigger{}, Version: 4}, nil
}

type webhookReceiver struct{}

func (webhookReceiver) HandleRunFinishedNotificationWebhook(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func encode(t *testing.T, v any) map[string]any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
