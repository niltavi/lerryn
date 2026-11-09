import {
  agentPanels,
  credentialSummary,
  formatBytes,
  formatDate,
  formatDateTime,
  formatDuration,
  historyNeeds,
  isRunActive,
  isNotFound,
  runRidges,
  runStatusMeta,
  taskStatus,
  taskTools,
  toolLocation,
  useI18n,
  useProjectMembers,
  useProjects,
  useResultHistory,
  useRun,
  useTask,
  useTaskCredentials,
  useTaskList,
  useTaskRuns,
  useTaskTriggers,
  useTaskVersions,
  useTools,
  type RidgeBar,
  type RunResult,
  type Task,
  type TaskCredential,
  type TaskToolView,
  type TaskVersion,
  type Trigger,
} from "@lerryn/core";
import {
  AgentPanels,
  CopyButton,
  CronDetails,
  CronText,
  InfoChip,
  ResultPanel,
  RunsStrip,
  StatusGlyph,
  ToneGlyph,
} from "@lerryn/ui-web";
import { Link, useParams } from "@tanstack/react-router";
import { Box, CalendarClock, Check, Clock, Copy, FileText, KeyRound, Link2, Webhook, X } from "lucide-react";
import { useState } from "react";

import { useNow } from "../../lib/hooks";
import { GlyphView, shortWhen } from "../../lib/present";
import { initialOf } from "../../shell/AppShell";
import { useOpenCredentialFill } from "../../shell/CredentialFill";
import { Board, LoadError, usePageTitle } from "../../shell/layout";
import { NotFoundBody } from "../NotFoundPage";
import { RunNowButton } from "../run/RunActions";
import { TaskListColumn } from "../TaskListColumn";
import { EditTaskTagsButton, TaskTags } from "./TaskTags";
import { VersionDiffDialog } from "./VersionDiffDialog";
import { TaskActions } from "./TaskActions";

export function TaskPage() {
  const { taskId } = useParams({ from: "/app/tasks/$taskId" });
  const task = useTask(taskId);
  return (
    <>
      <TaskListColumn currentTaskId={taskId} />
      {task.isPending ? (
        <Board>{null}</Board>
      ) : task.isError ? (
        <Board>
          {isNotFound(task.error) ? <NotFoundBody /> : <LoadError error={task.error} onRetry={() => void task.refetch()} />}
        </Board>
      ) : (
        <TaskBoard task={task.data} />
      )}
    </>
  );
}

const LANGUAGES: Record<string, string> = { python: "Python", node: "Node.js", shell: "Bash" };

