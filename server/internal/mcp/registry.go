// Package mcp exposes HTTP API operations as MCP tools.
//
// The MCP layer is a shell around the OpenAPI contract and holds no business
// logic. Tools are derived at startup from operations that carry an x-mcp
// extension. A tool call is turned into an in-process HTTP request and handed
// to the same http.Handler that serves the REST API, so authentication,
// validation, error mapping and audit are shared by construction.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/getkin/kin-openapi/openapi3"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// OperationKind is the value of the x-operation-kind extension.
type OperationKind string

// Operation kinds, see the tech design section 9.3.
const (
	KindRead    OperationKind = "read"
	KindMutate  OperationKind = "mutate"
	KindExecute OperationKind = "execute"
)

// Operation is one operation of the contract, indexed for api_describe and
// api_call and shared by the dedicated tools. It holds everything needed to
// turn tool arguments into an HTTP request for the operation.
type Operation struct {
	OperationID string
	Kind        OperationKind
	Method      string
	// Path is the request path template including the server base path,
	// for example /api/v1/runs/{runId}.
	Path        string
	Summary     string
	Description string
	Tags        []string
	Deprecated  bool
	// ToolName is the dedicated tool for the operation, empty when it is
	// reached only through api_call.
	ToolName string
	// Anonymous is true when the operation can be called without
	// credentials; SessionOnly when it accepts only a console session and
	// therefore never an access token.
	Anonymous   bool
	SessionOnly bool
	// InputSchema describes the arguments, with the same mapping a
	// dedicated tool uses.
	InputSchema map[string]any
	// SuccessStatus, ResponseMediaType and ResponseSchema describe the first
	// 2xx response. ResponseSchema is nil when the response has no JSON
	// body or its schema cannot be inlined.
	SuccessStatus     int
	ResponseMediaType string
	ResponseSchema    map[string]any
	// Streaming is true when the success response is a server-sent event
	// stream, which a single tool result cannot carry.
	Streaming bool
	// Unsupported explains why the arguments cannot be mapped onto a
	// request, for example a non-JSON request body. It is empty for every
	// callable operation and always empty for exposed ones, which must map
	// faithfully or fail to load.
	Unsupported string

	// args maps a tool argument name to where it goes in the HTTP request.
	args map[string]binding
	// bodyNested is true when the request body is passed as a single "body"
	// argument instead of being flattened into top-level arguments.
	bodyNested bool
}

// Scope returns the access token scope the operation needs, following the
// rule in api/README.md: read needs read, mutate write, execute execute.
func (o *Operation) Scope() string {
	switch o.Kind {
	case KindMutate:
		return "write"
	case KindExecute:
		return "execute"
	default:
		return "read"
	}
}

// Args returns the argument names the operation accepts, sorted.
func (o *Operation) Args() []string { return sortedKeys(o.args) }

// HasArg reports whether the operation accepts the named argument.
func (o *Operation) HasArg(name string) bool {
	_, ok := o.args[name]
	return ok
}

// Tool is an MCP tool derived from one OpenAPI operation.
type Tool struct {
	*Operation
	Name        string
	Title       string
	Description string
	Annotations *mcpsdk.ToolAnnotations
}

type location int

const (
	inPath location = iota
	inQuery
	inHeader
	inBody
)

// binding maps a tool argument to an HTTP request element.
type binding struct {
	in   location
	name string // parameter name, or body property name
}

// Registry is the set of operations and tools derived from a spec.
type Registry struct {
	tools []*Tool      // sorted by name
	ops   []*Operation // sorted by operationId
}

// Tools returns the tools sorted by name.
func (r *Registry) Tools() []*Tool { return r.tools }

// Operations returns every operation of the contract, sorted by
// operationId.
func (r *Registry) Operations() []*Operation { return r.ops }

