import {
  credentialState,
  formatDateTime,
  useCredentials,
  useI18n,
  usePendingCredentialRequests,
  useRequestCredentialUpdate,
  useRevokeCredential,
  useRevokeGrant,
  type Credential,
  type CredentialRequest,
} from "@lerryn/core";
import { Button, ConsoleDialog, Disclosure, IconButton, StateCard, ToneGlyph } from "@lerryn/ui-web";
import { Ban, Check, ChevronRight, Clock, KeyRound, Pencil, Unlink } from "lucide-react";
import { useState } from "react";

import { useNow } from "../../lib/hooks";
import { shortWhen } from "../../lib/present";
import { useOpenCredentialFill } from "../../shell/CredentialFill";
import { Board, BoardTop, LoadError, LoadMore, usePageTitle } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

/**
 * The user's own credentials (prototype Settings-Credentials.dc.html):
 * pending fill-in requests first, then each credential with the tasks it
 * is granted to. Values are typed in the fill dialog and never shown
 * (tech design 12.4).
 */
export function CredentialsPage() {
  const { t } = useI18n();
  usePageTitle(t("credentials.title"));
  const credentials = useCredentials();
  const requests = usePendingCredentialRequests();
  const [revoking, setRevoking] = useState<Credential | null>(null);
  const now = useNow(60_000);
  const items = credentials.data?.items ?? [];
  const pending = requests.data?.items ?? [];

  return (
    <Board label={t("credentials.title")}>
      <BoardTop title={t("credentials.title")} />
      {pending.length > 0 || requests.isError || requests.hasNextPage ? (
        <section className="stack" aria-label={t("credentials.pending")}>
          {pending.length > 0 ? (
            <div className="att" role="list" aria-label={t("credentials.pending")}>
              {pending.map((r) => (
                <RequestRow key={r.id} request={r} now={now} />
              ))}
            </div>
          ) : null}
          {requests.isError ? (
            <LoadError error={requests.error} onRetry={() => void (
              requests.isFetchNextPageError
                ? requests.fetchNextPage({ cancelRefetch: false })
                : requests.refetch({ cancelRefetch: false })
            )} />
          ) : null}
          <LoadMore
            show={!!requests.hasNextPage && !requests.isError}
            pending={requests.isFetching}
            onPress={() => void requests.fetchNextPage({ cancelRefetch: false })}
          />
        </section>
      ) : null}
      {credentials.isError ? (
        <LoadError error={credentials.error} onRetry={() => void (
          credentials.isFetchNextPageError
            ? credentials.fetchNextPage({ cancelRefetch: false })
            : credentials.refetch({ cancelRefetch: false })
        )} />
      ) : credentials.data && requests.isSuccess && items.length === 0 && pending.length === 0 && !credentials.hasNextPage && !requests.hasNextPage ? (
        <StateCard icon={KeyRound} title={t("credentials.empty")} />
      ) : null}
      {items.length > 0 ? (
        <div className="rows" role="list" aria-label={t("credentials.title")}>
          {items.map((c) => (
            <CredentialRow key={c.id} credential={c} now={now} onRevoke={() => setRevoking(c)} />
          ))}
        </div>
      ) : null}
      <LoadMore
        show={!!credentials.hasNextPage && !credentials.isError}
        pending={credentials.isFetching}
        onPress={() => void credentials.fetchNextPage({ cancelRefetch: false })}
      />
      <RevokeDialog credential={revoking} onClose={() => setRevoking(null)} />
    </Board>
  );
}

function RequestRow({ request: r, now }: { request: CredentialRequest; now: number }) {
  const { t, locale } = useI18n();
  const openFill = useOpenCredentialFill();
  return (
    <button type="button" className="att-item" role="listitem" onClick={() => openFill(r.id)}>
      <span className="g g--key g--lg" aria-hidden="true">
        <KeyRound className="wl-icon" />
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="att-name mono">{r.name}</span>
        <span className="att-why">{[r.task?.name, r.purpose].filter(Boolean).join(" · ")}</span>
      </span>
      <span className="att-t">
        {t("overview.dueBy", { time: shortWhen(r.expires_at, locale, now) })}
        <ChevronRight className="wl-icon" aria-hidden="true" />
      </span>
    </button>
  );
}

