import { resultStatusMeta, type RunResult } from '@lerryn/core';
import { Numeral, WaveGlyph, useLerrynTheme } from '@lerryn/ui-native';
import { View } from 'react-native';

import { useStrings } from '../i18n/useStrings';
import { panelViews, type PanelSpec } from '../lib/view';
import { Glyph } from './Glyph';
import { Txt } from './Txt';

/**
 * What the task reported with `sbx result`, kept apart from the run status
 * the platform recorded (tech design 13.2, ResultPanel). Metric and text
 * panels from the manifest show under the summary.
 */
export function ResultCard({
  title,
  result,
  panels,
  time,
}: {
  title: string;
  result: RunResult;
  panels?: readonly PanelSpec[];
  time?: string;
}) {
  const { t } = useStrings();
  const { colors } = useLerrynTheme();
  const meta = resultStatusMeta(result.status);
  const views = panelViews(panels, result.data);
  const metrics = views.filter((v) => v.type === 'metric');
  const texts = views.filter((v) => v.type === 'text');
  return (
    <View className="gap-4 rounded-3xl border border-line bg-liner p-4 shadow-flute-b">
      <View className="flex-row items-center gap-2">
        <WaveGlyph size={18} color={colors['kraft-strong']} />
        <Txt variant="body" weight="semibold" className="flex-1">
          {title}
        </Txt>
        {time ? (
          <Txt variant="caption" className="text-ink-muted">
            {time}
          </Txt>
        ) : null}
      </View>
      <View className="flex-row items-start gap-2.5">
        <Glyph
          glyph={{ tone: meta.tone, icon: result.status === 'ok' ? 'check' : result.status === 'partial' ? 'unplug' : 'x' }}
          label={t(meta.label)}
          size="sm"
        />
        <Txt variant="body-sm" className="flex-1">
          {result.summary || t(meta.label)}
        </Txt>
      </View>
      {metrics.length > 0 ? (
        <View className="flex-row flex-wrap gap-x-7 gap-y-3">
          {metrics.map((p) =>
            p.type === 'metric' ? (
              <View key={p.id} className="gap-1" accessible accessibilityLabel={`${p.title} ${p.value}${p.unit ?? ''}`}>
                <View className="flex-row items-baseline gap-1">
                  <Numeral size="display">{formatMetric(p.value)}</Numeral>
                  {p.unit ? (
                    <Txt variant="body-sm" weight="semibold" className="text-ink-muted">
                      {p.unit}
                    </Txt>
                  ) : null}
                </View>
                <Txt variant="body-sm" className="text-ink-muted">
                  {p.label ?? p.title}
                </Txt>
              </View>
            ) : null,
          )}
        </View>
      ) : null}
      {texts.map((p) =>
        p.type === 'text' ? (
          <View key={p.id} className="gap-1 rounded-2xl bg-liner-2 p-3">
            <Txt variant="caption" weight="semibold" className="text-ink-muted">
              {p.title}
            </Txt>
            <Txt variant="body-sm" selectable>
              {p.text}
            </Txt>
          </View>
        ) : null,
      )}
    </View>
  );
}

function formatMetric(n: number): string {
  if (Number.isInteger(n)) return String(n);
  return String(Math.round(n * 100) / 100);
}
