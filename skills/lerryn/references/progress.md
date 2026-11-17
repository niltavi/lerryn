# 检查任务和运行进度

判断依据只用平台的结构化字段：运行的 `status`、`stages`、`result`、`error`，任务的 `state`、`active_version`。日志文字只帮助理解。

## 运行状态

| `status` | 含义 | 下一步 |
| --- | --- | --- |
| `queued`、`preparing`、`running`、`cancelling` | 进行中 | `run_get` 带 `wait` 继续等 |
| `completed` | 进程以 0 退出 | 看 `result.status`：`ok` 成功，`partial` 部分完成，`failed` 业务失败；没有 `result` 时如实告诉用户“进程正常结束，但没有上报结果” |
| `failed`、`timed_out`、`interrupted` | 失败 | 看 `error.code` 和 `error.stage`，转到 `lerryn-troubleshoot` Skill |
| `cancelled` | 已取消 | 说明谁取消的（`cancelled_by`） |
| `skipped` | 没有执行 | 看 `skip_reason` |

## 等一次运行

1. `run_start` 返回运行 ID 后，调用 `run_get`，带 `run_id` 和 `wait`（最多 60 秒）。服务端在运行结束或等待到时才返回，不需要自己隔几秒查一次。
2. 还没结束时，可以再等一两次。每次告诉用户当前阶段：`stages` 里 `status` 为 `running` 的那一项。
3. 预计要跑几分钟以上的任务，不要一直等：把运行 ID 和 `links.console` 交给用户，说明之后可以再问。用户回来时直接 `run_get`。
4. 结束后：
   - 读 `result` 的 `status`、`summary` 和 `data`，对照用户的要求逐项核对。
   - 需要看过程时 `run_logs` 带 `tail: 200`；从头读用 `next_cursor` 逐页续读，直到 `complete` 为 true。
   - 有产物时 `api_call` 调用 `listArtifacts`（`run_id`）。下载用 `lerryn api downloadArtifact` 或控制台链接。

CLI 对应：`lerryn runs get <运行> --wait 30s`、`lerryn runs wait <运行> --timeout 10m`、`lerryn runs logs <运行> --tail 200`。

## 一个任务的近况

1. `task_get`：`state`（是否激活或暂停）、`active_version`（触发器运行的版本）、`latest_version`（最新提交的版本）。两者不同，说明新版本还没激活。
2. 最近的运行：

   ```json
   {"operation_id": "listRuns", "arguments": {"task_id": "<TASK_ID>", "limit": 10}}
   ```

   只看失败的加 `status`，只看正式运行加 `test: false`，按时间范围加 `since`、`until`。还有更多时用返回的 `next_cursor`。参数以 `api_describe` 的结果为准。
3. 下一次触发：`getOverview` 的 `upcoming`，或 `api_call` 调用 `listTriggers`。

## 整体情况

用户问“今天怎么样”“有什么要处理的”时，`api_call` 调用 `getOverview`（可带 `project_id`）：

| 字段 | 内容 |
| --- | --- |
| `counts` | 任务数、激活数、待处理数、运行中和排队中的数量 |
| `attention` | 需要处理的事：`run_failed`、`run_timed_out`、`run_interrupted`、`credential_request`（等用户填写）、`credential_expiring`、`runner_offline` |
| `active` | 进行中的运行及其 `current_stage` |
| `upcoming` | 已激活任务的下一次计划触发，带 `schedule_text` |

先报 `attention`，再报进行中和接下来的触发。每一项附上 `link` 或 `links.console`。

## 运行事件

需要知道运行过程中发生了什么（阶段切换、工具调用、结果上报）时，`api_call` 调用 `listRunEvents`（`run_id`，可按 `kind` 筛选），按 `seq` 顺序阅读。

## 汇报格式

一次运行：

> 「GitHub 费用日报」第 3 版的测试运行已完成，结果 `ok`：汇总了 12 个仓库，本月合计 $84。用时 41 秒。控制台：<links.console>

多个任务：先列需要处理的，再列正常的，每项一行，带链接。不要只说“已完成”；`completed` 不等于业务成功。
