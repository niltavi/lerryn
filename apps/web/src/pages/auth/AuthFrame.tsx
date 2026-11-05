import { useI18n, type ApiError, type MessageKey } from "@lerryn/core";
import { FieldError, Input, Label, Logo, TextField } from "@lerryn/ui-web";
import { CircleAlert, Eye, EyeOff, Languages } from "lucide-react";
import { useState, type ReactNode } from "react";

import { usePageTitle } from "../../shell/layout";

/** The centred single column of the sign-in and sign-up pages. */
export function AuthFrame({ title, badge, children }: { title: string; badge?: ReactNode; children: ReactNode }) {
  usePageTitle(title);
  return (
    <div className="wl-app">
      <main className="auth">
        <div className="auth-col">
          <div className="stack" style={{ alignItems: "center", gap: 14 }}>
            <span className="auth-logo">
              <Logo size={17} />
            </span>
            <h1 className="h2">{title}</h1>
            {badge}
          </div>
          {children}
        </div>
      </main>
    </div>
  );
}

/** Switches between the two console languages. */
export function LanguageChip() {
  const { t, locale, setLocale } = useI18n();
  return (
    <button
      className="chip chip--sm"
      type="button"
      aria-label={t("common.switchLanguage")}
      onClick={() => setLocale(locale === "zh-CN" ? "en" : "zh-CN")}
    >
      <Languages className="wl-icon" aria-hidden="true" />
      {t("common.otherLanguage")}
    </button>
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
      <div className="spread">
        <Label>{label}</Label>
        {labelEnd}
      </div>
      <div className="pw-wrap">
        <Input placeholder={placeholder} />
        <button
          className="pw-eye"
          type="button"
          aria-label={shown ? t("auth.hidePassword") : t("auth.showPassword")}
          aria-pressed={shown}
          onClick={() => setShown((s) => !s)}
        >
          {shown ? <EyeOff className="wl-icon" width={18} aria-hidden="true" /> : <Eye className="wl-icon" width={18} aria-hidden="true" />}
        </button>
      </div>
      <FieldError>{error}</FieldError>
    </TextField>
  );
}

/** A form-level error, e.g. wrong credentials. */
export function FormError({ children }: { children: ReactNode }) {
  return (
    <div className="form-error" role="alert">
      <CircleAlert className="wl-icon" aria-hidden="true" />
      <span>{children}</span>
    </div>
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
