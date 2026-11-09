import { useEffect, useRef, useState } from 'react';

/**
 * Per-digit animation: only the digits that changed slide in (compared right-aligned; on carry, the left digits that change move too);
 * the animation is .num-tick in index.css.
 * Use it for "a single changing number" like one card or one reading: block height, TPS, the current value at a chart's top right,
 * node detail summary cards. Don't use it in tables; every cell animating is too noisy.
 */
export function AnimatedNumber({ value, className = '' }: { value: string; className?: string }) {
  const prev = useRef(value);
  const [ver, setVer] = useState<number[]>(() => Array.from(value, () => 0));
  useEffect(() => {
    if (prev.current === value) return;
    const a = prev.current;
    const b = value;
    prev.current = b;
    setVer((old) => Array.from(b, (ch, i) => {
      const ai = a.length - (b.length - i); // right-aligned: compare the same digit position
      const oi = old.length - (b.length - i);
      const was = oi >= 0 ? (old[oi] ?? 0) : 0;
      return ai < 0 || a[ai] !== ch ? was + 1 : was;
    }));
  }, [value]);
  return (
    <span className={`inline-flex ${className}`}>
      {Array.from(value, (ch, i) => (
        <span key={`${i}-${ver[i] ?? 0}`} className={`inline-block ${(ver[i] ?? 0) > 0 ? 'num-tick' : ''}`}>{ch}</span>
      ))}
    </span>
  );
}
