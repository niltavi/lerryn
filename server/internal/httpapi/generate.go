package httpapi

// The input is the bundle that `make generate` writes with Redocly before
// running go generate; see api/README.md.
//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../../../api/bundled/openapi.yaml