function TaskBoard({ task }: { task: Task }) {
  const { t, locale } = useI18n();
  const title = task.title || task.name;
  usePageTitle(title);
  const now = useNow(30_000);
  const triggers = useTaskTriggers(task.id);
  const runs = useTaskRuns(task.id);
  const summary = useTaskList(null).data?.items.find((item) => item.id === task.id);
  const [diffSeq, setDiffSeq] = useState<number | null>(null);

  const version = task.latest_version;
  const m = version.manifest;
  const active = task.state === "active" && !task.archived_at;
  const allTriggers = triggers.data?.triggers ?? [];
  const nextFire = allTriggers
    .filter((tr) => tr.enabled && tr.type === "cron")
    .map((tr) => tr.next_fire_at)
    .filter((x): x is string => !!x)
    .sort()[0];
  const runItems = (runs.data?.items ?? []).filter((r) => !r.test);
  const lastRun = runItems[0];
  const activeRun = runs.data?.items.find((run) => isRunActive(run.status));
  const status = taskStatus({ ...task, active_runs: summary?.active_runs, last_run: activeRun, next_run_at: nextFire });
  // Tools the manifest names, resolved against the project's registry.
  const registry = useTools(task.project_id, { enabled: !!m.tools?.length });
  const tools = taskTools(m.tools, registry.data?.items ?? [], task.project_id);
  const toolMissing = registry.isSuccess && tools.some((v) => !v.tool);

  return (
    <Board label={title}>
      <div className="board-top">
        <span className="eyebrow" aria-live="polite" aria-atomic="true">
          <GlyphView glyph={status.glyph} label={t(status.label)} />
          {t(status.label)}
          {active && task.active_version ? <span className="mono-sm">v{task.active_version}</span> : null}
        </span>
        <div className="tools ml-auto flex-wrap justify-end">
          <RunNowButton key={task.id} task={task} triggers={triggers.data?.triggers} />
          <TaskActions key={task.id} task={task} nextFire={nextFire} />
        </div>
      </div>

      <div className="stack" style={{ gap: 6 }}>
        <div className="flex items-start justify-between gap-3">
          <h1 className="h1 min-w-0">{title}</h1>
          <div className="tools shrink-0">
            <EditTaskTagsButton key={task.id} task={task} />
            <CopyButton value={task.links.console} label={t("task.copyLink")} icon={Link2} />
          </div>
        </div>
        {task.description ? <p className="muted">{task.description}</p> : null}
        <TaskTags tags={task.tags} />
      </div>

      <div className="hero">
        {active && nextFire ? (
          <div className="stat">
            <span className="stat-n">{shortWhen(nextFire, locale, now)}</span>
            <span className="stat-l">{t("task.nextRun")}</span>
          </div>
        ) : null}
        {lastRun ? (
          <div className="stat">
            <span className="stat-n stat-n--md">{shortWhen(lastRun.started_at ?? lastRun.queued_at, locale, now)}</span>
            <span className="stat-l">
              {t("task.lastRun")}
              <StatusGlyph status={lastRun.status} />
            </span>
          </div>
        ) : runs.data ? (
          <div className="stat">
            <span className="stat-n stat-n--md stat-n--quiet">—</span>
            <span className="stat-l">{active && nextFire ? t("states.awaitingFirstRun") : t("states.noRuns")}</span>
          </div>
        ) : null}
      </div>

      {runItems.length > 0 ? <RunHistory task={task} now={now} /> : null}

      <div className="chips" aria-label={t("task.info")}>
        {allTriggers.map((tr) => (
          <TriggerChip key={tr.id} trigger={tr} taskActive={triggers.data?.task_active ?? active} />
        ))}
        <CredentialsChip taskId={task.id} now={now} />
        <InfoChip
          detailsLabel={t("task.runtime")}
          details={
            <dl className="kv">
              <dt>{t("task.entrypoint")}</dt>
              <dd className="mono-sm">{m.entrypoint.join(" ")}</dd>
              {m.runtime.lockfile ? (
                <>
                  <dt>{t("task.dependencies")}</dt>
                  <dd className="mono-sm">{m.runtime.lockfile}</dd>
                </>
              ) : null}
              {tools.length ? (
                <>
                  <dt>{t("task.tools")}</dt>
                  <dd>
                    <ToolList tools={tools} resolved={registry.isSuccess} />
                  </dd>
                </>
              ) : null}
              {m.runner_labels?.length ? (
                <>
                  <dt>{t("task.runnerLabels")}</dt>
                  <dd className="mono-sm">{m.runner_labels.join(" · ")}</dd>
                </>
              ) : null}
              <dt>{t("task.network")}</dt>
              <dd>{m.network === "none" ? t("task.networkNone") : t("task.networkFull")}</dd>
              <dt>{t("task.limits")}</dt>
              <dd>{[m.limits?.timeout ?? "10m", `${m.limits?.cpu ?? 1} CPU`, m.limits?.memory ?? "1Gi"].join(" · ")}</dd>
              <dt>{t("task.concurrency")}</dt>
              <dd>{m.concurrency === "allow" ? t("task.concurrencyAllow") : t("task.concurrencyForbid")}</dd>
              <dt>{t("task.retry")}</dt>
              <dd>
                {m.retry?.max_attempts ? t("task.retryTimes", { count: m.retry.max_attempts }) : t("task.retryOff")}
              </dd>
              {m.side_effects ? (
                <>
                  <dt>{t("task.sideEffects")}</dt>
                  <dd>{t("task.sideEffectsYes")}</dd>
                </>
              ) : null}
            </dl>
          }
        >
          <Box className="wl-icon" aria-hidden="true" />
          {`${LANGUAGES[m.runtime.language] ?? m.runtime.language} ${m.runtime.version}`}
          {toolMissing ? <span className="dot dot--bad" role="img" aria-label={t("task.toolMissing")} /> : null}
        </InfoChip>
        <InfoChip
          detailsLabel={t("task.files")}
          details={version.files.map((f) => (
            <div className="pop-row" key={f.path}>
              <span className="mono-sm" style={{ color: "var(--ink)" }}>
                {f.path}
              </span>
              <span className="mono-sm muted">{formatBytes(f.size)}</span>
            </div>
          ))}
        >
          <FileText className="wl-icon" aria-hidden="true" />
          {t("task.fileCount", { count: version.files.length })}
        </InfoChip>
        <VersionsChip task={task} onPick={setDiffSeq} />
        <OwnerChip task={task} />
      </div>

      <LastResult task={task} runId={runItems.find((r) => r.result_status !== null)?.id} />

      <VersionDiffDialog taskId={task.id} seq={diffSeq} onClose={() => setDiffSeq(null)} />
    </Board>
  );
}

