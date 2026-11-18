import { formatAgo, formatDate, formatDateTime, useCreateRunnerToken, useI18n, useRunners, useUpdateRunner, type Runner } from "@lerryn/core";
import { ActionMenu, Button, Card, Chip, ConsoleDialog, CopyButton, Meter, StateCard } from "@lerryn/ui-web";
import { Ban, Check, Copy, Ellipsis, LinkSlash, Plus, Server } from "@gravity-ui/icons";
import { useState, type FormEvent } from "react";

import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";
import { FormError, TextInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";
import { PressedGroup } from "./AdminAuditPage";

const EXPIRY_HOURS = [1, 24, 168] as const;

/** Runners: capacity, status, disable, registration tokens. */
export function AdminRunnersPage() {
  const { t } = useI18n();
  usePageTitle(t("admin.runners"));
  const runners = useRunners();
  const [creating, setCreating] = useState(false);
  const items = runners.data?.items ?? [];
  const running = items.reduce((n, r) => n + r.running, 0);
  const slots = items.filter((r) => r.status === "online").reduce((n, r) => n + r.slots, 0);
  const online = items.filter((r) => r.status === "online").length;
  const offline = items.filter((r) => r.status === "offline").length;

  return (
    <Board label={t("admin.runners")}>
      <BoardTop title={t("admin.runners")}>
        <Button size="sm" onPress={() => setCreating(true)}>
          <Plus aria-hidden="true" width={14} height={14} />
          {t("admin.registrationToken")}
        </Button>
      </BoardTop>
      {runners.isError ? (
        <LoadError error={runners.error} onRetry={() => void (runners.isFetchNextPageError ? runners.fetchNextPage({ cancelRefetch: false }) : runners.refetch())} />
      ) : null}
      {runners.data && items.length === 0 && !runners.hasNextPage && !runners.isError ? (
        <StateCard icon={Server} title={t("admin.noRunners")} />
      ) : items.length > 0 ? (
        <>
          <div className="hero tpl-grid tpl-grid--kpi lr-admin-kpis">
            <Card className="tpl-kpi stat">
              <span className="tpl-kpi__label">{t(runners.hasNextPage ? "admin.shownRunningOfCapacity" : "admin.runningOfCapacity")}</span>
              <span className="tpl-kpi__value stat-n">
                {running}
                <small className="lr-admin-kpis__of">/ {slots}</small>
              </span>
            </Card>
            <Card className="tpl-kpi stat">
              <span className="tpl-kpi__label">{t(runners.hasNextPage ? "admin.shownOnline" : "admin.online")}</span>
              <span className="tpl-kpi__value stat-n">{online}</span>
            </Card>
            {offline > 0 ? (
              <Card className="tpl-kpi stat">
                <span className="tpl-kpi__label">{t(runners.hasNextPage ? "admin.shownOffline" : "admin.offline")}</span>
                <span className="tpl-kpi__value stat-n text-danger-soft-foreground">{offline}</span>
              </Card>
            ) : null}
          </div>
          <Card className="tpl-card">
            <div className="lr-rows" role="list" aria-label={t("admin.runners")}>
              {items.map((r) => (
                <RunnerRow key={r.id} runner={r} />
              ))}
            </div>
          </Card>
        </>
      ) : null}
      <LoadMore show={!!runners.hasNextPage && !runners.isError} pending={runners.isFetching} onPress={() => void runners.fetchNextPage({ cancelRefetch: false })} />
      <RunnerTokenDialog isOpen={creating} onClose={() => setCreating(false)} />
    </Board>
  );
}

function RunnerRow({ runner: r }: { runner: Runner }) {
  const { t, locale } = useI18n();
  const update = useUpdateRunner();
  const disabled = r.status === "disabled";
  const offline = r.status === "offline";
  // An offline node says when it was last heard from, since that is what decides whether to disable it.
  const sub = [r.labels.join(" · "), r.version, offline ? formatDateTime(r.last_seen_at, locale) : null].filter(Boolean).join(" · ");
  return (
    <div className="lr-row" role="listitem" data-quiet={disabled || undefined}>
      <span className="lr-row__icon" data-tone={disabled ? "idle" : offline ? "bad" : "ok"} aria-hidden="true">
        {offline ? <LinkSlash width={16} height={16} /> : <Server width={16} height={16} />}
      </span>
      <span className="lr-row__text">
        <span className="lr-row__title font-mono text-sm">{r.name}</span>
        <span className="lr-row__sub font-mono">{sub || "—"}</span>
      </span>
      <span className="lr-row__end">
        {/* A disabled node only shows its slots while runs are still draining from it. */}
        {!disabled || r.running > 0 ? (
          <Meter
            aria-label={t("admin.slotsUsed", { running: r.running, slots: r.slots })}
            value={r.running}
            maxValue={Math.max(r.slots, 1)}
            size="sm"
            color={disabled ? "default" : "accent"}
            className="flex w-28 items-center gap-2"
          >
            <Meter.Track className="flex-1">
              <Meter.Fill />
            </Meter.Track>
            <Meter.Output className="font-mono text-xs font-normal text-muted">
              {r.running}/{r.slots}
            </Meter.Output>
          </Meter>
        ) : null}
        {disabled ? (
          <>
            <Chip size="sm" variant="soft">
              {t("admin.disabledOn", { date: formatDate(r.disabled_at ?? r.last_seen_at, locale) })}
            </Chip>
            <Button variant="secondary" size="sm" isPending={update.isPending} onPress={() => update.mutate({ runnerId: r.id, disabled: false })}>
              {t("admin.enable")}
            </Button>
          </>
        ) : offline ? (
          <>
            <Chip size="sm" variant="soft" color="danger">
              {t("admin.offline")}
            </Chip>
            <Button variant="secondary" size="sm" isPending={update.isPending} onPress={() => update.mutate({ runnerId: r.id, disabled: true })}>
              {t("admin.disable")}
            </Button>
          </>
        ) : (
          <>
            <Chip size="sm" variant="soft" color="success" title={t("admin.onlineSeen", { time: formatAgo(r.last_seen_at, locale) })}>
              {t("admin.online")}
            </Chip>
            <ActionMenu
              label={t("admin.actionsFor", { name: r.name })}
              icon={Ellipsis}
              items={[{ label: t("admin.disable"), icon: Ban, danger: true, onSelect: () => update.mutate({ runnerId: r.id, disabled: true }) }]}
            />
          </>
        )}
      </span>
    </div>
  );
}

function RunnerTokenDialog({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  const { t, locale } = useI18n();
  const create = useCreateRunnerToken();
  const [name, setName] = useState("");
  const [hours, setHours] = useState<(typeof EXPIRY_HOURS)[number]>(24);
  const token = create.data;
  const close = () => {
    if (create.isPending) return;
    create.reset();
    setName("");
    setHours(24);
    onClose();
  };
  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (create.isPending) return;
    create.mutate({ ...(name.trim() ? { name: name.trim() } : {}), expires_in_hours: hours });
  };

  if (token) {
    return (
      <ConsoleDialog
        isOpen={isOpen}
        isDismissable={false}
        onOpenChange={(open) => (open ? undefined : close())}
        title={
          <span className="flex items-center gap-3">
            <span className="lr-row__icon" data-tone="ok" aria-hidden="true">
              <Check width={16} height={16} />
            </span>
            {t("tokens.onceTitle")}
          </span>
        }
        footer={<Button onPress={close}>{t("common.done")}</Button>}
      >
        <div className="flex flex-col gap-3">
          <div className="lr-copy lr-copy--wrap">
            <code data-testid="runner-token">{token.token}</code>
            <CopyButton value={token.token} label={t("tokens.copyToken")} icon={Copy} />
          </div>
          <p className="text-sm text-muted">
            {t("admin.tokenUse")} <span className="font-mono text-xs">runner.registration_token_file</span>
          </p>
          <p className="text-sm text-muted">{t("admin.tokenExpires", { time: formatDateTime(token.expires_at, locale) })}</p>
        </div>
      </ConsoleDialog>
    );
  }
  const label = (h: number) => (h === 1 ? t("admin.hours", { count: 1 }) : h === 24 ? t("admin.hours", { count: 24 }) : t("tokens.expiryDays", { count: 7 }));
  return (
    <ConsoleDialog
      isOpen={isOpen}
      isDismissable={!create.isPending}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t("admin.registrationTokenTitle")}
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
        <TextInput name="name" label={t("admin.tokenNote")} value={name} onChange={setName} placeholder={t("auth.optional")} autoFocus />
        <div className="flex flex-col gap-1">
          <span className="text-sm font-medium" id="rt-exp">
            {t("tokens.expiry")}
          </span>
          <PressedGroup labelledBy="rt-exp" fullWidth value={hours} onChange={setHours} options={EXPIRY_HOURS.map((h) => ({ id: h, label: label(h) }))} />
        </div>
        {create.error ? <FormError>{t(writeErrorMessage(create.error))}</FormError> : null}
        <button type="submit" hidden disabled={create.isPending} />
      </form>
    </ConsoleDialog>
  );
}
