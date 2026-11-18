import {
  credentialState,
  formatDateTime,
  useCredentials,
  useDeleteCredential,
  useI18n,
  usePendingCredentialRequests,
  useRequestCredentialUpdate,
  useRevokeCredential,
  useRevokeGrant,
  type Credential,
  type CredentialRequest,
} from "@lerryn/core";
import { Button, Card, Chip, ConsoleDialog, IconButton, Popover, StateCard, Table } from "@lerryn/ui-web";
import { Ban, ChevronRight, Clock, Key, LinkSlash, Pencil, TrashBin } from "@gravity-ui/icons";
import { useState } from "react";

import { useNow } from "../../lib/hooks";
import { shortWhen } from "../../lib/present";
import { useOpenCredentialFill } from "../../shell/CredentialFill";
import { Board, BoardTop, LoadError, LoadMore, Section, usePageTitle } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";
import { AIConfigurationSection } from "./AIConfigurationSection";

type CredentialAction = "revoke" | "delete";

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
  const [confirmation, setConfirmation] = useState<{ credential: Credential; action: CredentialAction } | null>(null);
  const now = useNow(60_000);
  const items = credentials.data?.items ?? [];
  const pending = requests.data?.items ?? [];

  return (
    <Board label={t("credentials.title")}>
      <BoardTop title={t("credentials.title")} />
      <AIConfigurationSection />
      {pending.length > 0 || requests.isError || requests.hasNextPage ? (
        <Section
          title={t("credentials.pending")}
          actions={
            pending.length > 0 ? (
              <Chip size="sm" variant="soft" color="warning">
                {t("credentials.pendingCount", { count: requests.hasNextPage ? `${pending.length}+` : pending.length })}
              </Chip>
            ) : null
          }
        >
          {pending.length > 0 ? (
            <Card className="tpl-card nt-fade-up">
              <ul className="lr-rows" aria-label={t("credentials.pending")}>
                {pending.map((r) => (
                  <RequestRow key={r.id} request={r} now={now} />
                ))}
              </ul>
            </Card>
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
        </Section>
      ) : null}
      {credentials.isError ? (
        <LoadError error={credentials.error} onRetry={() => void (
          credentials.isFetchNextPageError
            ? credentials.fetchNextPage({ cancelRefetch: false })
            : credentials.refetch({ cancelRefetch: false })
        )} />
      ) : credentials.data && requests.isSuccess && items.length === 0 && pending.length === 0 && !credentials.hasNextPage && !requests.hasNextPage ? (
        <StateCard icon={Key} title={t("credentials.empty")} />
      ) : null}
      {items.length > 0 ? (
        <Card className="tpl-card nt-fade-up">
          <Table>
            <Table.ScrollContainer>
              <Table.Content aria-label={t("credentials.title")} className="min-w-140">
                <Table.Header>
                  <Table.Column isRowHeader>{t("credentials.title")}</Table.Column>
                  <Table.Column>{t("credentials.grants")}</Table.Column>
                  <Table.Column>{t("credentials.status")}</Table.Column>
                  <Table.Column className="text-end">
                    <span className="sr-only">{t("credentials.actions")}</span>
                  </Table.Column>
                </Table.Header>
                <Table.Body>
                  {items.map((c) => (
                    <CredentialRow key={c.id} credential={c} now={now} onAction={(action) => setConfirmation({ credential: c, action })} />
                  ))}
                </Table.Body>
              </Table.Content>
            </Table.ScrollContainer>
          </Table>
        </Card>
      ) : null}
      <LoadMore
        show={!!credentials.hasNextPage && !credentials.isError}
        pending={credentials.isFetching}
        onPress={() => void credentials.fetchNextPage({ cancelRefetch: false })}
      />
      {confirmation ? <CredentialActionDialog {...confirmation} onClose={() => setConfirmation(null)} /> : null}
    </Board>
  );
}

function RequestRow({ request: r, now }: { request: CredentialRequest; now: number }) {
  const { t, locale } = useI18n();
  const openFill = useOpenCredentialFill();
  return (
    <li className="lr-row">
      <button type="button" className="lr-keys__request" onClick={() => openFill(r.id)}>
        <span className="lr-row__icon" data-tone="warn" aria-hidden="true">
          <Key width={16} height={16} />
        </span>
        <span className="lr-row__text">
          <span className="lr-row__title font-mono text-sm">{r.name}</span>
          <span className="lr-row__sub">{[r.task?.name, r.purpose].filter(Boolean).join(" · ")}</span>
        </span>
        <span className="lr-row__end text-xs text-muted">
          {t("overview.dueBy", { time: shortWhen(r.expires_at, locale, now) })}
          <ChevronRight aria-hidden="true" width={14} height={14} />
        </span>
      </button>
    </li>
  );
}

function CredentialRow({ credential: c, now, onAction }: { credential: Credential; now: number; onAction: (action: CredentialAction) => void }) {
  const { t, locale } = useI18n();
  const { state, days } = credentialState(c, now);
  const update = useRequestCredentialUpdate();
  const revokeGrant = useRevokeGrant();
  const openFill = useOpenCredentialFill();
  const usable = state === "active" || state === "expiring";
  const sub =
    state === "revoked"
      ? formatDateTime(c.revoked_at ?? c.updated_at, locale, now)
      : `${c.purpose} · ${formatDateTime(c.updated_at, locale, now)}`;

  return (
    <Table.Row id={c.id}>
      <Table.Cell>
        <div className="flex min-w-0 items-center gap-3">
          <span className="lr-row__icon" data-tone={usable ? undefined : "idle"} aria-hidden="true">
            {state === "revoked" ? <Ban width={16} height={16} /> : <Key width={16} height={16} />}
          </span>
          <div className="flex min-w-0 flex-col">
            <span className={usable ? "truncate font-mono text-sm font-medium" : "truncate font-mono text-sm text-muted"}>{c.name}</span>
            <span className="truncate text-xs text-muted">{sub}</span>
            {update.isError || revokeGrant.isError ? (
              <span className="text-xs text-danger-soft-foreground">{t(writeErrorMessage(update.error ?? revokeGrant.error))}</span>
            ) : null}
          </div>
        </div>
      </Table.Cell>
      <Table.Cell>
        {usable && c.grants.length > 0 ? (
          <Popover>
            <Button size="sm" variant="tertiary">
              {t("credentials.taskCount", { count: c.grants.length })}
            </Button>
            <Popover.Content placement="bottom start" className="w-72">
              <Popover.Dialog aria-label={t("credentials.grants")}>
                <Popover.Heading className="text-xs font-medium text-muted">{t("credentials.grants")}</Popover.Heading>
                <ul className="lr-rows mt-2">
                  {c.grants.map((g) => (
                    <li className="lr-row pop-row" key={g.task.id + g.name}>
                      <span className="lr-row__text">
                        <span className="truncate font-mono text-xs">{g.task.name}</span>
                        {g.name !== c.name ? <span className="truncate font-mono text-xs text-muted">{g.name}</span> : null}
                      </span>
                      <IconButton
                        label={t("credentials.ungrantNamed", { task: g.task.name })}
                        tip={t("credentials.ungrant")}
                        icon={LinkSlash}
                        isPending={revokeGrant.isPending && revokeGrant.variables?.taskId === g.task.id}
                        onPress={() => revokeGrant.mutate({ credentialId: c.id, taskId: g.task.id })}
                      />
                    </li>
                  ))}
                </ul>
              </Popover.Dialog>
            </Popover.Content>
          </Popover>
        ) : null}
      </Table.Cell>
      <Table.Cell>
        {state === "expiring" && days !== null ? (
          <Chip size="sm" variant="soft" color="warning" title={t("credentials.expiresIn", { count: days })}>
            <Clock role="img" aria-label={t("task.credentialExpiring")} width={12} height={12} />
            <Chip.Label>{t("task.daysLeft", { count: days })}</Chip.Label>
          </Chip>
        ) : state === "active" ? (
          <Chip size="sm" variant="soft" color="success">
            {t("task.credentialOk")}
          </Chip>
        ) : state === "expired" ? (
          <Chip size="sm" variant="soft" color="danger">
            {t("credentials.expired")}
          </Chip>
        ) : state === "revoked" ? (
          <Chip size="sm" variant="soft" color="default">
            {t("credentials.revoked")}
          </Chip>
        ) : null}
      </Table.Cell>
      <Table.Cell className="text-end">
        {state === "revoked" ? (
          <IconButton
            variant="danger-soft"
            label={t("credentials.deleteNamed", { name: c.name })}
            tip={t("credentials.delete")}
            icon={TrashBin}
            onPress={() => onAction("delete")}
          />
        ) : (
          <span className="inline-flex items-center gap-1">
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
              <IconButton variant="danger-soft" label={t("credentials.revokeNamed", { name: c.name })} tip={t("credentials.revoke")} icon={Ban} onPress={() => onAction("revoke")} />
            ) : null}
          </span>
        )}
      </Table.Cell>
    </Table.Row>
  );
}

