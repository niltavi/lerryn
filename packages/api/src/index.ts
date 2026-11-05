// Typed client for the Lerryn HTTP API, shared by the web console and the
// mobile app. The types in schema.gen.ts are generated from
// api/bundled/openapi.yaml by `make generate`; this file only adds a thin
// layer on top of openapi-fetch.
import createClient, { type Client, type ClientOptions } from "openapi-fetch";

import type { components, paths } from "./schema.gen";

export type * from "./schema.gen";

export type Schemas = components["schemas"];
export type Problem = Schemas["Problem"];
export type ErrorCode = Schemas["ErrorCode"];
export type Discovery = Schemas["Discovery"];

/** A client whose methods are typed by path, e.g. `client.GET("/api/v1/discover")`. */
export type LerrynClient = Client<paths>;

export interface LerrynClientOptions extends Omit<ClientOptions, "baseUrl"> {
  /**
   * Origin of the deployment, e.g. `https://lerryn.example.com`. Paths in the
   * spec already carry the `/api/v1` prefix.
   */
  baseUrl: string;
  /**
   * Access token (`wl_pat_...`) for callers outside the console. The console
   * leaves it unset and relies on the session cookie.
   */
  token?: string;
}

/** Creates a typed client for one deployment. */
export function createLerrynClient({ token, ...options }: LerrynClientOptions): LerrynClient {
  const client = createClient<paths>(options);
  if (token) {
    client.use({
      onRequest({ request }) {
        request.headers.set("Authorization", `Bearer ${token}`);
        return request;
      },
    });
  }
  return client;
}

/**
 * Reports whether an error body is a Lerryn problem. Typed responses already
 * carry the Problem type; this is for bodies that reach the caller untyped,
 * such as errors raised in middleware.
 */
export function isProblem(value: unknown): value is Problem {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return typeof v.code === "string" && typeof v.status === "number" && typeof v.request_id === "string";
}
