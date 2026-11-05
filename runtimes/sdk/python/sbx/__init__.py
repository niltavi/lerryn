# SPDX-License-Identifier: Apache-2.0
"""Report stages and results from a Lerryn task.

Every call appends one JSON line to the file named by LERRYN_EVENTS_FILE.
The runner reads that file and forwards the events to the platform, so this
works without network access. The line format is described in README.md.

Only the standard library is used, so the module can be imported from any
task environment.
"""

from __future__ import annotations

import contextlib
import datetime
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request
from typing import Any, Dict, Iterator, Mapping, Optional

__all__ = [
    "DATA_MAX_BYTES",
    "MESSAGE_MAX_BYTES",
    "RESULT_STATUSES",
    "SUMMARY_MAX_BYTES",
    "ToolError",
    "mcp_call",
    "params",
    "result",
    "stage",
    "stage_done",
    "stage_fail",
    "stage_start",
]

#: Version of the event line format, written as "v" in every line.
FORMAT_VERSION = 1

TOOLS_ENV = "LERRYN_TOOLS"
EVENTS_FILE_ENV = "LERRYN_EVENTS_FILE"
PARAMS_ENV = "LERRYN_PARAMS"
LOG_MARKER_ENV = "LERRYN_LOG_MARKER"

#: Limits enforced by the runner. Longer messages and summaries are cut;
#: larger data is rejected here so the task notices instead of losing it.
MESSAGE_MAX_BYTES = 1024
SUMMARY_MAX_BYTES = 2048
DATA_MAX_BYTES = 64 * 1024

RESULT_STATUSES = ("ok", "partial", "failed")


# Same rule as stage IDs in the task manifest.
_STAGE_ID = re.compile(r"[a-z0-9][a-z0-9_-]{0,62}")


def params() -> Dict[str, Any]:
    """Return the run's parameters, decoded from LERRYN_PARAMS."""
    raw = os.environ.get(PARAMS_ENV, "")
    if not raw.strip():
        return {}
    value = json.loads(raw)
    if not isinstance(value, dict):
        raise ValueError(f"{PARAMS_ENV} is not a JSON object")
    return value


def stage_start(stage_id: str, message: Optional[str] = None) -> None:
    """Report that the stage started."""
    _stage("stage.start", stage_id, message)


def stage_done(stage_id: str, message: Optional[str] = None) -> None:
    """Report that the stage finished successfully."""
    _stage("stage.done", stage_id, message)


def stage_fail(stage_id: str, message: Optional[str] = None) -> None:
    """Report that the stage failed."""
    _stage("stage.fail", stage_id, message)


@contextlib.contextmanager
def stage(stage_id: str, message: Optional[str] = None) -> Iterator[None]:
    """Run a block as a stage.

    The stage is reported done when the block finishes and failed when it
    raises; the exception is re-raised. ``sys.exit(0)`` inside the block
    counts as done.
    """
    stage_start(stage_id, message)
    try:
        yield
    except SystemExit as exc:
        if exc.code in (None, 0):
            stage_done(stage_id)
        else:
            stage_fail(stage_id, f"exit status {exc.code}")
        raise
    except BaseException as exc:
        stage_fail(stage_id, _describe(exc))
        raise
    else:
        stage_done(stage_id)


def result(status: str, summary: str, data: Any = None) -> None:
    """Report the business result of the run.

    ``status`` is "ok", "partial" or "failed". ``data`` is any value that
    ``json.dumps`` accepts, at most 64 KiB once encoded.
    """
    if status not in RESULT_STATUSES:
        raise ValueError(f"status must be one of {', '.join(RESULT_STATUSES)}, got {status!r}")
    if not isinstance(summary, str):
        raise ValueError("summary must be a string")
    event: Dict[str, Any] = {
        "kind": "result",
        "status": status,
        "summary": _truncate(summary, SUMMARY_MAX_BYTES),
    }
    if data is not None:
        encoded = _dumps(data)
        size = len(encoded.encode("utf-8"))
        if size > DATA_MAX_BYTES:
            raise ValueError(f"result data is {size} bytes encoded, the limit is {DATA_MAX_BYTES}")
        event["data"] = data
    _emit(event)


class ToolError(Exception):
    """A tool call failed: the tool is not available to the run, the
    server could not be reached or answered with an error, or the tool
    reported an error result (then ``result`` holds it)."""

    def __init__(self, message: str, result: Optional[Dict[str, Any]] = None) -> None:
        super().__init__(message)
        self.result = result


#: MCP protocol version sent in initialize; servers may answer with another.
MCP_PROTOCOL_VERSION = "2025-06-18"
#: Characters of the compact arguments kept in the call record.
ARGS_SUMMARY_MAX_CHARS = 200


