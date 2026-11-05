import type { CSSProperties } from "react";

/**
 * The corrugation path: alternating crests every half period. The first
 * segment uses Q, the rest T, so crests and troughs join smoothly. Same
 * geometry as @lerryn/ui-native.
 */
export function wavePath(length: number, period: number, amplitude: number, center: number): string {
  const half = period / 2;
  let d = `M 0 ${center} Q ${half / 2} ${center - amplitude * 2} ${half} ${center}`;
  for (let along = period; along <= length + half; along += half) d += ` T ${along} ${center}`;
  return d;
}

const LOADER = {
  sm: { width: 16, period: 8, amplitude: 2, stroke: 2 },
  md: { width: 28, period: 12, amplitude: 3, stroke: 2.5 },
  lg: { width: 44, period: 18, amplitude: 4, stroke: 3.5 },
} as const;

export interface WaveLoaderProps {
  size?: keyof typeof LOADER;
  /** Read by screen readers. Pass "" when a neighbouring label already says it. */
  label?: string;
  className?: string;
}

/**
 * The flowing corrugation that marks work in progress. It takes the text
 * colour of its parent and stops moving when the system asks for reduced
 * motion (console.css).
 */
export function WaveLoader({ size = "md", label, className }: WaveLoaderProps) {
  const spec = LOADER[size];
  const height = spec.amplitude * 2 + spec.stroke;
  const length = spec.width + spec.period;
  const style = { width: spec.width, height, "--wl-period": `${spec.period}px` } as CSSProperties;
  const a11y = label ? { role: "progressbar" as const, "aria-label": label } : { "aria-hidden": true as const };
  return (
    <span className={["wl-loader", className].filter(Boolean).join(" ")} style={style} {...a11y}>
      <svg width={length} height={height} viewBox={`0 0 ${length} ${height}`} fill="none">
        <path
          d={wavePath(length, spec.period, spec.amplitude, spec.amplitude + spec.stroke / 2)}
          stroke="currentColor"
          strokeWidth={spec.stroke}
          strokeLinecap="round"
        />
      </svg>
    </span>
  );
}

export interface WaveGlyphProps {
  size?: number;
  strokeWidth?: number;
  className?: string;
}

/** Two static crests on a 24 grid, used where Lucide has no fitting icon for agent work. */
export function WaveGlyph({ size = 24, strokeWidth = 2, className }: WaveGlyphProps) {
  return (
    <svg
      className={["wl-icon", className].filter(Boolean).join(" ")}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      aria-hidden="true"
    >
      <path
        d="M 3 12 Q 6 5 9 12 T 15 12 T 21 12"
        stroke="currentColor"
        strokeWidth={strokeWidth}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
