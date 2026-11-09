import {
  elapsedMs,
  filterTasks,
  formatDuration,
  groupTasks,
  isRunActive,
  runGlyph,
  runStatusMeta,
  taskGlyph,
  taskTagOptions,
  tasksWithTag,
  useI18n,
  useTaskList,
  type TaskGroups,
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
 * attention, running, scheduled, enabled and inactive. When the list
 * cannot be read the column says so instead of showing an empty list,
 * which would claim there are no tasks.
 */
export function TaskListColumn({ currentTaskId, projectId = null }: { currentTaskId?: string; projectId?: string | null }) {
  const { t, locale } = useI18n();
  const tasks = useTaskList(projectId);
  const [searching, setSearching] = useState(false);
  const [query, setQuery] = useState("");
  const [tag, setTag] = useState("");
  const items = tasks.data?.items ?? [];
  const tags = taskTagOptions(items, locale);
  const selectedTag = tags.includes(tag) ? tag : "";
  const shown = filterTasks(tasksWithTag(items, selectedTag), query);
  const groups = groupTasks(shown);
  const live = groups.running.length > 0;
  const now = useNow(1000, live);

  return (
    <ListColumn
      title={t("task.tasks")}
      count={tasks.data ? shown.length : undefined}
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
      {tags.length > 0 ? (
        <div className="list-search field-select">
          <select value={selectedTag} onChange={(e) => setTag(e.target.value)} aria-label={t("task.filterByTag")}>
            <option value="">{t("task.allTags")}</option>
            {tags.map((tag) => <option key={tag} value={tag}>{tag}</option>)}
          </select>
        </div>
      ) : null}
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
      <Group label={t("task.active")} tasks={groups.enabled} current={currentTaskId} now={now} kind="enabled" />
      <Group label={t("task.inactiveGroup")} tasks={groups.inactive} current={currentTaskId} now={now} kind="inactive" />
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
  kind: keyof TaskGroups;
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

function TaskItem({ task, current, now, kind }: { task: TaskSummary; current: boolean; now: number; kind: keyof TaskGroups }) {
  const { t, locale } = useI18n();
  const run = task.last_run;
  const tags = task.tags?.join(" · ");
  let meta: string = "—";
  let sub: string | null = null;
  let bad = false;
  if (kind === "attention" && run) {
    meta = shortWhen(run.finished_at ?? run.queued_at, locale, now);
    sub = errorCodeLabel(run.error_code, t);
    bad = run.status !== "interrupted";
  } else if (kind === "running") {
    if (run && isRunActive(run.status) && run.started_at && !run.finished_at) meta = formatDuration(elapsedMs(run.started_at, null, now), { whole: true });
    else meta = run?.status === "queued" ? t("task.queuedShort") : taskGlyphLabel(task, t);
  } else if (task.state === "active" && task.next_run_at) {
    meta = shortWhen(task.next_run_at, locale, now);
  }
  const glyph =
    kind === "attention" && run ? runGlyph(run.status) : taskGlyph(task);
  const glyphLabel =
    kind === "attention" && run ? t(runStatusMeta(run.status).label) : taskGlyphLabel(task, t);
  return (
    <Link to="/tasks/$taskId" params={{ taskId: task.id }} className="li" aria-current={current ? "page" : undefined}>
      <GlyphView glyph={glyph} label={glyphLabel} />
      <span style={{ minWidth: 0 }}>
        <span className="li-name" style={kind === "inactive" ? { color: "var(--ink-muted)" } : undefined}>
          {task.name}
        </span>
        {sub ? <span className="li-sub">{sub}</span> : null}
        {tags ? <span className="li-sub" title={tags}>{tags}</span> : null}
      </span>
      <span className={bad ? "li-meta bad" : "li-meta"}>{meta}</span>
    </Link>
  );
}
