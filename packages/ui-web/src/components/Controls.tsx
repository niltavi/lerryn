import type { StatusTone } from "@lerryn/core";
import type { LucideIcon } from "lucide-react";
import { useEffect, useId, useRef, useState, type ComponentType, type ReactNode } from "react";

import { IconButton } from "./IconButton";

const cx = (...parts: (string | false | undefined | null)[]) => parts.filter(Boolean).join(" ");

export type GlyphTone = StatusTone | "key";

export interface ToneGlyphProps {
  tone: GlyphTone;
  icon: ComponentType<{ className?: string }>;
  /** Accessible name and tooltip; the shape is never the only signal. */
  label: string;
  size?: "md" | "lg";
  className?: string;
}

/**
 * A round status shape for things that are not run states: a paused task,
 * a credential to fill, an offline runner. The text is in the accessible
 * name and the tooltip.
 */
export function ToneGlyph({ tone, icon: Icon, label, size = "md", className }: ToneGlyphProps) {
  return (
    <span className={cx("g", `g--${tone}`, size === "lg" && "g--lg", className)} role="img" aria-label={label} title={label}>
      <Icon className="wl-icon" />
    </span>
  );
}

export interface ToggleProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: string;
  size?: "sm" | "md";
  disabled?: boolean;
}

/** An on-off switch with a kraft track, for settings that apply at once. */
export function Toggle({ checked, onChange, label, size = "md", disabled }: ToggleProps) {
  return (
    <button
      type="button"
      role="switch"
      className={size === "sm" ? "switch-sm" : "toggle"}
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
    />
  );
}

export interface MenuItem {
  label: string;
  icon?: LucideIcon;
  danger?: boolean;
  onSelect: () => void;
}

export interface ActionMenuProps {
  /** Accessible name of the trigger, e.g. "陈默的操作". */
  label: string;
  icon: ComponentType<{ className?: string }>;
  items: readonly MenuItem[];
}

/**
 * A small menu behind an icon button, for the actions of a row. Escape or a
 * click elsewhere closes it.
 */
export function ActionMenu({ label, icon, items }: ActionMenuProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const id = useId();
  const close = () => {
    trigger.current?.focus();
    setOpen(false);
  };
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onDown);
    return () => document.removeEventListener("pointerdown", onDown);
  }, [open]);
  return (
    <div
      ref={ref}
      className={cx("disc disc--right", open && "is-open")}
      onKeyDown={(e) => {
        if (open && e.key === "Escape") close();
      }}
    >
      <IconButton ref={trigger} label={label} icon={icon} aria-expanded={open} aria-controls={id} onPress={() => setOpen((o) => !o)} />
      {open ? (
        <div className="pop menu" id={id} role="menu" aria-label={label}>
          {items.map((item) => (
            <button
              key={item.label}
              type="button"
              role="menuitem"
              className={item.danger ? "danger" : undefined}
              onClick={() => {
                close();
                item.onSelect();
              }}
            >
              {item.icon ? <item.icon className="wl-icon" aria-hidden="true" /> : null}
              {item.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}

export interface DisclosureProps {
  /** The trigger's content. */
  trigger: ReactNode;
  /** Accessible name of the trigger when its content is only a shape. */
  label?: string;
  details: ReactNode;
  detailsLabel: string;
  alignRight?: boolean;
  /** Class of the trigger button; defaults to a chip. */
  triggerClassName?: string;
}

/**
 * Like InfoChip, but the trigger can be anything: a status shape, a count.
 * Shows the details on hover or focus, pins them on click.
 */
export function Disclosure({ trigger, label, details, detailsLabel, alignRight, triggerClassName = "chip" }: DisclosureProps) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  const id = useId();
  useEffect(() => {
    if (!open) return;
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("pointerdown", onDown);
    return () => document.removeEventListener("pointerdown", onDown);
  }, [open]);
  return (
    <div
      ref={ref}
      className={cx("disc", alignRight && "disc--right", open && "is-open")}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          setOpen(false);
          (document.activeElement as HTMLElement | null)?.blur();
        }
      }}
    >
      <button
        className={triggerClassName}
        type="button"
        aria-label={label}
        aria-expanded={open}
        aria-controls={id}
        onClick={() => setOpen((o) => !o)}
      >
        {trigger}
      </button>
      <div className="pop" id={id} role="group" aria-label={detailsLabel}>
        {details}
      </div>
    </div>
  );
}
