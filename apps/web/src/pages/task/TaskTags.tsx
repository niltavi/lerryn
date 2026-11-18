import { isApiError, useCanControlRuns, useI18n, useSetTaskTags, type Task } from "@lerryn/core";
import { Button, Chip, ConsoleDialog, Description, IconButton, Label, TextArea, TextField } from "@lerryn/ui-web";
import { Tags } from "@gravity-ui/icons";
import { useState } from "react";

import { FormError } from "../auth/AuthFrame";

export function TaskTags({ tags }: { tags: Task["tags"] }) {
  const { t } = useI18n();
  if (!tags?.length) return null;

  return (
    <div className="flex min-w-0 flex-wrap gap-1.5" role="group" aria-label={t("task.tags")}>
      {tags.map((tag) => (
        <Chip key={tag} size="sm" variant="soft" className="max-w-full" title={tag}>
          <Chip.Label className="truncate">{tag}</Chip.Label>
        </Chip>
      ))}
    </div>
  );
}

export function EditTaskTagsButton({ task }: { task: Task }) {
  const { t } = useI18n();
  const canEdit = useCanControlRuns(task.project_id) && !task.archived_at;
  const update = useSetTaskTags(task.id);
  const [draft, setDraft] = useState<string | null>(null);
  const tags = task.tags ?? [];
  if (!canEdit) return null;

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
    <>
      <IconButton label={t("task.editTags")} icon={Tags} onPress={() => { update.reset(); setDraft(tags.join("\n")); }} />
      <ConsoleDialog
        isOpen={draft !== null}
        onOpenChange={(open) => { if (!open) close(); }}
        title={t("task.editTags")}
        isDismissable={!update.isPending}
        footer={<>
          <Button variant="ghost" onPress={close} isDisabled={update.isPending}>{t("common.cancel")}</Button>
          <Button isPending={update.isPending} onPress={() => void save()}>{t("common.save")}</Button>
        </>}
      >
        {/* One tag per line: the API takes the list whole, so the draft is plain text. */}
        <TextField fullWidth value={draft ?? ""} onChange={setDraft} isDisabled={update.isPending}>
          <Label>{t("task.tags")}</Label>
          <TextArea rows={5} className="resize-y" />
          <Description>{t("task.tagsInputHelp")}</Description>
        </TextField>
        {update.isError ? <FormError>{message}</FormError> : null}
      </ConsoleDialog>
    </>
  );
}