// Operation returns the operation with the given operationId.
func (r *Registry) Operation(id string) (*Operation, bool) {
	i, ok := slices.BinarySearchFunc(r.ops, id, func(o *Operation, n string) int { return strings.Compare(o.OperationID, n) })
	if !ok {
		return nil, false
	}
	return r.ops[i], true
}

// Tool returns the tool with the given name.
func (r *Registry) Tool(name string) (*Tool, bool) {
	i, ok := slices.BinarySearchFunc(r.tools, name, func(t *Tool, n string) int { return strings.Compare(t.Name, n) })
	if !ok {
		return nil, false
	}
	return r.tools[i], true
}

// xMCP is the shape of the x-mcp operation extension. api/README.md is the
// source of truth for it.
type xMCP struct {
	// Expose registers the operation as a named tool.
	Expose bool `json:"expose"`
	// Tool is the tool name, for example run_get.
	Tool string `json:"tool"`
	// Title is the display name; the operation summary is used when empty.
	Title string `json:"title"`
	// Description is written for the agent, not for API documentation.
	Description string `json:"description"`
	// Hints overrides annotations derived from x-operation-kind.
	Hints *xHints `json:"hints"`
}

// xHints overrides individual derived annotations, for operations where the
// derivation is wrong, such as a PUT that replaces a whole collection.
type xHints struct {
	Destructive *bool `json:"destructive"`
	Idempotent  *bool `json:"idempotent"`
	OpenWorld   *bool `json:"open_world"`
}

// reservedTools are the generic tools every server adds next to the
// contract's own.
var reservedTools = map[string]bool{describeToolName: true, callToolName: true}

var supportedMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true,
	http.MethodPut: true, http.MethodPatch: true, http.MethodDelete: true,
}

// bodyArg is the argument that carries a request body that cannot be
// flattened.
const bodyArg = "body"

// toolNamePattern follows the MCP tool name rules, narrowed to the snake_case
// style used by Lerryn.
var toolNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// headers that OpenAPI says to ignore when declared as parameters; they are
// managed by the forwarder.
var reservedHeaders = map[string]bool{"accept": true, "content-type": true, "authorization": true}

// Load parses an OpenAPI 3.1 document and builds the tool registry. It fails
// on anything it cannot map faithfully, so that contract mistakes surface at
// build or startup time rather than as confusing tool behaviour.
func Load(ctx context.Context, spec []byte) (*Registry, error) {
	loader := openapi3.NewLoader()
	loader.Context = ctx
	doc, err := loader.LoadFromData(spec)
	if err != nil {
		return nil, fmt.Errorf("load openapi: %w", err)
	}
	if !doc.IsOpenAPI31OrLater() {
		return nil, fmt.Errorf("openapi %s: only 3.1 documents are supported", doc.OpenAPI)
	}
	if err := doc.Validate(ctx); err != nil {
		return nil, fmt.Errorf("validate openapi: %w", err)
	}

	base, err := basePath(doc)
	if err != nil {
		return nil, err
	}
	conv := newSchemaConverter(doc, "readOnly")
	outConv := newSchemaConverter(doc, "writeOnly")
	reg := &Registry{}
	seen := map[string]string{}    // tool name -> operationId
	seenOps := map[string]string{} // operationId -> method and path

	paths := doc.Paths.Map()
	for _, path := range sortedKeys(paths) {
		item := paths[path]
		ops := item.Operations()
		for _, method := range sortedKeys(ops) {
			op := ops[method]
			kind, err := operationKind(op)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			if err := checkKind(kind, method); err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			x, ok, err := parseXMCP(op)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", method, path, err)
			}
			exposed := ok && x.Expose
			if op.OperationID == "" {
				if exposed {
					return nil, fmt.Errorf("%s %s: an exposed operation needs an operationId", method, path)
				}
				continue // api_call addresses operations by operationId
			}
			if prev, dup := seenOps[op.OperationID]; dup {
				return nil, fmt.Errorf("operationId %q: declared by both %s and %s %s", op.OperationID, prev, method, path)
			}
			seenOps[op.OperationID] = method + " " + path

			o, buildErr := buildOperation(conv, base+path, method, item, op, kind)
			describeResponse(outConv, o, op)
			o.Anonymous, o.SessionOnly = access(doc, op)
			if !exposed {
				if buildErr != nil {
					o.Unsupported = buildErr.Error()
				}
				reg.ops = append(reg.ops, o)
				continue
			}
			if buildErr != nil {
				return nil, fmt.Errorf("tool %q (%s): %w", x.Tool, op.OperationID, buildErr)
			}
			if prev, dup := seen[x.Tool]; dup {
				return nil, fmt.Errorf("tool %q: declared by both %s and %s", x.Tool, prev, op.OperationID)
			}
			if reservedTools[x.Tool] {
				return nil, fmt.Errorf("tool %q (%s): the name is reserved for a generic tool", x.Tool, op.OperationID)
			}
			seen[x.Tool] = op.OperationID
			t, err := newTool(o, op, x)
			if err != nil {
				return nil, fmt.Errorf("tool %q (%s): %w", x.Tool, op.OperationID, err)
			}
			reg.ops = append(reg.ops, o)
			reg.tools = append(reg.tools, t)
		}
	}
	sort.Slice(reg.tools, func(i, j int) bool { return reg.tools[i].Name < reg.tools[j].Name })
	sort.Slice(reg.ops, func(i, j int) bool { return reg.ops[i].OperationID < reg.ops[j].OperationID })
	return reg, nil
}

