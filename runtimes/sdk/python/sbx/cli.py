# SPDX-License-Identifier: Apache-2.0
"""The sbx command line.

    sbx stage start|done|fail <id> [--message M]
    sbx result --status ok|partial|failed --summary S [--data-json JSON | --data-file FILE]
    sbx mcp call <tool> <name> [--args JSON | --args-file FILE] [--header 'Name: value']... [--timeout S]
"""

from __future__ import annotations

import argparse
import json
import sys
from typing import List, Optional

import sbx


def main(argv: Optional[List[str]] = None) -> int:
    parser = _parser()
    args = parser.parse_args(argv)
    try:
        if args.command == "stage":
            report = {"start": sbx.stage_start, "done": sbx.stage_done, "fail": sbx.stage_fail}[args.action]
            report(args.id, args.message)
        elif args.command == "mcp":
            return _mcp_call(args)
        else:
            data = None
            if args.data_json is not None:
                data = json.loads(args.data_json)
            elif args.data_file is not None:
                with open(args.data_file, encoding="utf-8") as f:
                    data = json.load(f)
            sbx.result(args.status, args.summary, data)
    except (ValueError, OSError) as exc:
        # json.JSONDecodeError is a ValueError.
        print(f"sbx: {exc}", file=sys.stderr)
        return 1
    return 0


def _mcp_call(args: argparse.Namespace) -> int:
    if args.args_file is not None:
        with open(args.args_file, encoding="utf-8") as f:
            arguments = json.load(f)
    else:
        arguments = json.loads(args.args) if args.args is not None else {}
    if not isinstance(arguments, dict):
        raise ValueError("tool arguments must be a JSON object")
    headers = {}
    for h in args.header or []:
        name, sep, value = h.partition(":")
        if not sep or not name.strip():
            print(f"sbx: header {name.strip()!r} must look like 'Name: value'", file=sys.stderr)
            return 2
        headers[name.strip()] = value.strip()
    try:
        res = sbx.mcp_call(args.tool, args.name, arguments, headers, args.timeout)
    except sbx.ToolError as exc:
        if exc.result is not None:
            print(json.dumps(exc.result, ensure_ascii=False))
        print(f"sbx: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(res, ensure_ascii=False))
    return 0


def _parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="sbx",
        description="Report stages and results of a Lerryn run.",
    )
    commands = parser.add_subparsers(dest="command", metavar="command")
    commands.required = True

    stage = commands.add_parser("stage", help="report a stage starting, finishing or failing")
    stage.add_argument("action", choices=["start", "done", "fail"])
    stage.add_argument("id", help="stage id, as declared in the manifest")
    stage.add_argument("--message", "-m", help="short note shown with the stage")

    result = commands.add_parser("result", help="report the business result of the run")
    result.add_argument("--status", required=True, choices=sbx.RESULT_STATUSES)
    result.add_argument("--summary", required=True, help="one or two sentences for people and agents")
    data = result.add_mutually_exclusive_group()
    data.add_argument("--data-json", metavar="JSON", help="structured data as a JSON string")
    data.add_argument("--data-file", metavar="FILE", help="read structured data from a JSON file")

    mcp = commands.add_parser("mcp", help="call a tool of a registered MCP server")
    mcp_commands = mcp.add_subparsers(dest="mcp_command", metavar="command")
    mcp_commands.required = True
    call = mcp_commands.add_parser("call", help="call an MCP tool and print its result as JSON")
    call.add_argument("tool", help="registered mcp tool, as listed in the manifest's tools")
    call.add_argument("name", help="the MCP tool to call")
    given = call.add_mutually_exclusive_group()
    given.add_argument("--args", metavar="JSON", help="arguments as a JSON object")
    given.add_argument("--args-file", metavar="FILE", help="read the arguments from a JSON file")
    call.add_argument("--header", "-H", action="append", metavar="'NAME: VALUE'", help="HTTP header for the tool, repeatable")
    call.add_argument("--timeout", type=float, default=60.0, help="seconds per request (default 60)")
    return parser


if __name__ == "__main__":
    sys.exit(main())
