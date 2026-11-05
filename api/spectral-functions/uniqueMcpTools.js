// Spectral custom function: every x-mcp.tool name is used by one operation
// only. Spectral's built-in functions check a single location, so the
// cross-operation comparison lives here.
const METHODS = ["get", "put", "post", "delete", "options", "head", "patch", "trace"];

export default function uniqueMcpTools(doc) {
  const owners = new Map();
  const results = [];
  for (const [path, item] of Object.entries(doc?.paths ?? {})) {
    for (const method of METHODS) {
      const tool = item?.[method]?.["x-mcp"]?.tool;
      if (typeof tool !== "string") continue;
      const here = `${method.toUpperCase()} ${path}`;
      const owner = owners.get(tool);
      if (owner) {
        results.push({
          message: `MCP 工具名 ${tool} 已被 ${owner} 使用`,
          path: ["paths", path, method, "x-mcp", "tool"],
        });
      } else {
        owners.set(tool, here);
      }
    }
  }
  return results;
}
