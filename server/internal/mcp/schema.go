package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

const componentSchemaPrefix = "#/components/schemas/"

// schemaConverter turns OpenAPI 3.1 schemas into self-contained JSON Schema
// 2020-12 objects for tool input. Component references are inlined because
// several MCP clients do not resolve $ref or $defs in tool schemas.
type schemaConverter struct {
	doc   *openapi3.T
	cache map[string]any // component name -> raw JSON value
	// drop is the property marker that does not apply in this direction:
	// readOnly for request input, writeOnly for responses.
	drop string
}

func newSchemaConverter(doc *openapi3.T, drop string) *schemaConverter {
	return &schemaConverter{doc: doc, cache: map[string]any{}, drop: drop}
}

// convert returns the input schema for ref with references inlined,
// OpenAPI-only keywords removed and properties marked c.drop removed.
func (c *schemaConverter) convert(ref *openapi3.SchemaRef) (map[string]any, error) {
	raw, err := toJSONValue(ref)
	if err != nil {
		return nil, err
	}
	out, err := c.walk(raw, nil)
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		// A boolean schema; true accepts anything.
		return map[string]any{}, nil
	}
	return m, nil
}

// Keywords whose value is a schema, a map of schemas or a list of schemas.
// Everything else (enum, const, default, examples, ...) is data and is copied
// untouched, so a property that happens to be named "xml" is not stripped.
var (
	schemaKeywords = []string{
		"items", "additionalProperties", "not", "if", "then", "else", "contains",
		"propertyNames", "unevaluatedItems", "unevaluatedProperties", "contentSchema",
	}
	schemaMapKeywords  = []string{"properties", "patternProperties", "$defs", "dependentSchemas"}
	schemaListKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems"}
	// OpenAPI additions to JSON Schema that mean nothing to an MCP client.
	openAPIOnlyKeywords = []string{"discriminator", "xml", "externalDocs", "example"}
)

func (c *schemaConverter) walk(v any, stack []string) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return v, nil // boolean schema
	}
	if ref, ok := m["$ref"].(string); ok {
		name, ok := strings.CutPrefix(ref, componentSchemaPrefix)
		if !ok {
			return nil, fmt.Errorf("$ref %q: only %s references are supported", ref, componentSchemaPrefix)
		}
		if slices.Contains(stack, name) {
			return nil, fmt.Errorf("schema %q is recursive; recursive schemas cannot be inlined into tool input", name)
		}
		target, err := c.component(name)
		if err != nil {
			return nil, err
		}
		merged := map[string]any{}
		if tm, ok := target.(map[string]any); ok {
			for k, val := range tm {
				merged[k] = val
			}
		}
		// OpenAPI 3.1 allows keywords next to $ref; they refine the target,
		// typically with a more specific description.
		for k, val := range m {
			if k != "$ref" {
				merged[k] = val
			}
		}
		return c.walk(merged, append(stack, name))
	}

	out := make(map[string]any, len(m))
	for k, val := range m {
		if strings.HasPrefix(k, "x-") || slices.Contains(openAPIOnlyKeywords, k) {
			continue
		}
		var err error
		switch {
		case slices.Contains(schemaKeywords, k):
			out[k], err = c.walk(val, stack)
		case slices.Contains(schemaMapKeywords, k):
			out[k], err = c.walkMap(val, stack)
		case slices.Contains(schemaListKeywords, k):
			out[k], err = c.walkList(val, stack)
		default:
			out[k] = val
		}
		if err != nil {
			return nil, err
		}
	}
	dropMarked(out, c.drop)
	return out, nil
}

func (c *schemaConverter) walkMap(v any, stack []string) (any, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return v, nil
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		w, err := c.walk(val, stack)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = w
	}
	return out, nil
}

func (c *schemaConverter) walkList(v any, stack []string) (any, error) {
	items, ok := v.([]any)
	if !ok {
		return v, nil
	}
	out := make([]any, len(items))
	for i, it := range items {
		w, err := c.walk(it, stack)
		if err != nil {
			return nil, err
		}
		out[i] = w
	}
	return out, nil
}

// dropMarked removes properties carrying marker. readOnly properties only
// appear in responses, so offering them as tool input would invite the
// agent to set server-assigned fields such as id; writeOnly ones, such as a
// password, never appear in a response.
func dropMarked(schema map[string]any, marker string) {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return
	}
	var dropped []string
	for name, p := range props {
		if pm, ok := p.(map[string]any); ok && pm[marker] == true {
			delete(props, name)
			dropped = append(dropped, name)
		}
	}
	if len(dropped) == 0 {
		return
	}
	if req, ok := schema["required"].([]any); ok {
		kept := slices.DeleteFunc(slices.Clone(req), func(r any) bool {
			s, _ := r.(string)
			return slices.Contains(dropped, s)
		})
		if len(kept) == 0 {
			delete(schema, "required")
		} else {
			schema["required"] = kept
		}
	}
}

func (c *schemaConverter) component(name string) (any, error) {
	if v, ok := c.cache[name]; ok {
		return v, nil
	}
	ref, ok := c.doc.Components.Schemas[name]
	if !ok || ref == nil {
		return nil, fmt.Errorf("schema %q not found in components", name)
	}
	v, err := toJSONValue(ref)
	if err != nil {
		return nil, fmt.Errorf("schema %q: %w", name, err)
	}
	c.cache[name] = v
	return v, nil
}

// toJSONValue marshals a schema reference and decodes it into generic JSON
// values. Numbers stay json.Number so limits such as maximum round-trip
// exactly.
func toJSONValue(ref *openapi3.SchemaRef) (any, error) {
	data, err := json.Marshal(ref)
	if err != nil {
		return nil, fmt.Errorf("marshal schema: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("decode schema: %w", err)
	}
	return v, nil
}
