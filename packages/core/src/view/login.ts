// Which sign-in paths a login screen offers, from discover's capabilities.
// Shared by the web console and the phone so both read the switch the same way.
import type { Schemas } from "@lerryn/api";

type LoginCapabilities = Pick<Schemas["Capabilities"], "login_methods" | "admin_password_login">;

export interface LoginLayout {
  /** Single sign-on is configured. */
  sso: boolean;
  /**
   * How the username and password form appears: "primary" for everyone,
   * "admin" only behind a quiet admin entry (local password login is off),
   * "none" when no one may sign in with a password here.
   */
  password: "primary" | "admin" | "none";
}

/**
 * Lays out the login screen. With local password login off, login_methods
 * drops password and admin_password_login is true (tech design 12.1), so the
 * screen leads with single sign-on before anyone tries a password. Before
 * discover answers, the plain password layout is assumed.
 */
export function loginLayout(capabilities: LoginCapabilities | null | undefined): LoginLayout {
  const methods = capabilities?.login_methods ?? ["password"];
  const sso = methods.includes("oidc");
  if (methods.includes("password")) return { sso, password: "primary" };
  return { sso, password: capabilities?.admin_password_login ? "admin" : "none" };
}