// basePath returns the path of the first server URL when it is relative, so
// that the in-process request matches the route the REST API serves.
func basePath(doc *openapi3.T) (string, error) {
	if len(doc.Servers) == 0 {
		return "", nil
	}
	u := doc.Servers[0].URL
	if !strings.HasPrefix(u, "/") {
		// An absolute server URL describes a deployment, not a route prefix.
		return "", nil
	}
	if strings.Contains(u, "{") {
		return "", fmt.Errorf("server url %q: variables are not supported", u)
	}
	return strings.TrimSuffix(u, "/"), nil
}

func operationKind(op *openapi3.Operation) (OperationKind, error) {
	raw, ok := op.Extensions["x-operation-kind"]
	if !ok {
		return "", errors.New("missing x-operation-kind")
	}
	s, _ := raw.(string)
	switch k := OperationKind(s); k {
	case KindRead, KindMutate, KindExecute:
		return k, nil
	default:
		return "", fmt.Errorf("x-operation-kind %v: want read, mutate or execute", raw)
	}
}

func parseXMCP(op *openapi3.Operation) (xMCP, bool, error) {
	raw, ok := op.Extensions["x-mcp"]
	if !ok {
		return xMCP{}, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return xMCP{}, false, fmt.Errorf("x-mcp: %w", err)
	}
	var x xMCP
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&x); err != nil {
		return xMCP{}, false, fmt.Errorf("x-mcp: %w", err)
	}
	if !x.Expose {
		if x.Tool != "" || x.Title != "" || x.Description != "" || x.Hints != nil {
			return xMCP{}, false, errors.New("x-mcp: no other fields are allowed when expose is false")
		}
		return x, true, nil
	}
	if !toolNamePattern.MatchString(x.Tool) {
		return xMCP{}, false, fmt.Errorf("x-mcp.tool %q: want snake_case matching %s", x.Tool, toolNamePattern)
	}
	if strings.TrimSpace(x.Description) == "" {
		return xMCP{}, false, errors.New("x-mcp.description is required when expose is true")
	}
	return x, true, nil
}

func newTool(o *Operation, op *openapi3.Operation, x xMCP) (*Tool, error) {
	ann, err := annotations(o.Kind, o.Method)
	if err != nil {
		return nil, err
	}
	title := x.Title
	if title == "" {
		title = op.Summary
	}
	o.ToolName = x.Tool
	return &Tool{
		Operation:   o,
		Name:        x.Tool,
		Title:       title,
		Description: strings.TrimSpace(x.Description),
		Annotations: withHints(ann, x.Hints),
	}, nil
}

