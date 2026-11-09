// Package goserver holds the Go strict server generated from the S2 spike
// spec (api/spikes/s2/openapi.yaml) and tests that pin down how the
// generator treats OpenAPI 3.1 constructs. It is validation material for
// docs/zh-CN/spikes/s2-openapi-codegen.md, not part of the product.
package goserver

//go:generate go tool oapi-codegen -config oapi-codegen.yaml ../openapi.yaml
