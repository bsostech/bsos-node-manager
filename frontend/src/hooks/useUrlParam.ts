import { useSearchParams } from 'react-router-dom';

/**
 * A URL param as state: the param is removed at the default value to keep the URL clean; replace avoids filling history.
 * validate receives the raw string and the value falls back to def when it returns false; def itself need not pass validate
 * (some callers' allowlists list only non-default values). A raw value that passes validation is treated as T directly.
 */
export function useUrlParam<T extends string>(key: string, def: T, validate?: (raw: string) => boolean): [T, (v: T) => void] {
  const [sp, setSp] = useSearchParams();
  const raw = sp.get(key);
  const value = raw != null && (!validate || validate(raw)) ? (raw as T) : def;
  // Always use the functional form so several params set on the same page do not overwrite each other (prev is the latest URL, not the snapshot from render time).
  const set = (v: T) =>
    setSp((prev) => {
      const p = new URLSearchParams(prev);
      v === def ? p.delete(key) : p.set(key, v);
      return p;
    }, { replace: true });
  return [value, set];
}
