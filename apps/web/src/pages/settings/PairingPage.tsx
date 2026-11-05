import { formatAgo, useCreatePairingCode, useDevices, useI18n, useRevokeDevice, type Device } from "@lerryn/core";
import { Button, ConsoleDialog, IconButton, QrCode, StateCard } from "@lerryn/ui-web";
import { BookOpen, RefreshCw, ScanLine, Smartphone, Tablet, Trash2 } from "lucide-react";
import { useEffect, useState } from "react";

import { useNow } from "../../lib/hooks";
import { Board, BoardTop, LoadError, usePageTitle } from "../../shell/layout";
import { FormError } from "../auth/AuthFrame";
import { writeErrorMessage } from "../../lib/origin";

/** "4:32" from milliseconds left. */
export function countdown(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

/**
 * Phone pairing (prototype Settings-Pairing.dc.html): a one-time code as a
 * QR code for the Lerryn app, valid for five minutes, and the phones
 * already signed in. Phone sessions are read-only.
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
      <div className="row" style={{ gap: 28, alignItems: "center" }}>
        {code ? (
          <QrCode value={code.uri} label={t("pairing.qr")} dimmed={expired} />
        ) : (
          <div className="qr qr--off" aria-hidden="true" />
        )}
        <div className="stack" style={{ gap: 10 }}>
          <span className="row" style={{ gap: 8 }}>
            <ScanLine className="wl-icon" width={18} aria-hidden="true" />
            <span style={{ fontWeight: 600 }}>{t("pairing.scan")}</span>
          </span>
          {pairing.isError ? (
            <FormError>{t(writeErrorMessage(pairing.error))}</FormError>
          ) : code ? (
            <span className="row" style={{ gap: 8 }}>
              <span className="num" style={{ fontSize: 32, lineHeight: "38px", color: expired ? "var(--ink-muted)" : undefined }} aria-live="off">
                {expired ? t("pairing.expired") : countdown(left)}
              </span>
              <IconButton label={t("pairing.regenerate")} icon={RefreshCw} isPending={pairing.isPending} onPress={() => mutate()} />
            </span>
          ) : null}
          {code && !expired ? (
            <span className="row caption muted" style={{ gap: 6 }}>
              {t("pairing.manualCode")}
              <span className="mono" style={{ color: "var(--ink)", letterSpacing: "0.08em" }}>
                {code.code}
              </span>
            </span>
          ) : null}
          <span className="row caption muted" style={{ gap: 6 }}>
            <BookOpen className="wl-icon" width={14} aria-hidden="true" />
            {t("pairing.readOnly")}
          </span>
        </div>
      </div>

      {devices.isError ? (
        <LoadError error={devices.error} onRetry={() => void devices.refetch()} />
      ) : devices.data && items.length === 0 ? (
        <StateCard icon={Smartphone} title={t("pairing.none")} />
      ) : items.length > 0 ? (
        <div className="rows" role="list" aria-label={t("pairing.devices")}>
          {items.map((d) => (
            <DeviceRow key={d.id} device={d} onRemove={() => setRemoving(d)} />
          ))}
        </div>
      ) : null}
      <RemoveDialog device={removing} onClose={() => setRemoving(null)} />
    </Board>
  );
}

function DeviceRow({ device: d, onRemove }: { device: Device; onRemove: () => void }) {
  const { t, locale } = useI18n();
  const Icon = /ipad|tablet/i.test(d.name) ? Tablet : Smartphone;
  return (
    <div className="rowi" role="listitem">
      <span className="ic-box" aria-hidden="true">
        <Icon className="wl-icon" width={18} />
      </span>
      <span style={{ minWidth: 0 }}>
        <span className="rowi-name">{d.name}</span>
        <span className="rowi-sub">{[formatAgo(d.last_seen_at, locale), d.os_version].filter(Boolean).join(" · ")}</span>
      </span>
      <span className="rowi-end">
        <span className="acts">
          <IconButton variant="danger-soft" label={t("pairing.removeNamed", { name: d.name })} tip={t("pairing.remove")} icon={Trash2} onPress={onRemove} />
        </span>
      </span>
    </div>
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
      <p className="body-sm muted">{t("pairing.removeBody")}</p>
      {revoke.error ? <FormError>{t(writeErrorMessage(revoke.error))}</FormError> : null}
    </ConsoleDialog>
  );
}
