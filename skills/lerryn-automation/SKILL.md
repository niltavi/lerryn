---
name: lerryn-automation
description: 在 Lerryn 上建立、修改、测试和激活自动化任务。用户要把一段脚本做成定时或 webhook 触发的任务、修改已有任务的代码或频率、给任务加阶段和结果上报时使用。通过 lerryn MCP 工具（discover、task_apply、run_start、trigger_set、task_set_active 等）或 lerryn CLI 操作。
license: Apache-2.0
---

# Lerryn 自动化任务

Lerryn 是企业自部署的自动化沙箱。你提交任务清单和文件，平台生成不可变的版本，在加固的容器里运行；触发器只运行激活的版本。

## 主流程

1. **发现能力。** 调用 `discover`。记下 `principal.personal_project_id`（建任务用的 `project_id`）、`runtimes`（运行时只能从这里选）、`capabilities.network_policies` 和 `capabilities.require_test_before_activate`。`skills.version` 与本 Skill 目录下 `.lerryn-skill` 记录的版本不同时，提醒用户用同版本的 `lerryn skills install` 更新。
2. **写脚本和清单。** 按 [references/manifest.md](references/manifest.md) 写清单，按 [references/sbx.md](references/sbx.md) 用 `sbx` 上报阶段和结果。三种运行时的完整示例在 [references/examples.md](references/examples.md)。字段的完整定义在 [references/manifest-fields.md](references/manifest-fields.md)。
3. **提交。** 先 `task_apply` 带 `dry_run: true` 看校验结果和 `diff`，再正式提交。修改已有任务时先 `task_get`，把 `latest_version.seq` 作为 `if_match`。
4. **凭证。** 清单引用了凭证而还没有绑定时（`warnings` 或 `task_validate` 里出现 `credential_missing`），用 `credential_request` 发起填写请求，把返回的 `url` 原样交给用户。交出链接后不要就此结束：在同一轮里用 `api_call`（参数放在 `arguments` 里：`{"operation_id": "getCredentialRequest", "arguments": {"request_id": "<id>"}}`） 调用一次 `getCredentialRequest`看 `status`，`fulfilled` 就直接继续第 5 步，`pending` 才结束这一轮，请用户填好后说一声。字段写法和更新、撤销见 `lerryn-credentials` Skill。
5. **测试。** `run_start` 带 `test: true`，用 `run_get`（带 `wait`，最多 60 秒）等结束，用 `run_logs` 读日志。逐项核对 `result` 是否符合用户的要求。失败时转到 `lerryn-troubleshoot` Skill。
6. **触发器。** `trigger_set` 先带 `dry_run: true`，把返回的 `schedule_text` 和 `next_fires`（换算成用户的时区）复述给用户确认，再正式设置。细节见 [references/triggers.md](references/triggers.md)。
7. **激活。** `task_set_active` 带 `active: true` 和测试通过的 `version`。把任务和测试运行的 `links.console` 交给用户。

用 CLI 时命令与工具一一对应，对照表见 [references/tools.md](references/tools.md)。

## 硬规则

1. 先调用 `discover` 再操作，不凭记忆猜接口、字段和运行时。不认识的参数用 `api_describe` 查。
2. 只有测试运行成功、并且检查过 `result` 符合用户要求之后，才激活版本。
3. 凭证只通过 `credential_request` 获取。不在对话中让用户粘贴，不写进代码、文件、`params`、清单或日志。
4. cron 触发器必须带 `timezone`（IANA 时区名）。用户没说时区时问清楚，不默认 UTC。
5. 运行没有上报 `result` 时，不向用户宣称业务成功。`completed` 只表示进程以 0 退出。
6. `side_effects: true` 的任务失败后，不自动重跑，先向用户说明已经发生了什么，等用户确认。测试运行也真实执行代码，换了参数照样会发消息、写数据，所以这类任务的测试运行同样要先征得用户同意。
7. 收到 `version_conflict` 时，先读最新版本和差异，把别人的修改合进来再提交；不要拿旧内容覆盖。
8. 每完成一步对用户可见的操作，把响应里的 `links.console` 交给用户。

## 常见坑

- `task_apply` 每次提交完整的文件列表，没有列出的文件不会保留到新版本。修改时先取回现有文件（`api_call` 调用 `getTaskFile`）。
- 提交新版本不会改变激活的版本。改完代码要重新测试、再激活新版本，触发器才会用它。
- 把任务复制到另一个项目或另一个部署：`api_call` 导出（`{"operation_id": "exportTask", "arguments": {"task_id": "<id>", "history": true}}`），再在目标处导入（`{"operation_id": "importTask", "arguments": {"project_id": "<目标项目>", "bundle": <导出的结果>}}`）。任务包较大时 MCP 结果会被截断，这时改用 CLI 的 `lerryn tasks export` 和 `lerryn tasks import`。导入的任务未激活，版本没有测试过，凭证都没有绑定，按第 4、5、7 步重新走一遍；webhook 令牌是新的，交给调用方。
- 新建任务默认允许手动执行，维护者和所有者可以在控制台点“立即重新触发”，用默认参数运行一次。修改已有任务时沿用原设置。只有任务明确要求禁止从控制台手动触发时，才调用 `api_call`，传入 `{"operation_id": "setTaskConsole", "arguments": {"task_id": "<id>", "manual_run": false}}`。关闭后，Agent 仍可用 `run_start`，触发器和控制台的“重试”仍可用。
- Shell 任务里不要用 `$(sbx ...)` 捕获 `sbx` 的输出：它会把阶段标记行一起捕获进来。直接调用 `sbx`。
- 容器的根文件系统只读，以 uid 65532 运行。临时文件写 `/sbx/work` 或 `/tmp`，产物写 `/sbx/out`，跨运行的游标等状态写 `/sbx/state`。状态按 TaskID 跨版本共享，测试运行同样读写；只有退出码 0 的完成运行提交完整状态，并发以最后成功提交覆盖，失败保留旧状态。状态上限为 10 MiB、1000 项、路径 255 个 UTF-8 字节，不放符号链接或特殊文件；需要更强的并发一致性时使用外部存储。
- 参数不放凭证。`params` 会出现在运行记录和控制台里。清单、文件、参数、触发器参数或工具里出现已登记凭证的值时，平台拒绝提交（422 `validation_failed`，`errors` 指出位置和凭证名）：改成在 `credentials` 中引用，并提醒用户更换已经出现在对话里的凭证。
