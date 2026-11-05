package httpapi

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"
)

// bundlePath is the spec api.gen.go is generated from.
const bundlePath = "../../../api/bundled/openapi.yaml"

var (
	specOnce   sync.Once
	specDoc    *openapi3.T
	specRouter routers.Router
	specErr    error
)

// loadSpec parses the bundled spec once per test binary.
func loadSpec(t *testing.T) (*openapi3.T, routers.Router) {
	t.Helper()
	specOnce.Do(func() {
		// The contract declares event streams as strings; read them as
		// such, as kin-openapi does for files.
		openapi3filter.RegisterBodyDecoder("text/event-stream", openapi3filter.FileBodyDecoder)
		loader := openapi3.NewLoader()
		specDoc, specErr = loader.LoadFromFile(bundlePath)
		if specErr != nil {
			return
		}
		if specErr = specDoc.Validate(context.Background()); specErr != nil {
			return
		}
		// Match on paths only; the servers list names an example host.
		routed := *specDoc
		routed.Servers = nil
		specRouter, specErr = legacy.NewRouter(&routed)
	})
	if specErr != nil {
		t.Fatalf("load %s: %v", bundlePath, specErr)
	}
	return specDoc, specRouter
}

// assertMatchesSpec checks that rec is a response the spec allows for req:
// a declared status (or default), the declared media type, and a body that
// validates against the schema. This is what keeps handlers honest about
// required, nullable and enum fields that the generated types cannot
// enforce on their own.
func assertMatchesSpec(t *testing.T, req *http.Request, rec *httptest.ResponseRecorder) {
	t.Helper()
	_, router := loadSpec(t)
	route, params, err := router.FindRoute(req)
	if err != nil {
		t.Fatalf("%s %s is not in the spec: %v", req.Method, req.URL.Path, err)
	}
	err = openapi3filter.ValidateResponse(context.Background(), &openapi3filter.ResponseValidationInput{
		RequestValidationInput: &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: params,
			Route:      route,
		},
		Status: rec.Code,
		Header: rec.Header(),
		Body:   io.NopCloser(bytes.NewReader(rec.Body.Bytes())),
		Options: &openapi3filter.Options{
			IncludeResponseStatus: true,
			MultiError:            true,
		},
	})
	if err != nil {
		t.Fatalf("response does not match the spec: %v\nbody: %s", err, rec.Body)
	}
}

func TestSpecVersionMatchesBundle(t *testing.T) {
	doc, _ := loadSpec(t)
	if doc.Info.Version != SpecVersion {
		t.Fatalf("SpecVersion = %q but the bundle says %q; update the constant in httpapi.go", SpecVersion, doc.Info.Version)
	}
	if doc.OpenAPI != "3.1.0" {
		t.Errorf("openapi = %q, want 3.1.0", doc.OpenAPI)
	}
}

func TestEveryErrorCodeHasATitle(t *testing.T) {
	doc, _ := loadSpec(t)
	schema := doc.Components.Schemas["ErrorCode"].Value
	if len(schema.OneOf) == 0 {
		t.Fatal("ErrorCode is expected to be declared with oneOf + const")
	}
	for _, branch := range schema.OneOf {
		code := ErrorCode(branch.Value.Const.(string))
		if !code.Valid() {
			t.Errorf("%q is in the spec but not in the generated enum; run make generate", code)
		}
		if problemTitles[code] == "" {
			t.Errorf("no title for %q in problemTitles", code)
		}
	}
	if len(problemTitles) != len(schema.OneOf) {
		t.Errorf("problemTitles has %d entries, the spec has %d codes", len(problemTitles), len(schema.OneOf))
	}
}

// Every operation must carry the extensions the MCP generator and the
// token scope check rely on. Spectral checks their full shape (make
// api-lint); this guards the parts Go code reads, in the bundle the server
// is generated from.
func TestOperationsDeclareExtensions(t *testing.T) {
	doc, _ := loadSpec(t)
	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			switch op.Extensions["x-operation-kind"] {
			case "read", "mutate", "execute":
			default:
				t.Errorf("%s %s: x-operation-kind = %v", method, path, op.Extensions["x-operation-kind"])
			}
			mcp, ok := op.Extensions["x-mcp"].(map[string]any)
			if _, isBool := mcp["expose"].(bool); !ok || !isBool {
				t.Errorf("%s %s: x-mcp = %#v, want an object with a boolean expose", method, path, op.Extensions["x-mcp"])
			}
		}
	}
}
