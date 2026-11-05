import { isApiError, loginLayout, useDiscovery, useI18n, useLogin, type LoginLayout } from "@lerryn/core";
import { Button, Form } from "@lerryn/ui-web";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Building2 } from "lucide-react";
import { useState, type FormEvent } from "react";

import { AuthFrame, FormError, LanguageChip, PasswordInput, TextInput, authErrorMessage } from "./AuthFrame";

/** Where the SSO button goes: the server starts the OIDC flow and comes back to return_to. */
export function oidcStartUrl(returnTo: string | undefined): string {
  const path = returnTo ?? "/";
  return `/api/v1/auth/oidc/start?return_to=${encodeURIComponent(path)}`;
}

export function LoginPage() {
  const { t } = useI18n();
  const search = useSearch({ from: "/login" });
  const navigate = useNavigate();
  const discovery = useDiscovery();
  const login = useLogin();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [missing, setMissing] = useState<{ username?: boolean; password?: boolean }>({});
  const [adminLogin, setAdminLogin] = useState(false);

  const layout = loginLayout(discovery.data?.capabilities);
  const sso = layout.sso;
  // Without password login only admins may still use it (tech design 12.1),
  // behind a quiet link. discover says so up front; a refused attempt, from
  // a server that predates admin_password_login, switches to the same layout.
  const passwordDisabled = isApiError(login.error) && login.error.code === "password_login_disabled";
  let formMode: LoginLayout["password"] = passwordDisabled ? "admin" : layout.password;
  // Never leave the page with no way in at all.
  if (!sso && formMode === "none") formMode = "primary";
  const ssoOnly = sso && formMode !== "primary";
  const showForm = formMode === "primary" || (formMode === "admin" && (adminLogin || passwordDisabled || !sso));
  const adminEntry = ssoOnly && formMode === "admin" && !showForm;
  const registrationOpen = discovery.data?.capabilities.registration_open ?? false;
  // Wait for discover rather than flash a password form that SSO replaces.
  const ready = !discovery.isPending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const m = { username: !username.trim(), password: !password };
    setMissing(m);
    if (m.username || m.password) return;
    login.mutate(
      { username: username.trim(), password },
      { onSuccess: () => void navigate({ href: search.redirect ?? "/", replace: true }) },
    );
  };

  const ssoButton = sso ? (
    <Button
      variant={ssoOnly ? "primary" : "secondary"}
      size="lg"
      fullWidth
      onPress={() => window.location.assign(oidcStartUrl(search.redirect))}
    >
      <Building2 className="wl-icon" aria-hidden="true" />
      {t("auth.sso")}
    </Button>
  ) : null;

  return (
    <AuthFrame title={t("auth.login")} badge={<span className="mono-sm muted">{window.location.host}</span>}>
      {search.error ? <FormError>{t("auth.errors.sso")}</FormError> : null}
      {ready && ssoOnly ? ssoButton : null}
      {ready && showForm ? (
        <Form className="stack" style={{ gap: 14 }} onSubmit={submit} validationBehavior="aria">
          <TextInput
            name="username"
            label={t("auth.username")}
            value={username}
            onChange={setUsername}
            autoComplete="username"
            autoFocus={!ssoOnly}
            error={missing.username ? t("auth.errors.required") : undefined}
          />
          <PasswordInput
            name="password"
            label={t("auth.password")}
            value={password}
            onChange={setPassword}
            autoComplete="current-password"
            error={missing.password ? t("auth.errors.required") : undefined}
            labelEnd={
              <span className="caption muted" tabIndex={0} data-tip={t("auth.forgotPasswordTip")} data-tip-pos="top">
                {t("auth.forgotPassword")}
              </span>
            }
          />
          {login.error ? <FormError>{t(isApiError(login.error) ? authErrorMessage(login.error) : "auth.errors.generic")}</FormError> : null}
          <Button type="submit" size="lg" fullWidth isPending={login.isPending} variant={ssoOnly ? "secondary" : "primary"}>
            {t("auth.login")}
          </Button>
        </Form>
      ) : null}
      {ready && !ssoOnly && sso ? (
        <>
          <div className="or">{t("auth.or")}</div>
          {ssoButton}
        </>
      ) : null}
      <div className="spread" style={{ justifyContent: "center", gap: 20 }}>
        {registrationOpen ? (
          <Link to="/register" search={search.redirect ? { redirect: search.redirect } : {}} className="link body-sm">
            {t("auth.register")}
          </Link>
        ) : null}
        {ready && adminEntry ? (
          <button type="button" className="link body-sm" style={{ border: 0, background: "none", cursor: "pointer", padding: 0 }} onClick={() => setAdminLogin(true)}>
            {t("auth.adminLogin")}
          </button>
        ) : null}
        <LanguageChip />
      </div>
    </AuthFrame>
  );
}
