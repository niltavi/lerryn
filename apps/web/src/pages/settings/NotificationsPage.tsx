import {
  formatDateTime,
  isApiError,
  useCreateNotificationChannel,
  useDeleteNotificationChannel,
  useI18n,
  useNotificationChannels,
  useNotificationDeliveries,
  useProjects,
  useTaskList,
  useTestNotificationChannel,
  useUpdateNotificationChannel,
  type NotificationChannel,
  type NotificationChannelKind,
  type NotificationDelivery,
  type NotificationEventType,
} from "@lerryn/core";
import { Button, ConsoleDialog, CopyButton, Disclosure, IconButton, StateCard, Toggle, ToneGlyph } from "@lerryn/ui-web";
import { Bell, Check, Clock, Copy, History, KeyRound, MessageSquare, Plus, Send, Timer, Trash2, Unplug, Webhook, X, type LucideIcon } from "lucide-react";
import { useState, type FormEvent } from "react";

import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";
import { FormError, TextInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

const EVENTS: { type: NotificationEventType; icon: LucideIcon }[] = [
  { type: "run_failed", icon: X },
  { type: "run_timed_out", icon: Timer },
  { type: "run_interrupted", icon: Unplug },
  { type: "credential_expiring", icon: KeyRound },
];
const KINDS: NotificationChannelKind[] = ["feishu", "slack", "webhook"];

/**
 * Notification channels and what they subscribe to (prototype
 * Settings-Notify.dc.html), with a test send and the delivery log. Target
 * addresses come back masked; a webhook's signing secret is shown once.
 */
export function NotificationsPage() {
  const { t } = useI18n();
  usePageTitle(t("notifications.title"));
  const channels = useNotificationChannels();
  const [adding, setAdding] = useState(false);
  const [log, setLog] = useState<NotificationChannel | null>(null);
  const [removing, setRemoving] = useState<NotificationChannel | null>(null);
  const items = channels.data?.items ?? [];

  return (
    <Board label={t("notifications.title")}>
      <BoardTop title={t("notifications.title")}>
        <Button size="sm" onPress={() => setAdding(true)}>
          <Plus className="wl-icon" aria-hidden="true" />
          {t("notifications.add")}
        </Button>
      </BoardTop>
      {channels.isError ? (
        <LoadError error={channels.error} onRetry={() => void channels.refetch()} />
      ) : channels.data && items.length === 0 ? (
        <StateCard icon={Bell} title={t("notifications.empty")} />
      ) : items.length > 0 ? (
        <div className="rows" role="list" aria-label={t("notifications.title")}>
          {items.map((c) => (
            <ChannelRow key={c.id} channel={c} onLog={() => setLog(c)} onDelete={() => setRemoving(c)} />
          ))}
        </div>
      ) : null}
      <AddChannelDialog isOpen={adding} onClose={() => setAdding(false)} />
      <DeliveriesDialog channel={log} onClose={() => setLog(null)} />
      <DeleteDialog channel={removing} onClose={() => setRemoving(null)} />
    </Board>
  );
}

function kindLabel(kind: string, t: ReturnType<typeof useI18n>["t"]) {
  return kind === "feishu" ? t("notifications.kind.feishu") : kind === "slack" ? t("notifications.kind.slack") : t("notifications.kind.webhook");
}

function ChannelRow({ channel: c, onLog, onDelete }: { channel: NotificationChannel; onLog: () => void; onDelete: () => void }) {
  const { t } = useI18n();
  const update = useUpdateNotificationChannel();
  const test = useTestNotificationChannel();
  const subscribed = new Set(c.subscriptions.flatMap((s) => s.events));
  const scope = c.subscriptions
    .map((s) => (s.task ? t("notifications.scopeTask", { name: s.task.name }) : t("notifications.scopeProject", { name: s.project.name })))
    .join(" · ");
  const Icon = c.kind === "webhook" ? Webhook : MessageSquare;
  const names = EVENTS.filter((e) => subscribed.has(e.type)).map((e) => t(`notifications.event.${e.type}`));

  return (
    <div className={c.enabled ? "rowi" : "rowi rowi--quiet"} role="listitem">
      <span className="ic-box" aria-hidden="true">
        <Icon className="wl-icon" width={18} />
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name" style={c.enabled ? undefined : { color: "var(--ink-muted)" }}>
          {c.name}
        </span>
        <span className="rowi-sub">{[kindLabel(c.kind, t), scope || t("notifications.noSubscription")].join(" · ")}</span>
      </span>
      <span className="rowi-end">
        <span className="acts">
          <IconButton label={t("notifications.testNamed", { name: c.name })} tip={t("notifications.test")} icon={Send} isPending={test.isPending} onPress={() => test.mutate(c.id)} />
          <IconButton label={t("notifications.logNamed", { name: c.name })} tip={t("notifications.log")} icon={History} onPress={onLog} />
          <IconButton label={t("notifications.deleteNamed", { name: c.name })} tip={t("common.delete")} icon={Trash2} onPress={onDelete} />
        </span>
        <span className="scopes" role="img" aria-label={names.length ? t("notifications.subscribedTo", { events: names.join("、") }) : t("notifications.noSubscription")}>
          {EVENTS.map(({ type, icon: EIcon }) => (
            <span key={type} className={subscribed.has(type) ? undefined : "off"} data-tip={subscribed.has(type) ? t(`notifications.event.${type}`) : undefined} data-tip-pos="top" aria-hidden="true">
              <EIcon className="wl-icon" />
            </span>
          ))}
        </span>
        <LastDelivery delivery={test.data ?? c.last_delivery} />
        <Toggle
          size="sm"
          checked={c.enabled}
          label={t("notifications.enableNamed", { name: c.name })}
          disabled={update.isPending}
          onChange={(enabled) => update.mutate({ channelId: c.id, body: { enabled } })}
        />
      </span>
    </div>
  );
}

function LastDelivery({ delivery }: { delivery: NotificationDelivery | null }) {
  const { t, locale } = useI18n();
  if (!delivery) return <ToneGlyph tone="idle" icon={Clock} label={t("notifications.neverSent")} />;
  const when = formatDateTime(delivery.delivered_at ?? delivery.last_attempt_at ?? delivery.created_at, locale);
  const tone = delivery.status === "delivered" ? "ok" : delivery.status === "failed" ? "bad" : "idle";
  const label =
    delivery.status === "delivered"
      ? t("notifications.delivered", { time: when })
      : delivery.status === "failed"
        ? t("notifications.failed", { time: when })
        : t("notifications.pendingSend");
  return (
    <Disclosure
      alignRight
      triggerClassName={`g g--${tone} g-btn`}
      label={label}
      detailsLabel={t("notifications.lastSend")}
      trigger={tone === "ok" ? <Check className="wl-icon" aria-hidden="true" /> : tone === "bad" ? <X className="wl-icon" aria-hidden="true" /> : <Clock className="wl-icon" aria-hidden="true" />}
      details={<DeliveryDetail delivery={delivery} />}
    />
  );
}

function DeliveryDetail({ delivery: d }: { delivery: NotificationDelivery }) {
  const { t, locale } = useI18n();
  return (
    <div className="stack" style={{ gap: 0 }}>
      <div className="pop-row">
        <span>{formatDateTime(d.last_attempt_at ?? d.created_at, locale)}</span>
        <span className="mono-sm" style={d.status === "failed" ? { color: "var(--danger)" } : undefined}>
          {d.http_status ? `HTTP ${d.http_status}` : t(`notifications.status.${d.status}`)}
        </span>
      </div>
      <div className="pop-row">
        <span>{t("notifications.attempts")}</span>
        <span className="mono-sm">{d.attempts}</span>
      </div>
      {d.error ? (
        <div className="pop-row">
          <span className="caption muted">{d.error}</span>
        </div>
      ) : null}
    </div>
  );
}

function AddChannelDialog({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const create = useCreateNotificationChannel();
  const projects = useProjects();
  const [name, setName] = useState("");
  const [kind, setKind] = useState<NotificationChannelKind>("feishu");
  const [url, setUrl] = useState("");
  const [projectId, setProjectId] = useState("");
  const [taskId, setTaskId] = useState("");
  const [events, setEvents] = useState<NotificationEventType[]>(["run_failed", "run_timed_out", "run_interrupted"]);
  const [errors, setErrors] = useState<{ name?: string; url?: string; events?: string }>({});
  const tasks = useTaskList(projectId || null);
  const projectItems = projects.data?.items ?? [];
  const project = projectId || projectItems[0]?.id || "";
  const secret = create.data?.signing_secret;

  const close = () => {
    if (create.isPending) return;
    create.reset();
    setName("");
    setUrl("");
    setTaskId("");
    setErrors({});
    onClose();
  };

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (create.isPending) return;
    const next = {
      name: name.trim() ? undefined : t("auth.errors.required"),
      url: /^https?:\/\/\S+$/.test(url.trim()) ? undefined : t("notifications.errors.url"),
      events: events.length ? undefined : t("notifications.errors.events"),
    };
    setErrors(next);
    if (next.name || next.url || next.events || !project) return;
    create.mutate(
      {
        name: name.trim(),
        kind,
        url: url.trim(),
        enabled: true,
        subscriptions: [{ project_id: project, ...(taskId ? { task_id: taskId } : {}), events }],
      },
      { onSuccess: (res) => (res.signing_secret ? undefined : close()) },
    );
  };

  if (secret) {
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
            {t("notifications.secretTitle")}
          </>
        }
        footer={<Button onPress={close}>{t("common.done")}</Button>}
      >
        <div className="copyline copyline--wrap">
          <code data-testid="signing-secret">{secret}</code>
          <CopyButton value={secret} label={t("notifications.copySecret")} icon={Copy} />
        </div>
        <p className="body-sm muted">{t("notifications.secretBody")}</p>
      </ConsoleDialog>
    );
  }

  return (
    <ConsoleDialog
      isOpen={isOpen}
      isDismissable={!create.isPending}
      onOpenChange={(open) => (open ? undefined : close())}
      title={t("notifications.addTitle")}
      footer={
        <>
          <Button variant="ghost" onPress={close} isDisabled={create.isPending}>
            {t("common.cancel")}
          </Button>
          <Button onPress={() => submit()} isPending={create.isPending}>
            {t("notifications.add")}
          </Button>
        </>
      }
    >
      <form className="stack" style={{ gap: "var(--space-5)" }} onSubmit={submit}>
        <div className="stack" style={{ gap: 8 }}>
          <span className="label-text" id="kind-label">
            {t("notifications.kindLabel")}
          </span>
          <div className="seg seg--block" role="group" aria-labelledby="kind-label">
            {KINDS.map((k) => (
              <button key={k} type="button" aria-pressed={kind === k} onClick={() => setKind(k)}>
                {kindLabel(k, t)}
              </button>
            ))}
          </div>
        </div>
        <TextInput name="name" label={t("notifications.name")} value={name} onChange={setName} placeholder={t("notifications.namePlaceholder")} autoFocus error={errors.name} />
        <TextInput name="url" label={t("notifications.url")} value={url} onChange={setUrl} placeholder="https://" error={errors.url} />
        <div className="field-row">
          <label className="field-select">
            <span className="label-text">{t("notifications.project")}</span>
            <select value={project} onChange={(e) => (setProjectId(e.target.value), setTaskId(""))}>
              {projectItems.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.is_personal ? t("overview.personalProject") : p.name}
                </option>
              ))}
            </select>
          </label>
          <label className="field-select">
            <span className="label-text">{t("notifications.task")}</span>
            <select value={taskId} onChange={(e) => setTaskId(e.target.value)}>
              <option value="">{t("notifications.allTasks")}</option>
              {(tasks.data?.items ?? [])
                .filter((x) => x.project_id === project)
                .map((x) => (
                  <option key={x.id} value={x.id}>
                    {x.name}
                  </option>
                ))}
            </select>
          </label>
        </div>
        <fieldset className="stack" style={{ gap: 8, border: 0, margin: 0, padding: 0 }}>
          <legend className="label-text" style={{ marginBottom: 8 }}>
            {t("notifications.events")}
          </legend>
          <div className="row">
            {EVENTS.map(({ type, icon: Icon }) => (
              <label className="choice" key={type}>
                <input
                  type="checkbox"
                  checked={events.includes(type)}
                  onChange={(e) => setEvents((s) => (e.target.checked ? [...s, type] : s.filter((x) => x !== type)))}
                />
                <Icon className="wl-icon" aria-hidden="true" />
                {t(`notifications.event.${type}`)}
              </label>
            ))}
          </div>
          {errors.events ? <span className="caption" style={{ color: "var(--danger)" }}>{errors.events}</span> : null}
        </fieldset>
        {create.error ? (
          <FormError>{isApiError(create.error) && create.error.code === "validation_failed" ? t("notifications.errors.url") : t(writeErrorMessage(create.error))}</FormError>
        ) : null}
        <button type="submit" hidden disabled={create.isPending} />
      </form>
    </ConsoleDialog>
  );
}

