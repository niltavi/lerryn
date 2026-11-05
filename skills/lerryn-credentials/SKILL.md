---
name: lerryn-credentials
description: 为 Lerryn 任务获取、更新和撤销凭证（API Key、Token、Cookie、账号密码）。任务需要访问外部服务的密钥、运行报 credential_missing 或 credential_unavailable、用户要更换或停用某个密钥时使用。凭证只经平台的填写页提交，Agent 拿不到明文。
license: Apache-2.0
---

# Lerryn 凭证

明文只由用户本人在 Lerryn 的填写页里提交，平台加密保存，运行时注入容器。你只处理引用名、填写请求和状态，永远接触不到值。

## 硬规则

1. 不在对话中让用户粘贴、发送或确认凭证的值，也不接受用户主动贴出的值。用户贴了，提醒他到填写页提交，并建议更换已经暴露的凭证。
2. 不把凭证写进任务文件、`params`、清单、触发器参数或日志，也不让任务打印它。
3. 填写链接原样交给用户，不改写、不缩短，不替用户打开或提交。
4. 只用平台给出的状态判断是否填好，不凭用户的口头确认就当作已完成。

## 新任务需要凭证

1. 清单的 `credentials` 写引用名，例如 `{"name": "LLM_API_KEY", "as": "env", "description": "调用模型"}`。引用名为大写字母、数字和下划线。
2. 提交后看 `task_apply` 的 `warnings`，或调用 `task_validate`：出现 `credential_missing` 就需要填写。
3. 调用 `credential_request`：
   - `task_id`：任务 ID。
   - `name`：清单里的引用名。
   - `purpose`：一句话说明用途，显示在填写页上，例如“工作日摘要任务调用模型”。
   - `fields`：需要填写的字段。`name` 同时是注入时的环境变量名或文件名；API Key 通常只有一个字段，名字与引用名相同。非机密的字段（例如区域）写 `secret: false`，证书、Cookie 写 `multiline: true`。
   - 建议带 `idempotency_key`，重试不会产生重复的请求。
4. 把返回的 `url` 交给用户，说明用途和有效期（`expires_at`），请他在浏览器中打开填写。手机浏览器也可以。
5. 交出链接后，在同一轮里先用 `api_call`（参数放在 `arguments` 里：`{"operation_id": "getCredentialRequest", "arguments": {"request_id": "<id>"}}`） 调用一次 `getCredentialRequest`查看 `status`：用户可能已经在另一个窗口填好了。`fulfilled` 就继续测试运行、设置触发器和激活，不必等用户回话；`pending` 时结束这一轮，告诉用户填好后说一声，届时再查；`expired` 或 `cancelled` 时重新发起。不要连续轮询。
6. 填写后平台已按引用名把凭证授权给任务，不需要再调用授权接口。

## 运行报凭证错误

| `code` | 含义 | 做法 |
| --- | --- | --- |
| `credential_missing` | 引用名没有绑定凭证，或还没填写 | 按上一节发起填写请求 |
| `credential_unavailable` | 绑定的凭证已撤销、已过期，或没有授权给这个任务 | 用 `api_call` 调用 `listTaskCredentials` 看每个引用名的 `status`，再按下一节更新，或请凭证所有者授权 |

准备阶段的凭证错误发生在执行业务代码之前，任务本身没有运行，可以放心重试。

## 更新、授权与撤销

- **更换值：** `credential_request` 带 `credential_id`（来自 `listTaskCredentials` 的 `credential.id`）、`purpose` 和 `fields`，用户在填写页提交新值。新运行使用新值，任务不产生新版本。只能更新自己的凭证；别人的凭证请所有者处理。
- **授权给另一个任务：** 凭证所有者用 `api_call` 调用 `grantCredential`（`credential_id`、`task_id`、`name`）。同一任务的同一引用名已绑定时会被替换。
- **取消授权：** `revokeGrant`。之后这个任务的新运行拿不到它。
- **撤销：** `revokeCredential`。之后所有新运行在准备阶段以 `credential_unavailable` 失败；已经开始的运行里已注入的值收不回来，需要时取消这些运行。撤销前向用户确认影响哪些任务（凭证的 `grants`）。
- **查看：** `listCredentials`、`getCredential` 只返回元信息和授权，不返回值。

## 在任务里使用

- `as: env`：每个字段成为同名环境变量，例如 Python 的 `os.environ["LLM_API_KEY"]`。
- `as: file`：写入 `/run/secrets/<引用名>/<字段名>`，只有任务用户可读。
- 字段缺失时直接失败，并在错误里写字段名，不写值。