def mcp_call(
    tool: str,
    name: str,
    args: Optional[Mapping[str, Any]] = None,
    headers: Optional[Mapping[str, str]] = None,
    timeout: float = 60.0,
) -> Dict[str, Any]:
    """Call the MCP tool ``name`` of the registered MCP tool ``tool``.

    ``tool`` must be listed in the manifest's ``tools`` and registered as an
    ``mcp`` tool; its address comes from LERRYN_TOOLS. ``headers`` are sent
    with every request, for example ``{"Authorization": "Bearer " +
    os.environ["CRM_TOKEN"]}`` with a credential from the manifest. The
    call is recorded in the run's events (kind ``tool.call``) with a
    redacted summary of the arguments; the response is not recorded.

    Returns the MCP CallToolResult as a dict (``content``, and
    ``structuredContent`` when the tool returns it). Raises ToolError when
    the call fails or the tool returns ``isError``.
    """
    arguments = dict(args or {})
    summary = _cut_chars(_dumps(arguments), ARGS_SUMMARY_MAX_CHARS)
    started = time.monotonic()
    try:
        url = _mcp_url(tool)
        res = _McpSession(url, dict(headers or {}), timeout).call(name, arguments)
    except ToolError as exc:
        _record_call(tool, name, False, started, summary, str(exc))
        raise
    if res.get("isError"):
        message = _first_text(res) or "the tool returned an error"
        _record_call(tool, name, False, started, summary, message)
        raise ToolError(f"{tool} {name}: {message}", res)
    _record_call(tool, name, True, started, summary, None)
    return res


def _mcp_url(tool: str) -> str:
    raw = os.environ.get(TOOLS_ENV, "")
    try:
        tools = json.loads(raw) if raw.strip() else []
    except ValueError:
        tools = []
    for t in tools if isinstance(tools, list) else []:
        if isinstance(t, dict) and t.get("name") == tool:
            if t.get("kind") != "mcp":
                raise ToolError(f"tool {tool} is a {t.get('kind')} tool; only mcp tools can be called this way")
            return str(t.get("url", ""))
    raise ToolError(f"tool {tool} is not one of this run's tools; list it in the manifest's tools")


class _McpSession:
    """A minimal MCP client over Streamable HTTP: initialize, then one
    tools/call, then end the session."""

    def __init__(self, url: str, headers: Dict[str, str], timeout: float) -> None:
        self.url, self.headers, self.timeout = url, headers, timeout
        self.session: Optional[str] = None
        self.version: Optional[str] = None

    def call(self, name: str, arguments: Dict[str, Any]) -> Dict[str, Any]:
        init = self._request(1, "initialize", {
            "protocolVersion": MCP_PROTOCOL_VERSION,
            "capabilities": {},
            "clientInfo": {"name": "lerryn-sbx", "version": "1"},
        })
        version = init.get("protocolVersion")
        self.version = version if isinstance(version, str) else MCP_PROTOCOL_VERSION
        try:
            self._post({"jsonrpc": "2.0", "method": "notifications/initialized"})
            return self._request(2, "tools/call", {"name": name, "arguments": arguments})
        finally:
            self._end()

    def _request(self, rid: int, method: str, params: Dict[str, Any]) -> Dict[str, Any]:
        body, ctype = self._post({"jsonrpc": "2.0", "id": rid, "method": method, "params": params})
        for msg in _messages(body, ctype):
            if isinstance(msg, dict) and msg.get("id") == rid:
                if "error" in msg:
                    err = msg["error"] if isinstance(msg["error"], dict) else {}
                    raise ToolError(f"{method}: {err.get('message', 'error')} (code {err.get('code')})")
                res = msg.get("result")
                if isinstance(res, dict):
                    return res
        raise ToolError(f"{method}: the server sent no answer")

    def _headers(self) -> Dict[str, str]:
        h = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}
        h.update(self.headers)
        if self.session:
            h["Mcp-Session-Id"] = self.session
        if self.version:
            h["MCP-Protocol-Version"] = self.version
        return h

    def _post(self, message: Dict[str, Any]):
        req = urllib.request.Request(self.url, data=_dumps(message).encode("utf-8"), headers=self._headers(), method="POST")
        try:
            with urllib.request.urlopen(req, timeout=self.timeout) as resp:  # noqa: S310 -- the registered tool URL
                sid = resp.headers.get("Mcp-Session-Id")
                if sid:
                    self.session = sid
                return resp.read(), resp.headers.get("Content-Type", "")
        except urllib.error.HTTPError as exc:
            raise ToolError(f"HTTP {exc.code} from the tool") from None
        except (urllib.error.URLError, OSError) as exc:
            reason = getattr(exc, "reason", exc)
            raise ToolError(f"cannot reach the tool: {reason}") from None

    def _end(self) -> None:
        if not self.session:
            return
        req = urllib.request.Request(self.url, headers=self._headers(), method="DELETE")
        with contextlib.suppress(Exception):
            urllib.request.urlopen(req, timeout=min(self.timeout, 5)).close()  # noqa: S310


