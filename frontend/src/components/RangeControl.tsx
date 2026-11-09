import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import * as Popover from '@radix-ui/react-popover';
import { CaretDown, X } from '@phosphor-icons/react';
import { Tooltip } from '@/components/Tooltip';
import { Button } from '@/components/Button';
import { Listbox } from '@/components/Listbox';
import { RANGE_PRESETS, type Preset, absLabel, durToMs, toLocalInput, fromLocalInput } from '@/lib/range';

/**
 * Time range control: 5 preset segments + custom (relative "last N min/hours/days" / absolute from-to) + a label for the actual current window.
 * An absolute window can be entered here by hand, or produced by drag-zoom on a chart (both frozen, not live).
 * The window label is always shown (in relative mode too, to fix "in live mode you cannot tell which stretch you are looking at");
 * only an absolute window produced by drag-zoom is a clickable chip that clears it; in relative mode it is purely informational and not interactive.
 * Quiet segmented: selection is carried by background + shadow, no fill color.
 */
export function RangeControl({
  rel, abs, window, onPickRel, onSetAbs, onClearAbs, variant = 'segments',
}: {
  /** segments = segmented buttons (default); dropdown = a "Range" label + dropdown, with custom reached from the options */
  variant?: 'segments' | 'dropdown';
  rel: string;                                   // current relative duration ('1h' or custom such as '3d')
  abs: { from: number; to: number } | null;      // absolute window; non-null = frozen
  window: [number, number];                       // actual current window (epoch seconds; computed by the parent for both relative and absolute modes)
  onPickRel: (dur: string) => void;              // pick a preset/custom relative -> clears abs
  onSetAbs: (from: number, to: number) => void;  // manual from-to entry -> sets the absolute window (epoch seconds)
  onClearAbs: () => void;                         // clear abs -> back to relative
}) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<'rel' | 'abs'>('rel');
  const [num, setNum] = useState('3');
  const [unit, setUnit] = useState<'m' | 'h' | 'd'>('d');
  const [from, setFrom] = useState('');
  const [to, setTo] = useState('');
  const [absErr, setAbsErr] = useState(false);

  const customActive = !abs && !RANGE_PRESETS.includes(rel as Preset);
  const seg = (active: boolean) =>
    `rounded px-2.5 py-1 font-mono text-xs transition-colors ${active ? 'bg-surface font-medium text-ink' : 'text-ink-3 hover:text-ink-2'}`;

  // Open the popup: prefill from-to (reuse the current absolute window; otherwise derive it from the current relative window), and pick the tab based on the current mode.
  const onOpenChange = (next: boolean) => {
    if (next) {
      const now = Math.floor(Date.now() / 1000);
      const f = abs ? abs.from : now - Math.round(durToMs(rel) / 1000);
      const tt = abs ? abs.to : now;
      setFrom(toLocalInput(f));
      setTo(toLocalInput(tt));
      setTab(abs ? 'abs' : 'rel');
      setAbsErr(false);
    }
    setOpen(next);
  };

  const apply = () => {
    const n = Math.max(1, Math.floor(Number(num) || 0));
    onPickRel(`${n}${unit}`);
    setOpen(false);
  };

  const applyAbs = () => {
    const f = fromLocalInput(from);
    const tt = fromLocalInput(to);
    if (f == null || tt == null || f >= tt) { setAbsErr(true); return; }
    onSetAbs(f, tt);
    setOpen(false);
  };

  return (
    <div className="flex items-center gap-2">
      <Popover.Root open={open} onOpenChange={onOpenChange}>
        {variant === 'dropdown' ? (
          // Dropdown: preset ranges + the current custom value (if any) + "Custom…"; choosing custom opens the same popup
          <Popover.Anchor asChild>
            <label className="flex items-center gap-2 text-xs text-ink-3">
              {t('overview.rangeLabel')}
              <Listbox
                width="w-auto"
                aria-label={t('overview.rangeLabel')}
                value={abs ? '__abs' : customActive ? '__rel' : rel}
                options={[
                  ...RANGE_PRESETS.map((r) => ({ value: r as string, label: t(`range.${r}`) })),
                  ...(abs ? [{ value: '__abs', label: absLabel(window[0], window[1]) }] : []),
                  ...(customActive ? [{ value: '__rel', label: t('range.lastRel', { rel }) }] : []),
                  { value: '__custom', label: `${t('detail.rangeCustom')}…` },
                ]}
                onChange={(v) => {
                  // "Custom…": open the popup only after the dropdown has fully closed and focus has returned, or the pointer event from the dropdown closing is taken as an "outside click" and closes the popup immediately
                  if (v === '__custom') setTimeout(() => onOpenChange(true), 60);
                  else if (v !== '__abs' && v !== '__rel') onPickRel(v);
                }}
              />
            </label>
          </Popover.Anchor>
        ) : (
          <div className="inline-flex gap-0.5 rounded-md border border-hair bg-elevated p-0.5">
            {RANGE_PRESETS.map((r) => (
              <button key={r} aria-pressed={!abs && rel === r} onClick={() => onPickRel(r)} className={seg(!abs && rel === r)}>{r}</button>
            ))}
            {/* Custom (relative N window / absolute from-to) */}
            <Popover.Trigger asChild>
              <button aria-pressed={customActive} className={`inline-flex items-center gap-1 ${seg(customActive)}`}>
                {customActive ? rel : t('detail.rangeCustom')}
                <CaretDown size={11} weight="bold" aria-hidden="true" />
              </button>
            </Popover.Trigger>
          </div>
        )}
          <Popover.Portal>
            <Popover.Content align="end" sideOffset={4} collisionPadding={8} className="pop-in z-50 flex w-60 flex-col gap-2.5 rounded-md border border-hair bg-surface p-3 shadow-lg focus:outline-none">
              {/* Relative / absolute tabs */}
              <div className="inline-flex gap-0.5 self-start rounded-md border border-hair bg-elevated p-0.5">
                <button aria-pressed={tab === 'rel'} onClick={() => setTab('rel')} className={seg(tab === 'rel')}>{t('detail.rangeRelative')}</button>
                <button aria-pressed={tab === 'abs'} onClick={() => setTab('abs')} className={seg(tab === 'abs')}>{t('detail.rangeAbsolute')}</button>
              </div>

              {tab === 'rel' ? (
                <>
                  <span className="text-xs text-ink-3">{t('detail.rangeLast')}</span>
                  <div className="flex items-center gap-2">
                    <input
                      type="number" min={1} value={num}
                      aria-label={t('detail.rangeLast')}
                      onChange={(e) => setNum(e.target.value)}
                      onKeyDown={(e) => e.key === 'Enter' && apply()}
                      className="w-16 rounded-md border border-hair bg-canvas px-2 py-1 text-right font-mono text-xs tabular-nums text-ink focus:border-accent focus:outline-none"
                    />
                    <div className="inline-flex gap-0.5 rounded-md border border-hair bg-elevated p-0.5">
                      {(['m', 'h', 'd'] as const).map((u) => (
                        <button key={u} aria-pressed={unit === u} onClick={() => setUnit(u)} className={seg(unit === u)}>{u}</button>
                      ))}
                    </div>
                  </div>
                  <Button variant="primary" onClick={apply}>
                    {t('common.apply')}
                  </Button>
                </>
              ) : (
                <>
                  <label className="flex flex-col gap-1 text-xs text-ink-3">
                    {t('detail.rangeFrom')}
                    <input
                      type="datetime-local" value={from}
                      onChange={(e) => { setFrom(e.target.value); setAbsErr(false); }}
                      className="rounded-md border border-hair bg-canvas px-2 py-1 font-mono text-xs tabular-nums text-ink focus:border-accent focus:outline-none"
                    />
                  </label>
                  <label className="flex flex-col gap-1 text-xs text-ink-3">
                    {t('detail.rangeTo')}
                    <input
                      type="datetime-local" value={to}
                      onChange={(e) => { setTo(e.target.value); setAbsErr(false); }}
                      className="rounded-md border border-hair bg-canvas px-2 py-1 font-mono text-xs tabular-nums text-ink focus:border-accent focus:outline-none"
                    />
                  </label>
                  {absErr && (
                    <span role="alert" className="text-xs" style={{ color: 'var(--bsos-status-error-fg)' }}>{t('detail.rangeAbsInvalid')}</span>
                  )}
                  <Button variant="primary" onClick={applyAbs}>
                    {t('common.apply')}
                  </Button>
                </>
              )}
            </Popover.Content>
        </Popover.Portal>
      </Popover.Root>

      {/* Actual current window: absolute window (drag-zoom) = a clickable chip that clears it; relative mode = informational mono text only (not repeated in dropdown mode). */}
      {abs ? (
        <Tooltip label={t('detail.rangeClearAbs')}>
          <button
            onClick={onClearAbs}
            aria-label={t('detail.rangeClearAbs')}
            className="inline-flex items-center gap-1.5 rounded-md border border-accent px-2.5 py-1 font-mono text-xs text-ink transition-colors hover:bg-elevated"
          >
            {absLabel(window[0], window[1])}
            <X size={12} weight="bold" className="text-ink-3" />
          </button>
        </Tooltip>
      ) : variant === 'dropdown' ? null : (
        <span className="font-mono text-xs tabular-nums text-ink-3">{absLabel(window[0], window[1])}</span>
      )}
    </div>
  );
}
