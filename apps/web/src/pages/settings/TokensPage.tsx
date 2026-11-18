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
  Card,
  Checkbox,
  CheckboxGroup,
  Chip,
  ConsoleDialog,
  CopyButton,
  ErrorState,
  FieldError,
  IconButton,
  Label,
  StateCard,
  Table,
  ToggleButton,
  ToggleButtonGroup,
  WaveGlyph,
} from "@lerryn/ui-web";
import { Ban, BookOpen, Check, Clock, Copy, PencilToLine, Play, Plus } from "@gravity-ui/icons";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useId, useState, type ComponentType, type FormEvent, type SVGProps } from "react";

import { origin } from "../../lib/hooks";
import { Board, BoardTop, Section, usePageTitle } from "../../shell/layout";
import { FormError, TextInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

type Scope = Schemas["TokenScope"];
const SCOPES: { scope: Scope; icon: ComponentType<SVGProps<SVGSVGElement>> }[] = [
  { scope: "read", icon: BookOpen },
  { scope: "write", icon: PencilToLine },
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
  const search = useSearch({ from: "/app/settings/_key/tokens" });
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
      <BoardTop title={t("tokens.title")}>
        <Button size="sm" onPress={() => openCreate(true)}>
          <Plus aria-hidden="true" width={14} height={14} />
          {t("tokens.new")}
        </Button>
      </BoardTop>

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
        <Card className="tpl-card nt-fade-up">
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label={t("tokens.title")} className="min-w-140">
                <Table.Header>
                  <Table.Column isRowHeader>{t("tokens.name")}</Table.Column>
                  <Table.Column>{t("tokens.scopes")}</Table.Column>
                  <Table.Column>{t("tokens.expiry")}</Table.Column>
                  <Table.Column>{t("tokens.status")}</Table.Column>
                  <Table.Column className="text-end">
                    <span className="sr-only">{t("tokens.revoke")}</span>
                  </Table.Column>
                </Table.Header>
                <Table.Body>
                  {tokens.data.items.map((token) => (
                    <TokenRow key={token.id} token={token} onRevoke={() => setRevoking(token)} />
                  ))}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        </Card>
      ) : null}
      {tokens.data?.hasMore ? (
        <div className="flex justify-center">
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

const STATE_CHIP = {
  active: { color: "success", label: "tokens.active" },
  expired: { color: "warning", label: "tokens.expired" },
  revoked: { color: "default", label: "tokens.revoked" },
} as const;

function TokenRow({ token, onRevoke }: { token: AccessToken; onRevoke: () => void }) {
  const { t, locale } = useI18n();
  const state = tokenState(token);
  const active = state === "active";
  const days = token.expires_at ? daysUntil(token.expires_at) : null;
  const soon = active && days !== null && days <= SOON_DAYS;
  const used = token.last_used_at ? formatAgo(token.last_used_at, locale) : t("tokens.neverUsed");

  return (
    <Table.Row id={token.id}>
      <Table.Cell>
        <div className="flex min-w-0 items-center gap-3">
          <span className="lr-row__icon" data-tone={active ? undefined : "idle"} aria-hidden="true">
            {active ? <WaveGlyph size={16} /> : state === "revoked" ? <Ban width={16} height={16} /> : <Clock width={16} height={16} />}
          </span>
          <div className="flex min-w-0 flex-col">
            <span className={active ? "truncate text-sm font-medium" : "truncate text-sm text-muted"}>{token.name}</span>
            <span className="truncate text-xs text-muted">
              <span className="font-mono">{token.prefix}••••</span> · {used}
            </span>
          </div>
        </div>
      </Table.Cell>
      <Table.Cell>
        <div className="flex flex-wrap gap-1">
          {SCOPES.filter(({ scope }) => token.scopes.includes(scope)).map(({ scope, icon: Icon }) => (
            <Chip key={scope} size="sm" variant="soft" color={active ? "accent" : "default"}>
              <Icon aria-hidden="true" width={12} height={12} />
              <Chip.Label>{t(`tokens.scope.${scope}`)}</Chip.Label>
            </Chip>
          ))}
        </div>
      </Table.Cell>
      <Table.Cell>
        <span
          className={soon ? "font-mono text-xs text-warning-soft-foreground" : "font-mono text-xs text-muted"}
          title={soon && days !== null ? t("tokens.expiresIn", { count: days }) : undefined}
        >
          {token.expires_at ? formatFullDate(token.expires_at, locale) : t("tokens.noExpiry")}
        </span>
      </Table.Cell>
      <Table.Cell>
        <Chip size="sm" variant="soft" color={STATE_CHIP[state].color}>
          {t(STATE_CHIP[state].label)}
        </Chip>
      </Table.Cell>
      <Table.Cell className="text-end">
        {active ? (
          <IconButton
            variant="danger-soft"
            label={t("tokens.revokeNamed", { name: token.name })}
            tip={t("tokens.revoke")}
            icon={Ban}
            onPress={onRevoke}
          />
        ) : null}
      </Table.Cell>
    </Table.Row>
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
    <Section title={t("tokens.connect")}>
      <div className="lr-keys__copies">
        {lines.map((l) => (
          <div className="lr-copy" key={l.tag}>
            <span className="lr-copy__tag">{l.tag}</span>
            <code>{l.value}</code>
            <CopyButton value={l.value} label={l.label} icon={Copy} />
          </div>
        ))}
      </div>
    </Section>
  );
}

function CreateTokenDialog({ isOpen, onOpenChange }: { isOpen: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n();
  const create = useCreateAccessToken();
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Scope[]>(["read", "write", "execute"]);
  const [expiry, setExpiry] = useState<(typeof EXPIRY)[number]>(90);
  const [errors, setErrors] = useState<{ name?: string; scopes?: string }>({});
  const expiryLabelId = useId();
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
            <span className="lr-row__icon" data-tone="ok" aria-hidden="true">
              <Check width={16} height={16} />
            </span>
            {t("tokens.onceTitle")}
          </>
        }
        footer={<Button onPress={close}>{t("common.done")}</Button>}
      >
        <div className="lr-copy lr-copy--wrap">
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
      <form className="flex flex-col gap-5" onSubmit={submit}>
        <TextInput
          name="name"
          label={t("tokens.name")}
          value={name}
          onChange={setName}
          placeholder={t("tokens.namePlaceholder")}
          autoFocus
          error={errors.name}
        />
        <CheckboxGroup name="scopes" value={scopes} onChange={(next) => setScopes(next as Scope[])} isInvalid={!!errors.scopes}>
          <Label>{t("tokens.scopes")}</Label>
          <div className="flex flex-wrap gap-x-5 gap-y-2">
            {SCOPES.map(({ scope, icon: Icon }) => (
              <Checkbox key={scope} value={scope}>
                <Checkbox.Content>
                  <Checkbox.Control>
                    <Checkbox.Indicator />
                  </Checkbox.Control>
                  <Icon aria-hidden="true" width={14} height={14} />
                  {t(`tokens.scope.${scope}`)}
                </Checkbox.Content>
              </Checkbox>
            ))}
          </div>
          <FieldError>{errors.scopes}</FieldError>
        </CheckboxGroup>
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium" id={expiryLabelId}>
            {t("tokens.expiry")}
          </span>
          {/* Multiple mode keeps each choice a pressed button; the handler holds it to one. */}
          <ToggleButtonGroup
            aria-labelledby={expiryLabelId}
            size="sm"
            fullWidth
            selectionMode="multiple"
            selectedKeys={[String(expiry)]}
            onSelectionChange={(keys) => {
              const next = EXPIRY.find((d) => d !== expiry && keys.has(String(d)));
              if (next !== undefined) setExpiry(next);
            }}
          >
            {EXPIRY.map((d, index) => (
              <ToggleButton key={String(d)} id={String(d)}>
                {index > 0 ? <ToggleButtonGroup.Separator /> : null}
                {expiryLabel(d)}
              </ToggleButton>
            ))}
          </ToggleButtonGroup>
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
          <span className="lr-row__icon" data-tone="bad" aria-hidden="true">
            <Ban width={16} height={16} />
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
      <p className="text-sm text-muted">{t("tokens.revokeBody")}</p>
      {revoke.error ? <FormError>{t(writeErrorMessage(revoke.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
