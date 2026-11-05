import { IconButton } from "@lerryn/ui-web";
import { isApiError, useCanControlRuns, useI18n, useRerunRun, useStartRun, type Run, type Translate } from "@lerryn/core";
import { useNavigate } from "@tanstack/react-router";
import { Play, RotateCcw } from "lucide-react";

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
export function RunNowButton({ task }: { task: { id: string; project_id: string; archived_at: string | null; console: { manual_run: boolean } } }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const canControl = useCanControlRuns(task.project_id);
  const start = useStartRun();
  if (!canControl || task.archived_at || !task.console.manual_run) return null;
  const label = t("task.runNow");
  return (
    <IconButton
      label={label}
      tip={start.isError ? runActionError(start.error, t) : label}
      icon={Play}
      isDisabled={start.isPending}
      onPress={() => start.mutate(task.id, { onSuccess: (run) => void navigate({ to: "/runs/$runId", params: { runId: run.id } }) })}
    />
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
