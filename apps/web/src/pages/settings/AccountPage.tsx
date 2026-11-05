import {
  LOCALES,
  formatFullDate,
  isApiError,
  localTimeZone,
  useChangePassword,
  useI18n,
  useLogout,
  useMe,
  useUpdateMe,
  type Locale,
} from "@lerryn/core";
import { Button, ConsoleDialog, ToneGlyph, useLerrynTheme, type ThemePreference } from "@lerryn/ui-web";
import { useNavigate } from "@tanstack/react-router";
import { Building2, Check, Clock, KeyRound, Languages, Mail, Palette, UserRound } from "lucide-react";
import { useState, type FormEvent, type ReactNode } from "react";

import { initialOf, useCurrentUser } from "../../shell/AppShell";
import { Board, BoardTop, usePageTitle } from "../../shell/layout";
import { FormError, PasswordInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

const LOCALE_NAMES: Record<Locale, string> = { "zh-CN": "简体中文", en: "English" };

function zones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  } catch {
    return [];
  }
}

function Row({ icon: Icon, label, htmlFor, children }: { icon: typeof Clock; label: string; htmlFor?: string; children: ReactNode }) {
  return (
    <div className="rowi">
      <span className="ic-box" aria-hidden="true">
        <Icon className="wl-icon" width={18} />
      </span>
      {htmlFor ? (
        <label htmlFor={htmlFor} style={{ fontWeight: 600 }}>
          {label}
        </label>
      ) : (
        <span style={{ fontWeight: 600 }}>{label}</span>
      )}
      <span className="rowi-end">{children}</span>
    </div>
  );
}

