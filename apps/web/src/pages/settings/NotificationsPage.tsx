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
import {
  Button,
  Card,
  Checkbox,
  CheckboxGroup,
  Chip,
  ConsoleDialog,
  CopyButton,
  FieldError,
  IconButton,
  Label,
  ListBox,
  Popover,
  Select,
  StateCard,
  Toggle,
  ToneGlyph,
} from "@lerryn/ui-web";
import {
  Bell,
  Check,
  CircleCheckFill,
  Clock,
  ClockArrowRotateLeft,
  Comment,
  Copy,
  Key,
  LinkSlash,
  LogoWebhook,
  PaperPlane,
  Plus,
  Stopwatch,
  TrashBin,
  Xmark,
} from "@gravity-ui/icons";
import { useId, useState, type ComponentType, type FormEvent, type SVGProps } from "react";

import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";
import { FormError, TextInput } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";
import { Segmented } from "./AccountPage";

type Icon = ComponentType<SVGProps<SVGSVGElement>>;

const EVENTS: { type: NotificationEventType; icon: Icon }[] = [
  { type: "run_failed", icon: Xmark },
  { type: "run_timed_out", icon: Stopwatch },
  { type: "run_interrupted", icon: LinkSlash },
  { type: "credential_expiring", icon: Key },
];
const KINDS: NotificationChannelKind[] = ["feishu", "slack", "webhook"];
/** The task picker's entry for a subscription to the whole project. */
const ALL_TASKS = "all";
const STATUS_COLOR = { delivered: "success", failed: "danger", pending: "default" } as const;

