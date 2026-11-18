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
import {
  Avatar,
  Button,
  Card,
  Chip,
  ComboBox,
  ConsoleDialog,
  Input,
  Label,
  ListBox,
  TextField,
  ToggleButton,
  ToggleButtonGroup,
  useLerrynTheme,
  type ThemePreference,
} from "@lerryn/ui-web";
import { ArrowRightFromSquare, Briefcase, Check, Globe, Key, Palette } from "@gravity-ui/icons";
import { useNavigate } from "@tanstack/react-router";
import { useState, type FormEvent } from "react";

import { initialOf, useCurrentUser } from "../../shell/AppShell";
import { Board, BoardTop, usePageTitle } from "../../shell/layout";
import { FormError, PasswordInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

const LOCALE_NAMES: Record<Locale, string> = { "zh-CN": "简体中文", en: "English" };
/** The timezone list's entry for "no preference": times follow the device. */
const DEVICE_ZONE = "device";

function zones(): string[] {
  try {
    return (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf?.("timeZone") ?? [];
  } catch {
    return [];
  }
}

/**
 * One choice out of a few, as pressed buttons. The group runs in multiple
 * mode and keeps a single key itself, because single mode would turn the
 * buttons into radios and drop the `aria-pressed` state the console relies on.
 */
export function Segmented<T extends string>({
  label,
  labelledBy,
  options,
  value,
  onChange,
  fullWidth,
}: {
  label?: string;
  labelledBy?: string;
  options: readonly { id: T; label: string; lang?: string }[];
  value: T;
  onChange: (value: T) => void;
  fullWidth?: boolean;
}) {
  return (
    <ToggleButtonGroup
      aria-label={label}
      aria-labelledby={labelledBy}
      size="sm"
      fullWidth={fullWidth}
      selectionMode="multiple"
      selectedKeys={[value]}
      onSelectionChange={(keys) => {
        const next = [...keys].find((key) => key !== value);
        if (next !== undefined) onChange(next as T);
      }}
    >
      {options.map((option, index) => (
        <ToggleButton key={option.id} id={option.id} lang={option.lang}>
          {index > 0 ? <ToggleButtonGroup.Separator /> : null}
          {option.label}
        </ToggleButton>
      ))}
    </ToggleButtonGroup>
  );
}

/** Profile, sign-in methods and preferences of the signed-in user. */
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
  const identity = me.identities[0];

  const themes: { id: ThemePreference; label: string }[] = [
    { id: "system", label: t("common.themeSystem") },
    { id: "light", label: t("common.themeLight") },
    { id: "dark", label: t("common.themeDark") },
  ];

  const saveName = () => {
    const next = name.trim();
    if (next !== (me.display_name ?? "")) update.mutate({ display_name: next });
  };

  return (
    <Board label={t("account.title")}>
      <BoardTop title={t("account.title")} />

      <Card className="tpl-card nt-fade-up">
        <div className="flex min-w-0 items-center gap-4">
          <Avatar size="lg" color="accent" aria-hidden="true">
            <Avatar.Fallback>{initialOf(me)}</Avatar.Fallback>
          </Avatar>
          <div className="flex min-w-0 flex-col gap-1">
            <div className="flex min-w-0 items-center gap-2">
              <span className="truncate text-base font-semibold">{me.display_name || me.username}</span>
              {me.is_admin ? (
                <Chip size="sm" color="accent" variant="soft">
                  {t("account.admin")}
                </Chip>
              ) : null}
            </div>
            <div className="flex flex-wrap items-center gap-x-2 text-xs text-muted">
              <span className="font-mono">{me.username}</span>
              <span>{t("account.since", { date: formatFullDate(me.created_at, locale) })}</span>
            </div>
          </div>
        </div>
        <form
          className="nt-form"
          aria-label={t("account.profile")}
          onSubmit={(e) => {
            e.preventDefault();
            saveName();
          }}
        >
          <TextField value={name} onChange={setName} onBlur={saveName} maxLength={64} name="display_name" fullWidth>
            <Label>{t("account.displayName")}</Label>
            <Input placeholder={me.username} />
          </TextField>
          {me.email ? (
            <TextField value={me.email} isReadOnly name="email" type="email" fullWidth>
              <Label>{t("account.email")}</Label>
              <Input />
            </TextField>
          ) : null}
          <ComboBox
            fullWidth
            selectedKey={zone || DEVICE_ZONE}
            // Clearing the text selects nothing; the stored zone stays until another one is picked.
            onSelectionChange={(key) => {
              if (key === null) return;
              const next = key === DEVICE_ZONE ? "" : String(key);
              if (next !== zone) update.mutate({ timezone: next });
            }}
          >
            <Label>{t("account.timezone")}</Label>
            <ComboBox.InputGroup>
              <Input />
              <ComboBox.Trigger />
            </ComboBox.InputGroup>
            <ComboBox.Popover>
              <ListBox>
                <ListBox.Item id={DEVICE_ZONE} textValue={t("account.timezoneDevice", { zone: localTimeZone() })}>
                  {t("account.timezoneDevice", { zone: localTimeZone() })}
                  <ListBox.ItemIndicator />
                </ListBox.Item>
                {zoneList.map((z) => (
                  <ListBox.Item key={z} id={z} textValue={z}>
                    {z}
                    <ListBox.ItemIndicator />
                  </ListBox.Item>
                ))}
              </ListBox>
            </ComboBox.Popover>
          </ComboBox>
        </form>
      </Card>

      {me.has_password || identity ? (
        <Card className="tpl-card nt-fade-up">
          <div className="tpl-card__head">
            <h2 className="text-sm font-semibold">{t("auth.login")}</h2>
          </div>
          <ul className="lr-rows">
            {me.has_password ? (
              <li className="lr-row">
                <span className="lr-row__icon" data-tone="idle" aria-hidden="true">
                  <Key width={16} height={16} />
                </span>
                <span className="lr-row__text">
                  <span className="lr-row__title">{t("auth.password")}</span>
                </span>
                <span className="lr-row__end">
                  <Button variant="secondary" size="sm" onPress={() => setChanging(true)}>
                    {t("account.changePassword")}
                  </Button>
                </span>
              </li>
            ) : null}
            {identity ? (
              <li className="lr-row">
                <span className="lr-row__icon" data-tone="idle" aria-hidden="true">
                  <Briefcase width={16} height={16} />
                </span>
                <span className="lr-row__text">
                  <span className="lr-row__title">{t("account.sso")}</span>
                  <span className="lr-row__sub font-mono">{hostOf(identity.issuer)}</span>
                </span>
                <span className="lr-row__end">
                  <Chip size="sm" color="success" variant="soft">
                    <Check aria-hidden="true" width={12} height={12} />
                    <Chip.Label>{t("account.ssoLinked")}</Chip.Label>
                  </Chip>
                </span>
              </li>
            ) : null}
          </ul>
        </Card>
      ) : null}

      <Card className="tpl-card nt-fade-up">
        <div className="tpl-card__head">
          <h2 className="text-sm font-semibold">{t("account.preferences")}</h2>
        </div>
        <div className="nt-settings">
          <div className="nt-settings__row">
            <span className="tpl-activity__icon" aria-hidden="true">
              <Globe width={14} height={14} />
            </span>
            <div>
              <strong>{t("common.language")}</strong>
            </div>
            <Segmented
              label={t("common.language")}
              options={LOCALES.map((l) => ({ id: l, label: LOCALE_NAMES[l], lang: l }))}
              value={locale}
              onChange={(l) => {
                setLocale(l);
                update.mutate({ locale: l });
              }}
            />
          </div>
          <div className="nt-settings__row">
            <span className="tpl-activity__icon" aria-hidden="true">
              <Palette width={14} height={14} />
            </span>
            <div>
              <strong>{t("common.theme")}</strong>
            </div>
            <Segmented label={t("common.theme")} options={themes} value={preference} onChange={setPreference} />
          </div>
        </div>
      </Card>

      {update.isError ? <FormError>{t(writeErrorMessage(update.error))}</FormError> : null}

      <div>
        <Button
          variant="danger-soft"
          size="sm"
          onPress={() => logout.mutate(undefined, { onSettled: () => void navigate({ to: "/login", replace: true }) })}
        >
          <ArrowRightFromSquare aria-hidden="true" width={16} height={16} />
          {t("auth.logout")}
        </Button>
      </div>
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
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <PasswordInput name="current_password" label={t("account.currentPassword")} value={current} onChange={setCurrent} autoComplete="current-password" error={errors.current} />
        <PasswordInput name="new_password" label={t("account.newPassword")} value={next} onChange={setNext} autoComplete="new-password" placeholder={t("auth.passwordPlaceholder")} error={errors.next} />
        {change.error ? <FormError>{wrong ? t("account.wrongPassword") : t(writeErrorMessage(change.error))}</FormError> : null}
        <button type="submit" hidden />
      </form>
    </ConsoleDialog>
  );
}
