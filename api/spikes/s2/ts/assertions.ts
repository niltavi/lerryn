// Type-level checks for the TypeScript types generated from the S2 spike
// spec. They only need to compile: `tsc --noEmit` failing means the
// generator no longer produces what docs/spikes/s2-openapi-codegen.md
// records. Nothing here runs.
import createClient from "openapi-fetch";

import type {
  components,
  paths,
  SchemaErrorCode,
  SchemaProblem,
  SchemaRun,
  SchemaTrigger,
  webhooks,
} from "./schema.gen";

type Equal<X, Y> = (<T>() => T extends X ? 1 : 2) extends <T>() => T extends Y ? 1 : 2 ? true : false;
type Expect<T extends true> = T;
type S = components["schemas"];

// --- nullable written as a type array -------------------------------------

export type NullableRequired = [
  Expect<Equal<SchemaRun["finished_at"], string | null>>,
  Expect<Equal<SchemaRun["exit_code"], number | null>>,
  Expect<Equal<SchemaRun["labels"], string[] | null>>,
  Expect<Equal<S["CursorPage"]["next_cursor"], string | null>>,
];

// Optional and nullable keeps the three states apart: absent, null, value.
export type NullableOptional = [
  Expect<Equal<SchemaRun["attempt"], number | null | undefined>>,
  Expect<Equal<SchemaRun["priority"], "low" | "normal" | "high" | null | undefined>>,
];

// oneOf / anyOf with { type: "null" } next to a $ref.
export type NullableRef = [
  Expect<Equal<SchemaRun["error"], S["RunError"] | null>>,
  Expect<Equal<SchemaRun["principal"], S["Principal"] | null | undefined>>,
];

// --- enum declared via oneOf + const --------------------------------------

export type EnumViaOneOf = [
  Expect<Equal<SchemaErrorCode, "validation_failed" | "version_conflict" | "not_found">>,
  Expect<Equal<S["StageState"], "started" | "done">>,
];

// --- oneOf + discriminator --------------------------------------------------

export function scheduleOf(t: SchemaTrigger): string | null {
  switch (t.type) {
    case "cron":
      return `${t.schedule} ${t.timezone}`;
    case "webhook":
      return null;
    default: {
      // Exhaustive: the union has exactly the two mapped members.
      const unreachable: never = t;
      return unreachable;
    }
  }
}

// @ts-expect-error an unknown discriminator value is rejected
export const badTrigger: SchemaTrigger = { type: "manual" };

// @ts-expect-error a cron trigger without its required fields is rejected
export const incompleteCron: SchemaTrigger = { type: "cron" };

// --- webhooks ---------------------------------------------------------------

export type Webhooks = [
  Expect<
    Equal<
      webhooks["run.finished"]["post"]["requestBody"]["content"]["application/json"],
      S["RunFinishedEvent"]
    >
  >,
  Expect<Equal<S["RunFinishedEvent"]["event"], "run.finished">>,
];

// --- problem+json through openapi-fetch ------------------------------------

const client = createClient<paths>({ baseUrl: "http://localhost:8080" });

export async function readRun(runId: string): Promise<string | null> {
  const { data, error } = await client.GET("/api/v1/runs/{runId}", {
    params: { path: { runId } },
  });
  if (error) {
    // error is typed from the application/problem+json responses.
    const problem: SchemaProblem = error;
    const code: SchemaErrorCode = error.code;
    return `${problem.request_id} ${code}`;
  }
  return data.finished_at;
}

export async function replaceTriggers(taskId: string, version: string) {
  const { data } = await client.PUT("/api/v1/tasks/{taskId}/triggers", {
    params: { path: { taskId }, header: { "If-Match": version } },
    body: { triggers: [{ type: "cron", schedule: "0 8 * * 1-5", timezone: "Asia/Shanghai" }] },
  });
  return data?.version;
}

export async function replaceTriggersWithoutIfMatch(taskId: string) {
  await client.PUT("/api/v1/tasks/{taskId}/triggers", {
    // @ts-expect-error the required If-Match header is enforced at compile time
    params: { path: { taskId } },
    body: { triggers: [] },
  });
}

export async function listFirstPage() {
  const { data } = await client.GET("/api/v1/runs", { params: { query: { limit: 20 } } });
  // allOf [CursorPage, { items }] merges into one object type.
  const next: string | null | undefined = data?.next_cursor;
  const first: SchemaRun | undefined = data?.items[0];
  return { next, first };
}