/**
 * Notification channels and what they subscribe to, with a test send and the
 * delivery log. Target addresses come back masked; a webhook's signing secret
 * is shown once.
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
          <Plus aria-hidden="true" width={16} height={16} />
          {t("notifications.add")}
        </Button>
      </BoardTop>
      {channels.isError ? (
        <LoadError error={channels.error} onRetry={() => void channels.refetch()} />
      ) : channels.data && items.length === 0 ? (
        <StateCard icon={Bell} title={t("notifications.empty")} />
      ) : items.length > 0 ? (
        <Card className="tpl-card nt-fade-up">
          <ul className="lr-rows" role="list" aria-label={t("notifications.title")}>
            {items.map((c) => (
              <ChannelRow key={c.id} channel={c} onLog={() => setLog(c)} onDelete={() => setRemoving(c)} />
            ))}
          </ul>
        </Card>
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
  const KindIcon = c.kind === "webhook" ? LogoWebhook : Comment;
  const events = EVENTS.filter((e) => subscribed.has(e.type));

  return (
    <li className="lr-row" data-quiet={c.enabled ? undefined : true}>
      <span className="lr-row__icon" data-tone={c.enabled ? undefined : "idle"} aria-hidden="true">
        <KindIcon width={16} height={16} />
      </span>
      <span className="lr-row__text">
        <span className="flex min-w-0 items-center gap-2">
          <span className="lr-row__title">{c.name}</span>
          <Chip size="sm" variant="soft" className="shrink-0">
            {kindLabel(c.kind, t)}
          </Chip>
        </span>
        <span className="lr-row__sub">{scope || t("notifications.noSubscription")}</span>
      </span>
      <span className="lr-row__end">
        {events.length > 0 ? (
          <span
            className="lr-events"
            role="img"
            aria-label={t("notifications.subscribedTo", { events: events.map((e) => t(`notifications.event.${e.type}`)).join("、") })}
          >
            {events.map(({ type, icon: EventIcon }) => (
              <span key={type} title={t(`notifications.event.${type}`)}>
                <EventIcon aria-hidden="true" width={14} height={14} />
              </span>
            ))}
          </span>
        ) : null}
        <LastDelivery delivery={test.data ?? c.last_delivery} />
        <span className="flex items-center gap-1">
          <IconButton label={t("notifications.testNamed", { name: c.name })} tip={t("notifications.test")} icon={PaperPlane} isPending={test.isPending} onPress={() => test.mutate(c.id)} />
          <IconButton label={t("notifications.logNamed", { name: c.name })} tip={t("notifications.log")} icon={ClockArrowRotateLeft} onPress={onLog} />
          <IconButton label={t("notifications.deleteNamed", { name: c.name })} tip={t("common.delete")} icon={TrashBin} onPress={onDelete} />
        </span>
        <Toggle
          size="sm"
          checked={c.enabled}
          label={t("notifications.enableNamed", { name: c.name })}
          disabled={update.isPending}
          onChange={(enabled) => update.mutate({ channelId: c.id, body: { enabled } })}
        />
      </span>
    </li>
  );
}

function LastDelivery({ delivery }: { delivery: NotificationDelivery | null }) {
  const { t, locale } = useI18n();
  if (!delivery) return <ToneGlyph tone="idle" icon={Clock} label={t("notifications.neverSent")} />;
  const when = formatDateTime(delivery.delivered_at ?? delivery.last_attempt_at ?? delivery.created_at, locale);
  const label =
    delivery.status === "delivered"
      ? t("notifications.delivered", { time: when })
      : delivery.status === "failed"
        ? t("notifications.failed", { time: when })
        : t("notifications.pendingSend");
  const StatusIcon = delivery.status === "delivered" ? Check : delivery.status === "failed" ? Xmark : Clock;
  return (
    <Popover>
      <Button
        isIconOnly
        size="sm"
        variant="ghost"
        aria-label={label}
        className={delivery.status === "delivered" ? "text-success-soft-foreground" : delivery.status === "failed" ? "text-danger-soft-foreground" : "text-muted"}
      >
        <StatusIcon aria-hidden="true" width={16} height={16} />
      </Button>
      <Popover.Content placement="bottom end" className="min-w-56">
        <Popover.Dialog>
          <Popover.Heading>{t("notifications.lastSend")}</Popover.Heading>
          <DeliveryDetail delivery={delivery} />
        </Popover.Dialog>
      </Popover.Content>
    </Popover>
  );
}

function DeliveryDetail({ delivery: d }: { delivery: NotificationDelivery }) {
  const { t, locale } = useI18n();
  return (
    <div className="mt-2 flex flex-col gap-1 text-sm">
      <div className="flex items-center justify-between gap-4">
        <span>{formatDateTime(d.last_attempt_at ?? d.created_at, locale)}</span>
        <span className={d.status === "failed" ? "font-mono text-xs text-danger-soft-foreground" : "font-mono text-xs"}>
          {d.http_status ? `HTTP ${d.http_status}` : t(`notifications.status.${d.status}`)}
        </span>
      </div>
      <div className="flex items-center justify-between gap-4">
        <span>{t("notifications.attempts")}</span>
        <span className="font-mono text-xs">{d.attempts}</span>
      </div>
      {d.error ? <p className="text-xs text-muted">{d.error}</p> : null}
    </div>
  );
}

function AddChannelDialog({ isOpen, onClose }: { isOpen: boolean; onClose: () => void }) {
  const { t } = useI18n();
  const create = useCreateNotificationChannel();
  const projects = useProjects();
  const kindLabelId = useId();
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
            <CircleCheckFill className="shrink-0 text-success" aria-hidden="true" width={18} height={18} />
            {t("notifications.secretTitle")}
          </>
        }
        footer={<Button onPress={close}>{t("common.done")}</Button>}
      >
        <div className="lr-copy lr-copy--wrap">
          <code data-testid="signing-secret">{secret}</code>
          <CopyButton value={secret} label={t("notifications.copySecret")} icon={Copy} />
        </div>
        <p className="text-sm text-muted">{t("notifications.secretBody")}</p>
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
      <form className="flex flex-col gap-4" onSubmit={submit}>
        <div className="flex flex-col gap-2">
          <span className="text-sm font-medium" id={kindLabelId}>
            {t("notifications.kindLabel")}
          </span>
          <Segmented labelledBy={kindLabelId} fullWidth options={KINDS.map((k) => ({ id: k, label: kindLabel(k, t) }))} value={kind} onChange={setKind} />
        </div>
        <TextInput name="name" label={t("notifications.name")} value={name} onChange={setName} placeholder={t("notifications.namePlaceholder")} autoFocus error={errors.name} />
        <TextInput name="url" label={t("notifications.url")} value={url} onChange={setUrl} placeholder="https://" error={errors.url} />
        <div className="grid grid-cols-2 gap-3">
          <Select
            fullWidth
            selectedKey={project || null}
            onSelectionChange={(key) => {
              if (key === null) return;
              setProjectId(String(key));
              setTaskId("");
            }}
          >
            <Label>{t("notifications.project")}</Label>
            <Select.Trigger>
              <Select.Value />
              <Select.Indicator />
            </Select.Trigger>
            <Select.Popover>
              <ListBox>
                {projectItems.map((p) => {
                  const label = p.is_personal ? t("overview.personalProject") : p.name;
                  return (
                    <ListBox.Item key={p.id} id={p.id} textValue={label}>
                      {label}
                      <ListBox.ItemIndicator />
                    </ListBox.Item>
                  );
                })}
              </ListBox>
            </Select.Popover>
          </Select>
          <Select
            fullWidth
            selectedKey={taskId || ALL_TASKS}
            onSelectionChange={(key) => {
              if (key !== null) setTaskId(key === ALL_TASKS ? "" : String(key));
            }}
          >
            <Label>{t("notifications.task")}</Label>
            <Select.Trigger>
              <Select.Value />
              <Select.Indicator />
            </Select.Trigger>
            <Select.Popover>
              <ListBox>
                <ListBox.Item id={ALL_TASKS} textValue={t("notifications.allTasks")}>
                  {t("notifications.allTasks")}
                  <ListBox.ItemIndicator />
                </ListBox.Item>
                {(tasks.data?.items ?? [])
                  .filter((x) => x.project_id === project)
                  .map((x) => (
                    <ListBox.Item key={x.id} id={x.id} textValue={x.name}>
                      {x.name}
                      <ListBox.ItemIndicator />
                    </ListBox.Item>
                  ))}
              </ListBox>
            </Select.Popover>
          </Select>
        </div>
        <CheckboxGroup
          value={events}
          onChange={(next) => setEvents(next as NotificationEventType[])}
          isInvalid={!!errors.events}
          validationBehavior="aria"
        >
          <Label>{t("notifications.events")}</Label>
          <div className="flex flex-wrap gap-x-5 gap-y-2">
            {EVENTS.map(({ type, icon: EventIcon }) => (
              <Checkbox key={type} value={type}>
                <Checkbox.Content>
                  <Checkbox.Control>
                    <Checkbox.Indicator />
                  </Checkbox.Control>
                  <EventIcon className="text-muted" aria-hidden="true" width={14} height={14} />
                  {t(`notifications.event.${type}`)}
                </Checkbox.Content>
              </Checkbox>
            ))}
          </div>
          <FieldError>{errors.events}</FieldError>
        </CheckboxGroup>
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
      {deliveries.data && items.length === 0 && !deliveries.hasNextPage && !deliveries.isError ? <p className="text-sm text-muted">{t("notifications.neverSent")}</p> : null}
      {items.length > 0 ? (
        <ul className="lr-rows">
          {items.map((d) => (
            // pop-row is the hook the end-to-end suite counts delivery rows by.
            <li className="lr-row pop-row" key={d.id}>
              <span className="lr-row__text">
                <span className="lr-row__title text-sm">
                  {[
                    d.event_type === "test" ? t("notifications.testEvent") : t(`notifications.event.${d.event_type as NotificationEventType}`),
                    d.task?.name,
                    d.merged_count > 1 ? `×${d.merged_count}` : undefined,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </span>
                <span className="lr-row__sub">{formatDateTime(d.created_at, locale)}</span>
              </span>
              <span className="lr-row__end">
                <Chip size="sm" variant="soft" color={STATUS_COLOR[d.status]}>
                  {`${t(`notifications.status.${d.status}`)}${d.http_status ? ` · ${d.http_status}` : ""}`}
                </Chip>
              </span>
            </li>
          ))}
        </ul>
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
      <p className="text-sm text-muted">{t("notifications.deleteBody")}</p>
      {remove.error ? <FormError>{t(writeErrorMessage(remove.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
