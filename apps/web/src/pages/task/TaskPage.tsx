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
  Button,
  Card,
  Chip,
  CollapsiblePanel,
  CopyButton,
  CronDetails,
  CronText,
  InfoChip,
  ResultPanel,
  RunsStrip,
  StatusGlyph,
  ToneGlyph,
  WaveGlyph,
} from "@lerryn/ui-web";
import { Link, useParams } from "@tanstack/react-router";
import { Calendar, Check, Clock, Copy, Cube, FileText, Link as LinkIcon, Person, Thunderbolt, Xmark } from "@gravity-ui/icons";
import { useState } from "react";

import { useNow } from "../../lib/hooks";
import { GlyphView, shortWhen } from "../../lib/present";
import { useOpenCredentialFill } from "../../shell/CredentialFill";
import { Board, BoardTop, LoadError, usePageTitle } from "../../shell/layout";
import { NotFoundBody } from "../NotFoundPage";
import { RunNowButton } from "../run/RunActions";
import { TaskListColumn } from "../TaskListColumn";
import { EditTaskTagsButton, TaskTags } from "./TaskTags";
import { VersionDiffDialog } from "./VersionDiffDialog";
import { TaskActions } from "./TaskActions";
import { TaskWorkflow } from "./TaskWorkflow";

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
        // Keyed by task so the header's buttons and dialogs start fresh for each one.
        <TaskBoard key={task.data.id} task={task.data} />
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
      <BoardTop title={title} description={task.description || undefined}>
        <div className="flex items-center gap-1">
          <EditTaskTagsButton task={task} />
          <CopyButton value={task.links.console} label={t("task.copyLink")} icon={LinkIcon} />
        </div>
        <RunNowButton task={task} triggers={triggers.data?.triggers} />
        <TaskActions task={task} nextFire={nextFire} />
      </BoardTop>

      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <span className="flex items-center gap-2 text-sm text-muted" aria-live="polite" aria-atomic="true">
          <GlyphView glyph={status.glyph} label={t(status.label)} />
          {t(status.label)}
          {active && task.active_version ? <span className="font-mono text-xs">v{task.active_version}</span> : null}
        </span>
        <TaskTags tags={task.tags} />
      </div>

      <div className="hero tpl-grid tpl-grid--kpi">
        {active && nextFire ? (
          <Card className="stat tpl-kpi">
            <span className="tpl-kpi__label">{t("task.nextRun")}</span>
            <strong className="stat-n tpl-kpi__value">{shortWhen(nextFire, locale, now)}</strong>
          </Card>
        ) : null}
        {lastRun ? (
          <Card className="stat tpl-kpi">
            <span className="tpl-kpi__label">{t("task.lastRun")}</span>
            <div className="tpl-kpi__row">
              <strong className="stat-n tpl-kpi__value">{shortWhen(lastRun.started_at ?? lastRun.queued_at, locale, now)}</strong>
              <StatusGlyph status={lastRun.status} />
            </div>
          </Card>
        ) : runs.data ? (
          <Card className="stat tpl-kpi">
            <span className="tpl-kpi__label">{active && nextFire ? t("states.awaitingFirstRun") : t("states.noRuns")}</span>
            <strong className="stat-n tpl-kpi__value text-muted">—</strong>
          </Card>
        ) : null}
      </div>

      {runItems.length > 0 ? <RunHistory task={task} now={now} /> : null}

      <div className="flex flex-wrap gap-2" role="group" aria-label={t("task.info")}>
        {allTriggers.map((tr) => (
          <TriggerChip key={tr.id} trigger={tr} taskActive={triggers.data?.task_active ?? active} />
        ))}
        <InfoChip
          detailsLabel={t("task.runtime")}
          details={
            <dl className="kv">
              <dt>{t("task.entrypoint")}</dt>
              <dd className="font-mono text-xs">{m.entrypoint.join(" ")}</dd>
              {m.runtime.lockfile ? (
                <>
                  <dt>{t("task.dependencies")}</dt>
                  <dd className="font-mono text-xs">{m.runtime.lockfile}</dd>
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
                  <dd className="font-mono text-xs">{m.runner_labels.join(" · ")}</dd>
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
          <Cube width={16} height={16} aria-hidden="true" />
          {`${LANGUAGES[m.runtime.language] ?? m.runtime.language} ${m.runtime.version}`}
          {toolMissing ? <span className="tk-pip dot--bad" role="img" aria-label={t("task.toolMissing")} /> : null}
        </InfoChip>
        <InfoChip
          detailsLabel={t("task.files")}
          details={
            <ul className="lr-rows">
              {version.files.map((f) => (
                <li className="lr-row" key={f.path}>
                  <span className="min-w-0 break-all font-mono text-xs">{f.path}</span>
                  <span className="lr-row__end font-mono text-xs text-muted">{formatBytes(f.size)}</span>
                </li>
              ))}
            </ul>
          }
        >
          <FileText width={16} height={16} aria-hidden="true" />
          {t("task.fileCount", { count: version.files.length })}
        </InfoChip>
        <VersionsChip task={task} onPick={setDiffSeq} />
        <OwnerChip task={task} />
      </div>

      <CredentialsCard taskId={task.id} now={now} />

      <TaskWorkflow key={`workflow-${task.id}`} version={version} />
      <LastResult key={`result-${task.id}`} task={task} runId={runItems.find((r) => r.result_status !== null)?.id} />

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
    <section className="tk-history" aria-label={t("task.recentRuns")}>
      <Card className="tpl-card">
        <div className="tpl-card__head">
          <h4>{t("task.recentRuns")}</h4>
          <Link to="/runs" search={{ task: task.id }} className="tk-link">
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
          <div className="flex justify-between font-mono text-xs text-muted" aria-hidden="true">
            <span>{formatDate(first.at, locale, now)}</span>
            <span>{t("time.todayShort")}</span>
          </div>
        ) : null}
      </Card>
    </section>
  );
}

