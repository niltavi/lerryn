import type { ReactNode } from "react";

import { Disclosure } from "./Controls";

export interface InfoChipProps {
  /** Content of the chip itself: an icon and a few words. */
  children: ReactNode;
  /** Details shown after a press. */
  details: ReactNode;
  /** Accessible name of the details, e.g. "时间线". */
  detailsLabel: string;
  /** Open towards the left, for chips at the right edge. */
  alignRight?: boolean;
}

/**
 * A capsule that keeps secondary information one press away, so the board
 * stays quiet. Escape or a press elsewhere closes it.
 */
export function InfoChip({ children, details, detailsLabel, alignRight }: InfoChipProps) {
  return <Disclosure trigger={children} details={details} detailsLabel={detailsLabel} alignRight={alignRight} />;
}
