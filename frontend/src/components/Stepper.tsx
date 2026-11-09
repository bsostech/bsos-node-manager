import { useTranslation } from 'react-i18next';
import { Check } from '@phosphor-icons/react';

/**
 * Wizard progress bar for the add-node flow: completed steps get a check, the current step is accented, future steps are plain.
 *
 * Accessibility: <ol>/<li> lets screen readers get "how many steps, which one now", with aria-current="step" on the current step;
 * icons are always aria-hidden (they are a visual encoding of state), and completed steps get sr-only text -- otherwise with visuals off
 * the steps read as indistinguishable pieces of text and the "progress" information is lost entirely.
 */
export function Stepper({ step, labels }: { step: number; labels: string[] }) {
  const { t } = useTranslation();
  return (
    <ol className="flex flex-wrap items-center gap-2">
      {labels.map((label, i) => {
        const n = i + 1;
        const active = n === step;
        const done = n < step;
        return (
          <li key={label} className="flex min-w-0 items-center gap-2" aria-current={active ? 'step' : undefined}>
            <span
              aria-hidden
              className={`inline-flex size-5 shrink-0 items-center justify-center rounded-full border text-xs font-medium tabular-nums ${
                active ? 'border-accent bg-accent text-white' : done ? 'border-accent text-accent' : 'border-hair text-ink-3'
              }`}
            >
              {done ? <Check size={12} /> : n}
            </span>
            <span className={`truncate text-xs ${active ? 'font-medium text-ink' : done ? 'text-ink-2' : 'text-ink-3'}`}>
              {label}
              {done && <span className="sr-only"> — {t('common.stepDone')}</span>}
            </span>
            {n < labels.length && <span aria-hidden className="h-px w-4 shrink-0 bg-hair" />}
          </li>
        );
      })}
    </ol>
  );
}
