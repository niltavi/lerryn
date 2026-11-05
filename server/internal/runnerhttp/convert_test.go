package runnerhttp

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/niltavi/lerryn/server/internal/runproto"
)

// Tools, script tool files included, survive the trip through the wire
// format.
func TestToolsRoundTrip(t *testing.T) {
	in := []runproto.Tool{
		{Name: "crm", Kind: runproto.ToolMCP, URL: "https://crm.example.com/mcp"},
		{Name: "lib", Kind: runproto.ToolScript, TreeHash: "abc", Files: []runproto.File{
			{Path: "bin/run", Content: []byte("#!/bin/sh\n\x00binary"), Executable: true},
			{Path: "lib.py", Content: []byte("x = 1\n")},
		}},
	}
	b, err := json.Marshal(convertTools(in, toWireTool))
	if err != nil {
		t.Fatal(err)
	}
	var wire []tool
	if err := json.Unmarshal(b, &wire); err != nil {
		t.Fatal(err)
	}
	if out := convertTools(wire, fromWireTool); !reflect.DeepEqual(out, in) {
		t.Fatalf("round trip = %+v\nwant %+v", out, in)
	}
}
