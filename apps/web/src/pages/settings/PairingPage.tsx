import { formatAgo, useCreatePairingCode, useDevices, useI18n, useRevokeDevice, type Device } from "@lerryn/core";
import { Button, Card, Chip, ConsoleDialog, IconButton, QrCode, StateCard } from "@lerryn/ui-web";
import { ArrowsRotateRight, BookOpen, Display, QrCode as QrCodeIcon, Smartphone, TrashBin } from "@gravity-ui/icons";
import { useEffect, useState } from "react";

import { useNow } from "../../lib/hooks";
import { Board, BoardTop, LoadError, Section, usePageTitle } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

/** "4:32" from milliseconds left. */
export function countdown(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/**
 * Phone pairing: a one-time code as a QR code for the Lerryn app, valid for
 * five minutes, and the phones already signed in. Phone sessions are read-only.
 */
export function PairingPage() {
  const { t } = useI18n();
  usePageTitle(t("pairing.title"));
  const devices = useDevices();
  const pairing = useCreatePairingCode();
  const [removing, setRemoving] = useState<Device | null>(null);
  const code = pairing.data;
  const now = useNow(1000, !!code);
  const left = code ? Date.parse(code.expires_at) - now : 0;
  const expired = !!code && left <= 0;
  const { mutate } = pairing;

  // A code is made when the page opens; leaving the page forgets it.
  useEffect(() => {
    mutate();
  }, [mutate]);

  const items = devices.data?.items ?? [];

  return (
    <Board label={t("pairing.title")}>
      <BoardTop title={t("pairing.title")} />
      <Card className="tpl-card nt-fade-up">
        <div className="flex flex-wrap items-center gap-6">
          {code ? (
            <QrCode value={code.uri} label={t("pairing.qr")} dimmed={expired} />
          ) : (
            <div className="qr qr--off" aria-hidden="true" />
          )}
          <div className="flex min-w-48 flex-1 flex-col gap-3">
            <div className="flex items-center gap-2">
              <QrCodeIcon className="text-muted" aria-hidden="true" width={16} height={16} />
              <h2 className="text-sm font-semibold">{t("pairing.scan")}</h2>
              <Chip size="sm" variant="soft">
                <BookOpen aria-hidden="true" width={12} height={12} />
                <Chip.Label>{t("pairing.readOnly")}</Chip.Label>
              </Chip>
            </div>
            {pairing.isError ? (
              <FormError>{t(writeErrorMessage(pairing.error))}</FormError>
            ) : code ? (
              <div className="flex items-center gap-2">
                <span className={expired ? "text-3xl font-semibold tabular-nums text-muted" : "text-3xl font-semibold tabular-nums"} aria-live="off">
                  {expired ? t("pairing.expired") : countdown(left)}
                </span>
                <IconButton label={t("pairing.regenerate")} icon={ArrowsRotateRight} isPending={pairing.isPending} onPress={() => mutate()} />
              </div>
            ) : null}
            {code && !expired ? (
              <dl className="kv text-sm">
                <dt>{t("pairing.manualCode")}</dt>
                <dd className="font-mono tracking-widest">{code.code}</dd>
              </dl>
            ) : null}
          </div>
        </div>
      </Card>

      {devices.isError ? (
        <LoadError error={devices.error} onRetry={() => void devices.refetch()} />
      ) : devices.data && items.length === 0 ? (
        <StateCard icon={Smartphone} title={t("pairing.none")} />
      ) : items.length > 0 ? (
        <Section title={t("pairing.devices")}>
          <Card className="tpl-card nt-fade-up">
            <ul className="lr-rows" role="list" aria-label={t("pairing.devices")}>
              {items.map((d) => (
                <DeviceRow key={d.id} device={d} onRemove={() => setRemoving(d)} />
              ))}
            </ul>
          </Card>
        </Section>
      ) : null}
      <RemoveDialog device={removing} onClose={() => setRemoving(null)} />
    </Board>
  );
}

function DeviceRow({ device: d, onRemove }: { device: Device; onRemove: () => void }) {
  const { t, locale } = useI18n();
  const DeviceIcon = /ipad|tablet/i.test(d.name) ? Display : Smartphone;
  return (
    <li className="lr-row">
      <span className="lr-row__icon" data-tone="idle" aria-hidden="true">
        <DeviceIcon width={16} height={16} />
      </span>
      <span className="lr-row__text">
        <span className="lr-row__title">{d.name}</span>
        <span className="lr-row__sub">{[formatAgo(d.last_seen_at, locale), d.os_version].filter(Boolean).join(" · ")}</span>
      </span>
      <span className="lr-row__end">
        <IconButton variant="danger-soft" label={t("pairing.removeNamed", { name: d.name })} tip={t("pairing.remove")} icon={TrashBin} onPress={onRemove} />
      </span>
    </li>
  );
}

function RemoveDialog({ device, onClose }: { device: Device | null; onClose: () => void }) {
  const { t } = useI18n();
  const revoke = useRevokeDevice();
  return (
    <ConsoleDialog
      isOpen={!!device}
      onOpenChange={(open) => (open ? undefined : onClose())}
      title={t("pairing.removeNamed", { name: device?.name ?? "" })}
      footer={
        <>
          <Button variant="ghost" onPress={onClose}>
            {t("common.cancel")}
          </Button>
          <Button variant="danger" isPending={revoke.isPending} onPress={() => device && revoke.mutate(device.id, { onSuccess: onClose })}>
            {t("pairing.remove")}
          </Button>
        </>
      }
    >
      <p className="text-sm text-muted">{t("pairing.removeBody")}</p>
      {revoke.error ? <FormError>{t(writeErrorMessage(revoke.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
