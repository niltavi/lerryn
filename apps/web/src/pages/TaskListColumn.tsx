import {
  elapsedMs,
  filterTasks,
  formatDuration,
  groupTasks,
  runGlyph,
  runStatusMeta,
  taskGlyph,
  useI18n,
  useTaskList,
  type TaskSummary,
} from "@lerryn/core";
import { IconButton } from "@lerryn/ui-web";
import { Link } from "@tanstack/react-router";
import { Search, X } from "lucide-react";
import { useState } from "react";

import { useNow } from "../lib/hooks";
import { GlyphView, errorCodeLabel, shortWhen, taskGlyphLabel } from "../lib/present";
import { ListColumn } from "../shell/layout";

/**
 * The task list beside the overview and task boards, grouped as needing
 * attention, running and scheduled (prototype Main.dc.html). When the list
 * cannot be read the column says so instead of showing an empty list,
 * which would claim there are no tasks.
 */
export function TaskListColumn({ currentTaskId, projectId = null }: { currentTaskId?: string; projectId?: string | null }) {
  const { t } = useI18n();
  const tasks = useTaskList(projectId);
  const [searching, setSearching] = useState(false);
  const [query, setQuery] = useState("");
  const items = tasks.data?.items ?? [];
  const shown = filterTasks(items, query);
  const groups = groupTasks(shown);
  const live = groups.running.length > 0;
  const now = useNow(1000, live);

  return (
    <ListColumn
      title={t("task.tasks")}
      count={tasks.data ? items.length : undefined}
      label={t("task.list")}
      tools={
        items.length > 0 ? (
          <IconButton
            label={searching ? t("task.closeSearch") : t("task.search")}
            icon={searching ? X : Search}
            onPress={() => {
              setSearching((s) => !s);
              setQuery("");
            }}
          />
        ) : null
      }
    >
      {searching ? (
        <div className="list-search">
          <label className="log-find">
            <Search className="wl-icon" aria-hidden="true" />
            <input
              type="search"
              value={query}
              autoFocus
              onChange={(e) => setQuery(e.target.value)}
              aria-label={t("task.search")}
              placeholder={t("task.searchPlaceholder")}
            />
          </label>
        </div>
      ) : null}
      {tasks.isError ? (
        <div className="list-empty">
          <span>{t("states.errorTitle")}</span>
          <button className="link" type="button" style={{ border: 0, background: "none", cursor: "pointer", font: "inherit" }} onClick={() => void tasks.refetch()}>
            {t("common.retry")}
          </button>
        </div>
      ) : tasks.data && items.length === 0 ? (
        <div className="list-empty">{t("task.none")}</div>
      ) : tasks.data && shown.length === 0 ? (
        <div className="list-empty">{t("task.noMatch")}</div>
      ) : null}
      <Group label={t("overview.attention")} tasks={groups.attention} current={currentTaskId} now={now} kind="attention" />
      <Group label={t("overview.running")} tasks={groups.running} current={currentTaskId} now={now} kind="running" />
      <Group label={t("task.scheduled")} tasks={groups.scheduled} current={currentTaskId} now={now} kind="scheduled" />
    </ListColumn>
  );
}

function Group({
  label,
  tasks,
  current,
  now,
  kind,
}: {
  label: string;
  tasks: TaskSummary[];
  current?: string;
  now: number;
  kind: "attention" | "running" | "scheduled";
}) {
  if (tasks.length === 0) return null;
  return (
    <div role="group" aria-label={label}>
      <div className="list-group" aria-hidden="true">
        {label}
      </div>
      {tasks.map((task) => (
        <TaskItem key={task.id} task={task} current={task.id === current} now={now} kind={kind} />
      ))}
    </div>
  );
}

function TaskItem({ task, current, now, kind }: { task: TaskSummary; current: boolean; now: number; kind: "attention" | "running" | "scheduled" }) {
  const { t, locale } = useI18n();
  const run = task.last_run;
  let meta: string = "—";
  let sub: string | null = null;
  let bad = false;
  if (kind === "attention" && run) {
    meta = shortWhen(run.finished_at ?? run.queued_at, locale, now);
    sub = errorCodeLabel(run.error_code, t);
    bad = run.status !== "interrupted";
  } else if (kind === "running") {
    if (run && run.started_at && !run.finished_at) meta = formatDuration(elapsedMs(run.started_at, null, now), { whole: true });
    else meta = t("task.queuedShort");
  } else if (task.state === "active" && task.next_run_at) {
    meta = shortWhen(task.next_run_at, locale, now);
  }
  const glyph =
    kind === "running" ? { tone: "run" as const, icon: "wave" as const } : kind === "attention" && run ? runGlyph(run.status) : taskGlyph(task);
  const glyphLabel =
    kind === "running" ? t("run.status.running") : kind === "attention" && run ? t(runStatusMeta(run.status).label) : taskGlyphLabel(task, t);
  return (
    <Link to="/tasks/$taskId" params={{ taskId: task.id }} className="li" aria-current={current ? "page" : undefined}>
      <GlyphView glyph={glyph} label={glyphLabel} />
      <span style={{ minWidth: 0 }}>
        <span className="li-name" style={task.state === "inactive" ? { color: "var(--ink-muted)" } : undefined}>
          {task.name}
        </span>
        {sub ? <span className="li-sub">{sub}</span> : null}
      </span>
      <span className={bad ? "li-meta bad" : "li-meta"}>{meta}</span>
    </Link>
  );
}
