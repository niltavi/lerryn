# 完整示例

每个示例是一次 `task_apply` 的完整参数（`project_id` 换成 `discover` 返回的 `principal.personal_project_id`）。这些示例在集成测试中真实提交和运行（`tests/agent-evals/examples_integration_test.go`），改动后要保证它们仍能跑通。

## Python：按阶段处理并上报结果

<!-- example: python-digest -->
```json
{
  "project_id": "<personal_project_id>",
  "task_name": "example-python-digest",
  "manifest": {
    "description": "统计给定文本中的高频词",
    "runtime": {"language": "python", "version": "3.12"},
    "entrypoint": ["python", "main.py"],
    "params": {"text": "Lerryn 任务 Lerryn 运行 Lerryn 任务", "top": 2},
    "stages": [
      {"id": "count", "name": "统计"},
      {"id": "report", "name": "生成结果", "after": ["count"]}
    ],
    "limits": {"timeout": "2m", "memory": "256Mi"},
    "network": "none",
    "panels": [{"id": "top-words", "type": "bars", "title": "高频词", "source": "/top"}]
  },
  "files": [
    {
      "path": "main.py",
      "content": "from collections import Counter\n\nimport sbx\n\nparams = sbx.params()\n\nwith sbx.stage(\"count\"):\n    words = params[\"text\"].split()\n    counts = Counter(words)\n    print(f\"{len(words)} words, {len(counts)} distinct\")\n\nwith sbx.stage(\"report\"):\n    top = [{\"label\": w, \"value\": n} for w, n in counts.most_common(params[\"top\"])]\n\nsbx.result(\"ok\", f\"共 {len(words)} 个词\", {\"words\": len(words), \"top\": top})\n"
    }
  ]
}
```

## Node：TypeScript 和 npm 依赖

依赖要同时提交 `package.json` 和 `package-lock.json`，清单里声明 `lockfile`。`@lerryn/sbx` 不写进 `package.json`。手写锁文件时，每个包的 `integrity` 可以省略，`npm ci` 只校验写了的哈希。

<!-- example: node-deps -->
```json
{
  "project_id": "<personal_project_id>",
  "task_name": "example-node-deps",
  "manifest": {
    "description": "检查输入里哪些值是数字",
    "runtime": {"language": "node", "version": "22", "lockfile": "package-lock.json"},
    "entrypoint": ["node", "main.ts"],
    "params": {"values": ["1", "x", "2.5", ""]},
    "stages": [{"id": "check", "name": "检查"}],
    "limits": {"timeout": "5m"}
  },
  "files": [
    {
      "path": "main.ts",
      "content": "import isNumber from \"is-number\";\nimport * as sbx from \"@lerryn/sbx\";\n\ninterface Params { values: string[] }\nconst { values } = sbx.params<Params>();\n\nconst numbers = await sbx.stage(\"check\", () => values.filter((v) => isNumber(v)));\nconsole.log(`numbers: ${numbers.join(\", \")}`);\nsbx.result(\"ok\", `${numbers.length} / ${values.length} 个是数字`, { numbers: numbers.length, total: values.length });\n"
    },
    {
      "path": "package.json",
      "content": "{\n  \"name\": \"example-node-deps\",\n  \"version\": \"1.0.0\",\n  \"private\": true,\n  \"type\": \"module\",\n  \"dependencies\": {\n    \"is-number\": \"7.0.0\"\n  }\n}\n"
    },
    {
      "path": "package-lock.json",
      "content": "{\n  \"name\": \"example-node-deps\",\n  \"version\": \"1.0.0\",\n  \"lockfileVersion\": 3,\n  \"requires\": true,\n  \"packages\": {\n    \"\": {\n      \"name\": \"example-node-deps\",\n      \"version\": \"1.0.0\",\n      \"dependencies\": {\n        \"is-number\": \"7.0.0\"\n      }\n    },\n    \"node_modules/is-number\": {\n      \"version\": \"7.0.0\",\n      \"resolved\": \"https://registry.npmjs.org/is-number/-/is-number-7.0.0.tgz\",\n      \"integrity\": \"sha512-41Cifkg6e8TylSpdtTpeLVMqvSBEVzTttHvERD741+pnZ8ANv0004MRL43QKPDlK9cGvNp6NZWZUBlbGXYxxng==\",\n      \"license\": \"MIT\"\n    }\n  }\n}\n"
    }
  ]
}
```

## Shell：命令行工具

<!-- example: shell-report -->
```json
{
  "project_id": "<personal_project_id>",
  "task_name": "example-shell-report",
  "manifest": {
    "description": "统计任务文件的行数",
    "runtime": {"language": "shell", "version": "5"},
    "entrypoint": ["bash", "main.sh"],
    "stages": [{"id": "count", "name": "统计"}],
    "limits": {"timeout": "1m", "memory": "128Mi"},
    "network": "none"
  },
  "files": [
    {
      "path": "main.sh",
      "content": "#!/usr/bin/env bash\nset -euo pipefail\nsbx stage start count\nlines=$(wc -l < /sbx/task/data.txt | tr -d ' ')\necho \"data.txt has ${lines} lines\"\nsbx stage done count --message \"${lines} 行\"\nsbx result --status ok --summary \"data.txt 共 ${lines} 行\" --data-json \"{\\\"lines\\\": ${lines}}\"\n",
      "executable": true
    },
    {"path": "data.txt", "content": "a\nb\nc\n"}
  ]
}
```

## 提交之后

```text
task_apply(dry_run: true) → 看 warnings 和 diff
task_apply                → 记下 task.id 和 version
run_start(task_id, version, test: true, idempotency_key)
run_get(run_id, wait: 60) → status、result、error
run_logs(run_id, tail: 100)
trigger_set(dry_run: true) → 复述 schedule_text 和 next_fires
trigger_set
task_set_active(active: true, version)
```
