# 触发器

触发器不在清单里，用 `trigger_set` 单独维护，修改频率不产生新版本。触发器只在任务激活后产生运行，运行的是激活的版本。

## 设置

`trigger_set` 整体替换任务的全部触发器：没有列出的会被删除，同名同类型的保留 ID 和 webhook 令牌。

```json
{
  "task_id": "<任务 ID>",
  "dry_run": true,
  "triggers": [
    {"name": "weekday-morning", "type": "cron", "schedule": "0 8 * * 1-5", "timezone": "Asia/Shanghai"},
    {"name": "crm-push", "type": "webhook", "max_body_bytes": 1048576}
  ]
}
```

步骤：

1. 先读现有触发器：`api_call` 调用 `listTriggers`（`{"operation_id": "listTriggers", "arguments": {"task_id": "<id>"}}`），记下 `revision`。任务从没设置过触发器时不需要。
2. 带 `dry_run: true` 调用 `trigger_set`，检查每个 cron 触发器的 `schedule_text` 和 `next_fires`。
3. 把频率和接下来的触发时间复述给用户。`next_fires` 是 UTC，换算成用户的时区再说。
4. 去掉 `dry_run` 正式设置；已有触发器时带 `if_match`（读到的 `revision`）。收到 `version_conflict` 时重新读取，合并别人的修改后再提交。

## cron

- `schedule` 是五段表达式（分 时 日 月 周），也接受 `@daily`、`@hourly`。
- `timezone` 必填，IANA 时区名，例如 `Asia/Shanghai`、`Asia/Singapore`、`America/New_York`。夏令时按该时区的本地时间计算。用户只说“早上 8 点”时，问清是哪个时区。
- `catch_up`：服务停机错过计划时间后，`skip`（默认）不补跑，记一条已跳过；`latest` 只补跑最近一次。只关心最新一次的任务（例如每天的报表）用 `latest`。
- `enabled: false` 保留触发器但不触发。
- `params` 整体替换清单中的 `params`。

## webhook

- 响应中 `webhook.url` 是调用地址，用 POST，令牌放在 `Authorization: Bearer <令牌>` 中。
- 令牌只在建立触发器的那次响应里出现（`webhook.token`），之后为 null。立即交给需要调用它的人或系统，不要写进任务文件或参数。丢失时用 `rotateTriggerToken` 轮换。
- 请求体存为运行输入，上限由 `max_body_bytes` 决定（默认 1 MiB）。调用方可以带 `Idempotency-Key` 防止重复投递。

## 暂停与恢复

- 暂停任务：`task_set_active` 带 `active: false`。触发器保留，不再产生运行；已经开始的运行不受影响。
- 只停一个触发器：把它的 `enabled` 设为 false 后整体提交。
