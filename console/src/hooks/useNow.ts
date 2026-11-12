import { useEffect, useState } from 'hono/jsx';

// useNow gives the clock and renders again every interval, so a text such as
// "3 s ago" stays right. It stops when active is false.
export function useNow(active: boolean, intervalMs = 1000): number {
  const [now, setNow] = useState(Date.now());

  useEffect(() => {
    if (!active) return;

    const timer = setInterval(() => setNow(Date.now()), intervalMs);

    return () => clearInterval(timer);
  }, [active, intervalMs]);

  return now;
}
