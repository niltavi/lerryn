import { isApiError, useCanControlRuns, useI18n, useSetTaskTags, type Task } from "@lerryn/core";
import { Button, ConsoleDialog, IconButton } from "@lerryn/ui-web";
import { Tags } from "lucide-react";
import { useId, useState } from "react";

export function TaskTags({ task }: { task: Task }) {
  const { t } = useI18n();
  const canEdit = useCanControlRuns(task.project_id) && !task.archived_at;
  const update = useSetTaskTags(task.id);
  const [draft, setDraft] = useState<string | null>(null);
  const fieldId = useId();
  const tags = task.tags ?? [];
  if (!canEdit && tags.length === 0) return null;

  const close = () => { if (!update.isPending) setDraft(null); };
  const save = async () => {
    if (draft === null || update.isPending) return;
    try {
      await update.mutateAsync(draft.split(/\r?\n/).map((line) => line.trim()).filter(Boolean));
      setDraft(null);
    } catch {
      // Leave the draft in the dialog so it can be corrected.
    }
  };
  const code = isApiError(update.error) ? update.error.code : null;
  const message = code === "validation_failed" ? t("task.tagsInvalid")
    : code === "permission_denied" || code === "insufficient_scope" ? t("runAction.denied")
    : t("task.tagsSaveFailed");

  return (
    <div className="chips" role="group" aria-label={t("task.tags")}>
      {tags.map((tag) => <span className="tag max-w-full" key={tag} title={tag}><span className="truncate">{tag}</span></span>)}
      {canEdit ? (
        <IconButton label={t("task.editTags")} icon={Tags} onPress={() => { update.reset(); setDraft(tags.join("\n")); }} />
      ) : null}
      <ConsoleDialog
        isOpen={draft !== null && canEdit}
        onOpenChange={(open) => { if (!open) close(); }}
        title={t("task.editTags")}
        isDismissable={!update.isPending}
        footer={<>
          <Button variant="ghost" onPress={close} isDisabled={update.isPending}>{t("common.cancel")}</Button>
          <Button isPending={update.isPending} onPress={() => void save()}>{t("common.save")}</Button>
        </>}
      >
        <label className="stack" htmlFor={fieldId}>
          <span>{t("task.tags")}</span>
          <textarea
            id={fieldId}
            className="textarea w-full resize-y"
            rows={5}
            value={draft ?? ""}
            disabled={update.isPending}
            aria-describedby={`${fieldId}-help`}
            onChange={(event) => setDraft(event.target.value)}
          />
        </label>
        <p className="caption muted" id={`${fieldId}-help`}>{t("task.tagsInputHelp")}</p>
        {update.isError ? <p className="danger" role="alert">{message}</p> : null}
      </ConsoleDialog>
    </div>
  );
}
