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
import { ActionMenu, Avatar, Button, Card, Checkbox, Chip, ConsoleDialog, CopyButton, SearchField, StateCard, Table } from "@lerryn/ui-web";
import { ArrowsRotateRight, Ban, Copy, Ellipsis, Key, Persons, Plus, Shield, ShieldCheck } from "@gravity-ui/icons";
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

/** Accounts: search, create, reset passwords, admin role, disable. */
export function AdminUsersPage() {
  const { t, locale } = useI18n();
  usePageTitle(t("admin.users"));
  const me = useCurrentUser();
  const [q, setQ] = useState("");
  const users = useAdminUsers({ q });
  const [creating, setCreating] = useState(false);
  const [resetting, setResetting] = useState<AdminUser | null>(null);
  const items = users.data?.items ?? [];

  return (
    <Board label={t("admin.users")}>
      <BoardTop title={t("admin.users")}>
        <SearchField aria-label={t("admin.searchUsers")} value={q} onChange={setQ}>
          <SearchField.Group>
            <SearchField.SearchIcon />
            <SearchField.Input className="w-40" />
            <SearchField.ClearButton />
          </SearchField.Group>
        </SearchField>
        <Button size="sm" onPress={() => setCreating(true)}>
          <Plus aria-hidden="true" width={14} height={14} />
          {t("common.create")}
        </Button>
      </BoardTop>
      {users.isError ? (
        <LoadError error={users.error} onRetry={() => void users.refetch()} />
      ) : users.data && items.length === 0 ? (
        <StateCard icon={Persons} title={q ? t("admin.noMatch") : t("admin.noUsers")} />
      ) : items.length > 0 ? (
        <Card className="tpl-card nt-fade-up">
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label={t("admin.users")} className="min-w-[560px]">
                <Table.Header>
                  <Table.Column isRowHeader>{t("admin.users")}</Table.Column>
                  <Table.Column>{t("admin.role")}</Table.Column>
                  <Table.Column>{t("admin.lastSeen")}</Table.Column>
                  <Table.Column className="text-end">
                    <span className="sr-only">{t("admin.rowActions")}</span>
                  </Table.Column>
                </Table.Header>
                <Table.Body>
                  {items.map((u) => {
                    const disabled = !!u.disabled_at;
                    return (
                      <Table.Row key={u.id} id={u.id}>
                        <Table.Cell>
                          <div className="flex min-w-0 items-center gap-3">
                            <Avatar size="sm" color={u.id === me.id ? "accent" : "default"} aria-hidden="true">
                              <Avatar.Fallback>{initialOf(u)}</Avatar.Fallback>
                            </Avatar>
                            <div className="flex min-w-0 flex-col">
                              <span className={disabled ? "truncate text-sm text-muted" : "truncate text-sm font-medium"}>{u.display_name || u.username}</span>
                              <span className="truncate font-mono text-xs text-muted">{u.username}</span>
                            </div>
                          </div>
                        </Table.Cell>
                        <Table.Cell>
                          <div className="flex flex-wrap items-center gap-1">
                            {u.is_admin ? (
                              <Chip size="sm" variant="soft" color="accent">
                                {t("account.admin")}
                              </Chip>
                            ) : null}
                            {u.sso ? (
                              <Chip size="sm" variant="soft">
                                {t("admin.ssoUser")}
                              </Chip>
                            ) : null}
                          </div>
                        </Table.Cell>
                        <Table.Cell className="text-muted">
                          {disabled ? (
                            <Chip size="sm" variant="soft">
                              {t("admin.disabledOn", { date: formatDate(u.disabled_at!, locale) })}
                            </Chip>
                          ) : u.last_seen_at ? (
                            formatAgo(u.last_seen_at, locale)
                          ) : (
                            t("tokens.neverUsed")
                          )}
                        </Table.Cell>
                        <Table.Cell>
                          <UserActions user={u} self={u.id === me.id} onReset={() => setResetting(u)} />
                        </Table.Cell>
                      </Table.Row>
                    );
                  })}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        </Card>
      ) : null}
      <LoadMore show={!!users.data?.hasMore} pending={users.isFetchingNextPage} onPress={() => void users.fetchNextPage()} />
      <CreateUserDialog isOpen={creating} onClose={() => setCreating(false)} />
      <ResetPasswordDialog user={resetting} onClose={() => setResetting(null)} />
    </Board>
  );
}

