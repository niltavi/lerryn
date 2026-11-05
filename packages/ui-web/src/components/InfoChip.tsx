import { useEffect, useId, useRef, useState, type ReactNode } from "react";

export interface InfoChipProps {
  /** Content of the chip itself: an icon and a few words. */
  children: ReactNode;
  /** Details shown on hover, on focus, or after a click. */
  details: ReactNode;
  /** Accessible name of the details, e.g. "时间线". */
  detailsLabel: string;
  /** Open towards the left, for chips at the right edge. */
  alignRight?: boolean;
}

/**
 * A capsule that discloses secondary information on hover or focus, so the
 * board stays quiet (prototype: "次要信息收进胶囊"). A click pins it open for
 * touch screens; Escape or a click elsewhere closes it.
 */
export function InfoChip({ children, details, detailsLabel, alignRight }: InfoChipProps) {
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
      className={["disc", alignRight && "disc--right", open && "is-open"].filter(Boolean).join(" ")}
      onKeyDown={(e) => {
        if (e.key === "Escape") {
          setOpen(false);
          (document.activeElement as HTMLElement | null)?.blur();
        }
      }}
    >
      <button className="chip" type="button" aria-expanded={open} aria-controls={id} onClick={() => setOpen((o) => !o)}>
        {children}
      </button>
      <div className="pop" id={id} role="group" aria-label={detailsLabel}>
        {details}
      </div>
    </div>
  );
}
