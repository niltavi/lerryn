import { isApiError, isRunActive, useCanControlRuns, useI18n, useSetRunArchive, type Run } from "@lerryn/core";
import { Button, Checkbox, ConsoleDialog, IconButton, Label } from "@lerryn/ui-web";
import { Archive, ArchiveRestore } from "lucide-react";
import { useRef, useState } from "react";

import { useCurrentUser } from "../../shell/AppShell";

function skipsArchiveConfirmation(key: string): boolean {
  try {
    return localStorage.getItem(key) === "true";
  } catch {
    return false;
  }
}

function rememberArchivePreference(key: string) {
  try {
    localStorage.setItem(key, "true");
  } catch {
    // The archive has already succeeded even if browser storage is unavailable.
  }
}

export function ArchiveRunButton({ run }: { run: Run }) {
  const { t } = useI18n();
  const { id: userId } = useCurrentUser();
  const preferenceKey = `lerryn.runs.skipArchiveConfirmation.${userId}`;
  const canControl = useCanControlRuns(run.project_id);
  const update = useSetRunArchive();
  const [confirmation, setConfirmation] = useState<{ archived: boolean; offerSkip?: boolean } | null>(null);
  const [skipConfirmation, setSkipConfirmation] = useState(false);
  const submitting = useRef(false);
  if (!canControl || isRunActive(run.status)) return null;
  const archived = !!run.archived_at;
  const label = t(archived ? "run.unarchive" : "run.archive");
  const title = confirmation ? t(confirmation.archived ? "run.archive" : "run.unarchive") : label;
  const close = () => { if (!submitting.current) setConfirmation(null); };
  const submit = async (nextArchived: boolean, remember = false) => {
    if (submitting.current) return;
    submitting.current = true;
    try {
      await update.mutateAsync({ runId: run.id, taskId: run.task_id, archived: nextArchived });
      if (remember) rememberArchivePreference(preferenceKey);
      setConfirmation(null);
    } catch {
      // Show the error and a retry even if the initial confirmation was skipped.
      setConfirmation((current) => current ?? { archived: nextArchived });
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
      onPress={() => {
        if (submitting.current) return;
        update.reset();
        setSkipConfirmation(false);
        if (!archived && skipsArchiveConfirmation(preferenceKey)) {
          void submit(true);
        } else {
          setConfirmation({ archived: !archived, offerSkip: !archived });
        }
      }} />
    <ConsoleDialog isOpen={confirmation !== null} onOpenChange={(value) => { if (!value) close(); }} title={title} isDismissable={!update.isPending}
      footer={<>
        <Button variant="ghost" onPress={close} isDisabled={update.isPending}>{t("common.cancel")}</Button>
        <Button onPress={() => {
          if (confirmation) void submit(confirmation.archived, confirmation.offerSkip && skipConfirmation);
        }} isPending={update.isPending}>{title}</Button>
      </>}>
      <p>{t(confirmation?.archived ? "run.archiveBody" : "run.unarchiveBody")}</p>
      {confirmation?.offerSkip ? (
        <Checkbox isSelected={skipConfirmation} onChange={setSkipConfirmation} isDisabled={update.isPending}>
          <Checkbox.Content>
            <Checkbox.Control><Checkbox.Indicator /></Checkbox.Control>
            <Label>{t("run.skipArchiveConfirmation")}</Label>
          </Checkbox.Content>
        </Checkbox>
      ) : null}
      {update.isError ? <p role="alert" className="danger">{message}</p> : null}
    </ConsoleDialog>
  </>;
}
