import { formatClock, formatDate, formatDuration, isRunActive, taskStatus, useTask, useTaskList } from '@lerryn/core';
import { useLerrynTheme } from '@lerryn/ui-native';
import * as Clipboard from 'expo-clipboard';
import { useLocalSearchParams } from 'expo-router';
import { CalendarClock, Copy, GitCommitHorizontal, KeyRound } from 'lucide-react-native';
import { View } from 'react-native';

import { useStrings } from '../../src/i18n/useStrings';
import {
  credentialSummary,
  nextCron,
  runDuration,
  runGlyph,
  runOrigin,
  runRidges,
  type RunSummary,
} from '../../src/lib/view';
import { shortWhen } from '../../src/lib/when';
import { useRunLive, useTaskCredentials, useTaskRuns, useTaskTriggers } from '../../src/queries/queries';
import { Chip } from '../../src/ui/Chip';
import { Glyph } from '../../src/ui/Glyph';
import { openTarget } from '../../src/ui/navigate';
import { ResultCard } from '../../src/ui/ResultCard';
import { Ridges } from '../../src/ui/Ridges';
import { ListRow, Stat } from '../../src/ui/Rows';
import { BackLink, IconAction, Screen, SectionLabel } from '../../src/ui/Screen';
import { ErrorState, LoadingState } from '../../src/ui/States';
import { Txt } from '../../src/ui/Txt';
import { useNow } from '../../src/ui/useNow';

/** Task detail (prototype M-Task): schedule, recent runs as ridges, the last reported result. */
export default function TaskScreen() {
  const { taskId } = useLocalSearchParams<{ taskId: string }>();
  const { t } = useStrings();
  const { colors } = useLerrynTheme();
  const task = useTask(taskId);
  const runs = useTaskRuns(taskId);
  const triggers = useTaskTriggers(taskId);
  const credentials = useTaskCredentials(taskId);
  const now = useNow(1_000);

  const refresh = () => Promise.all([task.refetch(), runs.refetch(), triggers.refetch(), credentials.refetch()]);
  const link = task.data?.links.console;

  return (
    <Screen
      header={
        <>
          <BackLink label={t('overview.title')} />
          {link ? (
            <IconAction label={t('task.copyLink')} onPress={() => void Clipboard.setStringAsync(link)}>
              <Copy size={18} color={colors.ink} strokeWidth={2.25} />
            </IconAction>
          ) : null}
        </>
      }
      onRefresh={refresh}
    >
      {task.isPending ? (
        <LoadingState />
      ) : task.isError ? (
        <ErrorState error={task.error} onRetry={() => void task.refetch()} />
      ) : (
        <TaskBody
          task={task.data}
          runs={runs}
          triggers={triggers.data?.triggers ?? []}
          credentials={credentials.data?.items}
          now={now}
        />
      )}
    </Screen>
  );
}