/** What an admin can do to one account; nothing for their own, so they cannot lock themselves out. */
function UserActions({ user: u, self, onReset }: { user: AdminUser; self: boolean; onReset: () => void }) {
  const { t } = useI18n();
  const update = useAdminUpdateUser();
  return (
    <div className="flex items-center justify-end gap-2">
      {update.isError ? <span className="text-xs text-danger-soft-foreground">{t(writeErrorMessage(update.error))}</span> : null}
      {u.disabled_at ? (
        <Button variant="secondary" size="sm" isPending={update.isPending} onPress={() => update.mutate({ userId: u.id, body: { disabled: false } })}>
          {t("admin.restore")}
        </Button>
      ) : self ? null : (
        <ActionMenu
          label={t("admin.actionsFor", { name: u.display_name || u.username })}
          icon={Ellipsis}
          items={[
            ...(u.has_password ? [{ label: t("admin.resetPassword"), icon: Key, onSelect: onReset }] : []),
            u.is_admin
              ? { label: t("admin.revokeAdmin"), icon: Shield, onSelect: () => update.mutate({ userId: u.id, body: { is_admin: false } }) }
              : { label: t("admin.makeAdmin"), icon: ShieldCheck, onSelect: () => update.mutate({ userId: u.id, body: { is_admin: true } }) },
            { label: t("admin.disable"), icon: Ban, danger: true, onSelect: () => update.mutate({ userId: u.id, body: { disabled: true } }) },
          ]}
        />
      )}
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
        <div className="flex flex-col gap-3">
          <div className="lr-copy lr-copy--wrap">
            <code>{createdPassword}</code>
            <CopyButton value={createdPassword} label={t("admin.copyPassword")} icon={Copy} />
          </div>
          <p className="text-sm text-muted">{t("admin.passwordOnce")}</p>
        </div>
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
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <TextInput name="username" label={t("auth.username")} value={username} onChange={setUsername} autoFocus placeholder={t("auth.usernamePlaceholder")} error={error ?? (taken ? t("auth.errors.usernameTaken") : undefined)} />
        <div className="flex flex-col gap-1">
          <span className="text-sm font-medium">{t("admin.initialPassword")}</span>
          <div className="lr-copy">
            <code>{password}</code>
            <Button variant="ghost" size="sm" isIconOnly isDisabled={create.isPending} aria-label={t("admin.regenerate")} onPress={() => setPassword(generatePassword())}>
              <ArrowsRotateRight aria-hidden="true" width={16} height={16} />
            </Button>
          </div>
        </div>
        <Checkbox isSelected={admin} onChange={setAdmin}>
          <Checkbox.Content>
            <Checkbox.Control>
              <Checkbox.Indicator />
            </Checkbox.Control>
            {t("account.admin")}
          </Checkbox.Content>
        </Checkbox>
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
      <div className="flex flex-col gap-3">
        <div className="lr-copy lr-copy--wrap">
          <code>{shownPassword}</code>
          {reset.isSuccess ? (
            <CopyButton value={shownPassword} label={t("admin.copyPassword")} icon={Copy} />
          ) : (
            <Button variant="ghost" size="sm" isIconOnly isDisabled={reset.isPending} aria-label={t("admin.regenerate")} onPress={() => setPassword(generatePassword())}>
              <ArrowsRotateRight aria-hidden="true" width={16} height={16} />
            </Button>
          )}
        </div>
        <p className="text-sm text-muted">{reset.isSuccess ? t("admin.passwordOnce") : t("admin.resetBody")}</p>
        {reset.error ? <FormError>{t(writeErrorMessage(reset.error))}</FormError> : null}
      </div>
    </ConsoleDialog>
  );
}
