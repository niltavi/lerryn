/**
 * Accepts a post-login redirect only when it is a path on this site, so a
 * crafted link cannot send a user elsewhere after they sign in.
 */
export function safeRedirect(value: unknown): string | undefined {
  if (typeof value !== "string") return undefined;
  if (!value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) return undefined;
  // Reject control characters and anything that parses to another origin.
  if (/[\u0000-\u001f]/.test(value)) return undefined;
  try {
    const url = new URL(value, "http://same.invalid");
    if (url.origin !== "http://same.invalid") return undefined;
    if (url.pathname === "/login" || url.pathname === "/register") return undefined;
    return url.pathname + url.search + url.hash;
  } catch {
    return undefined;
  }
}
