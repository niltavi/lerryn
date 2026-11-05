import { router } from 'expo-router';
import { Linking } from 'react-native';

import type { Target } from '../lib/view';

/**
 * Follows a row's target. Pages the phone does not offer (credential
 * filling, runner admin) open in the system browser, where the web console
 * handles them.
 */
export function openTarget(target: Target | null): void {
  if (!target) return;
  switch (target.kind) {
    case 'run':
      router.push({ pathname: '/runs/[runId]', params: { runId: target.runId } });
      return;
    case 'task':
      router.push({ pathname: '/tasks/[taskId]', params: { taskId: target.taskId } });
      return;
    case 'browser':
      if (/^https?:\/\//i.test(target.url)) void Linking.openURL(target.url);
  }
}
