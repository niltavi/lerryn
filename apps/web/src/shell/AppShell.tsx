import { LiveModeProvider, useDiscovery, useEventStream, useI18n, usePendingCredentialRequests, type Me } from "@lerryn/core";
import { Logo } from "@lerryn/ui-web";
import { Link, Outlet, useLinkProps, useRouteContext, useRouterState } from "@tanstack/react-router";
import { ArrowUpRight, Eye, History, KeyRound, LayoutDashboard, Shield, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { deploymentOriginMismatch } from "../lib/origin";
import { CredentialFillProvider } from "./CredentialFill";

/** The signed-in user, loaded by the app route before any page renders. */
export function useCurrentUser(): Me {
  return useRouteContext({ from: "/app" }).me;
}

/** Initial for the avatar: the first character of the display name or username. */
export function initialOf(user: { display_name?: string | null; username: string }): string {
  return Array.from(user.display_name?.trim() || user.username)[0] ?? "?";
}

function RailLink({
  to,
  label,
  tip,
  icon: Icon,
  active = false,
  pip,
  children,
}: {
  to: string;
  label: string;
  tip?: string;
  icon?: LucideIcon;
  active?: boolean;
  pip?: boolean;
  children?: ReactNode;
}) {
  const linkProps = useLinkProps({ to, className: "ri", activeProps: {} });
  return (
    <a
      {...linkProps}
      aria-label={label}
      data-tip={tip ?? label}
      data-status={active ? "active" : undefined}
      aria-current={active ? "page" : undefined}
    >
      {Icon ? <Icon className="wl-icon" width={22} height={22} aria-hidden="true" /> : null}
      {children}
      {pip ? <span className="pip" aria-hidden="true" /> : null}
    </a>
  );
}

/**
 * The console frame: an icon rail and the page area. Each page lays out its
 * own list column and board inside it, like the prototype (Main.dc.html).
 * The event stream opened here keeps every page current; while it is down
 * the queries poll instead.
 */
export function AppShell() {
  const { t } = useI18n();
  // Rail selection follows the section, independently of each entry's destination.
  const section = useRouterState({ select: ({ location }) => location.pathname.split("/")[1] });
  const me = useCurrentUser();
  const mode = useEventStream({ enabled: true });
  const requests = usePendingCredentialRequests();
  const pending = requests.data?.items.length ?? 0;
  const name = me.display_name || me.username;
  const discovery = useDiscovery();
  const expected = deploymentOriginMismatch(discovery.data?.links.console, window.location.origin);
  return (
    <LiveModeProvider mode={mode}>
      <CredentialFillProvider>
        <div className="wl-app shell">
          <nav className="rail" aria-label={t("nav.main")}>
            <Link to="/" className="rail-logo" aria-label={t("app.brand")}>
              <Logo size={12} label="" />
            </Link>
            <RailLink to="/" active={section === "" || section === "tasks"} label={t("nav.overview")} icon={LayoutDashboard} />
            <RailLink to="/runs" active={section === "runs"} label={t("nav.runs")} icon={History} />
            <RailLink
              to="/settings/credentials"
              active={section === "settings"}
              label={pending ? t("nav.credentialsPending", { count: requests.hasNextPage ? `${pending}+` : pending }) : t("nav.credentials")}
              tip={t("nav.credentials")}
              icon={KeyRound}
              pip={pending > 0}
            />
            {me.is_admin ? <RailLink to="/admin" active={section === "admin"} label={t("nav.admin")} icon={Shield} /> : null}
            <div className="rail-foot">
              <RailLink to="/settings/tokens" label={t("nav.settingsOf", { name })}>
                <span className="avatar" aria-hidden="true">
                  {initialOf(me)}
                </span>
              </RailLink>
            </div>
          </nav>
          <div className="shell-main">
            {expected ? (
              <div className="shell-origin" role="status">
                <Eye className="wl-icon" aria-hidden="true" />
                <span className="shell-origin-text">{t("nav.wrongOrigin")}</span>
                <a
                  className="chip chip--sm"
                  href={expected + window.location.pathname + window.location.search}
                  aria-label={t("nav.openDeployment", { expected })}
                >
                  <span className="mono-sm">{new URL(expected).host}</span>
                  <ArrowUpRight className="wl-icon" aria-hidden="true" />
                </a>
              </div>
            ) : null}
            <div className="shell-page">
              <Outlet />
            </div>
          </div>
        </div>
      </CredentialFillProvider>
    </LiveModeProvider>
  );
}
