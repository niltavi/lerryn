import { formatAgo, formatDate, formatDateTime, useCreateRunnerToken, useI18n, useRunners, useUpdateRunner, type Runner } from "@lerryn/core";
import { ActionMenu, Button, ConsoleDialog, CopyButton, Disclosure, StateCard, ToneGlyph } from "@lerryn/ui-web";
import { Ban, Check, Copy, Ellipsis, Plus, Server, Unplug } from "lucide-react";
import { useState, type FormEvent } from "react";

import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";
import { FormError, TextInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

const EXPIRY_HOURS = [1, 24, 168] as const;

/** Runners (prototype Admin-Runners.dc.html): capacity, status, disable, registration tokens. */
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
          <Plus className="wl-icon" aria-hidden="true" />
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
          <div className="hero">
            <div className={running > 0 ? "stat stat--run" : "stat"}>
              <span className="stat-n stat-n--md">
                {running}
                <small>/ {slots}</small>
              </span>
              <span className="stat-l">{t(runners.hasNextPage ? "admin.shownRunningOfCapacity" : "admin.runningOfCapacity")}</span>
            </div>
            <div className="stat">
              <span className="stat-n stat-n--md">{online}</span>
              <span className="stat-l">{t(runners.hasNextPage ? "admin.shownOnline" : "admin.online")}</span>
            </div>
            {offline > 0 ? (
              <div className="stat stat--bad">
                <span className="stat-n stat-n--md">{offline}</span>
                <span className="stat-l">{t(runners.hasNextPage ? "admin.shownOffline" : "admin.offline")}</span>
              </div>
            ) : null}
          </div>
          <div className="rows" role="list" aria-label={t("admin.runners")}>
            {items.map((r) => (
              <RunnerRow key={r.id} runner={r} />
            ))}
          </div>
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
  const sub = [r.labels.join(" · "), r.version].filter(Boolean).join(" · ");
  const pct = r.slots ? Math.min(100, (r.running / r.slots) * 100) : 0;
  if (r.status === "disabled") {
    return (
      <div className="rowi rowi--quiet" role="listitem">
        <span className="ic-box" aria-hidden="true" style={{ color: "var(--ink-muted)" }}>
          <Server className="wl-icon" width={18} />
        </span>
        <span style={{ minWidth: 0 }}>
          <span className="rowi-name mono" style={{ fontSize: 14 }}>
            {r.name}
          </span>
          <span className="rowi-sub mono-sm">{sub}</span>
        </span>
        <span className="rowi-end">
          {r.running > 0 ? (
            <span className="mono-sm" style={{ width: 36 }} aria-label={t("admin.slotsUsed", { running: r.running, slots: r.slots })}>
              {r.running}/{r.slots}
            </span>
          ) : null}
          <ToneGlyph tone="idle" icon={Ban} label={t("admin.disabledOn", { date: formatDate(r.disabled_at ?? r.last_seen_at, locale) })} />
          <Button variant="secondary" size="sm" isPending={update.isPending} onPress={() => update.mutate({ runnerId: r.id, disabled: false })}>
            {t("admin.enable")}
          </Button>
        </span>
      </div>
    );
  }
  const offline = r.status === "offline";
  return (
    <div className={offline ? "rowi rowi--bad" : "rowi"} role="listitem">
      <span className="ic-box" aria-hidden="true">
        {offline ? <Unplug className="wl-icon" width={18} /> : <Server className="wl-icon" width={18} />}
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name mono" style={{ fontSize: 14 }}>
          {r.name}
        </span>
        <span className="rowi-sub mono-sm" style={offline ? { color: "var(--ink)" } : undefined}>
          {sub || "—"}
        </span>
      </span>
      <span className="rowi-end">
        <span className="meter" aria-hidden="true">
          <span style={{ width: `${pct}%` }} />
        </span>
        <span className="mono-sm" style={{ width: 36 }} aria-label={t("admin.slotsUsed", { running: r.running, slots: r.slots })}>
          {r.running}/{r.slots}
        </span>
        {offline ? (
          <>
            <Disclosure
              alignRight
              triggerClassName="g g--bad g-btn"
              label={t("admin.offlineDetails")}
              detailsLabel={r.name}
              trigger={<Unplug className="wl-icon" aria-hidden="true" />}
              details={
                <div className="pop-row">
                  <span>{t("admin.offline")}</span>
                  <span className="mono-sm">{formatDateTime(r.last_seen_at, locale)}</span>
                </div>
              }
            />
            <Button variant="secondary" size="sm" isPending={update.isPending} onPress={() => update.mutate({ runnerId: r.id, disabled: true })}>
              {t("admin.disable")}
            </Button>
          </>
        ) : (
          <>
            <span data-tip={t("admin.onlineSeen", { time: formatAgo(r.last_seen_at, locale) })} data-tip-pos="top">
              <ToneGlyph tone="ok" icon={Check} label={t("admin.online")} />
            </span>
            <span className="acts">
              <ActionMenu
                label={t("admin.actionsFor", { name: r.name })}
                icon={Ellipsis}
                items={[{ label: t("admin.disable"), icon: Ban, danger: true, onSelect: () => update.mutate({ runnerId: r.id, disabled: true }) }]}
              />
            </span>
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
          <code data-testid="runner-token">{token.token}</code>
          <CopyButton value={token.token} label={t("tokens.copyToken")} icon={Copy} />
        </div>
        <p className="body-sm muted">
          {t("admin.tokenUse")} <span className="mono-sm">runner.registration_token_file</span>
        </p>
        <p className="body-sm muted">{t("admin.tokenExpires", { time: formatDateTime(token.expires_at, locale) })}</p>
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
      <form className="stack" style={{ gap: "var(--space-5)" }} onSubmit={submit}>
        <TextInput name="name" label={t("admin.tokenNote")} value={name} onChange={setName} placeholder={t("auth.optional")} autoFocus />
        <div className="stack" style={{ gap: 8 }}>
          <span className="label-text" id="rt-exp">
            {t("tokens.expiry")}
          </span>
          <div className="seg seg--block" role="group" aria-labelledby="rt-exp">
            {EXPIRY_HOURS.map((h) => (
              <button key={h} type="button" aria-pressed={hours === h} onClick={() => setHours(h)}>
                {label(h)}
              </button>
            ))}
          </div>
        </div>
        {create.error ? <FormError>{t(writeErrorMessage(create.error))}</FormError> : null}
        <button type="submit" hidden disabled={create.isPending} />
      </form>
    </ConsoleDialog>
  );
}