function CredentialActionDialog({ credential, action, onClose }: { credential: Credential; action: CredentialAction; onClose: () => void }) {
  const { t } = useI18n();
  const revoke = useRevokeCredential();
  const remove = useDeleteCredential();
  const deleting = action === "delete";
  const mutation = deleting ? remove : revoke;
  const Icon = deleting ? TrashBin : Ban;
  const label = t(deleting ? "credentials.delete" : "credentials.revoke");
  const grants = credential.grants;
  const description = deleting
    ? t("credentials.deleteBody")
    : grants.length > 0 ? t("credentials.revokeBody", { count: grants.length }) : t("credentials.revokeBodyNone");
  return (
    <ConsoleDialog
      isOpen
      isDismissable={!mutation.isPending}
      onOpenChange={(open) => { if (!open && !mutation.isPending) onClose(); }}
      title={
        <>
          <span className="lr-row__icon" data-tone="bad" aria-hidden="true">
            <Icon width={16} height={16} />
          </span>
          <span>
            {label} <span className="font-mono">{credential.name}</span>
          </span>
        </>
      }
      footer={
        <>
          <Button variant="ghost" isDisabled={mutation.isPending} onPress={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" isPending={mutation.isPending} onPress={() => mutation.mutate(credential.id, { onSuccess: onClose })}>
            {label}
          </Button>
        </>
      }
    >
      {grants.length > 0 ? (
        <div className="flex flex-wrap gap-2">
          {grants.map((g) => (
            <Chip key={g.task.id} size="sm" variant="soft" className="font-mono">
              {g.task.name}
            </Chip>
          ))}
        </div>
      ) : null}
      <p className="text-sm text-muted">{description}</p>
      {mutation.error ? <FormError>{t(writeErrorMessage(mutation.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
