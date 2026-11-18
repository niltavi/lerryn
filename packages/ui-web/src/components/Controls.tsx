import { Button, Dropdown, Label, Popover, Switch } from "@heroui/react";
import type { StatusTone } from "@lerryn/core";
import type { ReactNode } from "react";

import { IconButton, type IconType } from "./IconButton";

const cx = (...parts: (string | false | undefined | null)[]) => parts.filter(Boolean).join(" ");

export type GlyphTone = StatusTone | "key";

export interface ToneGlyphProps {
  tone: GlyphTone;
  icon: IconType;
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
  const px = size === "lg" ? 18 : 14;
  return (
    <span className={cx("g", `g--${tone}`, size === "lg" && "g--lg", className)} role="img" aria-label={label} title={label}>
      <Icon width={px} height={px} aria-hidden="true" />
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

/** An on-off switch for settings that apply at once. The label is its accessible name only. */
export function Toggle({ checked, onChange, label, size = "md", disabled }: ToggleProps) {
  return (
    <Switch isSelected={checked} onChange={onChange} aria-label={label} size={size} isDisabled={disabled}>
      <Switch.Content>
        <Switch.Control>
          <Switch.Thumb />
        </Switch.Control>
      </Switch.Content>
    </Switch>
  );
}

export interface MenuItem {
  label: string;
  icon?: IconType;
  danger?: boolean;
  onSelect: () => void;
}

export interface ActionMenuProps {
  /** Accessible name of the trigger, e.g. "陈默的操作". */
  label: string;
  icon: IconType;
  items: readonly MenuItem[];
}

/** A small menu behind an icon button, for the actions of a row. */
export function ActionMenu({ label, icon, items }: ActionMenuProps) {
  return (
    <Dropdown>
      <IconButton label={label} icon={icon} />
      <Dropdown.Popover placement="bottom end">
        <Dropdown.Menu aria-label={label} onAction={(key) => items.find((item) => item.label === key)?.onSelect()}>
          {items.map((item) => (
            <Dropdown.Item key={item.label} id={item.label} textValue={item.label} variant={item.danger ? "danger" : undefined}>
              {item.icon ? <item.icon width={16} height={16} aria-hidden="true" /> : null}
              <Label>{item.label}</Label>
            </Dropdown.Item>
          ))}
        </Dropdown.Menu>
      </Dropdown.Popover>
    </Dropdown>
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
  /** Class of the trigger when it is not the default chip, e.g. a status shape (`g g--bad g-btn`). */
  triggerClassName?: string;
}

/**
 * Secondary information behind a trigger, which can be anything: a chip, a
 * status shape, a count. A press opens the details in a popover; Escape or a
 * press elsewhere closes it.
 */
export function Disclosure({ trigger, label, details, detailsLabel, alignRight, triggerClassName }: DisclosureProps) {
  return (
    <Popover>
      {triggerClassName ? (
        <Popover.Trigger className={triggerClassName} aria-label={label}>
          {trigger}
        </Popover.Trigger>
      ) : (
        <Button variant="secondary" size="sm" className="rounded-full" aria-label={label}>
          {trigger}
        </Button>
      )}
      <Popover.Content placement={alignRight ? "bottom end" : "bottom start"} className="max-w-90 min-w-60">
        {/* The dialog takes its name from the trigger; the group names what the details are. */}
        <Popover.Dialog>
          <div className="flex flex-col" role="group" aria-label={detailsLabel}>
            {details}
          </div>
        </Popover.Dialog>
      </Popover.Content>
    </Popover>
  );
}
