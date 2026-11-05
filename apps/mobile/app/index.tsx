import { formatClock, formatDuration, useMe } from '@lerryn/core';
import { Logo, Mono, useLerrynTheme } from '@lerryn/ui-native';
import { router } from 'expo-router';
import { ChevronDown } from 'lucide-react-native';
import { Pressable, View } from 'react-native';

import { useStrings } from '../src/i18n/useStrings';
import {
  attentionView,
  dayStrip,
  runElapsed,
  runGlyph,
  sortTasksBySchedule,
  startOfLocalDay,
  taskGlyph,
  type AttentionView,
} from '../src/lib/view';
import { shortWhen } from '../src/lib/when';
import { usePrefs } from '../src/prefs/PrefsProvider';
import { useOverview, useProjects, useTaskList, useTodayCalendar } from '../src/queries/queries';
import { Glyph } from '../src/ui/Glyph';
import { openTarget } from '../src/ui/navigate';
import { Ridges } from '../src/ui/Ridges';
import { ListRow, Stat } from '../src/ui/Rows';
import { Screen, SectionLabel } from '../src/ui/Screen';
import { EmptyState, ErrorState, LoadingState } from '../src/ui/States';
import { Txt } from '../src/ui/Txt';
import { useNow } from '../src/ui/useNow';

/**
 * Overview (prototype M-Overview): what needs attention, what is running,
 * today's runs as a ridge strip, and the tasks with their next run. Each
 * section appears only when it has something to show.
 */
export default function OverviewScreen() {
  const { prefs } = usePrefs();
  const projectId = prefs.projectId;
  const now = useNow(1_000);
  const overview = useOverview(projectId);
  const calendar = useTodayCalendar(projectId, now);
  const tasks = useTaskList(projectId);

  const refresh = () => Promise.all([overview.refetch(), calendar.refetch(), tasks.refetch()]);

  return (
    <Screen header={<OverviewHeader />} onRefresh={refresh}>
      {overview.isPending ? (
        <LoadingState />
      ) : overview.isError && !overview.data ? (
        <ErrorState error={overview.error} onRetry={() => void overview.refetch()} />
      ) : overview.data ? (
        <Board overview={overview.data} now={now} calendar={calendar.data?.items} tasks={tasks} />
      ) : null}
    </Screen>
  );
}

function OverviewHeader() {
  const { m } = useStrings();
  const { colors } = useLerrynTheme();
  const { prefs } = usePrefs();
  const me = useMe();
  const projects = useProjects();
  const items = projects.data?.items ?? [];
  const current = items.find((p) => p.id === prefs.projectId);
  const name = current?.name ?? m('overview.allProjects');
  const initial = (me.data?.display_name || me.data?.username || '').slice(0, 1).toUpperCase();

  return (
    <>
      <Logo variant="square" size={36} />
      {items.length > 1 ? (
        <Pressable
          onPress={() => router.push('/projects')}
          accessibilityRole="button"
          accessibilityLabel={m('overview.switchProject', { name })}
          className="h-9 min-w-0 flex-shrink flex-row items-center gap-1.5 rounded-full border border-line bg-liner px-3"
        >
          <Txt variant="body-sm" numberOfLines={1} className="min-w-0 flex-shrink">
            {name}
          </Txt>
          <ChevronDown size={16} color={colors['ink-muted']} strokeWidth={2.25} />
        </Pressable>
      ) : (
        <View className="flex-1" />
      )}
      <Pressable
        onPress={() => router.push('/account')}
        accessibilityRole="button"
        accessibilityLabel={me.data?.display_name || me.data?.username || ''}
        className="h-9 w-9 items-center justify-center rounded-full bg-kraft-soft"
      >
        <Txt variant="body-sm" weight="semibold" className="text-kraft-strong">
          {initial}
        </Txt>
      </Pressable>
    </>
  );
}

function Board({
  overview,
  now,
  calendar,
  tasks,
}: {
  overview: NonNullable<ReturnType<typeof useOverview>['data']>;
  now: number;
  calendar: NonNullable<ReturnType<typeof useTodayCalendar>['data']>['items'] | undefined;
  tasks: ReturnType<typeof useTaskList>;
}) {
  const { t, m, locale } = useStrings();
  const attention = overview.attention.map(attentionView);
  const next = overview.upcoming[0];
  const running = overview.counts.running + overview.counts.queued;

  if (overview.counts.tasks === 0) return <EmptyState text={m('overview.noTasks')} />;

  return (
    <>
      <View className="flex-row flex-wrap items-end gap-x-9 gap-y-3">
        <Stat value={String(overview.counts.attention)} label={t('overview.attention')} tone={overview.counts.attention > 0 ? 'bad' : undefined} size="lg" />
        <Stat value={String(running)} label={t('overview.running')} tone={running > 0 ? 'run' : undefined} />
        {next ? <Stat value={shortWhen(next.at, locale, now)} label={m('overview.next')} /> : null}
      </View>

      {calendar && calendar.length > 0 ? <TodayStrip items={calendar} now={now} /> : null}

      {attention.length > 0 ? (
        <View className="gap-2" accessibilityLabel={t('overview.attention')}>
          {attention.map((a) => (
            <AttentionCard key={a.key} item={a} now={now} />
          ))}
        </View>
      ) : null}

      {overview.active.length > 0 ? (
        <View className="gap-2">
          <SectionLabel>{t('overview.running')}</SectionLabel>
          <View>
            {overview.active.map((r, i) => (
              <ListRow
                key={r.run_id}
                first={i === 0}
                glyph={<Glyph glyph={runGlyph(r.status)} label={t(`run.status.${r.status}`)} />}
                title={r.task.name}
                detail={
                  r.current_stage ? (
                    <Txt variant="caption" numberOfLines={1} className="text-ink-muted">
                      {r.current_stage}
                    </Txt>
                  ) : undefined
                }
                trailing={formatDuration(runElapsed(r, now), { whole: true })}
                trailingTone="number"
                onPress={() => openTarget({ kind: 'run', runId: r.run_id })}
              />
            ))}
          </View>
        </View>
      ) : null}

      <TaskSection tasks={tasks} now={now} />
    </>
  );
}

