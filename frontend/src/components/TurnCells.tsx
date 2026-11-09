import { useTranslation } from 'react-i18next';
import type { ValidatorTurn } from '@/api/schemas';
import { Tooltip } from '@/components/Tooltip';
import { isProducing } from '@/lib/validators';
import { useChainHalted } from '@/hooks/useChainHalted';

/**
 * Turn cells: each cell is one turn of this validator to produce a block; green = produced, red = its turn came and it did not produce (moved on to the next). No separate number; read the cells.
 * size=large is the big cells on the validators page.
 */
export function TurnCells({ turns, size = 'small' }: { turns: ValidatorTurn[]; size?: 'small' | 'large' }) {
  const { t } = useTranslation();
  const missed = turns.filter((x) => !x.proposed).length;
  const cell = size === 'large' ? 'h-7 w-3' : 'h-5 w-[7px]';
  return (
    <div role="img" aria-label={t('validators.turnsLabel', { n: turns.length, missed })} className={`flex flex-wrap ${size === 'large' ? 'gap-1' : 'gap-0.5'}`}>
      {turns.map((x) => (
        <Tooltip key={x.block} label={<span className="font-mono">#{x.block}</span>}>
          <span
            className={`${cell} shrink-0 rounded-sm`}
            style={{ background: x.proposed ? 'var(--bsos-status-healthy-fg)' : 'var(--bsos-status-syncing-fg)' }}
          />
        </Tooltip>
      ))}
    </div>
  );
}

/**
 * Production status: dot + text; only counts as stopped producing (red text) if it missed its last few turns, or the backend judges it not producing (our node dropped, or on a halt the external org lost contact with all of ours),
 * nothing shown without records. When the chain halts nobody is producing: anything not confirmed as faulty is gray "Consensus stalled", never "Producing".
 */
export function ProducingLabel({ turns, producing }: { turns: ValidatorTurn[]; producing?: boolean | null }) {
  const { t } = useTranslation();
  const halted = useChainHalted();
  const p = producing === false ? false : isProducing(turns);
  if (halted && p !== false) return <span className="inline-flex items-center gap-1.5 text-xs text-ink-3"><span className="size-1.5 rounded-full bg-ink-3" />{t('validators.chainHalted')}</span>;
  if (p === null) return <span className="text-xs text-ink-3">—</span>;
  const c = p ? 'var(--bsos-status-healthy-fg)' : 'var(--bsos-status-error-fg)';
  return (
    <span className={`inline-flex items-center gap-1.5 text-xs ${p ? 'text-ink-2' : ''}`} style={p ? undefined : { color: c }}>
      <span className="size-1.5 rounded-full" style={{ background: c }} />{t(p ? 'validators.producing' : 'validators.notProducing')}
    </span>
  );
}
