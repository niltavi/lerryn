import {
  isApiError,
  useCancelCredentialRequest,
  useCredentialRequest,
  useFillCredentialRequest,
  useI18n,
  type CredentialRequest,
} from "@lerryn/core";
import { Button, ConsoleDialog, Description, FieldError, Input, Label, TextArea, TextField, WaveGlyph } from "@lerryn/ui-web";
import { Clock, Eye, EyeOff, KeyRound, Lock, RefreshCw } from "lucide-react";
import { createContext, useCallback, useContext, useState, type FormEvent, type ReactNode } from "react";

import { useNow } from "../lib/hooks";
import { shortWhen } from "../lib/present";
import { FormError } from "../pages/auth/AuthFrame";
import { writeErrorMessage } from "../lib/origin";

type Field = CredentialRequest["fields"][number];

interface OpenFill {
  requestId: string;
  /** Cancel the request when the dialog closes unsaved: an update started from the console. */
  cancelOnClose: boolean;
}

type OpenFn = (requestId: string, opts?: { cancelOnClose?: boolean }) => void;

const OpenFillContext = createContext<OpenFn>(() => undefined);

/** Opens the fill dialog for a credential request, from any console page. */
export function useOpenCredentialFill(): OpenFn {
  return useContext(OpenFillContext);
}

/**
 * Hosts the console's fill dialog (tech design 12.4). It sits above the
 * pages, so a row inside a popover can open it without the popover's
 * closing taking it along. Links an agent hands out still go to the
 * server-rendered page /c/{id}.
 */
export function CredentialFillProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState<OpenFill | null>(null);
  const openFill = useCallback<OpenFn>((requestId, opts) => setOpen({ requestId, cancelOnClose: !!opts?.cancelOnClose }), []);
  return (
    <OpenFillContext.Provider value={openFill}>
      {children}
      {/* Keyed by request, so typed values never outlive the dialog they were typed in. */}
      <CredentialFillDialog key={open?.requestId ?? ""} open={open} onClose={() => setOpen(null)} />
    </OpenFillContext.Provider>
  );
}

const VALUE_POINTER = "#/values/";

