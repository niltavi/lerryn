import {
  daysUntil,
  formatAgo,
  formatFullDate,
  useAccessTokens,
  useCreateAccessToken,
  useI18n,
  useRevokeAccessToken,
  type AccessToken,
  type CreatedToken,
} from "@lerryn/core";
import type { Schemas } from "@lerryn/api";
import {
  Button,
  ConsoleDialog,
  CopyButton,
  ErrorState,
  IconButton,
  StateCard,
  WaveGlyph,
} from "@lerryn/ui-web";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { Ban, BookOpen, Check, Clock, Copy, PenLine, Play, Plus, type LucideIcon } from "lucide-react";
import { useState, type FormEvent } from "react";

import { origin } from "../../lib/hooks";
import { Board, usePageTitle } from "../../shell/layout";
import { FormError, TextInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

type Scope = Schemas["TokenScope"];
const SCOPES: { scope: Scope; icon: LucideIcon }[] = [
  { scope: "read", icon: BookOpen },
  { scope: "write", icon: PenLine },
  { scope: "execute", icon: Play },
];
/** Expiry choices in days; null means no expiry. */
const EXPIRY = [30, 90, 365, null] as const;
/** Tokens expiring within this many days are highlighted. */
const SOON_DAYS = 30;

export type TokenState = "active" | "expired" | "revoked";

export function tokenState(token: AccessToken, now = Date.now()): TokenState {
  if (token.revoked_at) return "revoked";
  if (token.expires_at && daysUntil(token.expires_at, now) <= 0) return "expired";
  return "active";
}

export function TokensPage() {
  const { t } = useI18n();
  usePageTitle(t("tokens.title"));
  const search = useSearch({ from: "/app/settings/tokens" });
  const navigate = useNavigate();
  const tokens = useAccessTokens();
  const [creating, setCreating] = useState(!!search.new);
  const [revoking, setRevoking] = useState<AccessToken | null>(null);

  const openCreate = (open: boolean) => {
    setCreating(open);
    // Drop ?new=1 so a reload does not reopen the dialog.
    if (!open && search.new) void navigate({ to: "/settings/tokens", search: {}, replace: true });
  };

  return (
    <Board label={t("tokens.title")}>
      <div className="board-top">
        <h1 className="h2">{t("tokens.title")}</h1>
        <Button size="sm" onPress={() => openCreate(true)}>
          <Plus className="wl-icon" aria-hidden="true" />
          {t("tokens.new")}
        </Button>
      </div>

      {tokens.isError ? (
        <ErrorState
          action={
            <Button size="sm" variant="secondary" onPress={() => void tokens.refetch()}>
              {t("common.retry")}
            </Button>
          }
        />
      ) : tokens.data && tokens.data.items.length === 0 ? (
        <StateCard icon={Clock} title={t("tokens.empty")} />
      ) : tokens.data ? (
        <div className="rows" role="list" aria-label={t("tokens.title")}>
          {tokens.data.items.map((token) => (
            <TokenRow key={token.id} token={token} onRevoke={() => setRevoking(token)} />
          ))}
        </div>
      ) : null}
      {tokens.data?.hasMore ? (
        <div>
          <Button variant="ghost" size="sm" onPress={() => void tokens.fetchNextPage()} isPending={tokens.isFetchingNextPage}>
            {t("log.loadMore")}
          </Button>
        </div>
      ) : null}

      <ConnectSection />

      <CreateTokenDialog isOpen={creating} onOpenChange={openCreate} />
      <RevokeTokenDialog token={revoking} onClose={() => setRevoking(null)} />
    </Board>
  );
}

function TokenRow({ token, onRevoke }: { token: AccessToken; onRevoke: () => void }) {
  const { t, locale } = useI18n();
  const state = tokenState(token);
  const days = token.expires_at ? daysUntil(token.expires_at) : null;
  const soon = state === "active" && days !== null && days <= SOON_DAYS;
  const used = token.last_used_at ? formatAgo(token.last_used_at, locale) : t("tokens.neverUsed");
  const sub = state === "revoked" ? t("tokens.revoked") : state === "expired" ? t("tokens.expired") : used;

  return (
    <div className={state === "active" ? "rowi" : "rowi rowi--quiet"} role="listitem">
      <span className="ic-box" style={state === "active" ? undefined : { color: "var(--ink-muted)" }} aria-hidden="true">
        {state === "active" ? <WaveGlyph size={18} /> : state === "revoked" ? <Ban className="wl-icon" width={18} /> : <Clock className="wl-icon" width={18} />}
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name">{token.name}</span>
        <span className="rowi-sub">
          <span className="mono-sm">{token.prefix}••••</span> · {sub}
        </span>
      </span>
      <span className="rowi-end">
        {state === "active" ? (
          <span className="scopes">
            {SCOPES.map(({ scope, icon: Icon }) => {
              const on = token.scopes.includes(scope);
              return on ? (
                <span key={scope} data-tip={t(`tokens.scope.${scope}`)} data-tip-pos="top" aria-label={t(`tokens.scope.${scope}`)} role="img">
                  <Icon className="wl-icon" />
                </span>
              ) : (
                <span key={scope} className="off" aria-hidden="true">
                  <Icon className="wl-icon" />
                </span>
              );
            })}
          </span>
        ) : null}
        <span
          className="mono-sm"
          style={soon ? { color: "var(--warning-ink)" } : state === "active" ? { color: "var(--ink-muted)" } : undefined}
          data-tip={soon && days !== null ? t("tokens.expiresIn", { count: days }) : undefined}
          data-tip-pos="top"
        >
          {token.expires_at ? formatFullDate(token.expires_at, locale) : t("tokens.noExpiry")}
        </span>
        {state === "active" ? (
          <span className="acts">
            <IconButton
              variant="danger-soft"
              label={t("tokens.revokeNamed", { name: token.name })}
              tip={t("tokens.revoke")}
              icon={Ban}
              onPress={onRevoke}
            />
          </span>
        ) : null}
      </span>
    </div>
  );
}

function ConnectSection() {
  const { t } = useI18n();
  const base = origin();
  const lines = [
    { tag: "MCP", value: `${base}/mcp`, label: t("tokens.copyMcp") },
    { tag: "API", value: `${base}/api/v1`, label: t("tokens.copyApi") },
    { tag: "CLI", value: `lerryn login ${base}`, label: t("tokens.copyCli") },
    // MCP clients read the same skills as resources; the CLI installs them for the rest.
    { tag: "Skill", value: "lerryn skills install", label: t("tokens.copySkills") },
  ];
  return (
    <section className="stack" style={{ gap: 10 }} aria-label={t("tokens.connect")}>
      <span className="sec-label">{t("tokens.connect")}</span>
      <div className="copygrid">
        {lines.map((l) => (
          <div className="copyline" key={l.tag}>
            <span className="tag tag--mono" style={{ background: "var(--liner)" }}>
              {l.tag}
            </span>
            <code>{l.value}</code>
            <CopyButton value={l.value} label={l.label} icon={Copy} />
          </div>
        ))}
      </div>
    </section>
  );
}

function CreateTokenDialog({ isOpen, onOpenChange }: { isOpen: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n();
  const create = useCreateAccessToken();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(["read", "write", "execute"]);
  const [expiry, setExpiry] = useState<(typeof EXPIRY)[number]>(90);
  const [errors, setErrors] = useState<{ name?: string; scopes?: string }>({});
  const created: CreatedToken | undefined = create.data;

  const close = () => {
    if (create.isPending) return;
    onOpenChange(false);
    // Forget the plaintext and the form once the dialog is gone.
    create.reset();
    setName("");
    setScopes(["read", "write", "execute"]);
    setExpiry(90);
    setErrors({});
  };

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (create.isPending) return;
    const next = {
      name: name.trim() ? undefined : t("tokens.errors.nameRequired"),
      scopes: scopes.length ? undefined : t("tokens.errors.scopeRequired"),
    };
    setErrors(next);
    if (next.name || next.scopes) return;
    create.mutate({ name: name.trim(), scopes, ...(expiry ? { expires_in_days: expiry } : {}) });
  };

  if (created) {
    return (
      <ConsoleDialog
        isOpen={isOpen}
        onOpenChange={(open) => (open ? undefined : close())}
        isDismissable={false}
        title={
          <>
            <span className="g g--ok g--lg" aria-hidden="true">
              <Check className="wl-icon" />
            </span>
            {t("tokens.onceTitle")}
          </>
        }
        footer={<Button onPress={close}>{t("common.done")}</Button>}
      >
        <div className="copyline copyline--wrap">
          <code data-testid="created-token">{created.token}</code>
          <CopyButton value={created.token} label={t("tokens.copyToken")} icon={Copy} />
        </div>
      </ConsoleDialog>
    );
  }

  const expiryLabel = (d: (typeof EXPIRY)[number]) =>
    d === null ? t("tokens.expiryNever") : d === 365 ? t("tokens.expiryYear") : t("tokens.expiryDays", { count: d });

  return (
    <ConsoleDialog
      isOpen={isOpen}
      onOpenChange={(open) => (open ? onOpenChange(true) : close())}
      isDismissable={!create.isPending}
      title={t("tokens.createTitle")}
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
        <TextInput
          name="name"
          label={t("tokens.name")}
          value={name}
          onChange={setName}
          placeholder={t("tokens.namePlaceholder")}
          autoFocus
          error={errors.name}
        />
        <fieldset className="stack" style={{ gap: 8, border: 0, margin: 0, padding: 0 }}>
          <legend className="label-text" style={{ marginBottom: 8 }}>
            {t("tokens.scopes")}
          </legend>
          <div className="row">
            {SCOPES.map(({ scope, icon: Icon }) => (
              <label className="choice" key={scope}>
                <input
                  type="checkbox"
                  checked={scopes.includes(scope)}
                  onChange={(e) =>
                    setScopes((s) => (e.target.checked ? [...s, scope] : s.filter((x) => x !== scope)))
                  }
                />
                <Icon className="wl-icon" aria-hidden="true" />
                {t(`tokens.scope.${scope}`)}
              </label>
            ))}
          </div>
          {errors.scopes ? <span className="caption" style={{ color: "var(--danger)" }}>{errors.scopes}</span> : null}
        </fieldset>
        <div className="stack" style={{ gap: 8 }}>
          <span className="label-text" id="expiry-label">
            {t("tokens.expiry")}
          </span>
          <div className="seg seg--block" role="group" aria-labelledby="expiry-label">
            {EXPIRY.map((d) => (
              <button key={String(d)} type="button" aria-pressed={expiry === d} onClick={() => setExpiry(d)}>
                {expiryLabel(d)}
              </button>
            ))}
          </div>
        </div>
        {create.error ? <FormError>{t(writeErrorMessage(create.error))}</FormError> : null}
        {/* Enter in the name field submits. */}
        <button type="submit" hidden disabled={create.isPending} />
      </form>
    </ConsoleDialog>
  );
}

function RevokeTokenDialog({ token, onClose }: { token: AccessToken | null; onClose: () => void }) {
  const { t } = useI18n();
  const revoke = useRevokeAccessToken();
  return (
    <ConsoleDialog
      isOpen={!!token}
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={
        <>
          <span className="g g--bad g--lg" aria-hidden="true">
            <Ban className="wl-icon" />
          </span>
          {token ? t("tokens.revokeTitle", { name: token.name }) : null}
        </>
      }
      footer={
        <>
          <Button variant="ghost" onPress={onClose}>
            {t("common.cancel")}
          </Button>
          <Button
            variant="danger"
            isPending={revoke.isPending}
            onPress={() => token && revoke.mutate(token.id, { onSuccess: onClose })}
          >
            {t("tokens.revoke")}
          </Button>
        </>
      }
    >
      <p className="body-sm muted">{t("tokens.revokeBody")}</p>
      {revoke.error ? <FormError>{t(writeErrorMessage(revoke.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
