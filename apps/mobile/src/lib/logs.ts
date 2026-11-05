import { applyStreamLines as applyLines, type LogLine, type LogState } from '@lerryn/core';

// The stream helpers live in @lerryn/core, shared with the web console; the
// phone keeps fewer lines in memory.
export { applyStreamEnd, isLogLine, lastSeq, parseEventData } from '@lerryn/core';

/** Lines the phone keeps while following a run; older ones become the gap. */
export const MOBILE_MAX_LINES = 2_000;

/** Applies lines pushed by streamRunLogs, keeping at most MOBILE_MAX_LINES. */
export function applyStreamLines(state: LogState, lines: readonly LogLine[], maxLines = MOBILE_MAX_LINES): LogState {
  return applyLines(state, lines, maxLines);
}
