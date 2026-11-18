import { useI18n, type ApiError, type MessageKey } from "@lerryn/core";
import { Alert, Button, FieldError, ForestScene, Input, InputGroup, Label, Logo, TextField, ToggleButton } from "@lerryn/ui-web";
import { Eye, EyeSlash, Globe } from "@gravity-ui/icons";
import { useState, type ReactNode } from "react";

import { usePageTitle } from "../../shell/layout";

/**
 * The signed-out frame: the forest panel beside the sign-in or sign-up form.
 * The panel is decoration and goes away on narrow screens.
 */
export function AuthFrame({ title, badge, children }: { title: string; badge?: ReactNode; children: ReactNode }) {
  usePageTitle(title);
  return (
    <div className="lr-auth">
      <div className="nt-authlayout">
        <aside className="nt-authlayout__brand">
          <ForestScene />
        </aside>
        <main className="nt-authlayout__main">
          <div className="nt-authlayout__logo">
            <Logo size={20} />
          </div>
          <div className="nt-auth nt-auth--signin nt-fade-up">
            <div className="flex flex-col gap-1">
              <h1 className="nt-auth__title">{title}</h1>
              {badge}
            </div>
            {children}
          </div>
        </main>
      </div>
    </div>
  );
}

/** Switches between the two console languages. */
export function LanguageChip() {
  const { t, locale, setLocale } = useI18n();
  return (
    <Button variant="ghost" size="sm" aria-label={t("common.switchLanguage")} onPress={() => setLocale(locale === "zh-CN" ? "en" : "zh-CN")}>
      <Globe aria-hidden="true" width={14} height={14} />
      {t("common.otherLanguage")}
    </Button>
  );
}

export function TextInput({
  label,
  value,
  onChange,
  error,
  placeholder,
  autoComplete,
  type = "text",
  autoFocus,
  isRequired,
  name,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  placeholder?: string;
  autoComplete?: string;
  type?: "text" | "email";
  autoFocus?: boolean;
  isRequired?: boolean;
  name: string;
}) {
  return (
    <TextField
      name={name}
      value={value}
      onChange={onChange}
      isInvalid={!!error}
      isRequired={isRequired}
      type={type}
      autoComplete={autoComplete}
      autoFocus={autoFocus}
      fullWidth
    >
      <Label>{label}</Label>
      <Input placeholder={placeholder} />
      <FieldError>{error}</FieldError>
    </TextField>
  );
}

export function PasswordInput({
  label,
  value,
  onChange,
  error,
  placeholder,
  autoComplete,
  labelEnd,
  name,
}: {
  label: string;
  value: string;
  onChange: (v: string) => void;
  error?: string;
  placeholder?: string;
  autoComplete: "current-password" | "new-password";
  labelEnd?: ReactNode;
  name: string;
}) {
  const { t } = useI18n();
  const [shown, setShown] = useState(false);
  return (
    <TextField
      name={name}
      value={value}
      onChange={onChange}
      isInvalid={!!error}
      isRequired
      type={shown ? "text" : "password"}
      autoComplete={autoComplete}
      fullWidth
    >
      <div className="flex items-center justify-between gap-3">
        <Label>{label}</Label>
        {labelEnd}
      </div>
      <InputGroup>
        <InputGroup.Input placeholder={placeholder} />
        <InputGroup.Suffix className="pe-0">
          <ToggleButton
            isIconOnly
            size="sm"
            variant="ghost"
            aria-label={shown ? t("auth.hidePassword") : t("auth.showPassword")}
            isSelected={shown}
            onChange={setShown}
          >
            {shown ? <EyeSlash aria-hidden="true" width={16} height={16} /> : <Eye aria-hidden="true" width={16} height={16} />}
          </ToggleButton>
        </InputGroup.Suffix>
      </InputGroup>
      <FieldError>{error}</FieldError>
    </TextField>
  );
}

/** A form-level error, e.g. wrong credentials. */
export function FormError({ children }: { children: ReactNode }) {
  return (
    <Alert status="danger" role="alert">
      <Alert.Indicator />
      <Alert.Content>
        <Alert.Description>{children}</Alert.Description>
      </Alert.Content>
    </Alert>
  );
}

/** Maps an API error on sign-in or sign-up to the message to show. */
export function authErrorMessage(err: ApiError): MessageKey {
  switch (err.code) {
    case "unauthenticated":
      return "auth.errors.invalidCredentials";
    case "rate_limited":
      return "auth.errors.rateLimited";
    case "username_taken":
      return "auth.errors.usernameTaken";
    case "registration_disabled":
      return "auth.registrationClosed";
    case "origin_not_allowed":
      return "auth.errors.originNotAllowed";
    case "password_login_disabled":
      return "auth.errors.passwordLoginDisabled";
    default:
      return "auth.errors.generic";
  }
}

export const USERNAME_PATTERN = /^[A-Za-z0-9][A-Za-z0-9._-]{1,31}$/;
