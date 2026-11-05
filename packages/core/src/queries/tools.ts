// The project tool registry (tech design 7.8). Agents register and remove
// tools through the API; the console only shows them (tech design 13.3).
import type { Schemas } from "@lerryn/api";
import { useQuery } from "@tanstack/react-query";

import { unwrap, useApi } from "../client";
import { shouldRetry } from "../errors";
import { queryKeys } from "./keys";

export type Tool = Schemas["Tool"];
export type ToolKind = Schemas["ToolKind"];

/** Tools registered in one project, or in every project the caller can see when projectId is null. */
export function useTools(projectId: string | null, opts: { enabled?: boolean } = {}) {
  const api = useApi();
  return useQuery({
    queryKey: queryKeys.tools(projectId),
    queryFn: () => unwrap(api.GET("/api/v1/tools", { params: { query: projectId ? { project_id: projectId } : {} } })),
    enabled: opts.enabled ?? true,
    retry: shouldRetry,
    staleTime: 60_000,
  });
}