/** Profile, password, linked SSO identity and preferences (prototype Settings-Account.dc.html). */
export function AccountPage() {
  const { t, locale, setLocale } = useI18n();
  usePageTitle(t("account.title"));
  const routeMe = useCurrentUser();
  const me = useMe().data ?? routeMe;
  const { preference, setPreference } = useLerrynTheme();
  const update = useUpdateMe();
  const logout = useLogout();
  const navigate = useNavigate();
  const [name, setName] = useState(me.display_name ?? "");
  const [changing, setChanging] = useState(false);
  const zoneList = zones();
  const zone = me.timezone ?? "";

  const themes: { key: ThemePreference; label: string }[] = [
    { key: "system", label: t("common.themeSystem") },
    { key: "light", label: t("common.themeLight") },
    { key: "dark", label: t("common.themeDark") },
  ];

  const saveName = () => {
    const next = name.trim();
    if (next !== (me.display_name ?? "")) update.mutate({ display_name: next });
  };

  return (
    <Board label={t("account.title")}>
      <BoardTop title={t("account.title")} />
      <div className="row" style={{ gap: 16 }}>
        <span className="avatar avatar--lg" aria-hidden="true">
          {initialOf(me)}
        </span>
        <div className="stack" style={{ gap: 2 }}>
          <span className="h3">{me.display_name || me.username}</span>
          <span className="row mono-sm muted" style={{ gap: 8 }}>
            {me.username}
            {me.is_admin ? (
              <span className="tag tag--kraft" style={{ fontFamily: "var(--font-sans)" }}>
                {t("account.admin")}
              </span>
            ) : null}
            <span style={{ fontFamily: "var(--font-sans)" }}>{t("account.since", { date: formatFullDate(me.created_at, locale) })}</span>
          </span>
        </div>
      </div>

      <div style={{ maxWidth: 720 }}>
        <div className="rows" aria-label={t("account.profile")}>
          <Row icon={UserRound} label={t("account.displayName")} htmlFor="acc-name">
            <form
              onSubmit={(e) => {
                e.preventDefault();
                saveName();
              }}
            >
              <input
                id="acc-name"
                className="input"
                value={name}
                maxLength={64}
                placeholder={me.username}
                onChange={(e) => setName(e.target.value)}
                onBlur={saveName}
                style={{ width: 220, height: 40 }}
              />
            </form>
          </Row>
          {me.email ? (
            <Row icon={Mail} label={t("account.email")}>
              <span className="body-sm">{me.email}</span>
            </Row>
          ) : null}
          {me.has_password ? (
            <Row icon={KeyRound} label={t("auth.password")}>
              <Button variant="secondary" size="sm" onPress={() => setChanging(true)}>
                {t("account.changePassword")}
              </Button>
            </Row>
          ) : null}
          {me.identities.length > 0 ? (
            <Row icon={Building2} label={t("account.sso")}>
              <span className="row" style={{ gap: 8 }}>
                <span className="mono-sm muted">{hostOf(me.identities[0]!.issuer)}</span>
                <ToneGlyph tone="ok" icon={Check} label={t("account.ssoLinked")} />
              </span>
            </Row>
          ) : null}
          <Row icon={Languages} label={t("common.language")}>
            <div className="seg" role="group" aria-label={t("common.language")}>
              {LOCALES.map((l) => (
                <button
                  key={l}
                  type="button"
                  lang={l}
                  aria-pressed={locale === l}
                  onClick={() => {
                    setLocale(l);
                    update.mutate({ locale: l });
                  }}
                >
                  {LOCALE_NAMES[l]}
                </button>
              ))}
            </div>
          </Row>
          <Row icon={Palette} label={t("common.theme")}>
            <div className="seg" role="group" aria-label={t("common.theme")}>
              {themes.map((th) => (
                <button key={th.key} type="button" aria-pressed={preference === th.key} onClick={() => setPreference(th.key)}>
                  {th.label}
                </button>
              ))}
            </div>
          </Row>
          <Row icon={Clock} label={t("account.timezone")} htmlFor="acc-tz">
            <span className="field-select">
              <select id="acc-tz" value={zone} onChange={(e) => update.mutate({ timezone: e.target.value })} style={{ height: 40 }}>
                <option value="">{t("account.timezoneDevice", { zone: localTimeZone() })}</option>
                {zoneList.map((z) => (
                  <option key={z} value={z}>
                    {z}
                  </option>
                ))}
              </select>
            </span>
          </Row>
        </div>
      </div>
      {update.isError ? <FormError>{t(writeErrorMessage(update.error))}</FormError> : null}

      <button
        type="button"
        className="body-sm"
        style={{ color: "var(--danger)", fontWeight: 600, alignSelf: "flex-start", border: 0, background: "none", padding: 0, cursor: "pointer" }}
        onClick={() => logout.mutate(undefined, { onSettled: () => void navigate({ to: "/login", replace: true }) })}
      >
        {t("auth.logout")}
      </button>
      <PasswordDialog isOpen={changing} onClose={() => setChanging(false)} />
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

function PasswordDialog({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const change = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [errors, setErrors] = useState<{ current?: string; next?: string }>({});
  const close = () => {
    change.reset();
    setCurrent("");
    setNext("");
    setErrors({});
    onClose();
  };
  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    const errs = {
      current: current ? undefined : t("auth.errors.required"),
      next: next.length >= 8 ? undefined : t("auth.errors.passwordLength"),
    };
    setErrors(errs);
    if (errs.current || errs.next) return;
    change.mutate({ current_password: current, new_password: next }, { onSuccess: close });
  };
  const wrong = isApiError(change.error) && (change.error.code === "unauthenticated" || change.error.code === "validation_failed");
  return (
    <ConsoleDialog
      isOpen={isOpen}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t("account.changePasswordTitle")}
      footer={
        <>
          <Button variant="ghost" onPress={close}>
            {t("common.cancel")}
          </Button>
          <Button onPress={() => submit()} isPending={change.isPending}>
            {t("common.save")}
          </Button>
        </>
      }
    >
      <form className="stack" style={{ gap: "var(--space-5)" }} onSubmit={submit}>
        <PasswordInput name="current_password" label={t("account.currentPassword")} value={current} onChange={setCurrent} autoComplete="current-password" error={errors.current} />
        <PasswordInput name="new_password" label={t("account.newPassword")} value={next} onChange={setNext} autoComplete="new-password" placeholder={t("auth.passwordPlaceholder")} error={errors.next} />
        {change.error ? <FormError>{wrong ? t("account.wrongPassword") : t(writeErrorMessage(change.error))}</FormError> : null}
        <button type="submit" hidden />
      </form>
    </ConsoleDialog>
  );
}
