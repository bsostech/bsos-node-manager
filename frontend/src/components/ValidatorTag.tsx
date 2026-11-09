import { useTranslation } from 'react-i18next';
import { Pill } from '@/components/Pill';
import { BallotStamp } from '@/components/icons/BallotStamp';

/** Validator tag: one and the same site-wide -- blue-gray pill + ballot stamp. My nodes, on-chain nodes, node detail and node settings all use this; do not draw another. */
export function ValidatorTag({ className = '' }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <Pill tone="slate" className={`gap-1 ${className}`}>
      <BallotStamp size={11} />
      {t('common.validator')}
    </Pill>
  );
}
