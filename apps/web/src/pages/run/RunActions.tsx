import { Button, CronText, Dropdown, IconButton, Tooltip } from "@lerryn/ui-web";
import { isApiError, useCanControlRuns, useI18n, useRerunRun, useStartRun, type Run, type Task, type Translate, type Trigger } from "@lerryn/core";
import { useNavigate } from "@tanstack/react-router";
import { ChevronDown, Play, RotateCcw } from "lucide-react";

/** Why a run could not start, in a few words for the button's tooltip. */
function runActionError(err: unknown, t: Translate): string {
  if (!isApiError(err)) return t("runAction.failed");
  switch (err.code) {
    case "run_in_progress":
      return t("runAction.inProgress");
    case "queue_full":
      return t("runAction.queueFull");
    case "manual_run_disabled":
      return t("runAction.disabled");
    case "task_archived":
      return t("runAction.archived");
    case "permission_denied":
    case "insufficient_scope":
      return t("runAction.denied");
    default:
      return t("runAction.failed");
  }
}

/**
 * Starts a run of the task and opens it. Shown only when the agent left
 * manual runs on, the task is not archived and the user may control runs.
 */
export function RunNowButton({ task, triggers }: { task: Task; triggers: readonly Trigger[] | undefined }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const canControl = useCanControlRuns(task.project_id);
  const start = useStartRun();
  if (!canControl || task.archived_at || !task.console.manual_run || !triggers) return null;

  const schedules = triggers.filter((trigger) => trigger.type === "cron" && trigger.enabled);
  const hasChoices = schedules.length > 1;
  const label = t("task.runNow");
  const runNow = (schedule?: Trigger) => {
    const params = schedule?.params;
    start.mutate(params == null ? task.id : { taskId: task.id, params }, {
      onSuccess: (run) => void navigate({ to: "/runs/$runId", params: { runId: run.id } }),
    });
  };
  const button = (
    <Button variant="primary" size="sm" isPending={start.isPending} isDisabled={hasChoices && start.isPending}
      onPress={hasChoices ? undefined : () => runNow(schedules[0])}>
      <Play className="wl-icon" aria-hidden="true" />
      {label}
      {hasChoices ? <ChevronDown className="wl-icon" aria-hidden="true" /> : null}
    </Button>
  );

  return (
    <Tooltip delay={300} isDisabled={!start.isError}>
      {hasChoices ? (
        <Dropdown>
          {button}
          <Dropdown.Popover className="max-w-[calc(100vw-2rem)]" placement="bottom end">
            <Dropdown.Menu aria-label={label}>
              {schedules.map((schedule) => (
                <Dropdown.Item key={schedule.id} id={schedule.id} textValue={schedule.name} onAction={() => runNow(schedule)}>
                  <span className="flex min-w-0 flex-col [overflow-wrap:anywhere]">
                    <span>{schedule.name}</span>
                    {schedule.cron ? <CronText cron={schedule.cron} className="caption muted" /> : null}
                  </span>
                </Dropdown.Item>
              ))}
            </Dropdown.Menu>
          </Dropdown.Popover>
        </Dropdown>
      ) : button}
      <Tooltip.Content>{runActionError(start.error, t)}</Tooltip.Content>
    </Tooltip>
  );
}

/** Reruns a finished run and opens the new one. */
export function RetryButton({ run, taskArchived }: { run: Run; taskArchived: boolean }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const canControl = useCanControlRuns(run.project_id);
  const rerun = useRerunRun();
  if (!canControl || taskArchived) return null;
  const label = t("run.retryNow");
  return (
    <IconButton
      label={label}
      tip={rerun.isError ? runActionError(rerun.error, t) : label}
      icon={RotateCcw}
      isDisabled={rerun.isPending}
      onPress={() => rerun.mutate(run.id, { onSuccess: (next) => void navigate({ to: "/runs/$runId", params: { runId: next.id } }) })}
    />
  );
}
