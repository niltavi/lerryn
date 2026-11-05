<!-- SPDX-License-Identifier: Apache-2.0 -->

# @lerryn/sbx：Node 任务的上报工具

Lerryn 在每个 Node 任务的容器里提供 `@lerryn/sbx` 模块和 `sbx` 命令，用来上报阶段和业务结果。模块只用 Node 内置模块，是 ES 模块，带类型声明，Node 20 及以上可用；运行时是 Node 22。

容器里的位置：

| 路径 | 内容 |
| --- | --- |
| `/sbx/node_modules/@lerryn/sbx/` | 模块。`/sbx/task` 下的文件直接 `import` 即可，不需要写进 `package.json` |
| `/sbx/sdk/bin/sbx` | 命令，已在 `PATH` 中，用法见 `runtimes/sdk/python/README.md` 的「命令行」一节 |

## 用法

TypeScript 文件可以直接用 `node main.ts` 运行，Node 22 会去掉类型注解。

```ts
import * as sbx from "@lerryn/sbx";

interface Params { source: string }
const params = sbx.params<Params>();   // LERRYN_PARAMS 解码后的对象

// 返回或 resolve 时报 done，抛出或 reject 时报 fail 并继续抛出
const records = await sbx.stage("fetch", () => fetchRecords(params.source));

sbx.stageStart("save");
try {
  await save(records);
  sbx.stageDone("save", `保存了 ${records.length} 条`);
} catch (err) {
  sbx.stageFail("save", String(err));
  throw err;
}

sbx.result("ok", `处理了 ${records.length} 条记录`, { count: records.length });
```

`require` 也可以使用：Node 22 支持 `require()` 加载 ES 模块。

- 阶段 ID 的规则、`status` 的取值和各项上限与 Python 辅助库相同：`message` 超过 1 KiB、`summary` 超过 2 KiB 时截断并以 `…` 结尾，`data` 编码后超过 64 KiB 时抛出 `RangeError`。
- `stage()` 里调用 `process.exit()` 会直接结束进程，不会报告 done 或 fail。
- 每次上报阶段时，模块先经 `process.stdout` 写一行阶段标记，再写入事件。标记与 `console.log` 的输出保持先后顺序，runner 据此逐行标注日志的阶段。
- 不在 Lerryn 中运行（没有 `LERRYN_EVENTS_FILE`）时，事件打印到 stderr。

## 调用 MCP 工具

```ts
const res = await sbx.mcpCall("crm-mcp", "search_accounts", { query: "acme" }, {
  headers: { Authorization: `Bearer ${process.env.CRM_TOKEN}` },
  timeout: 60, // 每次请求的秒数
});
```

行为与 Python 的 `sbx.mcp_call` 相同：工具来自清单的 `tools`，需要 `network: full`，每次调用记一条 `tool.call` 事件；失败或工具返回 `isError` 时以 `ToolError` reject。`sbx mcp call` 命令在 Node 运行时由这个模块的 `cli.js` 执行。

事件文件的格式与 Python 辅助库相同，见 `runtimes/sdk/python/README.md`。

## 跨运行状态

直接读写 `/sbx/state`，runner 在执行完成后提交完整快照：

```js
import { existsSync, readFileSync, writeFileSync } from "node:fs";

const cursor = "/sbx/state/counter.txt";
const before = existsSync(cursor) ? Number(readFileSync(cursor, "utf8")) : 0;
writeFileSync(cursor, String(before + 1));
```

同一 TaskID 的版本与 runner 共享，测试运行也会提交；只有进程退出码 0 才替换旧快照，`sbx.result("failed", ...)` 本身不会阻止提交。正常空目录会清空旧状态。上限为 10 MiB、1000 项、路径 255 个 UTF-8 字节，不接受链接或特殊文件；并发以最后成功提交覆盖，不合并目录。