function RunHistory({ task, now }: { task: Task; now: number }) {
  const { t, locale } = useI18n();
  const runs = useTaskRuns(task.id);
  const items = (runs.data?.items ?? []).slice(0, 30);
  const bars = runRidges(items, now);
  const first = bars[0];
  const describe = (bar: RidgeBar) => {
    const run = items.find((r) => r.id === bar.runId);
    const head = `${shortWhen(bar.at, locale, now)}${run?.test ? ` · ${t("run.origin.test")}` : ""}${run?.rerun_of ? ` · ${t("run.origin.rerun")}` : ""}`;
    const tail = [t(runStatusMeta(bar.status).label), bar.durationMs !== null ? formatDuration(bar.durationMs) : null, run ? `v${run.version}` : null]
      .filter(Boolean)
      .join(" · ");
    return `${head}\n${tail}`;
  };
  return (
    <section className="strip" aria-label={t("task.recentRuns")} style={{ maxWidth: 520 }}>
      <div className="spread">
        <span className="sec-label">{t("task.recentRuns")}</span>
        <Link to="/runs" search={{ task: task.id }} className="link body-sm">
          {t("task.allRuns")}
        </Link>
      </div>
      <RunsStrip
        bars={bars}
        describe={describe}
        label={t("task.recentRuns")}
        renderLink={(bar, props) => <Link to="/runs/$runId" params={{ runId: bar.runId }} {...props} />}
      />
      {first ? (
        <div className="strip-axis" aria-hidden="true">
          <span>{formatDate(first.at, locale, now)}</span>
          <span>{t("time.todayShort")}</span>
        </div>
      ) : null}
    </section>
  );
}

function TriggerChip({ trigger, taskActive }: { trigger: Trigger; taskActive: boolean }) {
  const { t } = useI18n();
  const firing = taskActive && trigger.enabled;
  if (trigger.cron) {
    return (
      <InfoChip detailsLabel={t("task.triggers")} details={<CronDetails cron={trigger.cron} name={trigger.name} firing={firing} />}>
        <CalendarClock className="wl-icon" aria-hidden="true" />
        <span style={firing ? undefined : { color: "var(--ink-muted)" }}>
          <CronText cron={trigger.cron} />
        </span>
      </InfoChip>
    );
  }
  if (trigger.webhook) {
    return (
      <InfoChip
        detailsLabel={t("trigger.webhook")}
        details={
          <div className="stack" style={{ gap: 10 }}>
            <div className="pop-title">
              <span>{trigger.name}</span>
              <span className="mono-sm">POST</span>
            </div>
            <div className="copyline">
              <code>{trigger.webhook.url}</code>
              <CopyButton value={trigger.webhook.url} label={t("trigger.copyUrl")} icon={Copy} />
            </div>
            <dl className="kv">
              <dt>{t("trigger.token")}</dt>
              <dd className="mono-sm">{trigger.webhook.token_prefix}••••</dd>
              {trigger.last_fired_at ? (
                <>
                  <dt>{t("trigger.lastFired")}</dt>
                  <dd>
                    <LastFired at={trigger.last_fired_at} />
                  </dd>
                </>
              ) : null}
            </dl>
            {!firing ? <span className="caption muted">{t("trigger.notFiring")}</span> : null}
          </div>
        }
      >
        <Webhook className="wl-icon" aria-hidden="true" />
        {t("trigger.webhook")}
      </InfoChip>
    );
  }
  return null;
}