function TaskBody({
  task,
  runs,
  triggers,
  credentials,
  now,
}: {
  task: NonNullable<ReturnType<typeof useTask>['data']>;
  runs: ReturnType<typeof useTaskRuns>;
  triggers: NonNullable<ReturnType<typeof useTaskTriggers>['data']>['triggers'];
  credentials: NonNullable<ReturnType<typeof useTaskCredentials>['data']>['items'] | undefined;
  now: number;
}) {
  const { t, m, locale } = useStrings();
  const cron = nextCron(triggers);
  const items = runs.data?.items ?? [];
  const summary = useTaskList(null).data?.items.find((item) => item.id === task.id);
  const status = taskStatus({
    ...task, active_runs: summary?.active_runs,
    last_run: items.find((run) => isRunActive(run.status)), next_run_at: cron?.next_fire_at,
  });
  const lastWithResult = items.find((r) => !r.test && r.result_status !== null);
  const creds = credentials ? credentialSummary(credentials, now) : null;
  const version = task.active_version ?? task.latest_version.seq;
  const panels = task.latest_version.manifest.panels;
  const ridges = runRidges(items, now);

  return (
    <>
      <View className="gap-1.5">
        <View className="flex-row items-center gap-2">
          <Glyph glyph={status.glyph} label={t(status.label)} />
          <Txt variant="caption" weight="semibold" className="text-ink-muted">
            {t(status.label)}
          </Txt>
        </View>
        <Txt variant="title-2" weight="bold" accessibilityRole="header">
          {task.name}
        </Txt>
        {task.description ? (
          <Txt variant="body-sm" className="text-ink-muted">
            {task.description}
          </Txt>
        ) : null}
      </View>

      {cron?.next_fire_at || items.length > 0 ? (
        <View className="flex-row flex-wrap items-end gap-x-8 gap-y-3">
          {cron?.next_fire_at ? <Stat value={shortWhen(cron.next_fire_at, locale, now)} label={m('task.nextRun')} /> : null}
          {items.length > 0 ? (
            <Stat value={`${items.length}${runs.data?.next_cursor ? '+' : ''}`} label={m('task.runCount')} />
          ) : null}
        </View>
      ) : null}

      {ridges.length > 1 ? (
        <Ridges
          fixedWidth={ridges.length < 20}
          axis={[formatDate(ridges[0]!.at, locale, now), formatDate(ridges[ridges.length - 1]!.at, locale, now)]}
          ridges={ridges.map((r) => ({
            key: r.key,
            tone: r.tone,
            height: r.height,
            label: `${shortWhen(r.at, locale, now)} ${t(`run.status.${r.status as RunSummary['status']}`)}${
              r.durationMs !== null ? ` ${formatDuration(r.durationMs)}` : ''
            }`,
            onPress: () => openTarget({ kind: 'run', runId: r.runId }),
          }))}
        />
      ) : null}

      <View className="flex-row flex-wrap gap-2">
        {cron?.cron ? <Chip icon={CalendarClock} label={cron.cron.schedule_text} /> : null}
        {creds && creds.total > 0 ? (
          <Chip
            icon={KeyRound}
            label={m('task.credentials', { count: creds.total })}
            dot={creds.broken > 0 ? 'bad' : creds.expiring > 0 ? 'warn' : undefined}
            accessibilityLabel={
              creds.broken + creds.expiring > 0
                ? `${m('task.credentials', { count: creds.total })}, ${m('task.credentialsAttention')}`
                : undefined
            }
          />
        ) : null}
        <Chip icon={GitCommitHorizontal} label={`v${version}`} />
      </View>

      {lastWithResult ? (
        <LastResult runId={lastWithResult.id} summary={lastWithResult} panels={panels} />
      ) : null}

      {runs.isError && !runs.data ? (
        <ErrorState error={runs.error} onRetry={() => void runs.refetch()} />
      ) : items.length > 0 ? (
        <View className="gap-2">
          <SectionLabel>{m('task.recentRuns')}</SectionLabel>
          <View>
            {items.slice(0, 10).map((r, i) => {
              const duration = runDuration(r, now);
              const bad = r.status === 'failed' || r.status === 'timed_out';
              return (
                <ListRow
                  key={r.id}
                  first={i === 0}
                  glyph={<Glyph glyph={runGlyph(r.status)} label={t(`run.status.${r.status}`)} />}
                  title={`${m(`task.origin.${runOrigin(r)}`)} · v${r.version}`}
                  trailing={`${shortWhen(r.started_at ?? r.queued_at, locale, now)}${
                    duration !== null ? ` · ${formatDuration(duration, { whole: r.finished_at === null })}` : ''
                  }`}
                  trailingTone={bad ? 'bad' : 'muted'}
                  onPress={() => openTarget({ kind: 'run', runId: r.id })}
                />
              );
            })}
          </View>
        </View>
      ) : runs.isSuccess ? (
        <Txt variant="body-sm" className="text-ink-muted">
          {t('states.noRuns')}
        </Txt>
      ) : null}
    </>
  );
}

/**
 * The list item carries only the summary; the result data the panels read
 * lives on the run itself, so it is fetched through the run query.
 */
function LastResult({
  runId,
  summary,
  panels,
}: {
  runId: string;
  summary: RunSummary;
  panels: NonNullable<ReturnType<typeof useTask>['data']>['latest_version']['manifest']['panels'];
}) {
  const { m } = useStrings();
  const run = useRunResult(runId);
  const result = run ?? (summary.result_status ? { status: summary.result_status, summary: summary.result_summary ?? '', data: null } : null);
  if (!result) return null;
  const at = summary.finished_at ?? summary.started_at ?? summary.queued_at;
  return <ResultCard title={m('task.lastResult')} result={result} panels={panels} time={formatClock(at)} />;
}

function useRunResult(runId: string) {
  return useRunLive(runId).data?.result ?? null;
}