// buildOperation maps the operation's parameters and request body onto tool
// arguments. On an error the returned operation is still usable for
// api_describe; only calling it is impossible.
func buildOperation(conv *schemaConverter, path, method string, item *openapi3.PathItem, op *openapi3.Operation, kind OperationKind) (*Operation, error) {
	t := &Operation{
		OperationID: op.OperationID,
		Kind:        kind,
		Method:      method,
		Path:        path,
		Summary:     op.Summary,
		Description: strings.TrimSpace(op.Description),
		Tags:        op.Tags,
		Deprecated:  op.Deprecated,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		args:        map[string]binding{},
	}
	if !supportedMethods[method] {
		return t, fmt.Errorf("method %s is not supported", method)
	}
	props := map[string]any{}
	var required []string

	add := func(arg string, b binding, schema map[string]any, req bool) error {
		if _, dup := t.args[arg]; dup {
			return fmt.Errorf("argument %q is declared twice; rename the parameter or body property", arg)
		}
		t.args[arg] = b
		props[arg] = schema
		if req {
			required = append(required, arg)
		}
		return nil
	}

	params, err := mergeParameters(item.Parameters, op.Parameters)
	if err != nil {
		return t, err
	}
	for _, p := range params {
		var loc location
		switch p.In {
		case openapi3.ParameterInPath:
			loc = inPath
		case openapi3.ParameterInQuery:
			loc = inQuery
		case openapi3.ParameterInHeader:
			if reservedHeaders[strings.ToLower(p.Name)] {
				continue
			}
			loc = inHeader
		default:
			return t, fmt.Errorf("parameter %q: location %q is not supported", p.Name, p.In)
		}
		if p.Schema == nil {
			return t, fmt.Errorf("parameter %q: only schema-based parameters are supported", p.Name)
		}
		schema, err := conv.convert(p.Schema)
		if err != nil {
			return t, fmt.Errorf("parameter %q: %w", p.Name, err)
		}
		if _, ok := schema["description"]; !ok && p.Description != "" {
			schema["description"] = p.Description
		}
		if err := add(snakeCase(p.Name), binding{in: loc, name: p.Name}, schema, p.Required || loc == inPath); err != nil {
			return t, err
		}
	}

	if rb := op.RequestBody; rb != nil && rb.Value != nil {
		media := rb.Value.Content.Get("application/json")
		if media == nil || media.Schema == nil {
			return t, errors.New("request body: only application/json with a schema is supported")
		}
		schema, err := conv.convert(media.Schema)
		if err != nil {
			return t, fmt.Errorf("request body: %w", err)
		}
		if bodyProps, ok := flattenable(schema); ok {
			bodyRequired := map[string]bool{}
			if rb.Value.Required {
				for _, r := range stringList(schema["required"]) {
					bodyRequired[r] = true
				}
			}
			for _, name := range sortedKeys(bodyProps) {
				ps, _ := bodyProps[name].(map[string]any)
				if ps == nil {
					// A boolean property schema carries no type for the agent;
					// keep it as an unconstrained argument.
					ps = map[string]any{}
				}
				if err := add(name, binding{in: inBody, name: name}, ps, bodyRequired[name]); err != nil {
					return t, err
				}
			}
		} else {
			if rb.Value.Description != "" {
				if _, ok := schema["description"]; !ok {
					schema["description"] = rb.Value.Description
				}
			}
			if err := add(bodyArg, binding{in: inBody}, schema, rb.Value.Required); err != nil {
				return t, err
			}
			t.bodyNested = true
		}
	}

	t.InputSchema = map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		sort.Strings(required)
		t.InputSchema["required"] = required
	}
	return t, nil
}