function CredentialRow({ credential: c, now, onRevoke }: { credential: Credential; now: number; onRevoke: () => void }) {
  const { t, locale } = useI18n();
  const { state, days } = credentialState(c, now);
  const update = useRequestCredentialUpdate();
  const revokeGrant = useRevokeGrant();
  const openFill = useOpenCredentialFill();
  const usable = state === "active" || state === "expiring";
  const sub =
    state === "revoked"
      ? `${t("credentials.revoked")} · ${formatDateTime(c.revoked_at ?? c.updated_at, locale, now)}`
      : state === "expired"
        ? t("credentials.expired")
        : `${c.purpose} · ${formatDateTime(c.updated_at, locale, now)}`;

  return (
    <div className={usable ? "rowi" : "rowi rowi--quiet"} role="listitem">
      <span className="ic-box" aria-hidden="true" style={usable ? undefined : { color: "var(--ink-muted)" }}>
        {state === "revoked" ? <Ban className="wl-icon" width={18} /> : <KeyRound className="wl-icon" width={18} />}
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name mono" style={{ fontSize: 14 }}>
          {c.name}
        </span>
        <span className="rowi-sub">{sub}</span>
      </span>
      <span className="rowi-end">
        {usable && c.grants.length > 0 ? (
          <Disclosure
            alignRight
            triggerClassName="chip chip--quiet"
            detailsLabel={t("credentials.grants")}
            trigger={t("credentials.taskCount", { count: c.grants.length })}
            details={
              <div className="stack" style={{ gap: 0 }}>
                {c.grants.map((g) => (
                  <div className="pop-row" key={g.task.id + g.name}>
                    <span className="stack" style={{ gap: 0, minWidth: 0 }}>
                      <span className="mono-sm" style={{ color: "var(--ink)" }}>
                        {g.task.name}
                      </span>
                      {g.name !== c.name ? <span className="caption muted mono-sm">{g.name}</span> : null}
                    </span>
                    <IconButton
                      label={t("credentials.ungrantNamed", { task: g.task.name })}
                      tip={t("credentials.ungrant")}
                      icon={Unlink}
                      isPending={revokeGrant.isPending && revokeGrant.variables?.taskId === g.task.id}
                      onPress={() => revokeGrant.mutate({ credentialId: c.id, taskId: g.task.id })}
                    />
                  </div>
                ))}
              </div>
            }
          />
        ) : null}
        {state === "expiring" && days !== null ? (
          <span className="row" style={{ gap: 6, color: "var(--warning-ink)", fontSize: 13 }} data-tip={t("credentials.expiresIn", { count: days })} data-tip-pos="top">
            {t("task.daysLeft", { count: days })}
            <ToneGlyph tone="warn" icon={Clock} label={t("task.credentialExpiring")} />
          </span>
        ) : state === "active" ? (
          <ToneGlyph tone="ok" icon={Check} label={t("task.credentialOk")} />
        ) : state === "expired" ? (
          <ToneGlyph tone="bad" icon={Clock} label={t("credentials.expired")} />
        ) : null}
        {state !== "revoked" ? (
          <span className="acts">
            <IconButton
              label={t("credentials.updateNamed", { name: c.name })}
              tip={t("credentials.update")}
              icon={Pencil}
              isPending={update.isPending}
              onPress={() =>
                update.mutate(c, {
                  // Closing the dialog unsaved withdraws the request it was opened on.
                  onSuccess: (req) => openFill(req.id, { cancelOnClose: true }),
                })
              }
            />
            {state !== "expired" ? (
              <IconButton variant="danger-soft" label={t("credentials.revokeNamed", { name: c.name })} tip={t("credentials.revoke")} icon={Ban} onPress={onRevoke} />
            ) : null}
          </span>
        ) : null}
        {update.isError || revokeGrant.isError ? <span className="caption" style={{ color: "var(--danger)" }}>{t(writeErrorMessage(update.error ?? revokeGrant.error))}</span> : null}
      </span>
    </div>
  );
}

function RevokeDialog({ credential, onClose }: { credential: Credential | null; onClose: () => void }) {
  const { t } = useI18n();
  const revoke = useRevokeCredential();
  const close = () => {
    revoke.reset();
    onClose();
  };
  const grants = credential?.grants ?? [];
  return (
    <ConsoleDialog
      isOpen={!!credential}
      onOpenChange={(open) => (open ? undefined : close())}
      title={
        <>
          <span className="g g--bad g--lg" aria-hidden="true">
            <Ban className="wl-icon" />
          </span>
          <span>
            {t("credentials.revoke")} <span className="mono" style={{ fontSize: 18 }}>{credential?.name}</span>
          </span>
        </>
      }
      footer={
        <>
          <Button variant="ghost" onPress={close}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" isPending={revoke.isPending} onPress={() => credential && revoke.mutate(credential.id, { onSuccess: close })}>
            {t("credentials.revoke")}
          </Button>
        </>
      }
    >
      {grants.length > 0 ? (
        <div className="meta-chips">
          {grants.map((g) => (
            <span className="meta-chip" key={g.task.id}>
              <span className="mono-sm">{g.task.name}</span>
            </span>
          ))}
        </div>
      ) : null}
      <p className="body-sm muted">{grants.length > 0 ? t("credentials.revokeBody", { count: grants.length }) : t("credentials.revokeBodyNone")}</p>
      {revoke.error ? <FormError>{t(writeErrorMessage(revoke.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
