import { useState } from 'react';

/**
 * After an inline rename is saved, flash the name's background once (.flash-once): Enter / blur saves and the input just closes, so without this there is no way to tell whether it saved.
 * Usage: onDone={(saved) => { ...; if (saved) flash.mark(id); }}, and give the name element key={flash.key(id)} and add flash.cls(id) to its className.
 */
export function useSavedFlash() {
  const [last, setLast] = useState<{ id: string; n: number } | null>(null);
  return {
    mark: (id: string) => setLast((x) => ({ id, n: (x?.n ?? 0) + 1 })),
    key: (id: string) => (last?.id === id ? last.n : 0),
    // 4px of extra background on each side, cancelled by negative margins: the flash is a bit wider than the text and the layout does not move
    cls: (id: string) => (last?.id === id ? 'flash-once -mx-1 rounded px-1' : ''),
  };
}
