import { findGap, type LogLine } from '@lerryn/core';
import { cn, Mono, useLerrynTheme, WaveLoader } from '@lerryn/ui-native';
import { ChevronUp } from 'lucide-react-native';
import { memo, useState } from 'react';
import { Pressable, View } from 'react-native';

import { useStrings } from '../i18n/useStrings';
import type { LogTail } from '../live/useLogTail';
import { ErrorState } from './States';
import { Txt } from './Txt';

/** Lines drawn at first; "earlier" shows more of what is held before reading from the server. */
const WINDOW = 200;
const STEP = 500;

const Line = memo(function Line({ line }: { line: LogLine }) {
  const tone = line.stream === 'stderr' ? 'text-danger' : line.stream === 'system' ? 'text-ink-muted' : 'text-ink';
  return (
    <View className="flex-row gap-2 px-3">
      <Mono size="mono-sm" className="w-10 text-right" selectable={false}>
        {line.seq}
      </Mono>
      <Mono size="mono-sm" className={cn('min-w-0 flex-1', tone)} selectable>
        {line.text}
      </Mono>
    </View>
  );
});

/**
 * The end of the log (prototype M-Run "log"): line numbers, stderr in the
 * danger color, system lines muted, and a button for earlier lines at the
 * top. New lines appear at the bottom as they arrive.
 */
export function LogTailView({ log }: { log: LogTail }) {
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const [visible, setVisible] = useState(WINDOW);

  if (log.error) return <ErrorState error={log.error} onRetry={log.retry} />;

  const shown = log.lines.slice(Math.max(0, log.lines.length - visible));
  const hiddenHeld = log.lines.length - shown.length;
  const gap = findGap(log.lines);
  // Lines before the first one held that the server still has.
  const missingBefore = hiddenHeld === 0 && gap ? gap.count : 0;
  const earlier = hiddenHeld > 0 ? hiddenHeld : missingBefore;
  const innerGap = gap && gap.index > 0 ? gap : null;

  const showEarlier = () => {
    if (hiddenHeld > 0) setVisible((v) => v + STEP);
    else log.loadEarlier();
  };

  return (
    <View
      accessibilityLabel={t('log.title')}
      className="overflow-hidden rounded-3xl border border-line bg-liner shadow-flute-b"
    >
      {earlier > 0 && !log.expired ? (
        <Pressable
          onPress={showEarlier}
          disabled={log.loadingEarlier}
          accessibilityRole="button"
          className="flex-row items-center justify-center gap-1.5 border-b border-dashed border-line py-2"
        >
          {log.loadingEarlier ? (
            <WaveLoader size="sm" accessibilityLabel={t('common.loading')} />
          ) : (
            <ChevronUp size={14} color={colors['kraft-strong']} strokeWidth={2.5} />
          )}
          <Txt variant="caption" weight="semibold" className="text-kraft-strong">
            {m('run.showEarlier', { count: earlier })}
          </Txt>
        </Pressable>
      ) : null}
      <View className="gap-0.5 py-2.5" accessibilityRole="text">
        {log.expired ? (
          <Txt variant="body-sm" className="px-4 py-2 text-ink-muted">
            {t('log.expired')}
          </Txt>
        ) : !log.loaded ? (
          <View className="items-center py-4">
            <WaveLoader size="sm" accessibilityLabel={t('common.loading')} />
          </View>
        ) : shown.length === 0 ? (
          <Txt variant="body-sm" className="px-4 py-2 text-ink-muted">
            {log.complete ? t('log.empty') : t('log.receiving')}
          </Txt>
        ) : (
          shown.map((line, i) => (
            <View key={line.seq}>
              {innerGap && line.seq === log.lines[innerGap.index]?.seq && i > 0 ? (
                <Pressable onPress={log.loadEarlier} accessibilityRole="button" className="items-center py-1">
                  <Txt variant="caption" weight="semibold" className="text-kraft-strong">
                    {t('log.gap', { count: innerGap.count })}
                  </Txt>
                </Pressable>
              ) : null}
              <Line line={line} />
            </View>
          ))
        )}
      </View>
      {log.dropped > 0 ? (
        <Txt variant="caption" className="border-t border-dashed border-line px-4 py-2 text-ink-muted">
          {t('log.dropped', { count: log.dropped })}
        </Txt>
      ) : null}
      {!log.complete && log.loaded && shown.length > 0 ? (
        <View className="flex-row items-center gap-2 border-t border-dashed border-line px-4 py-2">
          <WaveLoader size="sm" accessibilityLabel={t('log.receiving')} />
          <Txt variant="caption" className="text-ink-muted">
            {t('log.receiving')}
          </Txt>
        </View>
      ) : null}
    </View>
  );
}
