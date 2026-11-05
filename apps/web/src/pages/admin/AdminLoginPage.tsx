import { useAdminSettings, useI18n, useUpdateAdminSettings } from "@lerryn/core";
import { CopyButton, Disclosure, Toggle, ToneGlyph } from "@lerryn/ui-web";
import { Building2, Check, Copy, FlaskConical, KeyRound, Minus, UserPlus } from "lucide-react";

import { Board, BoardTop, LoadError, usePageTitle } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";

/** Open registration, local password login and the SSO setup (prototype Admin-Login.dc.html). */
export function AdminLoginPage() {
  const { t } = useI18n();
  usePageTitle(t("admin.login"));
  const settings = useAdminSettings();
  const update = useUpdateAdminSettings();
  const s = settings.data;
  const oidc = s?.oidc ?? null;

  return (
    <Board label={t("admin.login")}>
      <BoardTop title={t("admin.login")} />
      {settings.isError ? (
        <LoadError error={settings.error} onRetry={() => void settings.refetch()} />
      ) : s ? (
        <div style={{ maxWidth: 720 }}>
          <div className="rows">
            <div className="rowi">
              <span className="ic-box" aria-hidden="true">
                <UserPlus className="wl-icon" width={18} />
              </span>
              <span style={{ minWidth: 0 }}>
                <span className="rowi-name">{t("admin.openRegistration")}</span>
                <span className="rowi-sub">{t("admin.openRegistrationSub")}</span>
              </span>
              <Toggle checked={s.open_registration} label={t("admin.openRegistration")} disabled={update.isPending} onChange={(v) => update.mutate({ open_registration: v })} />
            </div>
            <div className="rowi">
              <span className="ic-box" aria-hidden="true">
                <KeyRound className="wl-icon" width={18} />
              </span>
              <span style={{ minWidth: 0 }}>
                <span className="rowi-name">{t("admin.localPassword")}</span>
                <span className="rowi-sub">{t("admin.localPasswordSub")}</span>
              </span>
              <span data-tip={oidc ? undefined : t("admin.needSso")} data-tip-pos="top">
                <Toggle
                  checked={s.local_password_login}
                  label={t("admin.localPassword")}
                  disabled={update.isPending || (!oidc && s.local_password_login)}
                  onChange={(v) => update.mutate({ local_password_login: v })}
                />
              </span>
            </div>
            <div className="rowi">
              <span className="ic-box" aria-hidden="true">
                <Building2 className="wl-icon" width={18} />
              </span>
              <span style={{ minWidth: 0 }}>
                <span className="rowi-name">{t("admin.sso")}</span>
                <span className="rowi-sub mono-sm">{oidc ? hostOf(oidc.issuer) : t("admin.ssoNotSet")}</span>
              </span>
              <span className="rowi-end">
                {oidc ? (
                  <Disclosure
                    alignRight
                    triggerClassName="g g--ok g-btn"
                    label={t("admin.ssoDetails")}
                    detailsLabel={t("admin.sso")}
                    trigger={<Check className="wl-icon" aria-hidden="true" />}
                    details={
                      <div className="stack" style={{ gap: 12, minWidth: 300 }}>
                        <dl className="kv">
                          <dt>Issuer</dt>
                          <dd className="mono-sm">{oidc.issuer}</dd>
                          <dt>Client ID</dt>
                          <dd className="mono-sm">{oidc.client_id}</dd>
                          <dt>{t("auth.username")}</dt>
                          <dd className="mono-sm">{oidc.username_claim}</dd>
                          <dt>{t("admin.ssoMembers")}</dt>
                          <dd>{oidc.user_count}</dd>
                        </dl>
                        <div className="copyline">
                          <code>{oidc.callback_url}</code>
                          <CopyButton value={oidc.callback_url} label={t("admin.copyCallback")} icon={Copy} />
                        </div>
                      </div>
                    }
                  />
                ) : (
                  <span data-tip={t("admin.ssoInConfig")} data-tip-pos="top">
                    <ToneGlyph tone="idle" icon={Minus} label={t("admin.ssoNotSet")} />
                  </span>
                )}
              </span>
            </div>
            <div className="rowi">
              <span className="ic-box" aria-hidden="true">
                <FlaskConical className="wl-icon" width={18} />
              </span>
              <span style={{ minWidth: 0 }}>
                <span className="rowi-name">{t("admin.testBeforeActivate")}</span>
                <span className="rowi-sub">{t("admin.inConfig")}</span>
              </span>
              <span className="rowi-end">
                {s.require_test_before_activate ? (
                  <ToneGlyph tone="ok" icon={Check} label={t("common.on")} />
                ) : (
                  <ToneGlyph tone="idle" icon={Minus} label={t("common.off")} />
                )}
              </span>
            </div>
          </div>
          {update.error ? <FormError>{t("auth.errors.generic")}</FormError> : null}
        </div>
      ) : null}
    </Board>
  );
}

function hostOf(issuer: string): string {
  try {
    return new URL(issuer).host;
  } catch {
    return issuer;
  }
}
