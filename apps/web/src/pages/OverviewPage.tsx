import {
  attentionView,
  dayStrip,
  elapsedMs,
  formatClock,
  formatDate,
  formatDuration,
  runStatusMeta,
  startOfLocalDay,
  useI18n,
  useOverview,
  useProjects,
  useTodayCalendar,
  type ActiveRun,
  type AttentionView,
  type Overview,
  type StripBucket,
} from "@lerryn/core";
import { Button, Card, Chip, CopyButton, DayStrip, Dropdown, Label, StatusGlyph, WaveGlyph } from "@lerryn/ui-web";
import { ChevronDown, ChevronRight, Copy, Folder, Key, PlugConnection, Plus } from "@gravity-ui/icons";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import type { ReactNode } from "react";

import { origin, useNow } from "../lib/hooks";
import { ApiLink } from "../lib/links";
import { useOpenCredentialFill } from "../shell/CredentialFill";
import { GlyphView, shortWhen } from "../lib/present";
import { Board, BoardTop, LoadError, usePageTitle } from "../shell/layout";
import { TaskListColumn } from "./TaskListColumn";

/**
 * Overview: what needs attention, what is running, and today's strip,
 * beside the task list. A deployment with no task yet shows how to
 * connect an agent instead.
 */
export function OverviewPage() {
  const { t, locale } = useI18n();
  usePageTitle(t("overview.title"));
  const { project } = useSearch({ from: "/app/" });
  const projectId = project ?? null;
  const overview = useOverview(projectId);
  const now = useNow(1000);
  const weekday = new Intl.DateTimeFormat(locale, { weekday: "short" }).format(now);

  return (
    <>
      <TaskListColumn projectId={projectId} />
      <Board label={t("overview.title")}>
        <BoardTop
          title={t("overview.title")}
          description={
            <span className="tabular-nums">
              {formatDate(now, locale, now)} {weekday} · {formatClock(now)}
            </span>
          }
        >
          <ProjectSwitch projectId={projectId} />
        </BoardTop>
        {overview.isError ? (
          <LoadError error={overview.error} onRetry={() => void overview.refetch()} />
        ) : overview.data ? (
          overview.data.counts.tasks === 0 && !projectId ? (
            <ConnectAgent />
          ) : (
            <OverviewBoard overview={overview.data} projectId={projectId} now={now} />
          )
        ) : null}
      </Board>
    </>
  );
}

/** Menu key of "all projects"; project IDs are UUIDs, so it cannot collide. */
const ALL_PROJECTS = "all";

function ProjectSwitch({ projectId }: { projectId: string | null }) {
  const { t } = useI18n();
  const projects = useProjects();
  const navigate = useNavigate();
  const items = projects.data?.items ?? [];
  // A single project needs no switch.
  if (items.length < 2) return null;
  const current = items.find((p) => p.id === projectId);
  const name = current?.name ?? t("overview.allProjects");
  const pick = (id: string | null) => void navigate({ to: "/", search: id ? { project: id } : {} });
  return (
    <Dropdown>
      <Button variant="tertiary" size="sm" aria-label={t("overview.switchProject", { name })}>
        <Folder width={16} height={16} aria-hidden="true" />
        <span className="max-w-40 truncate">{name}</span>
        <ChevronDown width={14} height={14} aria-hidden="true" />
      </Button>
      <Dropdown.Popover placement="bottom end">
        <Dropdown.Menu
          aria-label={t("overview.projects")}
          selectionMode="single"
          selectedKeys={[projectId ?? ALL_PROJECTS]}
          onAction={(key) => pick(key === ALL_PROJECTS ? null : String(key))}
        >
          <Dropdown.Item id={ALL_PROJECTS} textValue={t("overview.allProjects")}>
            <Dropdown.ItemIndicator />
            <Label>{t("overview.allProjects")}</Label>
          </Dropdown.Item>
          {items.map((p) => {
            const label = p.is_personal ? t("overview.personalProject") : p.name;
            return (
              <Dropdown.Item key={p.id} id={p.id} textValue={label}>
                <Dropdown.ItemIndicator />
                <Label>{label}</Label>
              </Dropdown.Item>
            );
          })}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown>
  );
}

/** One figure of the summary row. The `stat` hooks are what the tests read. */
function Stat({ value, label, tone }: { value: ReactNode; label: ReactNode; tone?: "bad" | "run" }) {
  return (
    <Card className="stat tpl-kpi" data-tone={tone}>
      <span className="tpl-kpi__label">{label}</span>
      <strong className="stat-n tpl-kpi__value">{value}</strong>
    </Card>
  );
}

