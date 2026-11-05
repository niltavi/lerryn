import { useEffect, useState } from 'react';

/** The current time, updated every `ms` while `ticking`, for live elapsed times. */
export function useNow(ms = 1_000, ticking = true): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!ticking) return;
    setNow(Date.now());
    const id = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(id);
  }, [ms, ticking]);
  return now;
}
