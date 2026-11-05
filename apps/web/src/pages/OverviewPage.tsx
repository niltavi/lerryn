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
import { CopyButton, DayStrip, Disclosure, StatusGlyph, WaveGlyph } from "@lerryn/ui-web";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { Check, ChevronDown, ChevronRight, Copy, Folder, KeyRound, Plug, Plus } from "lucide-react";

import { origin, useNow } from "../lib/hooks";
import { ApiLink } from "../lib/links";
import { useOpenCredentialFill } from "../shell/CredentialFill";
import { GlyphView, shortWhen } from "../lib/present";
import { Board, LoadError, usePageTitle } from "../shell/layout";
import { TaskListColumn } from "./TaskListColumn";

/**
 * Overview (prototype Main.dc.html): what needs attention, what is running,
 * and today's corrugation strip, beside the task list. A deployment with
 * no task yet shows how to connect an agent (Overview-Empty.dc.html).
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
        <div className="board-top">
          <span className="eyebrow">
            <span className="live-dot" aria-hidden="true" />
            {formatDate(now, locale, now)} {weekday} · {formatClock(now)}
          </span>
          <ProjectSwitch projectId={projectId} />
        </div>
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
    <Disclosure
      alignRight
      label={t("overview.switchProject", { name })}
      detailsLabel={t("overview.projects")}
      trigger={
        <>
          <Folder className="wl-icon" aria-hidden="true" />
          {name}
          <ChevronDown className="wl-icon" aria-hidden="true" />
        </>
      }
      details={
        <div className="menu" style={{ padding: 0 }}>
          <button type="button" aria-current={!projectId} onClick={() => pick(null)}>
            {!projectId ? <Check className="wl-icon" aria-hidden="true" /> : <span style={{ width: 16 }} />}
            {t("overview.allProjects")}
          </button>
          {items.map((p) => (
            <button key={p.id} type="button" aria-current={p.id === projectId} onClick={() => pick(p.id)}>
              {p.id === projectId ? <Check className="wl-icon" aria-hidden="true" /> : <span style={{ width: 16 }} />}
              {p.is_personal ? t("overview.personalProject") : p.name}
            </button>
          ))}
        </div>
      }
    />
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
      <div className="hero">
        <div className={counts.attention > 0 ? "stat stat--bad" : "stat"}>
          <span className="stat-n">{counts.attention}</span>
          <span className="stat-l">{t("overview.attention")}</span>
        </div>
        <div className={running > 0 ? "stat stat--run" : "stat"}>
          <span className="stat-n stat-n--md">{counts.running}</span>
          <span className="stat-l">
            {t("overview.running")}
            {counts.queued > 0 ? ` · ${t("overview.queuedCount", { count: counts.queued })}` : null}
          </span>
        </div>
        {next ? (
          <div className="stat">
            <span className="stat-n stat-n--md">{shortWhen(next.at, locale, now)}</span>
            <span className="stat-l">{t("overview.nextFire", { count: sameTime })}</span>
          </div>
        ) : null}
      </div>

      <TodayStrip projectId={projectId} now={now} />

      {items.length > 0 || active.length > 0 ? (
        <div className="pair">
          {items.length > 0 ? (
            <section className="stack" aria-labelledby="att-title">
              <span className="sec-label" id="att-title">
                {t("overview.attention")}
                {counts.attention > items.length ? <span className="num">{counts.attention}</span> : null}
              </span>
              <div className="att">
                {items.map((item) => (
                  <AttentionRow key={item.key} item={item} now={now} />
                ))}
              </div>
            </section>
          ) : null}
          {active.length > 0 ? (
            <section className="stack" aria-labelledby="run-title">
              <span className="sec-label" id="run-title">
                {t("overview.running")}
              </span>
              <div className="att">
                {active.map((run) => (
                  <ActiveRunRow key={run.run_id} run={run} now={now} />
                ))}
              </div>
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
      <span style={{ minWidth: 0 }}>
        <span className={item.titleMono ? "att-name mono" : "att-name"}>{item.title}</span>
        {why ? <span className="att-why">{why}</span> : null}
      </span>
      <span className="att-t">
        {due ? t("overview.dueBy", { time: when }) : when}
        <ChevronRight className="wl-icon" aria-hidden="true" />
      </span>
    </>
  );
  if (item.requestId) {
    const requestId = item.requestId;
    return (
      <button type="button" className="att-item" onClick={() => openFill(requestId)}>
        {body}
      </button>
    );
  }
  if (item.target?.kind === "run") {
    return (
      <Link to="/runs/$runId" params={{ runId: item.target.runId }} className="att-item">
        {body}
      </Link>
    );
  }
  if (item.target?.kind === "browser") {
    return (
      <ApiLink href={item.target.url} className="att-item">
        {body}
      </ApiLink>
    );
  }
  return <div className="att-item">{body}</div>;
}

function ActiveRunRow({ run, now }: { run: ActiveRun; now: number }) {
  const { t } = useI18n();
  const started = run.started_at;
  const queued = !started;
  const ms = elapsedMs(started ?? run.queued_at, null, now);
  return (
    <Link to="/runs/$runId" params={{ runId: run.run_id }} className="run-item">
      <span className="spread" style={{ flexWrap: "nowrap" }}>
        <span className="row" style={{ gap: 10, flexWrap: "nowrap", minWidth: 0 }}>
          <StatusGlyph status={run.status} />
          <span className="h4">{run.task.name}</span>
          {run.test ? <span className="tag">{t("run.test")}</span> : null}
        </span>
        <span className="num" style={{ fontSize: 20, color: queued ? "var(--ink-muted)" : undefined }}>
          {formatDuration(ms, { whole: true })}
        </span>
      </span>
      {run.current_stage ? <span className="caption muted">{run.current_stage}</span> : null}
      {queued ? <span className="caption muted">{t(runStatusMeta(run.status).label)}</span> : null}
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
    <div className="stack" style={{ gap: 6 }}>
      <span className="sec-label">{t("overview.today")}</span>
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
    </div>
  );
}

function ConnectAgent() {
  const { t } = useI18n();
  const mcp = `${origin()}/mcp`;
  return (
    <section className="stack" style={{ gap: 28, maxWidth: 560, width: "100%", margin: "auto" }} aria-labelledby="connect">
      <span className="onboard-mark" aria-hidden="true">
        <WaveGlyph size={32} />
      </span>
      <h1 className="h1" id="connect">
        {t("overview.connectAgent")}
      </h1>
      <ol className="steps">
        <li className="step">
          <span className="row" style={{ gap: 12, flexWrap: "nowrap" }}>
            <span className="step-n">1</span>
            <KeyRound className="wl-icon" width={18} aria-hidden="true" />
            <span style={{ fontWeight: 600 }}>{t("overview.stepToken")}</span>
          </span>
          <Link to="/settings/tokens" search={{ new: true }} className="chip chip--sm">
            <Plus className="wl-icon" aria-hidden="true" />
            {t("overview.newToken")}
          </Link>
        </li>
        <li className="step step--col">
          <span className="row" style={{ gap: 12 }}>
            <span className="step-n">2</span>
            <Plug className="wl-icon" width={18} aria-hidden="true" />
            <span style={{ fontWeight: 600 }}>{t("overview.stepMcp")}</span>
          </span>
          <div className="copyline" style={{ background: "var(--liner)" }}>
            <code>{mcp}</code>
            <CopyButton value={mcp} label={t("tokens.copyMcp")} icon={Copy} />
          </div>
        </li>
        <li className="step step--dashed">
          <span className="step-n">3</span>
          <WaveGlyph size={18} />
          <span className="muted">{t("overview.stepAsk")}</span>
        </li>
      </ol>
    </section>
  );
}
