import {
  LiveModeProvider,
  daysUntil,
  useAccessTokens,
  useDevices,
  useDiscovery,
  useEventStream,
  useI18n,
  useNotificationChannels,
  usePendingCredentialRequests,
  type Me,
} from "@lerryn/core";
import { Alert, Breadcrumbs, Link as TextLink } from "@lerryn/ui-web";
import {
  ArrowUpRight,
  Bell,
  CircleInfo,
  ClockArrowRotateLeft,
  House,
  Key,
  ListCheck,
  Person,
  Persons,
  PlugConnection,
  Server,
  Shield,
  Smartphone,
  Ticket,
} from "@gravity-ui/icons";
import { Outlet, useNavigate, useRouteContext, useRouterState } from "@tanstack/react-router";
import { useMemo, useState } from "react";

import { deploymentOriginMismatch } from "../lib/origin";
import { CredentialFillProvider } from "./CredentialFill";
import { AppSidebar, CommandMenu, CommandTrigger, UserMenu, type Command, type NavGroup } from "./Sidebar";

export { initialOf } from "./Sidebar";

/** The signed-in user, loaded by the app route before any page renders. */
export function useCurrentUser(): Me {
  return useRouteContext({ from: "/app" }).me;
}

const COLLAPSED_KEY = "lerryn.sidebar";

function readCollapsed(): boolean {
  try {
    return globalThis.localStorage?.getItem(COLLAPSED_KEY) === "collapsed";
  } catch {
    return false;
  }
}

/** The sidebar entry a path belongs to; task and run pages sit under their lists. */
function activeItem(pathname: string): string | undefined {
  const [, section, page] = pathname.split("/");
  if (!section || section === "tasks") return "overview";
  if (section === "runs") return "runs";
  if (section === "settings" || section === "admin") return page ? `${section}/${page}` : undefined;
  return undefined;
}

/**
 * The console's destinations with the counts that tell whether a page needs a
 * visit: `groups` for the sidebar, `account` for the pages behind the avatar.
 */
function useNav(me: Me): { groups: NavGroup[]; account: NavGroup } {
  const { t } = useI18n();
  const tokens = useAccessTokens();
  const requests = usePendingCredentialRequests();
  const channels = useNotificationChannels();
  const devices = useDevices();
  const usable = tokens.data?.items.filter((token) => !token.revoked_at && (!token.expires_at || daysUntil(token.expires_at) > 0)).length;
  const pending = requests.data?.items.length ?? 0;
  const pendingCount = requests.hasNextPage ? `${pending}+` : pending;
  const tokenCount = usable === undefined ? undefined : tokens.data?.hasMore ? `${usable}+` : usable;
  const channelCount = channels.data?.items.length || undefined;
  const deviceCount = devices.data?.items.length || undefined;
  return useMemo(() => {
    const groups: NavGroup[] = [
      {
        id: "main",
        items: [
          { id: "overview", to: "/", label: t("nav.overview"), icon: House },
          { id: "runs", to: "/runs", label: t("nav.runs"), icon: ClockArrowRotateLeft },
        ],
      },
      {
        id: "keys",
        label: t("settings.keyTitle"),
        items: [
          { id: "settings/tokens", to: "/settings/tokens", label: t("settings.tokens"), icon: Ticket, badge: tokenCount },
          {
            id: "settings/credentials",
            to: "/settings/credentials",
            label: t("settings.credentials"),
            name: pending ? t("nav.credentialsPending", { count: pendingCount }) : undefined,
            icon: Key,
            badge: pending ? pendingCount : undefined,
            attention: pending > 0,
          },
          { id: "settings/tools", to: "/settings/tools", label: t("settings.tools"), icon: PlugConnection },
        ],
      },
    ];
    if (me.is_admin) {
      groups.push({
        id: "admin",
        label: t("admin.title"),
        items: [
          { id: "admin/users", to: "/admin/users", label: t("admin.users"), icon: Persons },
          { id: "admin/login", to: "/admin/login", label: t("admin.login"), icon: Shield },
          { id: "admin/runners", to: "/admin/runners", label: t("admin.runners"), icon: Server },
          { id: "admin/audit", to: "/admin/audit", label: t("admin.audit"), icon: ListCheck },
        ],
      });
    }
    const account: NavGroup = {
      id: "account",
      label: t("nav.settings"),
      items: [
        { id: "settings/account", to: "/settings/account", label: t("settings.account"), icon: Person },
        { id: "settings/notifications", to: "/settings/notifications", label: t("settings.notifications"), icon: Bell, badge: channelCount },
        { id: "settings/pairing", to: "/settings/pairing", label: t("settings.pairing"), icon: Smartphone, badge: deviceCount },
        { id: "settings/about", to: "/settings/about", label: t("settings.about"), icon: CircleInfo },
      ],
    };
    return { groups, account };
  }, [t, me.is_admin, tokenCount, pending, pendingCount, channelCount, deviceCount]);
}

