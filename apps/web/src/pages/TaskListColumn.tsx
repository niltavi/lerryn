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
import { Button, IconButton, ListBox, SearchField, Select } from "@lerryn/ui-web";
import { Magnifier, Xmark } from "@gravity-ui/icons";
import { Link } from "@tanstack/react-router";
import { useState } from "react";

import { useNow } from "../lib/hooks";
import { GlyphView, errorCodeLabel, shortWhen, taskGlyphLabel } from "../lib/present";
import { ListColumn } from "../shell/layout";

/** Select keys: "all", or a tag behind a prefix so no tag can collide with it. */
const ALL_TAGS = "all";
const tagKey = (tag: string) => `tag:${tag}`;

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
            icon={searching ? Xmark : Magnifier}
            onPress={() => {
              setSearching((s) => !s);
              setQuery("");
            }}
          />
        ) : null
      }
    >
      {tags.length > 0 ? (
        <div className="lr-list__search">
          <Select
            fullWidth
            aria-label={t("task.filterByTag")}
            value={selectedTag ? tagKey(selectedTag) : ALL_TAGS}
            onChange={(key) => setTag(key === null || key === ALL_TAGS ? "" : String(key).slice(tagKey("").length))}
          >
            <Select.Trigger>
              <Select.Value />
              <Select.Indicator />
            </Select.Trigger>
            <Select.Popover>
              <ListBox>
                <ListBox.Item id={ALL_TAGS} textValue={t("task.allTags")}>
                  {t("task.allTags")}
                  <ListBox.ItemIndicator />
                </ListBox.Item>
                {tags.map((tag) => (
                  <ListBox.Item key={tag} id={tagKey(tag)} textValue={tag}>
                    {tag}
                    <ListBox.ItemIndicator />
                  </ListBox.Item>
                ))}
              </ListBox>
            </Select.Popover>
          </Select>
        </div>
      ) : null}
      {searching ? (
        <div className="lr-list__search">
          <SearchField fullWidth autoFocus aria-label={t("task.search")} value={query} onChange={setQuery}>
            <SearchField.Group>
              <SearchField.SearchIcon />
              <SearchField.Input placeholder={t("task.searchPlaceholder")} />
              <SearchField.ClearButton />
            </SearchField.Group>
          </SearchField>
        </div>
      ) : null}
      {tasks.isError ? (
        <div className="lr-list__empty">
          <span>{t("states.errorTitle")}</span>
          <Button variant="ghost" size="sm" onPress={() => void tasks.refetch()}>
            {t("common.retry")}
          </Button>
        </div>
      ) : tasks.data && items.length === 0 ? (
        <div className="lr-list__empty">{t("task.none")}</div>
      ) : tasks.data && shown.length === 0 ? (
        <div className="lr-list__empty">{t("task.noMatch")}</div>
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
      <div className="lr-list__group" aria-hidden="true">
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
      <span className="min-w-0">
        <span className="li-name" data-quiet={kind === "inactive" ? "" : undefined}>
          {task.title || task.name}
        </span>
        {sub ? <span className="li-sub">{sub}</span> : null}
        {tags ? <span className="li-sub" title={tags}>{tags}</span> : null}
      </span>
      <span className={bad ? "li-meta bad" : "li-meta"}>{meta}</span>
    </Link>
  );
}
