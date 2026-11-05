import { Link } from "@tanstack/react-router";
import type { CSSProperties, ReactNode } from "react";

/**
 * The console path of a link the API gave, when it points at a console page
 * of this deployment; null for anything else, including the
 * server-rendered credential page (/c/...) and API downloads.
 */
export function consolePath(href: string, origin = window.location.origin): string | null {
  try {
    const url = new URL(href, origin);
    if (url.origin !== origin) return null;
    if (url.pathname.startsWith("/c/") || url.pathname.startsWith("/api/") || url.pathname === "/mcp") return null;
    return url.pathname;
  } catch {
    return null;
  }
}

/** A link from the API: a client-side route inside the console, a plain link otherwise. */
export function ApiLink({
  href,
  className,
  style,
  children,
  ...rest
}: {
  href: string;
  className?: string;
  style?: CSSProperties;
  children: ReactNode;
  "aria-label"?: string;
}) {
  const path = consolePath(href);
  if (path) {
    return (
      <Link to={path} className={className} style={style} {...rest}>
        {children}
      </Link>
    );
  }
  return (
    <a href={href} className={className} style={style} {...rest}>
      {children}
    </a>
  );
}