function OverviewBoard({ overview, projectId, now }: { overview: Overview; projectId: string | null; now: number }) {
  const { t, locale } = useI18n();
  const { counts, attention, active, upcoming } = overview;
  const next = upcoming[0];
  const sameTime = next ? upcoming.filter((u) => Math.abs(Date.parse(u.at) - Date.parse(next.at)) < 60_000).length : 0;
  const items = attention.map(attentionView);
  const running = counts.running + counts.queued;

  return (
    <>
      <div className="hero tpl-grid tpl-grid--kpi">
        <Stat value={counts.attention} label={t("overview.attention")} tone={counts.attention > 0 ? "bad" : undefined} />
        <Stat
          value={counts.running}
          label={
            <>
              {t("overview.running")}
              {counts.queued > 0 ? ` · ${t("overview.queuedCount", { count: counts.queued })}` : null}
            </>
          }
          tone={running > 0 ? "run" : undefined}
        />
        {next ? <Stat value={shortWhen(next.at, locale, now)} label={t("overview.nextFire", { count: sameTime })} /> : null}
      </div>

      <TodayStrip projectId={projectId} now={now} />

      {items.length > 0 || active.length > 0 ? (
        <div className="tk-grid">
          {items.length > 0 ? (
            <section className="min-w-0" aria-labelledby="att-title">
              <Card className="tpl-card">
                <div className="tpl-card__head">
                  <h4>
                    <span id="att-title">{t("overview.attention")}</span>
                    {counts.attention > items.length ? (
                      <span className="ml-2 text-xs font-normal text-muted tabular-nums">{counts.attention}</span>
                    ) : null}
                  </h4>
                </div>
                <ul className="lr-rows">
                  {items.map((item) => (
                    <li className="lr-row" key={item.key}>
                      <AttentionRow item={item} now={now} />
                    </li>
                  ))}
                </ul>
              </Card>
            </section>
          ) : null}
          {active.length > 0 ? (
            <section className="min-w-0" aria-labelledby="run-title">
              <Card className="tpl-card">
                <div className="tpl-card__head">
                  <h4 id="run-title">{t("overview.running")}</h4>
                </div>
                <ul className="lr-rows">
                  {active.map((run) => (
                    <li className="lr-row" key={run.run_id}>
                      <ActiveRunRow run={run} now={now} />
                    </li>
                  ))}
                </ul>
              </Card>
            </section>
          ) : null}
        </div>
      ) : null}
    </>
  );
}

const ATTENTION_LABEL = {
  run_failed: "run.status.failed",
  run_timed_out: "run.status.timed_out",
  run_interrupted: "run.status.interrupted",
  credential_request: "overview.kind.credentialRequest",
  credential_expiring: "overview.kind.credentialExpiring",
  runner_offline: "overview.kind.runnerOffline",
} as const;

function AttentionRow({ item, now }: { item: AttentionView; now: number }) {
  const { t, locale } = useI18n();
  const openFill = useOpenCredentialFill();
  const due = item.kind === "credential_request" || item.kind === "credential_expiring";
  const when = shortWhen(item.at, locale, now);
  const why =
    item.kind === "credential_request" && item.subtitle ? t("overview.requestedBy", { task: item.subtitle }) : item.subtitle;
  const body = (
    <>
      <GlyphView glyph={item.glyph} label={t(ATTENTION_LABEL[item.kind])} size="lg" />
      <span className="lr-row__text">
        <span className={item.titleMono ? "lr-row__title font-mono" : "lr-row__title"}>{item.title}</span>
        {why ? <span className="lr-row__sub">{why}</span> : null}
      </span>
      <span className="lr-row__end tpl-meta tabular-nums">
        {due ? t("overview.dueBy", { time: when }) : when}
        <ChevronRight className="ov-item__go" width={16} height={16} aria-hidden="true" />
      </span>
    </>
  );
  if (item.requestId) {
    const requestId = item.requestId;
    return (
      <button type="button" className="ov-item" onClick={() => openFill(requestId)}>
        {body}
      </button>
    );
  }
  if (item.target?.kind === "run") {
    return (
      <Link to="/runs/$runId" params={{ runId: item.target.runId }} className="ov-item">
        {body}
      </Link>
    );
  }
  if (item.target?.kind === "browser") {
    return (
      <ApiLink href={item.target.url} className="ov-item">
        {body}
      </ApiLink>
    );
  }
  return <div className="ov-item">{body}</div>;
}

