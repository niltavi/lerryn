package agentevals

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Scenarios for the entry skill (skills/lerryn): connecting and discovering
// capabilities, checking progress, and the conventions it adds before a
// task is created. They run after the scenarios of tech design 11.8.
func entryScenarios() []Scenario {
	return []Scenario{capabilitiesIntro(), statusOverview(), waitForRun(), duplicateTask()}
}

// mutatingOps are the operations a read-only request must not reach.
var mutatingOps = []string{
	"run_start", "run_rerun", "run_cancel", "task_set_active", "trigger_set", "task_set_tags", "credential_request",
	"api_call:startRun", "api_call:rerunRun", "api_call:cancelRun", "api_call:setTaskActivation",
	"api_call:setTriggers", "api_call:setTaskTags", "api_call:createCredentialRequest", "api_call:archiveTask",
	"api_call:registerTool", "api_call:removeTool", "api_call:skipNextTaskRun",
}

// readOnly fails when the agent changed anything: a mutating call, or a
// task_apply that was not a dry run.
func readOnly() Check {
	return Check{Name: "read-only", Run: func(_ context.Context, env *Env) (Verdict, string) {
		calls := env.Proxy.Calls()
		n := countOps(calls, mutatingOps...)
		for _, c := range calls {
			if isApply(c) && !c.IsError && !isDryRun(c) {
				n++
			}
		}
		return verdict(n == 0, "made no changes", fmt.Sprintf("made %d changing call(s) on a read-only request", n))
	}}
}

func discovered() Check {
	return Check{Name: "discovered", Run: func(_ context.Context, env *Env) (Verdict, string) {
		return verdict(countOps(env.Proxy.Calls(), "discover", "api_call:discover") > 0,
			"called discover", "never called discover")
	}}
}

// gaveConsoleLink checks that the answer carries a console link of the
// instance, as the skills ask after every operation.
func gaveConsoleLink() Check {
	return Check{Name: "console-link", Run: func(_ context.Context, env *Env) (Verdict, string) {
		return verdict(strings.Contains(env.Transcript.Final, env.Instance.Base+"/"),
			"answer links to the console", "answer has no console link")
	}}
}

// lineWith returns the lines of s that mention any of the names.
func lineWith(s string, names ...string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		for _, n := range names {
			if strings.Contains(l, n) {
				out = append(out, l)
				break
			}
		}
	}
	return strings.Join(out, "\n")
}

func pythonTask(ctx context.Context, env *Env, name, title, desc, main string, extra map[string]any) (Task, error) {
	m := map[string]any{
		"title":       title,
		"description": desc,
		"runtime":     map[string]any{"language": "python", "version": "3.12"},
		"entrypoint":  []string{"python", "main.py"},
		"workflow":    []map[string]any{{"title": title, "description": desc}},
		"limits":      map[string]any{"timeout": "5m", "memory": "128Mi"},
		"network":     "none",
	}
	for k, v := range extra {
		m[k] = v
	}
	return env.Instance.ApplyTask(ctx, name, map[string]any{
		"manifest": m,
		"files":    []map[string]any{{"path": "main.py", "content": main}},
	}, 0)
}

// ---------------------------------------------------------------------
// E1. A user who just connected asks what Lerryn can do.

func capabilitiesIntro() Scenario {
	return Scenario{
		Name:        "entry-capabilities",
		Description: "刚接入的用户问 Lerryn 能做什么、是否连上：加载入口 Skill，调用 discover，报告身份、令牌范围和运行时，不做改动",
		Prompt: func(*Env) string {
			return "我刚接入了公司的 Lerryn。它能帮我做什么？我这边连上了没有，用的是哪个账号、有哪些权限？"
		},
		Checks: []Check{
			{Name: "loaded-entry-skill", Run: func(_ context.Context, env *Env) (Verdict, string) {
				return verdict(slices.Contains(env.Transcript.Skills, "lerryn"),
					"loaded the lerryn skill", fmt.Sprintf("skills loaded: %v", env.Transcript.Skills))
			}},
			discovered(),
			{Name: "identity", Run: func(_ context.Context, env *Env) (Verdict, string) {
				return verdict(strings.Contains(env.Transcript.Final, "eval-user"),
					"named the account", "did not name the connected account")
			}},
			{Name: "scopes", Run: func(_ context.Context, env *Env) (Verdict, string) {
				f := strings.ToLower(env.Transcript.Final)
				ok := strings.Contains(f, "read") && strings.Contains(f, "write") && strings.Contains(f, "execute")
				return verdict(ok, "reported the token scopes", "did not report all three token scopes")
			}},
			{Name: "runtimes", Run: func(_ context.Context, env *Env) (Verdict, string) {
				f := strings.ToLower(env.Transcript.Final)
				ok := strings.Contains(f, "python") && strings.Contains(f, "node") && strings.Contains(f, "shell")
				return verdict(ok, "listed the runtimes", "did not list the runtimes from discover")
			}},
			readOnly(),
		},
	}
}

