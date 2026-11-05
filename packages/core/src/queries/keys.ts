/**
 * Query keys in one place, so invalidation agrees across pages and apps.
 * Per-task reads nest under the task key and per-run reads under the run
 * key, so invalidating ["tasks", id] refreshes everything about that task.
 */
export const queryKeys = {
  discover: ["discover"] as const,
  me: ["me"] as const,
  tokens: ["tokens"] as const,

  overviewAll: ["overview"] as const,
  overview: (projectId: string | null) => ["overview", projectId] as const,
  calendarAll: ["calendar"] as const,
  calendar: (projectId: string | null, from: number, to?: number, taskId?: string) =>
    ["calendar", projectId, from, to ?? null, taskId ?? null] as const,
  taskListAll: ["taskList"] as const,
  taskList: (projectId: string | null) => ["taskList", projectId] as const,
  projects: ["projects"] as const,
  projectMembers: (projectId: string) => ["projects", projectId, "members"] as const,
  tools: (projectId: string | null) => ["tools", projectId] as const,

  task: (taskId: string) => ["tasks", taskId] as const,
  taskRuns: (taskId: string) => ["tasks", taskId, "runs"] as const,
  taskTriggers: (taskId: string) => ["tasks", taskId, "triggers"] as const,
  taskCredentials: (taskId: string) => ["tasks", taskId, "credentials"] as const,
  taskVersions: (taskId: string) => ["tasks", taskId, "versions"] as const,
  taskVersion: (taskId: string, seq: number) => ["tasks", taskId, "versions", seq] as const,
  taskDiff: (taskId: string, seq: number, base: number | null) => ["tasks", taskId, "versions", seq, "diff", base] as const,
  panelHistory: (taskId: string, panelId: string) => ["tasks", taskId, "panels", panelId] as const,

  runListAll: ["runList"] as const,
  runList: (filter: object) => ["runList", filter] as const,
  run: (runId: string) => ["runs", runId] as const,
  runLog: (runId: string) => ["runs", runId, "logs"] as const,
  runEvents: (runId: string) => ["runs", runId, "events"] as const,
  runArtifacts: (runId: string) => ["runs", runId, "artifacts"] as const,
  artifactPreview: (artifactId: string) => ["artifacts", artifactId, "preview"] as const,
  reruns: (runId: string) => ["runs", runId, "reruns"] as const,

  credentials: ["credentials"] as const,
  credentialRequests: ["credentialRequests"] as const,
  credentialRequest: (requestId: string) => ["credentialRequests", requestId] as const,
  channels: ["notificationChannels"] as const,
  deliveries: (channelId: string) => ["notificationChannels", channelId, "deliveries"] as const,
  devices: ["devices"] as const,

  adminUsersAll: ["admin", "users"] as const,
  adminUsers: (filter: object) => ["admin", "users", filter] as const,
  adminSettings: ["admin", "settings"] as const,
  auditEvents: (filter: object) => ["admin", "audit", filter] as const,
  runners: ["admin", "runners"] as const,
};
