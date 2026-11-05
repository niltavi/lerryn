import { cn, useLerrynTheme, WaveLoader, type LerrynColor } from '@lerryn/ui-native';
import {
  Ban,
  CalendarClock,
  Check,
  Circle,
  Clock,
  KeyRound,
  Pause,
  ServerOff,
  SkipForward,
  Timer,
  Unplug,
  X,
  type LucideIcon,
} from 'lucide-react-native';
import { View } from 'react-native';

import type { Glyph as GlyphSpec, GlyphIcon, Tone } from '../lib/view';

const TONE: Record<Tone, { bg: string; color: LerrynColor }> = {
  idle: { bg: 'bg-liner-2', color: 'ink-muted' },
  run: { bg: 'bg-run-soft', color: 'run' },
  ok: { bg: 'bg-success-soft', color: 'success' },
  bad: { bg: 'bg-danger-soft', color: 'danger' },
  warn: { bg: 'bg-warning-soft', color: 'warning-ink' },
  key: { bg: 'bg-kraft-soft', color: 'kraft-strong' },
};

const ICON: Record<Exclude<GlyphIcon, 'wave'>, LucideIcon> = {
  clock: Clock,
  check: Check,
  x: X,
  timer: Timer,
  ban: Ban,
  unplug: Unplug,
  skip: SkipForward,
  key: KeyRound,
  server: ServerOff,
  calendar: CalendarClock,
  pause: Pause,
  pending: Circle,
};

export function toneColor(tone: Tone): LerrynColor {
  return TONE[tone].color;
}

/**
 * A status mark: a soft tinted square with an icon, or the flowing wave for
 * work in progress. The prototype's list rows use this without a word next
 * to it, so the label always goes to the screen reader.
 */
export function Glyph({
  glyph,
  label,
  size = 'md',
  className,
}: {
  glyph: GlyphSpec;
  label: string;
  size?: 'sm' | 'md' | 'lg';
  className?: string;
}) {
  const { colors } = useLerrynTheme();
  const spec = TONE[glyph.tone];
  const color = colors[spec.color];
  const box = size === 'lg' ? 40 : size === 'md' ? 22 : 18;
  const icon = size === 'lg' ? 20 : size === 'md' ? 13 : 11;
  const Icon = glyph.icon === 'wave' ? null : ICON[glyph.icon];
  return (
    <View
      accessible
      accessibilityRole="image"
      accessibilityLabel={label}
      className={cn('items-center justify-center', size === 'lg' ? 'rounded-xl' : 'rounded-md', spec.bg, className)}
      style={{ width: box, height: box }}
    >
      {Icon ? (
        <Icon size={icon} color={color} strokeWidth={2.5} />
      ) : (
        <WaveLoader size="sm" color={color} accessibilityLabel={label} />
      )}
    </View>
  );
}
