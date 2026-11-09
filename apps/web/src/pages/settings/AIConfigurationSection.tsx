import {
  isApiError,
  useAIConfiguration,
  useI18n,
  useRemoveAIConfiguration,
  useSetAIConfiguration,
  type AIConfiguration,
} from "@lerryn/core";
import { Button, ConsoleDialog, Description, FieldError, Input, Label, TextArea, TextField, WaveGlyph } from "@lerryn/ui-web";
import { useState, type FormEvent } from "react";

import { writeErrorMessage } from "../../lib/origin";
import { LoadError } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";

export function AIConfigurationSection() {
  const { t } = useI18n();
  const config = useAIConfiguration();
  const [dialog, setDialog] = useState<"edit" | "remove" | null>(null);
  const close = () => setDialog(null);
  const current = config.data;

  return (
    <section className="stack" aria-labelledby="ai-settings-title">
      <h2 className="sec-label" id="ai-settings-title">{t("ai.title")}</h2>
      {config.isError ? <LoadError error={config.error} onRetry={() => void config.refetch()} /> : null}
      {current ? (
        <div className="rows">
          <div className="rowi">
            <span className="ic-box" aria-hidden="true"><WaveGlyph size={18} /></span>
            <span style={{ minWidth: 0 }}>
              <span className="rowi-name">{current.base_url ?? t("ai.notConfigured")}</span>
              <span className="rowi-sub">
                {current.base_url ? t("ai.modelCount", { count: current.models.length }) : t("ai.unconfigured")}
              </span>
            </span>
            <span className="rowi-end">
              <Button size="sm" variant="secondary" onPress={() => setDialog("edit")}>
                {t(current.base_url ? "ai.edit" : "ai.configure")}
              </Button>
            </span>
          </div>
        </div>
      ) : null}
      {dialog === "edit" && current ? <EditAIConfiguration config={current} onClose={close} onRemove={() => setDialog("remove")} /> : null}
      {dialog === "remove" ? <RemoveAIConfiguration onClose={close} /> : null}
    </section>
  );
}

function EditAIConfiguration({ config, onClose, onRemove }: { config: AIConfiguration; onClose: () => void; onRemove: () => void }) {
  const { t } = useI18n();
  const save = useSetAIConfiguration();
  const [url, setURL] = useState(config.base_url ?? "");
  const [key, setKey] = useState("");
  const [models, setModels] = useState(config.models.join("\n"));
  const baseURL = url.trim().replace(/\/+$/, "");
  const modelIDs = models.split("\n").map((id) => id.trim()).filter(Boolean);
  const requiresKey = baseURL !== config.base_url;
  const problem = isApiError(save.error) && save.error.code === "validation_failed" ? save.error.problem : null;
  const hasError = (field: string) => problem?.errors?.some(({ pointer }) => pointer === "#/" + field || pointer?.startsWith("#/" + field + "/"));
  const urlError = hasError("base_url");
  const keyError = hasError("api_key");
  const modelsError = hasError("models");
  const canSubmit = !!baseURL && modelIDs.length > 0 && (!requiresKey || !!key.trim()) && !save.isPending;

  const submit = (event?: FormEvent<HTMLFormElement>) => {
    event?.preventDefault();
    if (!canSubmit) return;
    save.mutate({ base_url: baseURL, models: modelIDs, ...(key.trim() ? { api_key: key.trim() } : {}) }, { onSuccess: onClose });
  };

  return (
    <ConsoleDialog
      isOpen
      onOpenChange={(open) => { if (!open && !save.isPending) onClose(); }}
      isDismissable={!save.isPending}
      title={t("ai.title")}
      footer={
        <>
          {config.base_url ? <Button variant="danger-soft" isDisabled={save.isPending} onPress={onRemove}>{t("ai.remove")}</Button> : null}
          <Button variant="ghost" isDisabled={save.isPending} onPress={onClose}>{t("common.cancel")}</Button>
          <Button onPress={() => submit()} isDisabled={!canSubmit && !save.isPending} isPending={save.isPending}>{t("common.save")}</Button>
        </>
      }
    >
      <form className="stack" style={{ gap: "var(--space-5)" }} onSubmit={submit}>
        <TextField name="base_url" value={url} onChange={setURL} type="url" autoComplete="off" isRequired isInvalid={urlError} fullWidth>
          <Label>{t("ai.baseUrl")}</Label>
          <Input placeholder="https://api.openai.com/v1" maxLength={2048} />
          {urlError ? <FieldError>{t("ai.invalidUrl")}</FieldError> : <Description>{t("ai.baseUrlHelp")}</Description>}
        </TextField>
        <TextField name="api_key" value={key} onChange={setKey} type="password" autoComplete="new-password" isRequired={requiresKey} isInvalid={keyError} fullWidth>
          <Label>{t("ai.apiKey")}</Label>
          <Input maxLength={4096} />
          {keyError ? <FieldError>{t("ai.invalidKey")}</FieldError> : config.base_url ? <Description>{t("ai.keyUnchanged")}</Description> : null}
        </TextField>
        <TextField name="models" value={models} onChange={setModels} isRequired isInvalid={modelsError} fullWidth>
          <Label>{t("ai.models")}</Label>
          <TextArea rows={4} spellCheck={false} />
          {modelsError ? <FieldError>{t("ai.invalidModels")}</FieldError> : <Description>{t("ai.modelsHelp")}</Description>}
        </TextField>
        {save.error && !(urlError || keyError || modelsError) ? (
          <FormError>{t(problem ? "ai.invalidConfig" : writeErrorMessage(save.error))}</FormError>
        ) : null}
        <button type="submit" hidden disabled={!canSubmit} />
      </form>
    </ConsoleDialog>
  );
}

function RemoveAIConfiguration({ onClose }: { onClose: () => void }) {
  const { t } = useI18n();
  const remove = useRemoveAIConfiguration();
  return (
    <ConsoleDialog
      isOpen
      onOpenChange={(open) => { if (!open && !remove.isPending) onClose(); }}
      isDismissable={!remove.isPending}
      title={t("ai.removeTitle")}
      footer={
        <>
          <Button variant="ghost" isDisabled={remove.isPending} onPress={onClose}>{t("common.cancel")}</Button>
          <Button variant="danger" isPending={remove.isPending} onPress={() => remove.mutate(undefined, { onSuccess: onClose })}>{t("ai.remove")}</Button>
        </>
      }
    >
      <p className="body-sm muted">{t("ai.removeBody")}</p>
      {remove.error ? <FormError>{t(writeErrorMessage(remove.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
