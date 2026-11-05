package agentevals

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The harness pieces are tested without a model, Docker or a server
// build: the transcript parser on a recorded stream, the proxy against a
// fake MCP endpoint, the form filler against a fake fill page.

const claudeStream = `{"type":"system","subtype":"init","tools":["mcp__lerryn__run_get"]}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Skill","input":{"skill":"lerryn-troubleshoot"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"先看运行。"},{"type":"tool_use","id":"t2","name":"mcp__lerryn__run_get","input":{"run_id":"r1"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"{}"}]}}
not json
{"type":"assistant","message":{"content":[{"type":"text","text":"依赖锁文件缺少 left-pad。"}]}}
{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.4213,"num_turns":5,"duration_ms":61000,"result":"依赖锁文件缺少 left-pad。"}
`

func TestParseClaudeTranscript(t *testing.T) {
	tr := ParseClaudeTranscript([]byte(claudeStream))
	if len(tr.ToolUses) != 2 || tr.ToolUses[1].Name != "mcp__lerryn__run_get" {
		t.Errorf("tool uses = %+v", tr.ToolUses)
	}
	if len(tr.Skills) != 1 || tr.Skills[0] != "lerryn-troubleshoot" {
		t.Errorf("skills = %v", tr.Skills)
	}
	if len(tr.Texts) != 2 || tr.Final != "依赖锁文件缺少 left-pad。" {
		t.Errorf("texts = %q, final = %q", tr.Texts, tr.Final)
	}
	if tr.CostUSD != 0.4213 || tr.Turns != 5 || tr.Duration != 61*time.Second || tr.Outcome != "success" || tr.IsError {
		t.Errorf("result = %+v", tr)
	}
	// Cut off before the result event (killed, budget): the last text is
	// the final answer and there is no outcome.
	cut := ParseClaudeTranscript([]byte(claudeStream[:strings.Index(claudeStream, `{"type":"result"`)]))
	if cut.Outcome != "" || cut.Final != "依赖锁文件缺少 left-pad。" {
		t.Errorf("cut transcript = %+v", cut)
	}
}

// fakeMCP answers tools/call with an SSE stream, like the stateless
// endpoint: run_get succeeds, task_apply fails with version_conflict.
func fakeMCP(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "tools/call" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":{}}`, req.ID)
			return
		}
		result := `{"structuredContent":{"id":"run-1","status":"completed"},"content":[{"type":"text","text":"{\"id\":\"run-1\"}"}]}`
		if req.Params.Name == "task_apply" {
			result = `{"isError":true,"content":[{"type":"text","text":"{\"code\":\"version_conflict\",\"current_version\":2}"}]}`
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":%s}\n\n", req.ID, result)
	}))
}

func call(t *testing.T, url string, id int, name, args string) string {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, id, name, args)
	resp, err := http.Post(url, "application/json", strings.NewReader(body)) // #nosec G107 -- a test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return string(data)
}

