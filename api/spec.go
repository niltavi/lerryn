// Package api embeds the bundled OpenAPI contract so the server can build
// its MCP tools from it at startup. The sources live in openapi/; run
// `make generate` after changing them.
package api

import _ "embed"

// Spec is bundled/openapi.yaml, the single-file contract.
//
//go:embed bundled/openapi.yaml
var Spec []byte
