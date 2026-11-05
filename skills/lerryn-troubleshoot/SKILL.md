---
name: lerryn-troubleshoot
description: 排查 Lerryn 任务运行失败、超时、中断或结果不对的原因，修复后测试并关联重跑。用户给出运行 ID 或控制台的运行链接、说任务失败了、问为什么没有跑或跑错了时使用。也用于处理 Lerryn API 和 MCP 工具返回的错误（code 和 next_actions）。
license: Apache-2.0
---

# Lerryn 运行排查

判断依据只有平台的结构化记录：运行的 `status`、`error.code`、`error.stage`、`exit_code`、`result`，以及 API 错误的 `code` 和 `next_actions`。`detail`、`message` 和日志文字只帮助理解，不作为分类依据。

## 流程

1. **读运行。** `run_get` 读取运行。记下 `task_id`、`version`、`test`、`status`、`error`、`stages`、`result`、`rerun_of`、`attempt`。运行还没结束时带 `wait`。
2. **按类别处理。** 以 `error.code` 为准，对照 [references/error-codes.md](references/error-codes.md) 的下一步。常见类别的做法见下一节。
3. **读日志。** `run_logs` 带 `tail: 200` 先看结尾；要从头读时省略 `tail`，用返回的 `next_cursor` 逐页续读，直到 `complete` 为 true。日志行带 `stage`，先看失败阶段（`error.stage`）的行。`dropped_lines` 大于 0 表示超过上限的行被丢弃了。
4. **看改了什么。** 失败是新版本引入的时候，用 `api_call` 调用 `diffTaskVersion`（`task_id`、`seq`，可选 `base`）比较版本，用 `getTaskFile` 读文件内容。
5. **修复。** 按 `lerryn-automation` Skill 提交新版本（带 `if_match`），用 `run_start` 带 `test: true` 测试新版本，确认 `result` 正确。`side_effects: true` 的任务，测试运行同样会真实发消息、写数据：先把修复方案告诉用户，用户同意后再测试。
6. **关联重跑。** 测试通过后，用 `run_rerun` 重跑原来失败的运行，新运行的 `rerun_of` 指向原运行，默认用最新版本和原参数。`side_effects: true` 的任务先和用户确认。
7. **激活。** 任务已激活、修复的是新版本时，用 `task_set_active` 激活新版本，否则触发器还在运行旧版本。
8. **汇报。** 告诉用户原因、改了什么、测试运行和重跑的结果，附上 `links.console`。

## 按类别处理

| `error.code` | 常见原因 | 做法 |
| --- | --- | --- |
| `script_error` | 代码异常、缺少模块、参数不对 | 看失败阶段的日志和退出码，修代码或参数 |
| `dependency_install_failed` | 锁文件与 `package.json` 或 `pyproject.toml` 不一致，包名或版本不存在，镜像源不可达 | 看准备阶段的安装日志，修正锁文件，两个文件一起提交。`package-lock.json` 可以手写条目：写 `version` 和 `resolved`，`integrity` 可以省略，`npm ci` 不校验没有写的哈希；不需要为了算哈希联网或运行本地命令 |
| `timeout` | 处理量变大、外部服务慢、死循环 | 看最后的日志停在哪个阶段；优化或调大 `limits.timeout` |
| `out_of_memory` | 一次读入太多数据 | 分批处理，或调大 `limits.memory` |
| `credential_missing`、`credential_unavailable` | 凭证没填、已撤销、已过期或未授权 | 转到 `lerryn-credentials` Skill |
| `runner_unavailable` | 没有满足 `runner_labels` 的在线 runner | 去掉不必要的标签，或请管理员处理 |
| `run_interrupted` | runner 重启或失联 | 不是代码问题。确认脚本可以重复执行后再重跑 |

其他状态：

- `completed` 但 `result.status` 为 `failed` 或 `partial`：进程正常退出，业务没有完成。看 `result.summary` 和日志。
- `completed` 而 `result` 为 null：任务没有上报结果，不能说业务成功了。
- `skipped`：看 `skip_reason`。`overlap` 是上一个运行没结束；`queue_full` 是队列满；`missed` 是服务停机错过；`inactive` 是触发时任务未激活。
- `cancelled`：看 `cancelled_by`，是谁取消的。

## API 和工具错误

工具返回 `isError` 时，内容是一个 problem：按 `code` 判断类别，按 `next_actions` 的顺序选下一步（`action` 是工具名或 operationId）。常见的几种：

- `validation_failed`：按 `errors` 里每一项的 `pointer` 或 `parameter` 修正后重试。
- `version_conflict`：别人改过了。重新 `task_get`，看 `current_version` 和差异，合并后再提交。不要拿旧内容覆盖。
- `precondition_required`：修改已有任务或触发器时缺少 `if_match`。
- `run_in_progress`：清单为 `concurrency: forbid`，上一个运行没结束。等它结束，或确认后取消。
- `not_found`：ID 不对，或没有权限。不要猜别的 ID。
- `internal_error`：稍后重试一次；持续出现时把 `request_id` 交给用户转给管理员。

## 重跑的规则

- 有副作用（清单 `side_effects: true`，或任务会发消息、写外部数据）的任务失败后，不自动重跑，也不自行发起测试运行。测试运行和正式运行执行同一份代码，换成"测试频道"之类的参数也可能真实发出。先告诉用户哪一步失败、哪些动作可能已经完成，等用户确认。
- 失败原因没有修复时，不要重跑碰运气。`run_interrupted`、`queue_full` 这类与代码无关的情况除外。
- 重跑用 `run_rerun`，不要用 `run_start` 代替，否则失去与原运行的关联。带 `idempotency_key`，网络出错后用同一个键重试不会重复运行。