function DeliveriesDialog({ channel, onClose }: { channel: NotificationChannel | null; onClose: () => void }) {
  const { t, locale } = useI18n();
  const deliveries = useNotificationDeliveries(channel?.id ?? null);
  const items = deliveries.data?.items ?? [];
  return (
    <ConsoleDialog
      isOpen={!!channel}
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t("notifications.logTitle", { name: channel?.name ?? "" })}
      footer={<Button onPress={onClose}>{t("common.close")}</Button>}
    >
      {deliveries.isError ? (
        <LoadError error={deliveries.error} onRetry={() => void (
          deliveries.isFetchNextPageError
            ? deliveries.fetchNextPage({ cancelRefetch: false })
            : deliveries.refetch({ cancelRefetch: false })
        )} />
      ) : null}
      {deliveries.data && items.length === 0 && !deliveries.hasNextPage && !deliveries.isError ? <p className="body-sm muted">{t("notifications.neverSent")}</p> : null}
      {items.length > 0 ? (
        <div className="stack" style={{ gap: 0 }}>
          {items.map((d) => (
            <div className="pop-row" key={d.id}>
              <span className="stack" style={{ gap: 0, minWidth: 0 }}>
                <span className="body-sm">
                  {d.event_type === "test" ? t("notifications.testEvent") : t(`notifications.event.${d.event_type as NotificationEventType}`)}
                  {d.task ? ` · ${d.task.name}` : ""}
                  {d.merged_count > 1 ? ` · ×${d.merged_count}` : ""}
                </span>
                <span className="caption muted">{formatDateTime(d.created_at, locale)}</span>
              </span>
              <span className="mono-sm" style={d.status === "failed" ? { color: "var(--danger)" } : d.status === "delivered" ? { color: "var(--success)" } : undefined}>
                {t(`notifications.status.${d.status}`)}
                {d.http_status ? ` · ${d.http_status}` : ""}
              </span>
            </div>
          ))}
        </div>
      ) : null}
      <LoadMore
        show={!!deliveries.hasNextPage && !deliveries.isError}
        pending={deliveries.isFetching}
        onPress={() => void deliveries.fetchNextPage({ cancelRefetch: false })}
      />
    </ConsoleDialog>
  );
}

function DeleteDialog({ channel, onClose }: { channel: NotificationChannel | null; onClose: () => void }) {
  const { t } = useI18n();
  const remove = useDeleteNotificationChannel();
  return (
    <ConsoleDialog
      isOpen={!!channel}
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t("notifications.deleteNamed", { name: channel?.name ?? "" })}
      footer={
        <>
          <Button variant="ghost" onPress={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" isPending={remove.isPending} onPress={() => channel && remove.mutate(channel.id, { onSuccess: onClose })}>
            {t("common.delete")}
          </Button>
        </>
      }
    >
      <p className="body-sm muted">{t("notifications.deleteBody")}</p>
      {remove.error ? <FormError>{t(writeErrorMessage(remove.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
