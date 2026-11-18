import { Ban, Check, Clock, ForwardStep, PlugConnection, Stopwatch, TriangleExclamation, Xmark } from "@gravity-ui/icons";
import { Chip, type ChipProps } from "@heroui/react";
import {
  resultStatusMeta,
  runStatusMeta,
  useI18n,
  type ResultStatus,
  type RunStatus,
  type StatusIcon as StatusIconName,
  type StatusTone,
} from "@lerryn/core";
import type { ReactNode } from "react";

import type { IconType } from "./IconButton";
import { WaveLoader } from "./Wave";

const ICONS: Record<Exclude<StatusIconName, "wave">, IconType> = {
  clock: Clock,
  check: Check,
  x: Xmark,
  timer: Stopwatch,
  ban: Ban,
  unplug: PlugConnection,
  skip: ForwardStep,
};

/** The icon of a status; `wave` is the animated loader. Decorative: the caller labels it. */
export function StatusIcon({ icon, size = "sm" }: { icon: StatusIconName; size?: "sm" | "md" }) {
  if (icon === "wave") return <WaveLoader size={size} />;
  const Icon = ICONS[icon];
  const px = size === "md" ? 18 : 14;
  return <Icon width={px} height={px} aria-hidden="true" />;
}

const cx = (...parts: (string | false | undefined)[]) => parts.filter(Boolean).join(" ");

// `run` has no HeroUI colour of its own; the theme's chip--info extension tints it.
const TONE_COLOR: Record<StatusTone | "key", ChipProps["color"]> = {
  ok: "success",
  bad: "danger",
  warn: "warning",
  run: "default",
  idle: "default",
  key: "accent",
};

const toneClass = (tone: StatusTone | "key") => cx("rs", `rs--${tone}`, tone === "run" && "chip--info");

export interface RunStatusBadgeProps {
  status: RunStatus;
  size?: "sm" | "md" | "lg";
  className?: string;
}

/**
 * A run status as icon plus text, in the tone the design system assigns
 * (docs/zh-CN/design-system.md section 7). Use it wherever the status is the main
 * information.
 */
export function RunStatusBadge({ status, size = "md", className }: RunStatusBadgeProps) {
  const { t } = useI18n();
  const meta = runStatusMeta(status);
  return (
    <Chip variant="soft" color={TONE_COLOR[meta.tone]} size={size} className={cx(toneClass(meta.tone), className)} data-status={status}>
      <StatusIcon icon={meta.icon} size={size === "lg" ? "md" : "sm"} />
      <Chip.Label>{t(meta.label)}</Chip.Label>
    </Chip>
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
  return (
    <Chip variant="soft" color={TONE_COLOR[tone]} size={size} className={toneClass(tone)}>
      {children}
    </Chip>
  );
}

/** The status a task reported with `sbx result`, shown apart from the run status. */
export function ResultStatusPill({ status }: { status: ResultStatus }) {
  const { t } = useI18n();
  const meta = resultStatusMeta(status);
  const Icon = meta.tone === "ok" ? Check : meta.tone === "bad" ? Xmark : TriangleExclamation;
  return (
    <TonePill tone={meta.tone}>
      <Icon width={12} height={12} aria-hidden="true" />
      <Chip.Label>{t(meta.label)}</Chip.Label>
    </TonePill>
  );
}