function CredentialFillDialog({ open, onClose }: { open: OpenFill | null; onClose: () => void }) {
  const { t, locale } = useI18n();
  const now = useNow(60_000);
  const request = useCredentialRequest(open?.requestId ?? null);
  const fill = useFillCredentialRequest();
  const cancel = useCancelCredentialRequest();
  const [values, setValues] = useState<Record<string, string>>({});
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const req = request.data;
  const pending = req?.status === "pending";

  const finish = () => {
    fill.reset();
    setValues({});
    onClose();
  };
  const dismiss = () => {
    // An update started here and left unsaved would otherwise sit in the
    // pending list until it expires.
    if (open?.cancelOnClose && pending) cancel.mutate(open.requestId);
    finish();
  };

  const submit = (e?: FormEvent) => {
    e?.preventDefault();
    if (!req || !pending) return;
    const missing: Record<string, string> = {};
    for (const f of req.fields) {
      if (!f.optional && !(values[f.name] ?? "").trim()) missing[f.name] = t("auth.errors.required");
    }
    setErrors(missing);
    setFormError(null);
    if (Object.keys(missing).length > 0) return;
    fill.mutate(
      { requestId: req.id, values: { ...values } },
      {
        onSuccess: finish,
        onError: (err) => {
          if (isApiError(err) && err.code === "request_closed") {
            // Filled elsewhere, expired or cancelled meanwhile: show which.
            void request.refetch();
            return;
          }
          if (isApiError(err) && err.code === "validation_failed") {
            const byField: Record<string, string> = {};
            let whole: string | null = null;
            for (const fe of err.problem?.errors ?? []) {
              if (fe.pointer?.startsWith(VALUE_POINTER)) byField[fe.pointer.slice(VALUE_POINTER.length)] = fe.detail;
              else whole = fe.detail;
            }
            setErrors(byField);
            setFormError(whole);
            return;
          }
          setFormError(t(writeErrorMessage(err)));
        },
      },
    );
  };

  const title = !req ? (
    t("credentials.fill")
  ) : req.task ? (
    t("credentials.fillTitleTask", { task: req.task.name })
  ) : (
    <span>
      {t("credentials.fillTitleUpdate")} <span className="mono" style={{ fontSize: 18 }}>{req.name}</span>
    </span>
  );

  return (
    <ConsoleDialog
      isOpen={!!open}
      onOpenChange={(isOpen) => (isOpen ? undefined : dismiss())}
      title={
        <>
          <span className="g g--key g--lg" aria-hidden="true">
            <KeyRound className="wl-icon" />
          </span>
          {title}
        </>
      }
      footer={
        pending ? (
          <>
            <Button variant="ghost" onPress={dismiss}>
              {t("common.cancel")}
            </Button>
            <Button onPress={() => submit()} isPending={fill.isPending}>
              {t("common.save")}
            </Button>
          </>
        ) : (
          <Button variant="ghost" onPress={dismiss}>
            {t("common.close")}
          </Button>
        )
      }
    >
      {request.isError ? (
        <p className="body-sm muted">{t("common.notFound")}</p>
      ) : !req ? (
        <p className="body-sm muted">{t("common.loading")}</p>
      ) : !pending ? (
        <p className="body-sm muted">
          {req.status === "fulfilled" ? t("credentials.fillDone") : req.status === "expired" ? t("credentials.fillExpired") : t("credentials.fillCancelled")}
        </p>
      ) : (
        <>
          {req.purpose ? <p className="body-sm muted">{req.purpose}</p> : null}
          <div className="meta-chips">
            {req.client ? (
              <span className="meta-chip">
                <WaveGlyph size={16} />
                {req.client}
              </span>
            ) : null}
            {req.task ? (
              <span className="meta-chip">
                <Lock className="wl-icon" width={16} aria-hidden="true" />
                {t("credentials.taskOnly")}
              </span>
            ) : (
              <span className="meta-chip">
                <RefreshCw className="wl-icon" width={16} aria-hidden="true" />
                {t("credentials.replaces")}
              </span>
            )}
            <span className="meta-chip">
              <Clock className="wl-icon" width={16} aria-hidden="true" />
              {t("overview.dueBy", { time: shortWhen(req.expires_at, locale, now) })}
            </span>
          </div>
          <form className="stack" style={{ gap: "var(--space-5)" }} onSubmit={submit} autoComplete="off" noValidate>
            {req.fields.map((f) => (
              <FillField
                key={f.name}
                field={f}
                value={values[f.name] ?? ""}
                onChange={(v) => setValues((cur) => ({ ...cur, [f.name]: v }))}
                error={errors[f.name]}
              />
            ))}
            {formError ? <FormError>{formError}</FormError> : null}
            <button type="submit" hidden />
          </form>
        </>
      )}
    </ConsoleDialog>
  );
}

/** One field of a request: secret values are masked until the eye is pressed. */
function FillField({ field: f, value, onChange, error }: { field: Field; value: string; onChange: (v: string) => void; error?: string }) {
  const { t } = useI18n();
  const [shown, setShown] = useState(false);
  const secret = f.secret !== false;
  const masked = secret && !shown;
  const inputProps = { autoCapitalize: "off", autoCorrect: "off", spellCheck: false } as const;
  return (
    <TextField
      name={f.name}
      value={value}
      onChange={onChange}
      isInvalid={!!error}
      isRequired={!f.optional}
      type={f.multiline || !masked ? "text" : "password"}
      autoComplete="off"
      fullWidth
    >
      <Label>
        <span className="mono">{f.name}</span>
        {f.label ? ` · ${f.label}` : null}
        {f.optional ? <span className="muted"> · {t("task.optional")}</span> : null}
      </Label>
      <div className="pw-wrap">
        {f.multiline ? <TextArea className={masked ? "textarea--masked" : undefined} rows={4} {...inputProps} /> : <Input {...inputProps} />}
        {secret ? (
          <button
            className="pw-eye"
            type="button"
            aria-label={shown ? t("credentials.hideValue") : t("credentials.showValue")}
            aria-pressed={shown}
            onClick={() => setShown((s) => !s)}
          >
            {shown ? <EyeOff className="wl-icon" width={18} aria-hidden="true" /> : <Eye className="wl-icon" width={18} aria-hidden="true" />}
          </button>
        ) : null}
      </div>
      {error ? <FieldError>{error}</FieldError> : f.description ? <Description>{f.description}</Description> : null}
    </TextField>
  );
}
