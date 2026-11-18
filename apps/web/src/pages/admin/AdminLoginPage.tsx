import { useAdminSettings, useI18n, useUpdateAdminSettings } from "@lerryn/core";
import { Card, Chip, CopyButton, Switch } from "@lerryn/ui-web";
import { Briefcase, Copy, Flask, Key, PersonPlus } from "@gravity-ui/icons";
import type { ComponentType, ReactNode, SVGProps } from "react";

import { Board, BoardTop, LoadError, usePageTitle } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";

/** Open registration, local password login and the SSO setup. */
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
        <>
          <Card className="tpl-card nt-settings nt-fade-up">
            <SettingRow icon={PersonPlus} title={t("admin.openRegistration")} detail={t("admin.openRegistrationSub")}>
              <OnOff label={t("admin.openRegistration")} isSelected={s.open_registration} isDisabled={update.isPending} onChange={(v) => update.mutate({ open_registration: v })} />
            </SettingRow>
            <SettingRow
              icon={Key}
              title={t("admin.localPassword")}
              // Password login is the only way in until SSO exists, so say why it cannot be turned off.
              detail={oidc ? t("admin.localPasswordSub") : `${t("admin.localPasswordSub")} · ${t("admin.needSso")}`}
            >
              <OnOff
                label={t("admin.localPassword")}
                isSelected={s.local_password_login}
                isDisabled={update.isPending || (!oidc && s.local_password_login)}
                onChange={(v) => update.mutate({ local_password_login: v })}
              />
            </SettingRow>
            <SettingRow icon={Briefcase} title={t("admin.sso")} detail={oidc ? hostOf(oidc.issuer) : t("admin.ssoNotSetDescription")} mono={!!oidc}>
              {oidc ? null : (
                <Chip size="sm" variant="soft" title={t("admin.ssoInConfig")}>
                  {t("admin.ssoNotSet")}
                </Chip>
              )}
            </SettingRow>
            <SettingRow icon={Flask} title={t("admin.testBeforeActivate")} detail={t("admin.inConfig")}>
              <Chip size="sm" variant="soft">
                {s.require_test_before_activate ? t("common.on") : t("common.off")}
              </Chip>
            </SettingRow>
          </Card>
          {update.error ? <FormError>{t("auth.errors.generic")}</FormError> : null}
          {oidc ? (
            <Card className="tpl-card nt-fade-up" role="group" aria-label={t("admin.sso")}>
              <div className="tpl-card__head">
                <h2 className="text-sm font-semibold">{t("admin.sso")}</h2>
              </div>
              <dl className="kv text-sm">
                <dt>Issuer</dt>
                <dd className="font-mono text-xs">{oidc.issuer}</dd>
                <dt>Client ID</dt>
                <dd className="font-mono text-xs">{oidc.client_id}</dd>
                <dt>{t("auth.username")}</dt>
                <dd className="font-mono text-xs">{oidc.username_claim}</dd>
                <dt>{t("admin.ssoMembers")}</dt>
                <dd>{oidc.user_count}</dd>
              </dl>
              <div className="lr-copy">
                <code>{oidc.callback_url}</code>
                <CopyButton value={oidc.callback_url} label={t("admin.copyCallback")} icon={Copy} />
              </div>
            </Card>
          ) : null}
        </>
      ) : null}
    </Board>
  );
}

function SettingRow({
  icon: Icon,
  title,
  detail,
  mono,
  children,
}: {
  icon: ComponentType<SVGProps<SVGSVGElement>>;
  title: string;
  detail: string;
  mono?: boolean;
  children?: ReactNode;
}) {
  return (
    <div className="nt-settings__row">
      <span className="tpl-activity__icon">
        <Icon aria-hidden="true" width={14} height={14} />
      </span>
      <div>
        <strong className="text-sm font-medium">{title}</strong>
        <span className={mono ? "font-mono" : undefined}>{detail}</span>
      </div>
      {children}
    </div>
  );
}

function OnOff({ label, isSelected, isDisabled, onChange }: { label: string; isSelected: boolean; isDisabled: boolean; onChange: (value: boolean) => void }) {
  return (
    <Switch aria-label={label} isSelected={isSelected} isDisabled={isDisabled} onChange={onChange}>
      <Switch.Content>
        <Switch.Control>
          <Switch.Thumb />
        </Switch.Control>
      </Switch.Content>
    </Switch>
  );
}

function hostOf(issuer: string): string {
  try {
    return new URL(issuer).host;
  } catch {
    return issuer;
  }
}