/** The manifest's tools with their kind; a name the project never registered is marked. */
function ToolList({ tools, resolved }: { tools: TaskToolView[]; resolved: boolean }) {
  const { t } = useI18n();
  return (
    <span className="stack" style={{ gap: 4 }}>
      {tools.map(({ name, tool }) => {
        const where = tool ? toolLocation(tool) : null;
        const address = where?.kind === "url" ? where.url : where?.kind === "files" ? where.treeHash : null;
        return (
          <span className="row" style={{ gap: 6 }} key={name}>
            <span className="mono-sm" title={tool ? [tool.description, address].filter(Boolean).join("\n") : undefined}>
              {name}
            </span>
            {tool ? (
              <span className="tag tag--mono">{tool.kind.toUpperCase()}</span>
            ) : resolved ? (
              <ToneGlyph tone="bad" icon={X} label={t("task.toolMissing")} />
            ) : null}
            {where?.kind === "files" ? (
              <span className="mono-sm muted">
                {[where.shortHash, t("tools.fileCount", { count: where.fileCount })].filter(Boolean).join(" · ")}
              </span>
            ) : null}
          </span>
        );
      })}
    </span>
  );
}

function LastFired({ at }: { at: string }) {
  const { locale } = useI18n();
  return <>{formatDateTime(at, locale)}</>;
}

function CredentialsChip({ taskId, now }: { taskId: string; now: number }) {
  const { t } = useI18n();
  const creds = useTaskCredentials(taskId);
  const items = creds.data?.items ?? [];
  if (items.length === 0) return null;
  const sum = credentialSummary(items, now);
  return (
    <InfoChip
      detailsLabel={t("task.credentials")}
      details={
        <div className="stack" style={{ gap: 0 }}>
          {items.map((c) => (
            <CredentialRow key={c.name} cred={c} now={now} />
          ))}
          <Link to="/settings/credentials" className="link body-sm" style={{ marginTop: 8 }}>
            {t("task.manageCredentials")}
          </Link>
        </div>
      }
    >
      <KeyRound className="wl-icon" aria-hidden="true" />
      {t("task.credentialCount", { count: sum.total })}
      {sum.broken > 0 ? (
        <span className="dot dot--bad" role="img" aria-label={t("task.credentialBroken")} />
      ) : sum.expiring > 0 ? (
        <span className="dot" role="img" aria-label={t("task.credentialExpiring")} />
      ) : null}
    </InfoChip>
  );
}

function CredentialRow({ cred, now }: { cred: TaskCredential; now: number }) {
  const { t } = useI18n();
  const openFill = useOpenCredentialFill();
  const days = cred.expires_at ? Math.ceil((Date.parse(cred.expires_at) - now) / 86_400_000) : null;
  let status;
  if (cred.status === "bound" && days !== null && days <= 7) {
    status = (
      <span className="row" style={{ gap: 6, color: "var(--warning-ink)", fontSize: 13 }}>
        {t("task.daysLeft", { count: Math.max(0, days) })}
        <ToneGlyph tone="warn" icon={Clock} label={t("task.credentialExpiring")} />
      </span>
    );
  } else if (cred.status === "bound") {
    status = <ToneGlyph tone="ok" icon={Check} label={t("task.credentialOk")} />;
  } else {
    const label = cred.status === "missing" ? t("task.credentialMissing") : cred.status === "revoked" ? t("credentials.revoked") : t("credentials.expired");
    status = (
      <span className="row" style={{ gap: 6, fontSize: 13 }}>
        {cred.pending_request_id ? (
          <button
            className="link"
            type="button"
            style={{ border: 0, background: "none", cursor: "pointer", font: "inherit" }}
            onClick={() => cred.pending_request_id && openFill(cred.pending_request_id)}
          >
            {t("credentials.fill")}
          </button>
        ) : null}
        <ToneGlyph tone={cred.optional ? "idle" : "bad"} icon={X} label={cred.optional ? `${label} · ${t("task.optional")}` : label} />
      </span>
    );
  }
  return (
    <div className="pop-row">
      <span className="mono" style={{ color: "var(--ink)" }}>
        {cred.name}
      </span>
      {status}
    </div>
  );
}

