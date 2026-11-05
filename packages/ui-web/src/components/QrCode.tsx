import { useMemo } from "react";
import { encode } from "uqr";

/** The modules of a QR code as one SVG path, one unit per module. Exported for tests. */
export function qrPath(value: string): { size: number; d: string } {
  const { data, size } = encode(value, { ecc: "M", border: 0 });
  let d = "";
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      if (data[y]?.[x]) d += `M${x} ${y}h1v1h-1z`;
    }
  }
  return { size, d };
}

export interface QrCodeProps {
  value: string;
  /** Accessible name, e.g. "配对二维码". */
  label: string;
  /** Dim the code, e.g. once it has expired. */
  dimmed?: boolean;
}

/**
 * A QR code drawn as inline SVG. It stays dark on the logo plate colour in
 * both themes, since scanners need the contrast; the value never leaves
 * the page.
 */
export function QrCode({ value, label, dimmed }: QrCodeProps) {
  const { size, d } = useMemo(() => qrPath(value), [value]);
  return (
    <div className={dimmed ? "qr qr--off" : "qr"} role="img" aria-label={label}>
      <svg viewBox={`0 0 ${size} ${size}`} shapeRendering="crispEdges" aria-hidden="true">
        <path d={d} />
      </svg>
    </div>
  );
}
