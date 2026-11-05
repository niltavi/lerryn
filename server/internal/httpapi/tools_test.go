package httpapi

import (
	"net/http"
	"testing"
)

// The tool operations as the contract describes them.
func TestToolEndpoints(t *testing.T) {
	c := newTaskClient(t)
	alice, project := c.agent("alice")
	bob, _ := c.agent("bob")
	path := "/api/v1/projects/" + project.String() + "/tools"
	body := map[string]any{"name": "crm-mcp", "kind": "mcp", "description": "CRM 查询", "url": "https://crm.example.com/mcp"}

	rec := c.as(alice, http.MethodPost, path, body)
	c.want(rec, http.StatusOK, "")
	tool := decode[Tool](t, rec)
	if tool.ID == nil || tool.Name != "crm-mcp" || tool.Kind != ToolKindMcp {
		t.Fatalf("tool = %+v", tool)
	}
	c.want(c.as(bob, http.MethodPost, path, body), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(alice, http.MethodPost, path, map[string]any{"name": "x", "kind": "ftp", "description": "d", "url": "https://a"}),
		http.StatusBadRequest, ErrorCodeValidationFailed)
	// A script tool takes files, not an address.
	c.want(c.as(alice, http.MethodPost, path, map[string]any{"name": "x", "kind": "script", "description": "d", "url": "https://a"}),
		http.StatusUnprocessableEntity, ErrorCodeValidationFailed)
	rec = c.as(alice, http.MethodPost, path, map[string]any{"name": "fmt-lib", "kind": "script", "description": "格式化",
		"files": []any{map[string]any{"path": "fmt.py", "content": "print(1)\n", "executable": true}}})
	c.want(rec, http.StatusOK, "")
	script := decode[Tool](t, rec)
	if script.URL != "" || script.Files == nil || len(*script.Files) != 1 || !(*script.Files)[0].Executable ||
		script.TreeHash == nil || len(*script.TreeHash) != 64 {
		t.Fatalf("script tool = %+v", script)
	}

	rec = c.as(alice, http.MethodGet, "/api/v1/tools?project_id="+project.String(), nil)
	c.want(rec, http.StatusOK, "")
	if list := decode[ToolList](t, rec); len(list.Items) != 2 {
		t.Fatalf("list = %+v", list)
	}
	if list := decode[ToolList](t, c.as(bob, http.MethodGet, "/api/v1/tools", nil)); len(list.Items) != 0 {
		t.Fatalf("bob's list = %+v", list)
	}
	c.want(c.as(bob, http.MethodDelete, "/api/v1/tools/"+tool.ID.String(), nil), http.StatusNotFound, ErrorCodeNotFound)
	c.want(c.as(alice, http.MethodDelete, "/api/v1/tools/"+tool.ID.String(), nil), http.StatusNoContent, "")
}