function VersionsChip({ task, onPick }: { task: Task; onPick: (seq: number) => void }) {
  const { t, locale } = useI18n();
  const versions = useTaskVersions(task.id);
  const items = versions.data?.items ?? [];
  const latest = task.latest_version as TaskVersion;
  return (
    <InfoChip
      alignRight
      detailsLabel={t("task.versions")}
      details={
        <div className="stack" style={{ gap: 0 }}>
          {(items.length ? items : [{ seq: latest.seq, created_at: latest.created_at, active: task.active_version === latest.seq, tested_at: latest.tested_at, client: latest.client }]).map((v) => (
            <div className="pop-row" key={v.seq}>
              <span className="row" style={{ gap: 6 }}>
                {v.seq > 1 ? (
                  <button
                    type="button"
                    className="mono link"
                    style={{ border: 0, background: "none", padding: 0, cursor: "pointer" }}
                    aria-label={t("task.showDiff", { seq: v.seq })}
                    onClick={() => onPick(v.seq)}
                  >
                    v{v.seq}
                  </button>
                ) : (
                  <span className="mono">v{v.seq}</span>
                )}
                {v.active ? <span className="tag tag--kraft">{t("task.current")}</span> : null}
              </span>
              <span className="row" style={{ gap: 6 }}>
                <span className="caption muted">{v.client ?? formatDateTime(v.created_at, locale)}</span>
                {v.tested_at ? <ToneGlyph tone="ok" icon={Check} label={t("task.tested")} /> : null}
              </span>
            </div>
          ))}
        </div>
      }
    >
      <span className="mono-sm">v{latest.seq}</span>
      {task.active_version && task.active_version !== latest.seq ? (
        <span className="caption muted">{t("task.activeIs", { seq: task.active_version })}</span>
      ) : null}
    </InfoChip>
  );
}

function OwnerChip({ task }: { task: Task }) {
  const { t, locale } = useI18n();
  const projects = useProjects();
  const members = useProjectMembers(task.project_id);
  const project = projects.data?.items.find((p) => p.id === task.project_id);
  const owner = members.data?.items.find((m) => m.user.id === task.created_by)?.user;
  if (!owner && !project) return null;
  const ownerName = owner ? owner.display_name || owner.username : null;
  return (
    <InfoChip
      alignRight
      detailsLabel={t("task.owner")}
      details={
        <dl className="kv">
          {ownerName ? (
            <>
              <dt>{t("task.owner")}</dt>
              <dd>{ownerName}</dd>
            </>
          ) : null}
          {project ? (
            <>
              <dt>{t("task.project")}</dt>
              <dd>{project.is_personal ? t("overview.personalProject") : project.name}</dd>
            </>
          ) : null}
          {task.latest_version.client ? (
            <>
              <dt>{t("task.submittedBy")}</dt>
              <dd>{task.latest_version.client}</dd>
            </>
          ) : null}
          <dt>{t("task.created")}</dt>
          <dd>{formatDateTime(task.created_at, locale)}</dd>
        </dl>
      }
    >
      {owner ? (
        <>
          <span className="avatar avatar--sm" aria-hidden="true">
            {initialOf(owner)}
          </span>
          {ownerName}
        </>
      ) : (
        project?.name
      )}
    </InfoChip>
  );
}

/** The last reported result: the task's Agent panels, or the plain result when it declared none. */
function LastResult({ task, runId }: { task: Task; runId: string | undefined }) {
  const run = useRun(runId ?? "", { enabled: !!runId });
  if (!runId || !run.data?.result) return null;
  return <ResultBody task={task} result={run.data.result} at={run.data.finished_at ?? run.data.queued_at} />;
}

function ResultBody({ task, result, at }: { task: Task; result: RunResult; at: string }) {
  const { t, locale } = useI18n();
  const manifest = task.latest_version.manifest;
  const need = historyNeeds(manifest.panels);
  const history = useResultHistory(task.id, need, need > 0);
  const histories = Object.fromEntries((manifest.panels ?? []).filter((p) => p.history).map((p) => [p.id, history.points]));
  const panels = agentPanels(manifest.panels, result.data, { history: histories, historyLabel: (iso) => formatDate(iso, locale) });
  if (panels.length === 0) return <ResultPanel result={result} />;
  return (
    <section className="stack" aria-label={t("task.lastResult")}>
      <span className="sec-label">
        {t("task.lastResult")}
        <span className="caption muted" style={{ fontWeight: 400 }}>
          {formatDateTime(at, locale)}
        </span>
      </span>
      <AgentPanels panels={panels} />
    </section>
  );
}
