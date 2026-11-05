import { cn, Mono } from '@lerryn/ui-native';
import type { ReactNode } from 'react';
import { Pressable, View } from 'react-native';

import type { StripTone } from '../lib/view';

const FILL: Record<StripTone, string> = {
  empty: 'bg-liner-3',
  ok: 'bg-kraft',
  idle: 'bg-line-strong',
  run: 'bg-run',
  warn: 'bg-warning',
  bad: 'bg-danger',
  plan: 'border-line-strong border-dashed border-t border-l border-r bg-transparent',
};

export interface Ridge {
  key: string;
  tone: StripTone;
  /** 0 to 1. */
  height: number;
  label: string;
  onPress?: () => void;
}

/**
 * The corrugated strip: one ridge per time slot or per run, as in the
 * prototype's overview and task pages. Each ridge is a button when it leads
 * somewhere and always carries a screen reader label.
 */
export function Ridges({
  ridges,
  axis,
  now,
  plotHeight = 84,
  fixedWidth,
}: {
  ridges: Ridge[];
  axis: ReactNode[];
  /** 0 to 1 across the strip: where to draw the "now" marker. */
  now?: number;
  plotHeight?: number;
  /** Narrow fixed-width ridges, for a few runs; otherwise they share the width. */
  fixedWidth?: boolean;
}) {
  const gap = fixedWidth ? 4 : 2;
  return (
    <View className="gap-1.5">
      <View className="relative flex-row items-end" style={{ height: plotHeight, gap }}>
        {ridges.map((r) => (
          <Pressable
            key={r.key}
            onPress={r.onPress}
            disabled={!r.onPress}
            accessibilityRole={r.onPress ? 'button' : 'image'}
            accessibilityLabel={r.label}
            className={cn('rounded-t-sm', FILL[r.tone])}
            style={{
              height: Math.max(4, Math.round(r.height * plotHeight)),
              ...(fixedWidth ? { width: 10 } : { flex: 1, minWidth: 2 }),
            }}
          />
        ))}
        {now !== undefined ? (
          <View
            pointerEvents="none"
            className="absolute bottom-0 rounded-full bg-ink"
            style={{ left: `${Math.min(100, Math.max(0, now * 100))}%`, top: -8, width: 2 }}
          />
        ) : null}
      </View>
      <View className="flex-row justify-between">
        {axis.map((a, i) => (
          <Mono key={i} size="mono-sm">
            {a}
          </Mono>
        ))}
      </View>
    </View>
  );
}