function AttentionCard({ item, now }: { item: AttentionView; now: number }) {
  const { t, locale } = useStrings();
  const label = attentionLabel(item, t);
  return (
    <Pressable
      onPress={() => openTarget(item.target)}
      disabled={!item.target}
      accessibilityRole={item.target?.kind === 'browser' ? 'link' : 'button'}
      className="flex-row items-center gap-3 rounded-3xl border border-line bg-liner px-3.5 py-3 shadow-flute-b active:opacity-80"
    >
      <Glyph glyph={item.glyph} label={label} size="lg" />
      <View className="min-w-0 flex-1">
        {item.titleMono ? (
          <Mono numberOfLines={1} className="text-ink">
            {item.title}
          </Mono>
        ) : (
          <Txt variant="body" weight="semibold" numberOfLines={1}>
            {item.title}
          </Txt>
        )}
        {item.subtitle ? (
          <Txt variant="body-sm" numberOfLines={2} className="text-ink-muted">
            {item.subtitle}
          </Txt>
        ) : null}
      </View>
      <Mono size="mono-sm">{shortWhen(item.at, locale, now)}</Mono>
    </Pressable>
  );
}

function attentionLabel(item: AttentionView, t: ReturnType<typeof useStrings>['t']): string {
  switch (item.kind) {
    case 'run_failed':
      return t('run.status.failed');
    case 'run_timed_out':
      return t('run.status.timed_out');
    case 'run_interrupted':
      return t('run.status.interrupted');
    case 'runner_offline':
      return t('admin.runners');
    default:
      return t('credentials.title');
  }
}

function TodayStrip({ items, now }: { items: NonNullable<Parameters<typeof dayStrip>[0]>; now: number }) {
  const { m } = useStrings();
  const dayStart = startOfLocalDay(now);
  const buckets = dayStrip(items, dayStart);
  const tallest = Math.max(1, ...buckets.map((b) => b.runs + b.planned));
  return (
    <View accessibilityLabel={m('overview.todayRuns')}>
      <Ridges
        now={(now - dayStart) / 86_400_000}
        axis={['00', '06', '12', '18', '24']}
        ridges={buckets.map((b) => ({
          key: String(b.start),
          tone: b.tone,
          height: b.tone === 'empty' ? 0.08 : 0.2 + (0.8 * (b.runs + b.planned)) / tallest,
          // Planned fires are named with the server's wording of each schedule.
          label: [
            m('overview.bucket', { time: formatClock(b.start), runs: b.runs, planned: b.planned }),
            ...b.plans.map((p) => (p.scheduleText ? `${p.taskName} · ${p.scheduleText}` : p.taskName)),
          ].join('\n'),
          onPress: b.runId ? () => openTarget({ kind: 'run', runId: b.runId! }) : undefined,
        }))}
      />
    </View>
  );
}

function TaskSection({ tasks, now }: { tasks: ReturnType<typeof useTaskList>; now: number }) {
  const { t, m, locale } = useStrings();
  if (tasks.isPending) return null;
  if (tasks.isError && !tasks.data) {
    return (
      <View className="gap-2">
        <SectionLabel>{m('overview.tasks')}</SectionLabel>
        <ErrorState error={tasks.error} onRetry={() => void tasks.refetch()} />
      </View>
    );
  }
  const items = sortTasksBySchedule(tasks.data?.items ?? []);
  if (items.length === 0) return null;
  return (
    <View className="gap-2">
      <SectionLabel>{m('overview.tasks')}</SectionLabel>
      <View>
        {items.map((task, i) => {
          const glyph = taskGlyph(task);
          const label =
            task.state === 'inactive'
              ? m('task.paused')
              : task.last_run
                ? t(`run.status.${task.last_run.status}`)
                : t('states.awaitingFirstRun');
          return (
            <ListRow
              key={task.id}
              first={i === 0}
              glyph={<Glyph glyph={glyph} label={label} />}
              title={task.name}
              titleMuted={task.state === 'inactive'}
              trailing={task.next_run_at ? shortWhen(task.next_run_at, locale, now) : '—'}
              onPress={() => openTarget({ kind: 'task', taskId: task.id })}
            />
          );
        })}
      </View>
    </View>
  );
}
