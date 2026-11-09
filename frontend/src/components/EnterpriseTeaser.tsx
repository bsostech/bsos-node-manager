import { useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Lock, Sparkle } from '@phosphor-icons/react';
import { ENTERPRISE_FEATURES, type EnterpriseFeature } from '@/lib/enterpriseFeatures';
import { ENTERPRISE_INFO_PATH } from '@/pages/EnterpriseInfo';
import { Button } from '@/components/Button';
import { Modal } from '@/components/Modal';
import { GoLink } from '@/components/TextLinks';

/** Small "Enterprise" tag (shared by sidebar, tabs and in-page entries): gray-bordered small text, deliberately low-key. */
export function EditionTag({ className = '' }: { className?: string }) {
  const { t } = useTranslation();
  return <span className={`shrink-0 rounded border border-hair px-1 text-[0.625rem] leading-4 font-normal text-ink-3 ${className}`}>{t('nav.editionTag')}</span>;
}

/** Gold Enterprise capsule (same as the title tag on the Enterprise info page). */
function GoldTag() {
  return <span className="rounded-full px-2 py-px text-2xs font-semibold tracking-wide" style={{ background: '#FFD84D', color: '#3A2A00' }}>Enterprise</span>;
}

/**
 * Intro for one commercial-edition feature: icon, name + Enterprise tag, a one-line value proposition, three short points.
 * Shared by the intro page and the in-page entry popup so the layout matches; deliberately stops here without details.
 */
export function FeatureBlurb({ slug, compact }: { slug: EnterpriseFeature; compact?: boolean }) {
  const { t } = useTranslation();
  const { icon: Icon } = ENTERPRISE_FEATURES[slug];
  return (
    <div className={`flex flex-col ${compact ? 'gap-4' : 'items-center gap-5 text-center'}`}>
      <span className={`inline-flex items-center justify-center rounded-xl bg-accent-soft text-accent ${compact ? 'size-10' : 'size-14'}`}>
        <Icon size={compact ? 22 : 30} weight="duotone" aria-hidden="true" />
      </span>
      <div className="flex flex-col gap-2">
        <h1 className={`inline-flex flex-wrap items-center gap-2 font-semibold text-ink ${compact ? 'text-base' : 'justify-center text-xl'}`}>
          {t(`edition.${slug}.title`)}
          <GoldTag />
        </h1>
        <p className={`leading-relaxed text-ink-2 ${compact ? 'text-xs' : 'text-sm'}`}>{t(`edition.${slug}.lead`)}</p>
      </div>
      <ul className={`grid gap-2 ${compact ? '' : 'w-full sm:grid-cols-3'}`}>
        {[1, 2, 3].map((i) => (
          <li key={i} className={`flex items-center gap-2 rounded-lg border border-hair bg-surface px-3 py-2.5 text-xs text-ink-2 ${compact ? '' : 'sm:flex-col sm:justify-center sm:py-4'}`}>
            <Sparkle size={14} weight="fill" aria-hidden="true" className="shrink-0 text-accent" />
            {t(`edition.${slug}.p${i}`)}
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * In-page commercial-edition entry: a small text button with lock + name + commercial-edition tag that opens an explanation popup (no navigation).
 * Used in places like card headers; deliberately lighter than a regular button so it doesn't compete with the page's own actions.
 */
export function EnterpriseTeaser({ slug, label, tagOnly }: { slug: EnterpriseFeature; label?: string; /** Only the "Enterprise" tag (e.g. next to a disabled button) */ tagOnly?: boolean }) {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  if (tagOnly) {
    return (
      <>
        <button type="button" onClick={() => setOpen(true)} aria-label={t(`edition.${slug}.title`)} className="rounded focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">
          <EditionTag />
        </button>
        {open && <EnterpriseDialog slug={slug} onClose={() => setOpen(false)} />}
      </>
    );
  }
  return (
    <>
      <button
        type="button"
        onClick={() => setOpen(true)}
        className="inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-xs text-ink-3 transition-colors hover:bg-elevated hover:text-ink-2 focus:outline-none focus-visible:ring-1 focus-visible:ring-accent"
      >
        <Lock size={12} aria-hidden="true" />
        {label ?? t(`edition.${slug}.title`)}
        <EditionTag />
      </button>
      {open && <EnterpriseDialog slug={slug} onClose={() => setOpen(false)} />}
    </>
  );
}

/** Commercial-edition popup: intro content + "About Enterprise" + close. */
export function EnterpriseDialog({ slug, onClose }: { slug: EnterpriseFeature; onClose: () => void }) {
  const { t } = useTranslation();
  return (
    <Modal width="max-w-sm" onClose={onClose} contentClass="gap-5 rounded-lg border border-hair bg-surface p-5">
      <FeatureBlurb slug={slug} compact />
      <div className="flex items-center justify-between gap-2">
        <GoLink to={ENTERPRISE_INFO_PATH}>{t('enterprise.learnMore')}</GoLink>
        <Button onClick={onClose}>{t('common.close')}</Button>
      </div>
    </Modal>
  );
}