def _messages(body: bytes, ctype: str):
    text = body.decode("utf-8", "replace")
    if "text/event-stream" not in ctype:
        if not text.strip():
            return []
        try:
            msg = json.loads(text)
        except ValueError:
            raise ToolError("the tool's answer is not JSON") from None
        return msg if isinstance(msg, list) else [msg]
    out, data = [], []
    for line in text.splitlines() + [""]:
        if line.startswith("data:"):
            data.append(line[5:][1:] if line[5:6] == " " else line[5:])
        elif line == "" and data:
            with contextlib.suppress(ValueError):
                out.append(json.loads("\n".join(data)))
            data = []
    return out


def _first_text(res: Dict[str, Any]) -> str:
    for c in res.get("content") or []:
        if isinstance(c, dict) and c.get("type") == "text" and isinstance(c.get("text"), str):
            return _cut_chars(c["text"], 500)
    return ""


def _record_call(tool: str, name: str, ok: bool, started: float, summary: str, error: Optional[str]) -> None:
    event: Dict[str, Any] = {
        "kind": "tool.call",
        "tool": tool,
        "method": name,
        "ok": ok,
        "duration_ms": int((time.monotonic() - started) * 1000),
        "args_summary": summary,
    }
    if error:
        event["error"] = _truncate(error, MESSAGE_MAX_BYTES)
    with contextlib.suppress(OSError):
        _emit(event)


def _cut_chars(text: str, limit: int) -> str:
    return text if len(text) <= limit else text[: limit - 1] + "…"


def _stage(kind: str, stage_id: str, message: Optional[str]) -> None:
    if not isinstance(stage_id, str) or not _STAGE_ID.fullmatch(stage_id):
        raise ValueError(
            f"stage id {stage_id!r} must be 1 to 63 lowercase letters, digits, '_' or '-', "
            "starting with a letter or digit"
        )
    event: Dict[str, Any] = {"kind": kind, "stage": stage_id}
    if message is not None:
        event["message"] = _truncate(str(message), MESSAGE_MAX_BYTES)

    # Flush the task's own output, then write a marker line to stdout, the
    # same channel as that output: the runner switches the stage of log
    # lines at exactly this point and removes the marker from the log. The
    # token comes from the runner; without it no one reads the marker.
    for stream in (sys.stdout, sys.stderr):
        with contextlib.suppress(Exception):
            stream.flush()
    token = os.environ.get(LOG_MARKER_ENV)
    if token and os.environ.get(EVENTS_FILE_ENV):
        mark = os.urandom(8).hex()
        try:
            # fd 1 directly: sys.stdout may be redirected inside the task.
            os.write(1, f"\x1elerryn {token} {mark} {kind} {stage_id}\n".encode("utf-8"))
            event["mark"] = mark
        except OSError:
            # Without a marker the runner labels lines from the events file.
            pass
    _emit(event)


def _emit(event: Dict[str, Any]) -> None:
    path = os.environ.get(EVENTS_FILE_ENV)

    record = {"v": FORMAT_VERSION, "ts": _now()}
    record.update(event)
    line = (_dumps(record) + "\n").encode("utf-8")
    if not path:
        # Outside Lerryn, show the event so a local run stays readable.
        sys.stderr.write("sbx: " + line.decode("utf-8"))
        return

    # One write per line on an O_APPEND descriptor: concurrent writers
    # (for example the sbx command and the Python API in one task) never
    # interleave within a line.
    flags = os.O_WRONLY | os.O_APPEND | os.O_CREAT | getattr(os, "O_CLOEXEC", 0)
    fd = os.open(path, flags, 0o644)
    try:
        view = memoryview(line)
        while view:
            written = os.write(fd, view)
            view = view[written:]
    finally:
        os.close(fd)


def _dumps(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, separators=(",", ":"), allow_nan=False)


def _now() -> str:
    now = datetime.datetime.now(datetime.timezone.utc)
    return now.isoformat(timespec="milliseconds").replace("+00:00", "Z")


def _truncate(text: str, limit: int) -> str:
    encoded = text.encode("utf-8")
    if len(encoded) <= limit:
        return text
    ellipsis = "…"
    room = limit - len(ellipsis.encode("utf-8"))
    return encoded[:room].decode("utf-8", "ignore") + ellipsis


def _describe(exc: BaseException) -> str:
    text = str(exc)
    name = type(exc).__name__
    return f"{name}: {text}" if text else name
