import { meQuery, type Me } from "@lerryn/core";
import type { LerrynClient } from "@lerryn/api";
import type { QueryClient } from "@tanstack/react-query";
import {
  Outlet,
  createRootRouteWithContext,
  createRoute,
  createRouter,
  notFound,
  redirect,
} from "@tanstack/react-router";

import { safeRedirect } from "./lib/redirect";
import { AdminLayout } from "./pages/admin/AdminLayout";
import { AdminAuditPage } from "./pages/admin/AdminAuditPage";
import { AdminLoginPage } from "./pages/admin/AdminLoginPage";
import { AdminRunnersPage } from "./pages/admin/AdminRunnersPage";
import { AdminUsersPage } from "./pages/admin/AdminUsersPage";
import { LoginPage } from "./pages/auth/LoginPage";
import { RegisterPage } from "./pages/auth/RegisterPage";
import { NotFoundPage } from "./pages/NotFoundPage";
import { OverviewPage } from "./pages/OverviewPage";
import { RunPage } from "./pages/run/RunPage";
import { RunsPage, validateRunsSearch } from "./pages/RunsPage";
import { AccountPage } from "./pages/settings/AccountPage";
import { CredentialsPage } from "./pages/settings/CredentialsPage";
import { NotificationsPage } from "./pages/settings/NotificationsPage";
import { PairingPage } from "./pages/settings/PairingPage";
import { SettingsLayout } from "./pages/settings/SettingsLayout";
import { TokensPage } from "./pages/settings/TokensPage";
import { ToolsPage } from "./pages/settings/ToolsPage";
import { TaskPage } from "./pages/task/TaskPage";
import { AppShell } from "./shell/AppShell";

export interface RouterContext {
  api: LerrynClient;
  queryClient: QueryClient;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
  notFoundComponent: NotFoundPage,
});

const currentUser = ({ context }: { context: RouterContext }) =>
  context.queryClient.ensureQueryData(meQuery(context.api));

interface AuthSearch {
  redirect?: string;
  /** The error code the OIDC callback sends back to the login page. */
  error?: string;
}
const authSearch = (search: Record<string, unknown>): AuthSearch => {
  const target = safeRedirect(search.redirect);
  const out: AuthSearch = target ? { redirect: target } : {};
  if (typeof search.error === "string" && /^[a-z_]{1,64}$/.test(search.error)) out.error = search.error;
  return out;
};

// Signed-in users have no business on the login and register pages.
const guestOnly = async (opts: { context: RouterContext; search: AuthSearch }) => {
  if (await currentUser(opts)) throw redirect({ href: opts.search.redirect ?? "/" });
};

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "login",
  validateSearch: authSearch,
  beforeLoad: guestOnly,
  component: LoginPage,
});

const registerRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "register",
  validateSearch: authSearch,
  beforeLoad: guestOnly,
  component: RegisterPage,
});

/** Every console page below needs a session; the shell draws the rail. */
const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  beforeLoad: async (opts): Promise<{ me: Me }> => {
    const me = await currentUser(opts);
    if (!me) {
      const back = opts.location.href;
      throw redirect({ to: "/login", search: back === "/" ? {} : { redirect: back } });
    }
    return { me };
  },
  component: AppShell,
});

const overviewRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  validateSearch: (s: Record<string, unknown>): { project?: string } =>
    typeof s.project === "string" && s.project ? { project: s.project } : {},
  component: OverviewPage,
});
const runsRoute = createRoute({ getParentRoute: () => appRoute, path: "runs", validateSearch: validateRunsSearch, component: RunsPage });
const runRoute = createRoute({ getParentRoute: () => appRoute, path: "runs/$runId", component: RunPage });
const taskRoute = createRoute({ getParentRoute: () => appRoute, path: "tasks/$taskId", component: TaskPage });

const settingsRoute = createRoute({ getParentRoute: () => appRoute, path: "settings", component: SettingsLayout });
const settingsIndexRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/settings/tokens" });
  },
});
const tokensRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "tokens",
  validateSearch: (s: Record<string, unknown>): { new?: boolean } => (s.new ? { new: true } : {}),
  component: TokensPage,
});
const credentialsRoute = createRoute({ getParentRoute: () => settingsRoute, path: "credentials", component: CredentialsPage });
const notificationsRoute = createRoute({
  getParentRoute: () => settingsRoute,
  path: "notifications",
  component: NotificationsPage,
});
const pairingRoute = createRoute({ getParentRoute: () => settingsRoute, path: "pairing", component: PairingPage });
const accountRoute = createRoute({ getParentRoute: () => settingsRoute, path: "account", component: AccountPage });
const toolsRoute = createRoute({ getParentRoute: () => settingsRoute, path: "tools", component: ToolsPage });

// Admin pages answer "not found" to everyone else, like the API does.
const adminRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "admin",
  beforeLoad: ({ context }) => {
    if (!context.me.is_admin) throw notFound();
  },
  component: AdminLayout,
});
const adminIndexRoute = createRoute({
  getParentRoute: () => adminRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/admin/users" });
  },
});
const adminUsersRoute = createRoute({ getParentRoute: () => adminRoute, path: "users", component: AdminUsersPage });
const adminLoginRoute = createRoute({ getParentRoute: () => adminRoute, path: "login", component: AdminLoginPage });
const adminRunnersRoute = createRoute({ getParentRoute: () => adminRoute, path: "runners", component: AdminRunnersPage });
const adminAuditRoute = createRoute({ getParentRoute: () => adminRoute, path: "audit", component: AdminAuditPage });

const routeTree = rootRoute.addChildren([
  loginRoute,
  registerRoute,
  appRoute.addChildren([
    overviewRoute,
    runsRoute,
    runRoute,
    taskRoute,
    settingsRoute.addChildren([
      settingsIndexRoute,
      tokensRoute,
      credentialsRoute,
      notificationsRoute,
      pairingRoute,
      toolsRoute,
      accountRoute,
    ]),
    adminRoute.addChildren([adminIndexRoute, adminUsersRoute, adminLoginRoute, adminRunnersRoute, adminAuditRoute]),
  ]),
]);

export function createAppRouter(context: RouterContext) {
  return createRouter({
    routeTree,
    context,
    defaultPreload: "intent",
    scrollRestoration: true,
    defaultNotFoundComponent: NotFoundPage,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}

