import { formatClock, formatDuration, isRunActive, runStatusMeta, shortId, summarizeRunEvents, useTask } from '@lerryn/core';
import { cn, Mono, Numeral, useLerrynTheme } from '@lerryn/ui-native';
import * as Clipboard from 'expo-clipboard';
import { useLocalSearchParams } from 'expo-router';
import { CircleAlert, Copy, ExternalLink, RotateCcw, Wrench } from 'lucide-react-native';
import { Linking, Pressable, View } from 'react-native';

import { useStrings } from '../../src/i18n/useStrings';
import { runCause, runDuration, runGlyph, stageRows, type Run } from '../../src/lib/view';
import { shortWhen } from '../../src/lib/when';
import { useLogTail } from '../../src/live/useLogTail';
import { useReruns, useRunEvents, useRunLive } from '../../src/queries/queries';
import { Chip } from '../../src/ui/Chip';
import { Glyph, toneColor } from '../../src/ui/Glyph';
import { LogTailView } from '../../src/ui/LogTailView';
import { openTarget } from '../../src/ui/navigate';
import { ResultCard } from '../../src/ui/ResultCard';
import { BackLink, IconAction, Screen } from '../../src/ui/Screen';
import { ErrorState, LoadingState } from '../../src/ui/States';
import { Txt } from '../../src/ui/Txt';
import { useNow } from '../../src/ui/useNow';

/**
 * Run detail (prototype M-Run): the platform's verdict, why it failed, the
 * stages, the end of the log, and what the task reported. Everything
 * updates live while the run is going.
 */
export default function RunScreen() {
  const { runId } = useLocalSearchParams<{ runId: string }>();
  const { t, m } = useStrings();
  const { colors } = useLerrynTheme();
  const run = useRunLive(runId);
  const task = useTask(run.data?.task_id);
  const link = run.data?.links.console;

  return (
    <Screen
      header={
        <>
          <BackLink label={task.data?.name ?? t('overview.title')} />
          {run.data ? (
            <View className="flex-row items-center">
              <IconAction label={t('run.copyId')} onPress={() => void Clipboard.setStringAsync(runId)}>
                <Copy size={18} color={colors.ink} strokeWidth={2.25} />
              </IconAction>
              {link && /^https?:\/\//i.test(link) ? (
                <IconAction label={m('run.openInBrowser')} onPress={() => void Linking.openURL(link)}>
                  <ExternalLink size={18} color={colors.ink} strokeWidth={2.25} />
                </IconAction>
              ) : null}
            </View>
          ) : null}
        </>
      }
      onRefresh={() => run.refetch()}
      bodyClassName="gap-5"
    >
      {run.isPending ? (
        <LoadingState />
      ) : run.isError ? (
        <ErrorState error={run.error} onRetry={() => void run.refetch()} />
      ) : (
        <RunBody run={run.data} panels={task.data?.latest_version.manifest.panels} />
      )}
    </Screen>
  );
}