/**
 * The console frame: a sidebar with the account menu at its foot, a top bar with the current location, and the
 * page area. Pages may put a list pane beside their board. The event stream
 * opened here keeps every page current; while it is down the queries poll instead.
 */
export function AppShell() {
  const { t } = useI18n();
  const me = useCurrentUser();
  const navigate = useNavigate();
  const pathname = useRouterState({ select: ({ location }) => location.pathname });
  const mode = useEventStream({ enabled: true });
  const discovery = useDiscovery();
  const expected = deploymentOriginMismatch(discovery.data?.links.console, window.location.origin);
  const { groups, account } = useNav(me);
  const active = activeItem(pathname);
  const [collapsed, setCollapsedState] = useState(readCollapsed);
  const [commandOpen, setCommandOpen] = useState(false);

  const setCollapsed = (next: boolean) => {
    setCollapsedState(next);
    try {
      globalThis.localStorage?.setItem(COLLAPSED_KEY, next ? "collapsed" : "open");
    } catch {
      // Applies now, not remembered.
    }
  };

  const everything = useMemo(() => [...groups, account], [groups, account]);
  const group = everything.find((g) => g.items.some((item) => item.id === active));
  const current = group?.items.find((item) => item.id === active);
  const commands: Command[] = useMemo(
    () =>
      everything.flatMap((g) =>
        g.items.map((item) => ({
          id: item.id,
          label: item.label,
          group: g.label ?? t("app.brand"),
          icon: item.icon,
          run: () => void navigate({ to: item.to }),
        })),
      ),
    [everything, navigate, t],
  );

  return (
    <LiveModeProvider mode={mode}>
      <CredentialFillProvider>
        <div className="nt-console" data-collapsed={collapsed || undefined}>
          <AppSidebar
            groups={groups}
            active={active}
            collapsed={collapsed}
            onCollapsedChange={setCollapsed}
            label={t("nav.main")}
            footer={<UserMenu me={me} showName={!collapsed} pages={account.items} active={active} />}
          />
          <div className="nt-console__main">
            <header className="nt-console__top">
              <Breadcrumbs className="nt-crumbs">
                {group?.label ? <Breadcrumbs.Item>{group.label}</Breadcrumbs.Item> : null}
                <Breadcrumbs.Item>{current?.label ?? t("app.brand")}</Breadcrumbs.Item>
              </Breadcrumbs>
              <div className="nt-console__tools">
                <CommandTrigger onPress={() => setCommandOpen(true)} />
              </div>
            </header>
            {expected ? (
              <Alert status="warning" role="status" className="nt-console__notice">
                <Alert.Indicator />
                <Alert.Content>
                  <Alert.Description>{t("nav.wrongOrigin")}</Alert.Description>
                </Alert.Content>
                <TextLink
                  href={expected + window.location.pathname + window.location.search}
                  aria-label={t("nav.openDeployment", { expected })}
                  className="font-mono text-xs"
                >
                  {new URL(expected).host}
                  <ArrowUpRight aria-hidden="true" width={14} height={14} />
                </TextLink>
              </Alert>
            ) : null}
            <div className="nt-console__body">
              <Outlet />
            </div>
          </div>
        </div>
        <CommandMenu isOpen={commandOpen} onOpenChange={setCommandOpen} commands={commands} />
      </CredentialFillProvider>
    </LiveModeProvider>
  );
}
