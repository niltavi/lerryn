import { isApiError, useDiscovery, useFirstAccountMode, useI18n, useRegister, type RegisterRequest } from "@lerryn/core";
import { Button, Form } from "@lerryn/ui-web";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { ShieldCheck } from "lucide-react";
import { useState, type FormEvent } from "react";

import {
  AuthFrame,
  FormError,
  LanguageChip,
  PasswordInput,
  TextInput,
  USERNAME_PATTERN,
  authErrorMessage,
} from "./AuthFrame";

type Field = "username" | "password";

export function RegisterPage() {
  const { t } = useI18n();
  const search = useSearch({ from: "/register" });
  const navigate = useNavigate();
  const discovery = useDiscovery();
  const firstAccount = useFirstAccountMode();
  const register = useRegister();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [errors, setErrors] = useState<Partial<Record<Field, string>>>({});

  const closed =
    discovery.data?.capabilities.registration_open === false ||
    (isApiError(register.error) && register.error.code === "registration_disabled");
  const takenError = isApiError(register.error) && register.error.code === "username_taken";

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const next: Partial<Record<Field, string>> = {};
    const name = username.trim();
    if (!name) next.username = t("auth.errors.required");
    else if (!USERNAME_PATTERN.test(name)) next.username = t("auth.errors.usernameFormat");
    if (!password) next.password = t("auth.errors.required");
    else if (password.length < 8) next.password = t("auth.errors.passwordLength");
    setErrors(next);
    if (Object.keys(next).length) return;
    const body: RegisterRequest = { username: name, password };
    if (displayName.trim()) body.display_name = displayName.trim();
    register.mutate(body, { onSuccess: () => void navigate({ href: search.redirect ?? "/", replace: true }) });
  };

  return (
    <AuthFrame
      title={t("auth.register")}
      badge={
        // Shown only when the server says the deployment has no account yet.
        firstAccount.data === true ? (
          <span className="tag tag--kraft">
            <ShieldCheck className="wl-icon" width={14} aria-hidden="true" />
            {t("auth.firstAccount")}
          </span>
        ) : null
      }
    >
      {closed ? (
        <FormError>{t("auth.registrationClosed")}</FormError>
      ) : (
        <Form className="stack" style={{ gap: 14 }} onSubmit={submit} validationBehavior="aria">
          <TextInput
            name="username"
            label={t("auth.username")}
            value={username}
            onChange={(v) => {
              setUsername(v);
              if (errors.username) setErrors((e) => ({ ...e, username: undefined }));
            }}
            placeholder={t("auth.usernamePlaceholder")}
            autoComplete="username"
            autoFocus
            error={errors.username ?? (takenError ? t("auth.errors.usernameTaken") : undefined)}
          />
          <PasswordInput
            name="password"
            label={t("auth.password")}
            value={password}
            onChange={setPassword}
            placeholder={t("auth.passwordPlaceholder")}
            autoComplete="new-password"
            error={errors.password}
          />
          <TextInput
            name="display_name"
            label={t("auth.displayName")}
            value={displayName}
            onChange={setDisplayName}
            placeholder={t("auth.optional")}
            autoComplete="nickname"
          />
          {register.error && !takenError ? (
            <FormError>{t(isApiError(register.error) ? authErrorMessage(register.error) : "auth.errors.generic")}</FormError>
          ) : null}
          <Button type="submit" size="lg" fullWidth isPending={register.isPending}>
            {t("auth.register")}
          </Button>
        </Form>
      )}
      <div className="spread" style={{ justifyContent: "center", gap: 20 }}>
        <Link to="/login" search={search.redirect ? { redirect: search.redirect } : {}} className="link body-sm">
          {t("auth.haveAccount")}
        </Link>
        <LanguageChip />
      </div>
    </AuthFrame>
  );
}
