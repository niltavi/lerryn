import {
  resultStatusMeta,
  runStatusMeta,
  useI18n,
  type ResultStatus,
  type RunStatus,
  type StatusIcon as StatusIconName,
  type StatusTone,
} from "@lerryn/core";
import { Ban, Check, Clock, SkipForward, Timer, TriangleAlert, Unplug, X, type LucideIcon } from "lucide-react";

import type { ReactNode } from "react";

import { WaveLoader } from "./Wave";

const ICONS: Record<Exclude<StatusIconName, "wave">, LucideIcon> = {
  clock: Clock,
  check: Check,
  x: X,
  timer: Timer,
  ban: Ban,
  unplug: Unplug,
  skip: SkipForward,
};

/** The icon of a status; `wave` is the animated loader. Decorative: the caller labels it. */
export function StatusIcon({ icon, size = "sm" }: { icon: StatusIconName; size?: "sm" | "md" }) {
  if (icon === "wave") return <WaveLoader size={size} />;
  const Icon = ICONS[icon];
  return <Icon className="wl-icon" aria-hidden="true" />;
}

const cx = (...parts: (string | false | undefined)[]) => parts.filter(Boolean).join(" ");

export interface RunStatusBadgeProps {
  status: RunStatus;
  size?: "sm" | "md" | "lg";
  className?: string;
}

/**
 * A run status as icon plus text, in the tone the design system assigns
 * (docs/design-system.md section 7). Use it wherever the status is the main
 * information.
 */
export function RunStatusBadge({ status, size = "md", className }: RunStatusBadgeProps) {
  const { t } = useI18n();
  const meta = runStatusMeta(status);
  return (
    <span className={cx("rs", `rs--${meta.tone}`, size !== "md" && `rs--${size}`, className)} data-status={status}>
      <StatusIcon icon={meta.icon} size={size === "lg" ? "md" : "sm"} />
      {t(meta.label)}
    </span>
  );
}

export interface StatusGlyphProps {
  status: RunStatus;
  size?: "md" | "lg";
  className?: string;
}

/**
 * A run status as a shape only, for dense lists. The text is in the
 * accessible name and the native tooltip, so it is never colour alone.
 */
export function StatusGlyph({ status, size = "md", className }: StatusGlyphProps) {
  const { t } = useI18n();
  const meta = runStatusMeta(status);
  const label = t(meta.label);
  return (
    <span
      className={cx("g", `g--${meta.tone}`, size === "lg" && "g--lg", className)}
      role="img"
      aria-label={label}
      title={label}
      data-status={status}
    >
      <StatusIcon icon={meta.icon} size={size === "lg" ? "md" : "sm"} />
    </span>
  );
}

/** A tone-coloured pill for things that are not run states, such as a reported result. */
export function TonePill({ tone, children, size = "sm" }: { tone: StatusTone; children: ReactNode; size?: "sm" | "md" }) {
  return <span className={cx("rs", `rs--${tone}`, size === "sm" && "rs--sm")}>{children}</span>;
}

/** The status a task reported with `sbx result`, shown apart from the run status. */
export function ResultStatusPill({ status }: { status: ResultStatus }) {
  const { t } = useI18n();
  const meta = resultStatusMeta(status);
  const Icon = meta.tone === "ok" ? Check : meta.tone === "bad" ? X : TriangleAlert;
  return (
    <TonePill tone={meta.tone}>
      <Icon className="wl-icon" aria-hidden="true" />
      {t(meta.label)}
    </TonePill>
  );
}