function ActiveRunRow({ run, now }: { run: ActiveRun; now: number }) {
  const { t } = useI18n();
  const started = run.started_at;
  const queued = !started;
  const ms = elapsedMs(started ?? run.queued_at, null, now);
  return (
    <Link to="/runs/$runId" params={{ runId: run.run_id }} className="ov-item">
      <StatusGlyph status={run.status} />
      <span className="lr-row__text">
        <span className="flex min-w-0 items-center gap-2">
          <span className="lr-row__title">{run.task.name}</span>
          {run.test ? (
            <Chip size="sm" variant="soft" className="shrink-0">
              {t("run.test")}
            </Chip>
          ) : null}
        </span>
        {run.current_stage ? <span className="lr-row__sub">{run.current_stage}</span> : null}
        {queued ? <span className="lr-row__sub">{t(runStatusMeta(run.status).label)}</span> : null}
      </span>
      <span className={queued ? "lr-row__end text-sm text-muted tabular-nums" : "lr-row__end text-sm font-medium tabular-nums"}>
        {formatDuration(ms, { whole: true })}
      </span>
    </Link>
  );
}

/** Planned fires named in one ridge's tooltip before the rest are counted. */
const MAX_PLANS_LISTED = 3;

function TodayStrip({ projectId, now }: { projectId: string | null; now: number }) {
  const { t, locale } = useI18n();
  // Refetch per minute, not per tick.
  const minute = Math.floor(now / 60_000) * 60_000;
  const calendar = useTodayCalendar(projectId, minute);
  if (!calendar.data) return null;
  const dayStart = startOfLocalDay(now);
  const buckets = dayStrip(calendar.data.items, dayStart);
  const describe = (b: StripBucket) => {
    const end = b.start + 30 * 60_000;
    const span = `${formatClock(b.start)} – ${end - dayStart >= 86_400_000 ? "24:00" : formatClock(end)}`;
    const parts: string[] = [];
    if (b.runs) parts.push(t("overview.stripRuns", { count: b.runs }));
    if (b.planned) parts.push(t("overview.stripPlanned", { count: b.planned }));
    if (b.tone === "bad") parts.push(t("overview.stripBad"));
    if (b.tone === "warn") parts.push(t("overview.stripWarn"));
    if (b.tone === "run") parts.push(t("overview.stripActive"));
    // Name the planned fires with the server's wording of each schedule.
    const plans = b.plans.slice(0, MAX_PLANS_LISTED).map((p) => (p.scheduleText ? `${p.taskName} · ${p.scheduleText}` : p.taskName));
    if (b.plans.length > MAX_PLANS_LISTED) plans.push(t("overview.stripMorePlans", { count: b.plans.length - MAX_PLANS_LISTED }));
    return [span, parts.join(locale === "zh-CN" ? "，" : ", "), ...plans].join("\n");
  };
  return (
    <Card className="tpl-card">
      <div className="tpl-card__head">
        <h4>{t("overview.today")}</h4>
      </div>
      <DayStrip
        buckets={buckets}
        dayStart={dayStart}
        now={now}
        describe={describe}
        label={t("overview.todayRuns")}
        legend={{
          run: t("overview.legendRun"),
          bad: t("overview.legendBad"),
          warn: t("overview.legendWarn"),
          active: t("overview.legendActive"),
          plan: t("overview.legendPlan"),
        }}
        renderLink={(b, props) => (
          <Link to="/runs/$runId" params={{ runId: b.runId! }} {...props} />
        )}
      />
    </Card>
  );
}

function ConnectAgent() {
  const { t } = useI18n();
  const mcp = `${origin()}/mcp`;
  return (
    <section className="ov-connect" aria-labelledby="connect">
      <div className="flex items-center gap-3">
        <span className="lr-row__icon" aria-hidden="true">
          <WaveGlyph size={18} />
        </span>
        <h2 className="text-lg font-semibold" id="connect">
          {t("overview.connectAgent")}
        </h2>
      </div>
      <Card className="tpl-card">
        <ol className="lr-rows">
          <li className="lr-row">
            <span className="lr-row__icon" aria-hidden="true">
              <Key width={16} height={16} />
            </span>
            <span className="lr-row__text">
              <span className="lr-row__title">{t("overview.stepToken")}</span>
            </span>
            <span className="lr-row__end">
              <Link to="/settings/tokens" search={{ new: true }} className="button button--primary button--sm">
                <Plus width={16} height={16} aria-hidden="true" />
                {t("overview.newToken")}
              </Link>
            </span>
          </li>
          <li className="lr-row">
            <span className="lr-row__icon" aria-hidden="true">
              <PlugConnection width={16} height={16} />
            </span>
            <div className="lr-row__text gap-2">
              <span className="lr-row__title">{t("overview.stepMcp")}</span>
              <div className="lr-copy">
                <code>{mcp}</code>
                <CopyButton value={mcp} label={t("tokens.copyMcp")} icon={Copy} />
              </div>
            </div>
          </li>
          <li className="lr-row" data-quiet="">
            <span className="lr-row__icon" data-tone="idle" aria-hidden="true">
              <WaveGlyph size={16} />
            </span>
            <span className="lr-row__text">
              <span className="lr-row__title">{t("overview.stepAsk")}</span>
            </span>
          </li>
        </ol>
      </Card>
    </section>
  );
}