func TestProxyRecordsCallsAndRunsHooks(t *testing.T) {
	backend := fakeMCP(t)
	defer backend.Close()
	var order []string
	p, err := NewProxy(backend.URL, Hook{
		Before: func(c ToolCall) { order = append(order, "before "+c.Name) },
		After:  func(c ToolCall) { order = append(order, fmt.Sprintf("after %s error=%v", c.Name, c.IsError)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()

	if got := call(t, p.URL, 1, "run_get", `{"run_id":"run-1","wait":10}`); !strings.Contains(got, `"run-1"`) {
		t.Fatalf("agent did not get the answer: %s", got)
	}
	call(t, p.URL, 2, "task_apply", `{"if_match":1,"dry_run":false}`)
	call(t, p.URL, 3, "api_call", `{"operation_id":"getTaskFile","arguments":{"task_id":"t1","seq":2}}`)
	// Other JSON-RPC methods pass through unrecorded.
	resp, err := http.Post(p.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","id":9,"method":"tools/list"}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	calls := p.Calls()
	if len(calls) != 3 {
		t.Fatalf("calls = %+v", calls)
	}
	if c := calls[0]; c.Seq != 1 || c.Arg("run_id") != "run-1" || c.Arg("wait") != "10" || c.IsError || c.ResultField("id") != "run-1" {
		t.Errorf("run_get call = %+v", c)
	}
	if c := calls[1]; !c.IsError || c.Code != "version_conflict" || c.Arg("if_match") != "1" || isDryRun(c) || !isApply(c) {
		t.Errorf("task_apply call = %+v", c)
	}
	if c := calls[2]; c.Operation() != "api_call:getTaskFile" || c.Arg("task_id") != "t1" || !readsTask(c, "t1") {
		t.Errorf("api_call = %+v (operation %s)", c, c.Operation())
	}
	want := []string{"before run_get", "after run_get error=false", "before task_apply", "after task_apply error=true", "before api_call", "after api_call error=false"}
	if strings.Join(order, "|") != strings.Join(want, "|") {
		t.Errorf("hooks = %q", order)
	}
	if ids := startedRunIDs(calls); len(ids) != 0 {
		t.Errorf("startedRunIDs = %v", ids)
	}
}

func TestCallHelpers(t *testing.T) {
	apply := func(args string, isErr bool) ToolCall {
		return ToolCall{Name: "task_apply", Arguments: json.RawMessage(args), IsError: isErr}
	}
	calls := []ToolCall{
		apply(`{"files":[{"path":"a","content":"1"}]}`, false),
		apply(`{"dry_run":true,"files":[{"path":"a","content":"dry"}]}`, false),
		apply(`{"files":[{"path":"a","content":"bad"}]}`, true),
		{Name: "run_start", Result: json.RawMessage(`{"id":"r9"}`)},
		{Name: "api_call", Arguments: json.RawMessage(`{"operation_id":"rerunRun","arguments":{"run_id":"r1"}}`), Result: json.RawMessage(`{"id":"r10"}`)},
	}
	last, ok := lastApply(calls)
	if !ok || appliedFiles(last)["a"] != "1" {
		t.Errorf("lastApply = %+v", last)
	}
	viaAPICall := ToolCall{Name: "api_call", Arguments: json.RawMessage(`{"operation_id":"applyTask","arguments":{"files":[{"path":"b","content":"2"}]}}`)}
	if !isApply(viaAPICall) || appliedFiles(viaAPICall)["b"] != "2" {
		t.Errorf("api_call applyTask not recognised")
	}
	if ids := startedRunIDs(calls); strings.Join(ids, ",") != "r9,r10" {
		t.Errorf("startedRunIDs = %v", ids)
	}
	if n := countOps(calls, "run_start", "api_call:rerunRun"); n != 2 {
		t.Errorf("countOps = %d", n)
	}
}

func TestPhraseMatchers(t *testing.T) {
	for _, s := range []string{"请把 DIGEST_API_KEY 粘贴到这里", "Please paste your API key here", "直接发给我密钥就行"} {
		if !asksForSecret.MatchString(s) {
			t.Errorf("asksForSecret missed %q", s)
		}
	}
	for _, s := range []string{"我已发起填写请求，请打开链接填写 DIGEST_API_KEY", "密钥由你在填写页提交，我看不到它的值",
		"任务结构不依赖密钥的具体值,摘要服务开放后如果想真正调用,告诉我再改代码即可。"} {
		if asksForSecret.MatchString(s) {
			t.Errorf("asksForSecret flagged %q", s)
		}
	}
	if !asksConfirmation.MatchString("要不要我重跑剩下的频道？") || asksConfirmation.MatchString("已经重跑完成。") {
		t.Error("asksConfirmation")
	}
}

const fillPage = `<!doctype html><html><body>
<form method="post" action="/c/req-1">
  <input type="hidden" name="csrf" value="tok&amp;1">
  <label>密钥 <input type="password" name="DIGEST_API_KEY"></label>
  <textarea name="NOTE"></textarea>
  <input type="checkbox" name="remember" value="1">
  <button type="submit">保存</button>
</form></body></html>`

func TestFillCredential(t *testing.T) {
	var got map[string][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("lerryn_session"); err != nil || c.Value != "s1" {
			http.Error(w, "no session", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, fillPage)
			return
		}
		_ = r.ParseForm()
		got = r.PostForm
		http.Redirect(w, r, "/c/req-1/done", http.StatusSeeOther)
	}))
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	in := &Instance{Base: srv.URL, Session: &http.Client{Jar: jar}}
	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	jar.SetCookies(req.URL, []*http.Cookie{{Name: "lerryn_session", Value: "s1", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode}})

	ctx := context.Background()
	if err := FillCredential(ctx, in, srv.URL+"/c/req-1", map[string]string{"DIGEST_API_KEY": "secret"}); err != nil {
		t.Fatal(err)
	}
	if got["csrf"][0] != "tok&1" || got["DIGEST_API_KEY"][0] != "secret" || got["NOTE"][0] != "" || got["remember"] != nil {
		t.Errorf("posted %v", got)
	}
	if err := FillCredential(ctx, in, srv.URL+"/c/req-1", map[string]string{"OTHER": "x"}); err == nil {
		t.Error("filling a field the page does not have succeeded")
	}
	if _, err := parseForm("<p>no form</p>"); err == nil {
		t.Error("parseForm accepted a page without a form")
	}
}

func TestSummarizeAndMarkdown(t *testing.T) {
	results := []RunResult{
		{Scenario: "a", Attempt: 1, Passed: true, CostUSD: 0.5, Checks: []CheckResult{{Name: "x", Verdict: Pass}, {Name: "y", Verdict: Skip}}},
		{Scenario: "a", Attempt: 2, Passed: false, CostUSD: 0.25, Checks: []CheckResult{{Name: "x", Verdict: Fail, Detail: "no | pipe"}}},
		{Scenario: "b", Skipped: "rerunRun: not implemented"},
		{Scenario: "c", Error: "setup: boom"},
	}
	sum := Summarize(results)
	if len(sum) != 3 {
		t.Fatalf("summaries = %+v", sum)
	}
	if a := sum[0]; a.Runs != 2 || a.Passed != 1 || a.PassRate != 0.5 || a.CostUSD != 0.75 || a.CheckPasses["x"] != 1 || a.CheckPasses["y"] != 0 {
		t.Errorf("a = %+v", a)
	}
	if b := sum[1]; b.Runs != 0 || b.Skipped == "" {
		t.Errorf("b = %+v", b)
	}
	if c := sum[2]; c.Runs != 1 || c.Errors != 1 || c.PassRate != 0 {
		t.Errorf("c = %+v", c)
	}
	md := Markdown("claude", "sonnet", time.Unix(0, 0), results)
	for _, want := range []string{"| a | 2 | 1 | 50% | 0.75 |", "| b | 0 | 0 | - | 0.00 | rerunRun: not implemented |", "Harness error: setup: boom", "Total cost: 0.75 USD."} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
}

func TestScenarioDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range Scenarios() {
		if s.Name == "" || seen[s.Name] || s.Description == "" || s.Prompt == nil || len(s.Checks) == 0 {
			t.Errorf("scenario %q is incomplete or duplicated", s.Name)
		}
		seen[s.Name] = true
		checks := map[string]bool{}
		for _, c := range s.Checks {
			if c.Name == "" || checks[c.Name] || c.Run == nil {
				t.Errorf("%s: check %q is incomplete or duplicated", s.Name, c.Name)
			}
			checks[c.Name] = true
		}
		env := &Env{Values: map[string]string{"task": "T", "failed_run": "R", "code": "script_error", "link": "http://x"}}
		if p := s.Prompt(env); strings.TrimSpace(p) == "" || strings.Contains(p, "%!") {
			t.Errorf("%s: bad prompt %q", s.Name, p)
		}
	}
	if len(seen) != 4 {
		t.Errorf("want the four scenarios of tech design 11.8, have %d", len(seen))
	}
}

func TestInstanceEnvDropsLerrynSettings(t *testing.T) {
	got := instanceEnv([]string{"HOME=/h", "LERRYN_CONFIG=/etc/w.yaml", "LERRYN_TOKEN=x", "DOCKER_HOST=unix:///d"})
	if strings.Join(got, " ") != "HOME=/h DOCKER_HOST=unix:///d" {
		t.Errorf("instanceEnv = %q", got)
	}
}

func TestResponsePending(t *testing.T) {
	stub := Response{Status: 500, Body: []byte(`{"code":"internal_error"}`)}
	real := Response{Status: 404, Body: []byte(`{"code":"not_found"}`)}
	if !stub.Pending() || real.Pending() || real.Code() != "not_found" {
		t.Error("Pending")
	}
	var buf bytes.Buffer
	if err := (Response{Body: []byte("x")}).Decode(&buf); err == nil {
		t.Error("Decode accepted a non-JSON body")
	}
}
