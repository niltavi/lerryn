// SPDX-License-Identifier: Apache-2.0
//
// The Node side of the sh sbx command: "sbx mcp call" runs here when Node
// is available, so the Node runtime, which has neither curl nor wget,
// calls tools with fetch. Same options and output as the Python command.
//
//   node cli.js mcp call <tool> <name> [--args JSON | --args-file FILE] [--header 'Name: value']... [--timeout S]

import { readFileSync } from "node:fs";
import { ToolError, mcpCall } from "./index.js";

function usage(message) {
  process.stderr.write(`sbx: ${message}\n`);
  process.stderr.write("usage: sbx mcp call TOOL NAME [--args JSON | --args-file FILE] [--header 'NAME: VALUE']... [--timeout S]\n");
  process.exit(2);
}

async function main(argv) {
  if (argv[0] !== "mcp" || argv[1] !== "call") usage("expected mcp call");
  const rest = argv.slice(2);
  const positional = [];
  let args;
  let argsFile;
  const headers = {};
  let timeout = 60;
  for (let i = 0; i < rest.length; i++) {
    const a = rest[i];
    const value = () => {
      if (i + 1 >= rest.length) usage(`${a} needs a value`);
      return rest[++i];
    };
    if (a === "--args") args = value();
    else if (a === "--args-file") argsFile = value();
    else if (a === "--header" || a === "-H") {
      const h = value();
      const at = h.indexOf(":");
      if (at <= 0 || h.slice(0, at).trim() === "") usage(`header must look like 'Name: value'`);
      headers[h.slice(0, at).trim()] = h.slice(at + 1).trim();
    } else if (a === "--timeout") {
      timeout = Number(value());
      if (!(timeout > 0)) usage("--timeout must be a positive number of seconds");
    } else if (a.startsWith("-")) usage(`unknown option ${a}`);
    else positional.push(a);
  }
  if (positional.length !== 2) usage("mcp call needs a tool and a name");
  if (args !== undefined && argsFile !== undefined) usage("--args and --args-file cannot be used together");
  let parsed = {};
  try {
    if (argsFile !== undefined) parsed = JSON.parse(readFileSync(argsFile, "utf8"));
    else if (args !== undefined) parsed = JSON.parse(args);
  } catch (err) {
    process.stderr.write(`sbx: tool arguments: ${err.message}\n`);
    return 1;
  }
  if (parsed === null || typeof parsed !== "object" || Array.isArray(parsed)) {
    process.stderr.write("sbx: tool arguments must be a JSON object\n");
    return 1;
  }
  try {
    const res = await mcpCall(positional[0], positional[1], parsed, { headers, timeout });
    process.stdout.write(JSON.stringify(res) + "\n");
    return 0;
  } catch (err) {
    if (err instanceof ToolError && err.result) process.stdout.write(JSON.stringify(err.result) + "\n");
    process.stderr.write(`sbx: ${err.message}\n`);
    return 1;
  }
}

process.exitCode = await main(process.argv.slice(2));