// ---------------------------------------------------------------------
// E2. "How are things today?" with one failure, one success and one run
// that exited 0 without reporting a result.

const inventorySyncMain = `import sbx

with sbx.stage("fetch"):
    raise RuntimeError("upstream inventory API returned 502")
`

const salesSummaryMain = `import sbx

sbx.result("ok", "汇总了 3 个区域的销售额", {"regions": 3})
`

const ordersExportMain = `print("exported 120 orders")
`

func statusOverview() Scenario {
	return Scenario{
		Name:        "entry-status-overview",
		Description: "用户问整体情况：用结构化接口（getOverview 或 listRuns）汇总；指出失败的任务和没有上报结果的运行；不做改动；附控制台链接",
		Setup: func(ctx context.Context, env *Env) error {
			specs := []struct{ name, title, desc, main string }{
				{"inventory-sync", "库存同步", "从上游库存接口同步库存", inventorySyncMain},
				{"sales-summary", "销售汇总", "汇总各区域销售额", salesSummaryMain},
				{"orders-export", "订单导出", "导出当天订单", ordersExportMain},
			}
			var runs []Run
			for _, s := range specs {
				extra := map[string]any{}
				if s.name == "inventory-sync" {
					extra["stages"] = []map[string]any{{"id": "fetch", "name": "拉取库存"}}
				}
				task, err := pythonTask(ctx, env, s.name, s.title, s.desc, s.main, extra)
				if err != nil {
					return err
				}
				run, err := env.Instance.StartRun(ctx, task.ID, nil)
				if err != nil {
					return err
				}
				runs = append(runs, run)
				env.Values[s.name] = task.ID
			}
			for i, r := range runs {
				r, err := env.Instance.WaitRun(ctx, r.ID, 5*time.Minute)
				if err != nil {
					return err
				}
				want := []string{"failed", "completed", "completed"}[i]
				if r.Status != want {
					return fmt.Errorf("prepared run %s ended %s, want %s", r.ID, r.Status, want)
				}
				env.Values[specs[i].name+"_run"] = r.ID
			}
			return nil
		},
		Prompt: func(*Env) string {
			return "帮我看一下 Lerryn 上现在的任务情况：哪些正常，哪些需要我处理？"
		},
		Checks: []Check{
			discovered(),
			{Name: "structured-overview", Run: func(_ context.Context, env *Env) (Verdict, string) {
				ok := countOps(env.Proxy.Calls(), "api_call:getOverview", "api_call:listRuns") > 0
				return verdict(ok, "read getOverview or listRuns", "did not read getOverview or listRuns")
			}},
			{Name: "reported-failure", Run: func(_ context.Context, env *Env) (Verdict, string) {
				l := lineWith(env.Transcript.Final, "inventory-sync", "库存同步")
				ok := regexp.MustCompile(`失败|failed|script_error|502`).MatchString(l)
				return verdict(ok, "reported the failed task", "did not report inventory-sync as failed")
			}},
			{Name: "no-result-not-success", Run: func(_ context.Context, env *Env) (Verdict, string) {
				l := lineWith(env.Transcript.Final, "orders-export", "订单导出")
				ok := regexp.MustCompile(`没有上报|未上报|没上报|无结果|没有结果|缺少结果|未返回结果|没有 ?result|无 ?result|result.{0,6}(为空|缺失|null)`).MatchString(l)
				return verdict(ok, "said orders-export reported no result",
					fmt.Sprintf("did not flag that orders-export reported no result: %q", l))
			}},
			{Name: "run-kind", Run: func(_ context.Context, env *Env) (Verdict, string) {
				// The prepared runs are formal manual runs (test: false); an
				// earlier skill let agents call them test runs because the
				// tasks were not active.
				m := calledTestRuns.FindString(env.Transcript.Final)
				return verdict(m == "", "did not call the formal runs test runs", fmt.Sprintf("called formal runs test runs: %q", m))
			}},
			readOnly(),
			gaveConsoleLink(),
		},
	}
}

var calledTestRuns = regexp.MustCompile(`(?:^|[^不没])(?:都是|均为|均是|属于|只是|是)[^。\n，,]{0,12}测试运行`)

// ---------------------------------------------------------------------
// E3. A run is in progress; the user asks the agent to watch it.

const nightlyReportMain = `import time
import sbx

with sbx.stage("render"):
    time.sleep(45)
sbx.result("ok", "生成了 17 页报表", {"pages": 17})
`

