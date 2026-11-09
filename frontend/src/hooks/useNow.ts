import { useEffect, useState } from 'react';

/** Current time, updated every ms milliseconds. Use only in small components that need "N seconds ago", so the whole page does not re-render every second. */
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(id);
  }, [ms]);
  return now;
}
