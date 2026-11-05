import { isApiError } from "./client";

/**
 * What a failed request means for the screen:
 * - offline: no answer from the server; check the network or the address.
 * - signedOut: the session ended (expired, revoked, account disabled).
 * - notFound: the resource does not exist or the user may not see it.
 * - readOnly: the server refused a write; the phone session is read-only.
 * - rateLimited: too many attempts, try again later.
 * - failed: anything else, including servers that do not have the endpoint yet.
 */
export type ErrorKind = "offline" | "signedOut" | "notFound" | "readOnly" | "rateLimited" | "failed";

export function errorKind(err: unknown): ErrorKind {
  if (!isApiError(err)) return "failed";
  if (err.code === "network_error") return "offline";
  if (err.status === 401 || err.code === "unauthenticated") return "signedOut";
  if (err.code === "not_found" || err.status === 404) return "notFound";
  if (err.code === "insufficient_scope" || err.code === "permission_denied") return "readOnly";
  if (err.code === "rate_limited" || err.status === 429) return "rateLimited";
  return "failed";
}

/** Whether the content was removed by the deployment's retention policy (410 expired). */
export function isExpired(err: unknown): boolean {
  return isApiError(err) && (err.code === "expired" || err.status === 410);
}

/** Whether retrying the same request can help, for TanStack Query's retry option. */
export function shouldRetry(failureCount: number, err: unknown): boolean {
  const kind = errorKind(err);
  if (kind === "signedOut" || kind === "notFound" || kind === "readOnly" || isExpired(err)) return false;
  return failureCount < 2;
}