function TriggerChip({ trigger, taskActive }: { trigger: Trigger; taskActive: boolean }) {
  const { t } = useI18n();
  const firing = taskActive && trigger.enabled;
  if (trigger.cron) {
    return (
      <InfoChip detailsLabel={t("task.triggers")} details={<CronDetails cron={trigger.cron} name={trigger.name} firing={firing} />}>
        <Calendar width={16} height={16} aria-hidden="true" />
        <span className={firing ? undefined : "text-muted"}>
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
          <div className="flex flex-col gap-3">
            <div className="flex items-baseline justify-between gap-3">
              <span className="min-w-0 truncate font-medium">{trigger.name}</span>
              <span className="font-mono text-xs text-muted">POST</span>
            </div>
            <div className="lr-copy lr-copy--wrap">
              <code>{trigger.webhook.url}</code>
              <CopyButton value={trigger.webhook.url} label={t("trigger.copyUrl")} icon={Copy} />
            </div>
            <dl className="kv">
              <dt>{t("trigger.token")}</dt>
              <dd className="font-mono text-xs">{trigger.webhook.token_prefix}••••</dd>
              {trigger.last_fired_at ? (
                <>
                  <dt>{t("trigger.lastFired")}</dt>
                  <dd>
                    <LastFired at={trigger.last_fired_at} />
                  </dd>
                </>
              ) : null}
            </dl>
            {!firing ? <span className="text-xs text-muted">{t("trigger.notFiring")}</span> : null}
          </div>
        }
      >
        <Thunderbolt width={16} height={16} aria-hidden="true" />
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
    <span className="flex flex-col gap-1">
      {tools.map(({ name, tool }) => {
        const where = tool ? toolLocation(tool) : null;
        const address = where?.kind === "url" ? where.url : where?.kind === "files" ? where.treeHash : null;
        return (
          <span className="flex flex-wrap items-center gap-1.5" key={name}>
            <span className="font-mono text-xs" title={tool ? [tool.description, address].filter(Boolean).join("\n") : undefined}>
              {name}
            </span>
            {tool ? (
              <Chip size="sm" variant="soft" className="font-mono">{tool.kind.toUpperCase()}</Chip>
            ) : resolved ? (
              <ToneGlyph tone="bad" icon={Xmark} label={t("task.toolMissing")} />
            ) : null}
            {where?.kind === "files" ? (
              <span className="font-mono text-xs text-muted">
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

function CredentialsCard({ taskId, now }: { taskId: string; now: number }) {
  const { t } = useI18n();
  const creds = useTaskCredentials(taskId);
  const items = creds.data?.items ?? [];
  if (items.length === 0) return null;
  const sum = credentialSummary(items, now);
  return (
    <section className="min-w-0" aria-label={t("task.credentials")}>
      <Card className="tpl-card">
        <div className="tpl-card__head">
          <h4 className="flex items-center gap-2">
            {t("task.credentialCount", { count: sum.total })}
            {sum.broken > 0 ? (
              <span className="tk-pip dot--bad" role="img" aria-label={t("task.credentialBroken")} />
            ) : sum.expiring > 0 ? (
              <span className="tk-pip" role="img" aria-label={t("task.credentialExpiring")} />
            ) : null}
          </h4>
          <Link to="/settings/credentials" className="tk-link">
            {t("task.manageCredentials")}
          </Link>
        </div>
        <ul className="lr-rows">
          {items.map((c) => (
            <CredentialRow key={c.name} cred={c} now={now} />
          ))}
        </ul>
      </Card>
    </section>
  );
}

function CredentialRow({ cred, now }: { cred: TaskCredential; now: number }) {
  const { t } = useI18n();
  const openFill = useOpenCredentialFill();
  const days = cred.expires_at ? Math.ceil((Date.parse(cred.expires_at) - now) / 86_400_000) : null;
  let status;
  if (cred.status === "bound" && days !== null && days <= 7) {
    status = (
      <>
        <span className="text-xs text-warning-soft-foreground">{t("task.daysLeft", { count: Math.max(0, days) })}</span>
        <ToneGlyph tone="warn" icon={Clock} label={t("task.credentialExpiring")} />
      </>
    );
  } else if (cred.status === "bound") {
    status = <ToneGlyph tone="ok" icon={Check} label={t("task.credentialOk")} />;
  } else {
    const label = cred.status === "missing" ? t("task.credentialMissing") : cred.status === "revoked" ? t("credentials.revoked") : t("credentials.expired");
    status = (
      <>
        {cred.pending_request_id ? (
          <Button variant="secondary" size="sm" onPress={() => cred.pending_request_id && openFill(cred.pending_request_id)}>
            {t("credentials.fill")}
          </Button>
        ) : null}
        <ToneGlyph tone={cred.optional ? "idle" : "bad"} icon={Xmark} label={cred.optional ? `${label} · ${t("task.optional")}` : label} />
      </>
    );
  }
  return (
    <li className="lr-row">
      <span className="lr-row__text">
        <span className="lr-row__title font-mono text-sm">{cred.name}</span>
      </span>
      <span className="lr-row__end">{status}</span>
    </li>
  );
}

function VersionsChip({ task, onPick }: { task: Task; onPick: (seq: number) => void }) {
  const { t, locale } = useI18n();
  const versions = useTaskVersions(task.id);
  const items = versions.data?.items ?? [];
  const latest = task.latest_version as TaskVersion;
  const rows = items.length
    ? items
    : [{ seq: latest.seq, created_at: latest.created_at, active: task.active_version === latest.seq, tested_at: latest.tested_at, client: latest.client }];
  return (
    <InfoChip
      alignRight
      detailsLabel={t("task.versions")}
      details={
        <ul className="lr-rows tk-versions">
          {rows.map((v) => (
            <li className="pop-row" key={v.seq}>
              <span className="flex min-w-0 items-center gap-2">
                {v.seq > 1 ? (
                  <Button variant="tertiary" size="sm" className="font-mono" aria-label={t("task.showDiff", { seq: v.seq })} onPress={() => onPick(v.seq)}>
                    v{v.seq}
                  </Button>
                ) : (
                  <span className="px-3 font-mono text-sm">v{v.seq}</span>
                )}
                {v.active ? <Chip size="sm" variant="soft" color="accent">{t("task.current")}</Chip> : null}
              </span>
              <span className="flex items-center gap-2">
                <span className="text-xs text-muted">{v.client ?? formatDateTime(v.created_at, locale)}</span>
                {v.tested_at ? <ToneGlyph tone="ok" icon={Check} label={t("task.tested")} /> : null}
              </span>
            </li>
          ))}
        </ul>
      }
    >
      <span className="font-mono text-xs">v{latest.seq}</span>
      {task.active_version && task.active_version !== latest.seq ? (
        <span className="text-xs text-muted">{t("task.activeIs", { seq: task.active_version })}</span>
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
          <Person width={16} height={16} aria-hidden="true" />
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
  if (panels.length === 0) return <ResultPanel result={result} collapsible />;
  return (
    <CollapsiblePanel
      title={t("task.lastResult")}
      icon={<WaveGlyph size={18} />}
      aside={<span className="text-xs text-muted">{formatDateTime(at, locale)}</span>}
    >
      <AgentPanels panels={panels} />
    </CollapsiblePanel>
  );
}