// describeResponse records the first success response for api_describe.
func describeResponse(conv *schemaConverter, o *Operation, op *openapi3.Operation) {
	if op.Responses == nil {
		return
	}
	responses := op.Responses.Map()
	for _, code := range sortedKeys(responses) {
		status, err := strconv.Atoi(code)
		if err != nil || status < 200 || status > 299 {
			continue
		}
		o.SuccessStatus = status
		ref := responses[code]
		if ref == nil || ref.Value == nil {
			return
		}
		for _, mt := range sortedKeys(ref.Value.Content) {
			media := ref.Value.Content[mt]
			if o.ResponseMediaType == "" || isJSON(mt) {
				o.ResponseMediaType = mt
			}
			if mt == "text/event-stream" {
				o.Streaming = true
			}
			if isJSON(mt) && media.Schema != nil && o.ResponseSchema == nil {
				// A schema that cannot be inlined, such as a recursive one, is
				// left out; the operation stays callable.
				if schema, err := conv.convert(media.Schema); err == nil {
					o.ResponseSchema = schema
				}
			}
		}
		return
	}
}

// access reads the operation's security requirements: whether it allows
// anonymous calls, and whether it accepts only a cookie session.
func access(doc *openapi3.T, op *openapi3.Operation) (anonymous, sessionOnly bool) {
	reqs := doc.Security
	if op.Security != nil {
		reqs = *op.Security
	}
	sessionOnly = len(reqs) > 0
	for _, req := range reqs {
		if len(req) == 0 {
			anonymous = true
			sessionOnly = false
			continue
		}
		for name := range req {
			if !isCookieScheme(doc, name) {
				sessionOnly = false
			}
		}
	}
	return anonymous, sessionOnly
}

func isCookieScheme(doc *openapi3.T, name string) bool {
	if doc.Components == nil {
		return false
	}
	ref, ok := doc.Components.SecuritySchemes[name]
	if !ok || ref == nil || ref.Value == nil {
		return false
	}
	return ref.Value.Type == "apiKey" && ref.Value.In == "cookie"
}

// flattenable reports whether a body schema is a plain object whose
// properties can become top-level tool arguments. Composed schemas (allOf,
// oneOf and so on) are passed through as a nested body argument instead.
func flattenable(schema map[string]any) (map[string]any, bool) {
	if schema["type"] != "object" {
		return nil, false
	}
	for _, k := range []string{"allOf", "anyOf", "oneOf", "not", "if", "patternProperties", "dependentSchemas", "unevaluatedProperties"} {
		if _, ok := schema[k]; ok {
			return nil, false
		}
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || len(props) == 0 {
		return nil, false
	}
	return props, true
}

// mergeParameters applies operation parameters over path-item parameters,
// keyed by name and location as OpenAPI specifies.
func mergeParameters(pathParams, opParams openapi3.Parameters) ([]*openapi3.Parameter, error) {
	type key struct{ in, name string }
	var order []key
	byKey := map[key]*openapi3.Parameter{}
	for _, list := range []openapi3.Parameters{pathParams, opParams} {
		for _, ref := range list {
			if ref == nil || ref.Value == nil {
				return nil, errors.New("unresolved parameter reference")
			}
			p := ref.Value
			k := key{p.In, p.Name}
			if p.In == openapi3.ParameterInHeader {
				k.name = http.CanonicalHeaderKey(p.Name)
			}
			if _, ok := byKey[k]; !ok {
				order = append(order, k)
			}
			byKey[k] = p
		}
	}
	out := make([]*openapi3.Parameter, 0, len(order))
	for _, k := range order {
		out = append(out, byKey[k])
	}
	return out, nil
}

// snakeCase turns parameter names such as runId or Idempotency-Key into
// run_id and idempotency_key, so that every tool argument uses one style.
func snakeCase(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '-' || r == ' ' || r == '.':
			b.WriteByte('_')
		case unicode.IsUpper(r):
			if i > 0 {
				prev := runes[i-1]
				if unicode.IsLower(prev) || unicode.IsDigit(prev) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func stringList(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
