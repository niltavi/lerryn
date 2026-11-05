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

function Mark() {
  return (
    <>
      <path fill={LOGO_MARK.bark.fill} d={LOGO_MARK.bark.d} />
      <path fill={LOGO_MARK.kraft.fill} d={LOGO_MARK.kraft.d} />
    </>
  );
}

/**
 * The Lerryn mark, drawn as vectors in its fixed brand colours (they do not
 * follow the theme). On dark surfaces use square or circle, or place the
 * horizontal mark on the `plate` colour as the rail does.
 */
export function Logo({ variant = "horizontal", size = 32, label = "Lerryn", className }: LogoProps) {
  const { width: mw, height: mh } = LOGO_MARK;
  const a11y = label ? { role: "img" as const, "aria-label": label } : { "aria-hidden": true as const };
  if (variant === "horizontal") {
    return (
      <svg className={className} width={Math.round(size * (mw / mh))} height={size} viewBox={`0 0 ${mw} ${mh}`} {...a11y}>
        <Mark />
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
        <Mark />
      </g>
    </svg>
  );
}
