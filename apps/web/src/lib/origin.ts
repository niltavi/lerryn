import { isApiError, type MessageKey } from "@lerryn/core";

/**
 * The deployment's own address when the console was opened at another one
 * (say the host's IP instead of the configured name), or null when they
 * match. The server accepts session writes only from server.public_url, so
 * at any other address every change fails; the shell says so up front.
 */
export function deploymentOriginMismatch(
  consoleUrl: string | undefined,
  current: string,
): string | null {
  if (!consoleUrl) return null;
  try {
    const expected = new URL(consoleUrl);
    return expected.origin === new URL(current).origin ? null : expected.origin;
  } catch {
    return null;
  }
}

/** The message for a failed change: why, when the address is the cause; otherwise the generic one. */
export function writeErrorMessage(err: unknown): MessageKey {
  return isApiError(err) && err.code === "origin_not_allowed"
    ? "auth.errors.wrongOrigin"
    : "auth.errors.generic";
}
