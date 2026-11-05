<!-- SPDX-License-Identifier: Apache-2.0 -->

# sbx 命令的 sh 实现

Node 和 Shell 运行时的镜像里没有 Python，`sbx` 命令在这两个运行时中由这里的 POSIX sh 脚本实现。runner 把它挂载为 `/sbx/sdk/bin/sbx`，已在 `PATH` 中。

参数、退出码和写出的事件格式与 Python 实现相同，见 `runtimes/sdk/python/README.md`：

```bash
sbx stage start fetch
sbx stage done fetch --message "获取 120 条记录"
sbx result --status ok --summary "生成 3 份摘要" --data-file result.json
```

- 只依赖 `/bin/sh`、awk，以及 coreutils 或 busybox 里的 `od`、`tr`、`date`、`dd`。`--data-json` 和 `--data-file` 的 JSON 由 awk 校验并压缩成一行，不合法时退出码为 1。数据文件最多 4 MiB，压缩后不超过 64 KiB。
- 每个事件用一次 `dd` 写入，多个进程同时写也不会交错。`dd` 不支持所需参数时退回到 shell 重定向，较长的行可能分成多次写入。
- `date` 不支持 `%N` 时（busybox），事件不带 `ts`，runner 以读到事件的时间为准。
- 上报阶段时，先向 stdout 写一行阶段标记，再写入事件。在脚本里用 `$(sbx ...)` 捕获它的输出，会把标记也捕获进去。
- `sbx mcp call` 的参数和输出与 Python 实现相同。有 `node` 时（Node 运行时）交给 Node 辅助库的 `cli.js` 执行；否则用 `curl`，再没有就用 `wget`（Shell 运行时的 busybox）。JSON-RPC 的应答，包括 SSE 形式的，由 awk 解析。它的 stdout 只有结果这一行，可以用 `$(...)` 捕获。

## 跨运行状态

游标等小文件可以直接写 `/sbx/state`：

```sh
cursor=/sbx/state/counter.txt
value=0
if [ -f "$cursor" ]; then value=$(cat "$cursor"); fi
printf '%s\n' "$((value + 1))" > "$cursor"
```

同一 TaskID 的版本与 runner 共享，测试运行也会提交。进程退出码 0 时提交完整快照；失败、取消、超时保留旧状态，正常空目录会清空。`sbx result --status failed` 本身不改变退出码。上限为 10 MiB、1000 项、路径 255 个 UTF-8 字节，不接受链接或特殊文件；并发以最后成功提交覆盖，不合并目录。

## 容器入口 sbx-exec

`sbx-exec` 是三种运行时共用的容器入口，挂载为 `/sbx/sdk/bin/sbx-exec`。runner 以 `sbx-exec -- <清单的 entrypoint>` 启动容器：它从 stdin 读取本次运行的凭证并安装，然后 exec 入口命令，进程、信号和退出码都是入口命令自己的。

- 清单中 `as: env` 的凭证，每个字段成为同名环境变量；`as: file` 的凭证写入 `/run/secrets/<引用名>/<字段名>`，目录只有任务用户可读。
- 入口命令启动时 stdin 已经结束。
- 输入格式不对或不完整时以 125 退出，不执行入口命令；入口命令不存在时以 127 退出。
- 凭证的值不会出现在日志里：runner 在上传之前把它们和常见的编码形式替换为 `[REDACTED]`。脚本把值写进产物文件，平台不做处理。
