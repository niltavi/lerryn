// The phone's read-only queries are shared with the web console and live in
// @lerryn/core. Their refetch intervals follow the live mode that
// LiveEventsProvider reports through core's LiveModeProvider.
export {
  useOverview,
  useProjects,
  useReruns,
  useRunEvents,
  useRunLive,
  useTaskCredentials,
  useTaskList,
  useTaskRuns,
  useTaskTriggers,
  useTodayCalendar,
} from '@lerryn/core';