function RunBody({
  run,
  panels,
}: {
  run: Run;
  panels: NonNullable<ReturnType<typeof useTask>['data']>['latest_version']['manifest']['panels'];
}) {
  const { t, m, locale } = useStrings();
  const { colors } = useLerrynTheme();
  const live = isRunActive(run.status);
  const now = useNow(1_000, live);
  const meta = runStatusMeta(run.status);
  const duration = runDuration(run, now);
  const cause = runCause(run);
  const stages = stageRows(run, now);
  // A log removed by retention is not read at all.
  const log = useLogTail(run.id, { expired: !!run.logs_expired_at });
  const color = colors[toneColor(meta.tone)];
  const reruns = useReruns(run.id, !live && meta.tone !== 'ok');
  const rerun = reruns.data?.items[0];
  // The phone shows only how many calls there were; the list is on the web.
  const events = useRunEvents(run.id, live);
  const calls = summarizeRunEvents(events.data?.events ?? []);
  const metaLine = [
    shortId(run.id),
    shortWhen(run.started_at ?? run.queued_at, locale, now),
    `v${run.version}`,
    run.test ? t('run.test') : null,
    run.max_attempts > 1 ? m('run.attempt', { attempt: run.attempt, max: run.max_attempts }) : null,
  ]
    .filter(Boolean)
    .join(' · ');

  return (
    <>
      <View className="gap-2.5">
        <View className="flex-row items-center gap-3">
          <Glyph glyph={runGlyph(run.status)} label={t(meta.label)} size="lg" />
          <Txt variant="title-2" weight="bold" style={{ color }} accessibilityRole="header" className="flex-1">
            {t(meta.label)}
          </Txt>
          {duration !== null ? (
            <Numeral size="title-1">{formatDuration(duration, { whole: live })}</Numeral>
          ) : null}
        </View>
        <Mono size="mono-sm" selectable>
          {metaLine}
        </Mono>
      </View>

      {cause || run.skip_reason ? (
        <View
          className={cn(
            'flex-row items-start gap-2.5 rounded-2xl px-3.5 py-3',
            !cause ? 'bg-liner-2' : meta.tone === 'warn' ? 'bg-warning-soft' : 'bg-danger-soft',
          )}
        >
          <CircleAlert
            size={18}
            color={!cause ? colors['ink-muted'] : meta.tone === 'warn' ? colors['warning-ink'] : colors.danger}
            strokeWidth={2.25}
          />
          <Txt variant="body-sm" className="flex-1" selectable>
            {cause ? (
              <>
                {cause.stage ? <Txt variant="body-sm" weight="bold">{`${cause.stage} · `}</Txt> : null}
                {cause.message}
              </>
            ) : (
              m(`run.skip.${run.skip_reason!}`)
            )}
          </Txt>
        </View>
      ) : null}

      {stages.length > 0 ? (
        <View accessibilityLabel={t('run.stages')}>
          {stages.map((s, i) => {
            const last = i === stages.length - 1;
            return (
              <View key={s.id} className="flex-row gap-3">
                <View className="items-center">
                  <Glyph glyph={s.glyph} label={s.skipped ? m('run.stageNotRun') : t(`run.stage.${run.stages[i]!.status}`)} />
                  {!last ? (
                    <View
                      className={cn('w-0 flex-1 border-l-2', s.failed || s.skipped ? 'border-dashed border-line-strong' : 'border-line')}
                      style={{ minHeight: 14 }}
                    />
                  ) : null}
                </View>
                <View className={cn('min-w-0 flex-1', !last && 'pb-3.5')}>
                  <Txt
                    variant="body"
                    weight="medium"
                    className={s.failed ? 'text-danger' : s.skipped ? 'text-ink-muted' : undefined}
                  >
                    {s.name}
                  </Txt>
                  {s.after.length > 0 ? (
                    <Txt variant="caption" className="text-ink-muted">
                      {t('run.stageAfter', { names: s.after.join(t('run.stageAfterSep')) })}
                    </Txt>
                  ) : null}
                  {s.message ? (
                    <Txt variant="caption" className="text-ink-muted">
                      {s.message}
                    </Txt>
                  ) : null}
                </View>
                <Mono size="mono-sm">{s.durationMs !== null ? formatDuration(s.durationMs, { whole: s.glyph.icon === 'wave' }) : '—'}</Mono>
              </View>
            );
          })}
        </View>
      ) : null}

      {calls.toolCallCount > 0 ? (
        <View className="flex-row">
          <Chip
            icon={Wrench}
            label={t('run.toolCount', { count: calls.toolCallCount })}
            dot={calls.toolFailedCount ? 'bad' : undefined}
            accessibilityLabel={[
              t('run.toolCount', { count: calls.toolCallCount }),
              calls.toolFailedCount ? t('run.toolFailed', { count: calls.toolFailedCount }) : null,
            ]
              .filter(Boolean)
              .join(' · ')}
          />
        </View>
      ) : null}

      <LogTailView log={log} />

      {run.result ? <ResultCard title={t('result.title')} result={run.result} panels={panels} /> : null}

      {rerun ? (
        <Pressable
          onPress={() => openTarget({ kind: 'run', runId: rerun.id })}
          accessibilityRole="button"
          className="h-10 flex-row items-center gap-2 self-start rounded-full border border-line bg-liner px-3.5"
        >
          <RotateCcw size={16} color={colors['ink-muted']} strokeWidth={2.25} />
          <Txt variant="body-sm">{m('run.rerun')}</Txt>
          <Glyph glyph={runGlyph(rerun.status)} label={t(`run.status.${rerun.status}`)} size="sm" />
          <Mono size="mono-sm">{`${formatClock(rerun.started_at ?? rerun.queued_at)} · v${rerun.version}`}</Mono>
        </Pressable>
      ) : null}
    </>
  );
}