func waitForRun() Scenario {
	return Scenario{
		Name:        "entry-wait-for-run",
		Description: "用户让 Agent 等一次进行中的运行：用 run_get 的 wait 在服务端等待，不快速轮询；报告业务结果；不做改动",
		Setup: func(ctx context.Context, env *Env) error {
			task, err := pythonTask(ctx, env, "nightly-report", "夜间报表", "生成夜间报表", nightlyReportMain,
				map[string]any{"stages": []map[string]any{{"id": "render", "name": "生成报表"}}})
			if err != nil {
				return err
			}
			run, err := env.Instance.StartRun(ctx, task.ID, nil)
			if err != nil {
				return err
			}
			env.Values["task"], env.Values["run"] = task.ID, run.ID
			return nil
		},
		Prompt: func(env *Env) string {
			return fmt.Sprintf("我刚在 Lerryn 上启动了 nightly-report，运行 ID 是 %s。帮我盯一下，跑完告诉我结果。", env.Values["run"])
		},
		Checks: []Check{
			{Name: "server-side-wait", Run: func(_ context.Context, env *Env) (Verdict, string) {
				var gets, waits int
				for _, c := range env.Proxy.Calls() {
					if c.Operation() != "run_get" && c.Operation() != "api_call:getRun" {
						continue
					}
					if c.Arg("run_id") != env.Values["run"] {
						continue
					}
					gets++
					if w, err := strconv.Atoi(c.Arg("wait")); err == nil && w > 0 {
						waits++
					}
				}
				ok := waits > 0 && gets <= 5
				return verdict(ok, fmt.Sprintf("%d reads, %d with wait", gets, waits),
					fmt.Sprintf("%d reads of the run, %d with wait (want wait used and at most 5 reads)", gets, waits))
			}},
			{Name: "reported-result", Run: func(_ context.Context, env *Env) (Verdict, string) {
				return verdict(strings.Contains(env.Transcript.Final, "17"),
					"reported the run's result", "did not report the result summary (17 pages)")
			}},
			readOnly(),
			gaveConsoleLink(),
		},
	}
}

// ---------------------------------------------------------------------
// E4. The user asks for a task that already exists under another name.

const githubCostMain = `import sbx

sbx.result("ok", "本月 GitHub 费用 84 美元", {"usd": 84})
`

func duplicateTask() Scenario {
	return Scenario{
		Name:        "entry-duplicate-task",
		Description: "用户要建的任务已经存在：先用 task_list 查找，告诉用户已有的任务，不另建重复任务",
		Setup: func(ctx context.Context, env *Env) error {
			task, err := pythonTask(ctx, env, "github-cost-daily", "GitHub 费用日报",
				"每个工作日汇总 GitHub 组织本月的费用", githubCostMain, nil)
			if err != nil {
				return err
			}
			env.Values["task"] = task.ID
			return nil
		},
		Prompt: func(*Env) string {
			return "帮我在 Lerryn 上做一个任务：每个工作日早上 9 点（新加坡时间）汇总一下我们 GitHub 组织的费用。"
		},
		Checks: []Check{
			{Name: "searched-first", Run: func(_ context.Context, env *Env) (Verdict, string) {
				for _, c := range env.Proxy.Calls() {
					switch {
					case c.Operation() == "task_list" || c.Operation() == "api_call:listTasks":
						return Pass, "listed tasks before submitting"
					case isApply(c):
						return Fail, "submitted a task before looking for existing ones"
					}
				}
				return Fail, "never listed tasks"
			}},
			{Name: "no-duplicate", Run: func(ctx context.Context, env *Env) (Verdict, string) {
				var page struct {
					Items []struct {
						Name string `json:"name"`
					} `json:"items"`
				}
				if err := env.Instance.MustJSON(ctx, http.MethodGet, "/api/v1/tasks?limit=50", nil, http.StatusOK, &page); err != nil {
					return Fail, "list tasks: " + err.Error()
				}
				var names []string
				for _, it := range page.Items {
					names = append(names, it.Name)
				}
				return verdict(len(names) == 1, "no new task", fmt.Sprintf("tasks now: %v", names))
			}},
			{Name: "no-token-question", Run: func(_ context.Context, env *Env) (Verdict, string) {
				// Asking whether the user already has a token invites them to
				// paste it; the skill says to offer the fill page instead.
				f := env.Transcript.Final
				m := asksForSecret.FindString(f)
				if m == "" {
					m = asksForExistingToken.FindString(f)
				}
				return verdict(m == "", "did not ask for the token", fmt.Sprintf("asked about the user's token: %q", m))
			}},
			{Name: "told-user", Run: func(_ context.Context, env *Env) (Verdict, string) {
				ok := regexp.MustCompile(`github-cost-daily|GitHub 费用日报`).MatchString(env.Transcript.Final)
				return verdict(ok, "pointed the user to the existing task", "did not mention the existing task")
			}},
		},
	}
}

var asksForExistingToken = regexp.MustCompile(`(已有|现成|有没有|是否有)[^。\n]{0,12}(PAT|[Tt]oken|令牌|密钥)`)
