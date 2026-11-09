package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/niltavi/lerryn/api"
	"github.com/niltavi/lerryn/server/internal/mcp"
)

func TestTaskTagsThroughMCPAndHTTP(t *testing.T) {
	c := newTaskClient(t)
	alice, project := c.agent("alice")
	bob, otherProject := c.agent("bob")
	submit := func(token string, project uuid.UUID, name string) Task {
		rec := c.as(token, http.MethodPut, "/api/v1/projects/"+project.String()+"/tasks/"+name, digestRequest())
		c.want(rec, http.StatusOK, "")
		task := appliedTask(t, rec)
		if task.Tags == nil || len(task.Tags) != 0 {
			t.Fatalf("new task tags = %#v, want []", task.Tags)
		}
		return task
	}
	digest := submit(alice, project, "digest")
	other := submit(alice, project, "summary")
	private := submit(bob, otherProject, "private")
	digestPath := "/api/v1/tasks/" + digest.ID.String()

	reg, err := mcp.Load(context.Background(), api.Spec)
	if err != nil {
		t.Fatal(err)
	}
	agent := mcp.NewHandler(reg, c.h, mcp.Options{JSONResponse: true})
	call := func(tool string, args map[string]any) json.RawMessage {
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": tool, "arguments": args},
		})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
		req.Header.Set("Authorization", alice)
		rec := httptest.NewRecorder()
		agent.ServeHTTP(rec, req)
		c.want(rec, http.StatusOK, "")
		reply := decode[struct {
			Result struct {
				Content json.RawMessage `json:"structuredContent"`
				IsError bool            `json:"isError"`
			} `json:"result"`
		}](t, rec)
		if reply.Result.IsError || len(reply.Result.Content) == 0 {
			t.Fatalf("%s: %s", tool, rec.Body.String())
		}
		return reply.Result.Content
	}
	const tag = "销售 日报 & 复盘"
	call("task_set_tags", map[string]any{"task_id": digest.ID, "tags": []string{tag, "CRM"}})
	for _, task := range []Task{other, private} {
		token := alice
		if task.ID == private.ID {
			token = bob
		}
		rec := c.as(token, http.MethodPut, "/api/v1/tasks/"+task.ID.String()+"/tags", TaskTagSet{Tags: []string{tag}})
		c.want(rec, http.StatusOK, "")
	}
	submit(alice, project, "untagged")
	rec := c.as(alice, http.MethodGet, digestPath, nil)
	c.want(rec, http.StatusOK, "")
	if got := decode[Task](t, rec); !slices.Equal(got.Tags, []string{"CRM", tag}) || got.LatestVersion.Seq != digest.LatestVersion.Seq {
		t.Fatalf("task after setting tags: tags = %v, version = %d", got.Tags, got.LatestVersion.Seq)
	}

	var first TaskSummaryPage
	if err := json.Unmarshal(call("task_list", map[string]any{"tag": tag, "limit": 1}), &first); err != nil {
		t.Fatal(err)
	}
	if first.NextCursor == nil || len(first.Items) != 1 || first.Items[0].ID != other.ID || !slices.Equal(first.Items[0].Tags, []string{tag}) {
		t.Fatalf("first page = %+v", first)
	}
	rec = c.as(alice, http.MethodGet, "/api/v1/tasks?limit=1&tag="+url.QueryEscape(tag)+"&cursor="+url.QueryEscape(*first.NextCursor), nil)
	c.want(rec, http.StatusOK, "")
	if last := decode[TaskSummaryPage](t, rec); last.NextCursor != nil || len(last.Items) != 1 || last.Items[0].ID != digest.ID {
		t.Fatalf("last page = %+v", last)
	}

	rec = c.as(alice, http.MethodPut, digestPath+"/tags", TaskTagSet{Tags: []string{}})
	c.want(rec, http.StatusOK, "")
	if got := decode[TaskTagSet](t, rec).Tags; got == nil || len(got) != 0 {
		t.Fatalf("cleared tags = %#v, want []", got)
	}
	rec = c.as(alice, http.MethodGet, "/api/v1/tasks?name=digest&tag=CRM", nil)
	c.want(rec, http.StatusOK, "")
	if got := decode[TaskSummaryPage](t, rec); len(got.Items) != 0 {
		t.Fatalf("cleared tag still matches: %+v", got)
	}
}
