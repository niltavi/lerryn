import { Button, Tooltip, type ButtonProps } from "@heroui/react";
import { useEffect, useState, type ComponentType } from "react";

import { useI18n } from "@lerryn/core";

export interface IconButtonProps extends Omit<ButtonProps, "children" | "isIconOnly"> {
  /** Accessible name, also shown as the tooltip. */
  label: string;
  icon: ComponentType<{ className?: string }>;
  /** Tooltip text when it should differ from the label. */
  tip?: string;
  /** Controls the tooltip; leave both unset for hover and focus behaviour. */
  isTipOpen?: boolean;
  onTipOpenChange?: (open: boolean) => void;
}

/** An icon-only button whose name appears as a tooltip on hover and focus. */
export function IconButton({
  label,
  icon: Icon,
  tip,
  isTipOpen,
  onTipOpenChange,
  variant = "ghost",
  size = "sm",
  ...rest
}: IconButtonProps) {
  return (
    <Tooltip delay={300} isOpen={isTipOpen} onOpenChange={onTipOpenChange}>
      <Button variant={variant} size={size} isIconOnly aria-label={label} {...rest}>
        <Icon className="wl-icon" />
      </Button>
      <Tooltip.Content>{tip ?? label}</Tooltip.Content>
    </Tooltip>
  );
}

export interface CopyButtonProps {
  /** Text put on the clipboard. */
  value: string;
  /** Accessible name, e.g. "复制运行 ID". */
  label: string;
  icon: ComponentType<{ className?: string }>;
  variant?: ButtonProps["variant"];
}

/**
 * Puts text on the clipboard. The Clipboard API only exists in secure contexts,
 * and self-hosted consoles are often served over plain HTTP, so fall back to
 * selecting a hidden textarea and running the legacy copy command.
 */
export async function copyText(value: string, anchor?: Element | null): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Denied or the document lost focus; try the legacy path below.
    }
  }
  return legacyCopy(value, anchor);
}

function legacyCopy(value: string, anchor?: Element | null): boolean {
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.top = "0";
  textarea.style.left = "0";
  textarea.style.opacity = "0";
  // A modal dialog pulls focus back inside itself, which would drop the
  // selection, so the textarea has to live in the same dialog as the button.
  const host = anchor?.closest("[role=dialog]") ?? document.body;
  const previous = document.activeElement as HTMLElement | null;
  host.appendChild(textarea);
  try {
    textarea.focus();
    textarea.select();
    return document.execCommand("copy");
  } catch {
    return false;
  } finally {
    textarea.remove();
    previous?.focus();
  }
}

/** Copies a value and confirms in the tooltip for a moment. */
export function CopyButton({ value, label, icon, variant }: CopyButtonProps) {
  const { t } = useI18n();
  const [result, setResult] = useState<"copied" | "failed" | null>(null);
  const [hovered, setHovered] = useState(false);
  useEffect(() => {
    if (!result) return;
    const id = setTimeout(() => setResult(null), 1500);
    return () => clearTimeout(id);
  }, [result]);
  const tip = result === "copied" ? t("common.copied") : result === "failed" ? t("common.copyFailed") : label;
  return (
    <IconButton
      label={label}
      tip={tip}
      icon={icon}
      variant={variant}
      // Pressing closes a tooltip, so hold it open while it carries the result.
      isTipOpen={hovered || result !== null}
      onTipOpenChange={setHovered}
      onPress={(e) => {
        void copyText(value, e.target as Element).then((ok) => setResult(ok ? "copied" : "failed"));
      }}
    />
  );
}
