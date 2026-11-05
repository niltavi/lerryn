import {
  formatAgo,
  formatDate,
  isApiError,
  useAdminCreateUser,
  useAdminResetPassword,
  useAdminUpdateUser,
  useAdminUsers,
  useI18n,
  type AdminUser,
} from "@lerryn/core";
import { ActionMenu, Button, ConsoleDialog, CopyButton, StateCard, ToneGlyph } from "@lerryn/ui-web";
import { Ban, Building2, Copy, Ellipsis, KeyRound, Plus, RefreshCw, Search, ShieldCheck, ShieldOff, Users } from "lucide-react";
import { useState, type FormEvent } from "react";

import { initialOf, useCurrentUser } from "../../shell/AppShell";
import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";
import { FormError, TextInput, USERNAME_PATTERN } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

/** A readable random password for a new account or a reset, from the browser's CSPRNG. */
export function generatePassword(): string {
  const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789";
  const bytes = crypto.getRandomValues(new Uint8Array(12));
  const chars = Array.from(bytes, (b) => alphabet[b % alphabet.length]).join("");
  return `${chars.slice(0, 4)}-${chars.slice(4, 8)}-${chars.slice(8, 12)}`;
}

/** Accounts (prototype Admin-Users.dc.html): search, create, reset passwords, admin role, disable. */
export function AdminUsersPage() {
  const { t } = useI18n();
  usePageTitle(t("admin.users"));
  const [q, setQ] = useState("");
  const users = useAdminUsers({ q });
  const [creating, setCreating] = useState(false);
  const [resetting, setResetting] = useState<AdminUser | null>(null);
  const items = users.data?.items ?? [];

  return (
    <Board label={t("admin.users")}>
      <BoardTop title={t("admin.users")}>
        <div className="tools" style={{ gap: 8 }}>
          <label className="log-find" style={{ height: 36 }}>
            <Search className="wl-icon" aria-hidden="true" />
            <input type="search" value={q} onChange={(e) => setQ(e.target.value)} aria-label={t("admin.searchUsers")} style={{ width: 140, fontFamily: "var(--font-sans)", fontSize: 13 }} />
          </label>
          <Button size="sm" onPress={() => setCreating(true)}>
            <Plus className="wl-icon" aria-hidden="true" />
            {t("common.create")}
          </Button>
        </div>
      </BoardTop>
      {users.isError ? (
        <LoadError error={users.error} onRetry={() => void users.refetch()} />
      ) : users.data && items.length === 0 ? (
        <StateCard icon={Users} title={q ? t("admin.noMatch") : t("admin.noUsers")} />
      ) : items.length > 0 ? (
        <div className="rows" role="list" aria-label={t("admin.users")}>
          {items.map((u) => (
            <UserRow key={u.id} user={u} onReset={() => setResetting(u)} />
          ))}
        </div>
      ) : null}
      <LoadMore show={!!users.data?.hasMore} pending={users.isFetchingNextPage} onPress={() => void users.fetchNextPage()} />
      <CreateUserDialog isOpen={creating} onClose={() => setCreating(false)} />
      <ResetPasswordDialog user={resetting} onClose={() => setResetting(null)} />
    </Board>
  );
}

function UserRow({ user: u, onReset }: { user: AdminUser; onReset: () => void }) {
  const { t, locale } = useI18n();
  const me = useCurrentUser();
  const update = useAdminUpdateUser();
  const self = u.id === me.id;
  const name = u.display_name || u.username;
  const disabled = !!u.disabled_at;
  return (
    <div className={disabled ? "rowi rowi--quiet" : "rowi"} role="listitem">
      <span className={self ? "avatar avatar--md" : "avatar avatar--md avatar--quiet"} aria-hidden="true" style={disabled ? { color: "var(--ink-muted)" } : undefined}>
        {initialOf(u)}
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name">{name}</span>
        <span className="rowi-sub mono-sm">{u.username}</span>
      </span>
      <span className="rowi-end">
        {u.is_admin ? <span className="tag tag--kraft">{t("account.admin")}</span> : null}
        {u.sso ? (
          <span className="muted" role="img" aria-label={t("admin.ssoUser")} data-tip={t("admin.ssoUser")} data-tip-pos="top">
            <Building2 className="wl-icon" width={16} aria-hidden="true" />
          </span>
        ) : null}
        {disabled ? (
          <>
            <ToneGlyph tone="idle" icon={Ban} label={t("admin.disabledOn", { date: formatDate(u.disabled_at!, locale) })} />
            <Button variant="secondary" size="sm" isPending={update.isPending} onPress={() => update.mutate({ userId: u.id, body: { disabled: false } })}>
              {t("admin.restore")}
            </Button>
          </>
        ) : (
          <>
            <span className="caption muted">{u.last_seen_at ? formatAgo(u.last_seen_at, locale) : t("tokens.neverUsed")}</span>
            {self ? null : (
              <span className="acts">
                <ActionMenu
                  label={t("admin.actionsFor", { name })}
                  icon={Ellipsis}
                  items={[
                    ...(u.has_password ? [{ label: t("admin.resetPassword"), icon: KeyRound, onSelect: onReset }] : []),
                    u.is_admin
                      ? { label: t("admin.revokeAdmin"), icon: ShieldOff, onSelect: () => update.mutate({ userId: u.id, body: { is_admin: false } }) }
                      : { label: t("admin.makeAdmin"), icon: ShieldCheck, onSelect: () => update.mutate({ userId: u.id, body: { is_admin: true } }) },
                    { label: t("admin.disable"), icon: Ban, danger: true, onSelect: () => update.mutate({ userId: u.id, body: { disabled: true } }) },
                  ]}
                />
              </span>
            )}
          </>
        )}
        {update.isError ? <span className="caption" style={{ color: "var(--danger)" }}>{t(writeErrorMessage(update.error))}</span> : null}
      </span>
    </div>
  );
}

