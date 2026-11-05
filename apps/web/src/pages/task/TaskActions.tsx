import { formatDateTime, isApiError, useCanControlRuns, useI18n, useTaskControl, type Task, type TaskControl } from "@lerryn/core";
import { Button, ConsoleDialog, IconButton } from "@lerryn/ui-web";
import { Pause, Play, SkipForward, Trash2 } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { useRef, useState } from "react";

/** Lifecycle controls use the same project roles as run controls. */
export function TaskActions({ task, nextFire }: { task: Task; nextFire: string | undefined }) {
  const { t, locale } = useI18n();
  const canModify = useCanControlRuns(task.project_id);
  const control = useTaskControl(task.id);
  const navigate = useNavigate();
  const [choice, setChoice] = useState<TaskControl | null>(null);
  const submitting = useRef(false);
  if (!canModify || task.archived_at) return null;

  const active = task.state === "active";
  const title = choice?.action === "delete" ? t("task.delete") : choice?.action === "skip" ? t("task.skipNext") : choice?.action === "pause" ? t("task.stop") : t("task.enable");
  const open = (input: TaskControl) => { control.reset(); setChoice(input); };
  const close = () => { if (!submitting.current) setChoice(null); };
  const submit = async () => {
    if (!choice || submitting.current) return;
    submitting.current = true;
    try {
      await control.mutateAsync(choice);
      setChoice(null);
      if (choice.action === "delete") void navigate({ to: "/" });
    } catch {
      // The dialog keeps the error and the exact choice the user confirmed.
    } finally {
      submitting.current = false;
    }
  };
  const error = isApiError(control.error) ? control.error.code : null;
  const message = error === "version_not_tested" ? t("task.enableNeedsTest")
    : error === "validation_failed" ? t("task.scheduleChanged")
    : error === "permission_denied" || error === "insufficient_scope" ? t("runAction.denied")
    : t("task.controlFailed");

  return (
    <>
      {active && nextFire ? <IconButton label={t("task.skipNext")} icon={SkipForward} isDisabled={control.isPending}
        onPress={() => open({ action: "skip", scheduledAt: nextFire })} /> : null}
      <Button variant="secondary" size="sm" isDisabled={control.isPending}
        onPress={() => open(active ? { action: "pause" } : { action: "resume", version: task.active_version ?? task.latest_version.seq })}>
        {active ? <Pause className="wl-icon" aria-hidden="true" /> : <Play className="wl-icon" aria-hidden="true" />}
        {active ? t("task.stop") : t("task.enable")}
      </Button>
      <IconButton label={t("task.delete")} icon={Trash2} isDisabled={control.isPending} onPress={() => open({ action: "delete" })} />
      <ConsoleDialog isOpen={choice !== null} onOpenChange={(isOpen) => { if (!isOpen) close(); }} title={title}
        isDismissable={!control.isPending}
        footer={<>
          <Button variant="ghost" isDisabled={control.isPending} onPress={close}>{t("common.cancel")}</Button>
          <Button variant={choice?.action === "delete" ? "danger" : "primary"} isPending={control.isPending}
            isDisabled={choice?.action === "skip" && choice.scheduledAt !== nextFire}
            onPress={() => void submit()}>{title}</Button>
        </>}>
        <p>{choice?.action === "delete" ? t("task.deleteBody", { name: task.name })
          : choice?.action === "skip" ? t("task.skipNextBody", { time: formatDateTime(choice.scheduledAt, locale) })
          : choice?.action === "pause" ? t("task.stopBody")
          : t("task.enableBody", { version: choice?.action === "resume" ? choice.version : "" })}</p>
        {control.isError ? <p className="danger" role="alert">{message}</p> : null}
      </ConsoleDialog>
    </>
  );
}
