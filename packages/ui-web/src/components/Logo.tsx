import { LOGO_BADGE, LOGO_MARK, LOGO_PLATE } from "../brand/logoMark";

export interface LogoProps {
  /** horizontal: the mark alone; square / circle: the mark on a paper plate. */
  variant?: "horizontal" | "square" | "circle";
  /** Height for horizontal, side or diameter for the others. */
  size?: number;
  /** Accessible name. Pass "" when the logo sits next to the product name. */
  label?: string;
  className?: string;
}

// themed: the mark sits on the page, so it takes the theme's mark colours
// (brighter on dark surfaces, as on the website). On a plate it keeps the brand colours.
function Mark({ themed }: { themed: boolean }) {
  const side = themed ? `var(--mark-side, ${LOGO_MARK.bark.fill})` : LOGO_MARK.bark.fill;
  const top = themed ? `var(--mark-top, ${LOGO_MARK.kraft.fill})` : LOGO_MARK.kraft.fill;
  return (
    <>
      <path style={{ fill: side }} d={LOGO_MARK.bark.d} />
      <path style={{ fill: top }} d={LOGO_MARK.kraft.d} />
    </>
  );
}

/**
 * The Lerryn mark, drawn as vectors. horizontal follows the theme through the
 * mark-side / mark-top tokens and needs no plate; square and circle keep the
 * brand colours on the paper plate.
 */
export function Logo({ variant = "horizontal", size = 32, label = "Lerryn", className }: LogoProps) {
  const { width: mw, height: mh } = LOGO_MARK;
  const a11y = label ? { role: "img" as const, "aria-label": label } : { "aria-hidden": true as const };
  if (variant === "horizontal") {
    return (
      <svg className={className} width={Math.round(size * (mw / mh))} height={size} viewBox={`0 0 ${mw} ${mh}`} {...a11y}>
        <Mark themed />
      </svg>
    );
  }
  const w = LOGO_BADGE[variant];
  const s = w / mw;
  const tx = 512 - w / 2;
  const ty = 512 - (mh * s) / 2;
  return (
    <svg className={className} width={size} height={size} viewBox="0 0 1024 1024" {...a11y}>
      {variant === "circle" ? (
        <circle cx={512} cy={512} r={512} fill={LOGO_PLATE} />
      ) : (
        <rect width={1024} height={1024} rx={225} fill={LOGO_PLATE} />
      )}
      <g transform={`translate(${tx} ${ty}) scale(${s})`}>
        <Mark themed={false} />
      </g>
    </svg>
  );
}