function CreateUserDialog({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const create = useAdminCreateUser();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState(generatePassword);
  const [admin, setAdmin] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const created = create.data;
  const createdPassword = create.variables?.password ?? "";
  const close = () => {
    if (create.isPending) return;
    create.reset();
    setUsername("");
    setPassword(generatePassword());
    setAdmin(false);
    setError(undefined);
    onClose();
  };
  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (create.isPending) return;
    const name = username.trim();
    const err = !name ? t("auth.errors.required") : USERNAME_PATTERN.test(name) ? undefined : t("auth.errors.usernameFormat");
    setError(err);
    if (err) return;
    create.mutate({ username: name, password, is_admin: admin });
  };
  const taken = isApiError(create.error) && create.error.code === "username_taken";

  if (created) {
    return (
      <ConsoleDialog
        isOpen={isOpen}
        isDismissable={false}
        onOpenChange={(open) => (open ? undefined : close())}
        title={t("admin.createdTitle", { name: created.username })}
        footer={<Button onPress={close}>{t("common.done")}</Button>}
      >
        <div className="copyline copyline--wrap">
          <code>{createdPassword}</code>
          <CopyButton value={createdPassword} label={t("admin.copyPassword")} icon={Copy} />
        </div>
        <p className="body-sm muted">{t("admin.passwordOnce")}</p>
      </ConsoleDialog>
    );
  }

  return (
    <ConsoleDialog
      isOpen={isOpen}
      isDismissable={!create.isPending}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t("admin.createTitle")}
      footer={
        <>
          <Button variant="ghost" onPress={close} isDisabled={create.isPending}>
            {t("common.cancel")}
          </Button>
          <Button onPress={() => submit()} isPending={create.isPending}>
            {t("common.create")}
          </Button>
        </>
      }
    >
      <form className="stack" style={{ gap: "var(--space-5)" }} onSubmit={submit}>
        <TextInput name="username" label={t("auth.username")} value={username} onChange={setUsername} autoFocus placeholder={t("auth.usernamePlaceholder")} error={error ?? (taken ? t("auth.errors.usernameTaken") : undefined)} />
        <div className="stack" style={{ gap: 6 }}>
          <span className="label-text">{t("admin.initialPassword")}</span>
          <div className="copyline">
            <code>{password}</code>
            <Button variant="ghost" size="sm" isIconOnly isDisabled={create.isPending} aria-label={t("admin.regenerate")} onPress={() => setPassword(generatePassword())}>
              <RefreshCw className="wl-icon" />
            </Button>
          </div>
        </div>
        <label className="check">
          <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} />
          {t("account.admin")}
        </label>
        {create.error && !taken ? <FormError>{t(writeErrorMessage(create.error))}</FormError> : null}
        <button type="submit" hidden />
      </form>
    </ConsoleDialog>
  );
}

function ResetPasswordDialog({ user, onClose }: { user: AdminUser | null; onClose: () => void }) {
  const { t } = useI18n();
  const reset = useAdminResetPassword();
  const [password, setPassword] = useState(generatePassword);
  const close = () => {
    if (reset.isPending) return;
    reset.reset();
    setPassword(generatePassword());
    onClose();
  };
  const name = user ? user.display_name || user.username : "";
  const shownPassword = reset.isSuccess ? reset.variables?.password ?? "" : password;
  return (
    <ConsoleDialog
      isOpen={!!user}
      isDismissable={!reset.isPending && !reset.isSuccess}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t("admin.resetTitle", { name })}
      footer={
        reset.isSuccess ? (
          <Button onPress={close}>{t("common.done")}</Button>
        ) : (
          <>
            <Button variant="ghost" onPress={close} isDisabled={reset.isPending}>
              {t("common.cancel")}
            </Button>
            <Button variant="danger" isPending={reset.isPending} onPress={() => user && reset.mutate({ userId: user.id, password })}>
              {t("admin.resetPassword")}
            </Button>
          </>
        )
      }
    >
      <div className="copyline copyline--wrap">
        <code>{shownPassword}</code>
        {reset.isSuccess ? (
          <CopyButton value={shownPassword} label={t("admin.copyPassword")} icon={Copy} />
        ) : (
          <Button variant="ghost" size="sm" isIconOnly isDisabled={reset.isPending} aria-label={t("admin.regenerate")} onPress={() => setPassword(generatePassword())}>
            <RefreshCw className="wl-icon" />
          </Button>
        )}
      </div>
      <p className="body-sm muted">{reset.isSuccess ? t("admin.passwordOnce") : t("admin.resetBody")}</p>
      {reset.error ? <FormError>{t(writeErrorMessage(reset.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
