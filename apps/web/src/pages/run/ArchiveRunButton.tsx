import { isApiError, isRunActive, useCanControlRuns, useI18n, useSetRunArchive, type Run } from "@lerryn/core";
import { Button, ConsoleDialog, IconButton } from "@lerryn/ui-web";
import { Archive, ArchiveRestore } from "lucide-react";
import { useRef, useState } from "react";

export function ArchiveRunButton({ run }: { run: Run }) {
  const { t } = useI18n();
  const canControl = useCanControlRuns(run.project_id);
  const update = useSetRunArchive();
  const [target, setTarget] = useState<boolean | null>(null);
  const submitting = useRef(false);
  if (!canControl || isRunActive(run.status)) return null;
  const archived = !!run.archived_at;
  const label = t(archived ? "run.unarchive" : "run.archive");
  const title = target === null ? label : t(target ? "run.archive" : "run.unarchive");
  const close = () => { if (!submitting.current) setTarget(null); };
  const submit = async () => {
    if (submitting.current || target === null) return;
    submitting.current = true;
    try {
      await update.mutateAsync({ runId: run.id, taskId: run.task_id, archived: target });
      setTarget(null);
    } catch {
      // Keep the confirmation open so the failure remains visible.
    } finally {
      submitting.current = false;
    }
  };
  const error = isApiError(update.error) ? update.error.code : null;
  const message = error === "expired" ? t("run.archiveExpired")
    : error === "validation_failed" ? t("run.archiveActive")
    : error === "permission_denied" || error === "insufficient_scope" ? t("runAction.denied")
    : t("run.archiveFailed");
  return <>
    <IconButton label={label} icon={archived ? ArchiveRestore : Archive}
      isDisabled={update.isPending || (!archived && !!run.logs_expired_at)}
      tip={!archived && run.logs_expired_at ? t("run.archiveExpired") : label}
      onPress={() => { update.reset(); setTarget(!archived); }} />
    <ConsoleDialog isOpen={target !== null} onOpenChange={(value) => { if (!value) close(); }} title={title} isDismissable={!update.isPending}
      footer={<>
        <Button variant="ghost" onPress={close} isDisabled={update.isPending}>{t("common.cancel")}</Button>
        <Button onPress={() => void submit()} isPending={update.isPending}>{title}</Button>
      </>}>
      <p>{t(target ? "run.archiveBody" : "run.unarchiveBody")}</p>
      {update.isError ? <p role="alert" className="danger">{message}</p> : null}
    </ConsoleDialog>
  </>;
}
