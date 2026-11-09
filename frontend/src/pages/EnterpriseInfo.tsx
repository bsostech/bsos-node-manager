import { Link } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { ENTERPRISE_FEATURES, featurePath, type EnterpriseFeature } from '@/lib/enterpriseFeatures';

export const ENTERPRISE_INFO_PATH = '/enterprise';
export const ENTERPRISE_CONTACT = 'business@bsos.co';
const CONTACT = ENTERPRISE_CONTACT;
// Feature blocks: the same list and order as the Enterprise features exposed in the sidebar and pages (sidebar top to bottom, then in-page entry points)
const SECTIONS: EnterpriseFeature[] = ['incident', 'failover', 'cross-org', 'audit', 'node-keys', 'identity', 'maker-checker', 'consortium-sync'];

/** Core's Enterprise info page: what Enterprise adds and who to contact (the Enterprise build doesn't mount this page, see App.tsx). */
export function EnterpriseInfo() {
  const { t } = useTranslation();
  return (
    <div className="flex flex-col items-center gap-6 pt-6 pb-8">
      <div className="flex w-full max-w-2xl flex-col gap-6">
        <div className="flex flex-col items-center gap-4 text-center">
          {/* Title = product name + a colored Enterprise tag: instantly reads as the advanced edition of the same product */}
          <h1 className="inline-flex items-start text-xl font-semibold text-ink">
            {t('app.name')}
            {/* Gold pill tag: superscript position, trailing the text without overlapping it */}
            <span className="ml-2 -translate-y-2 rounded-full px-2 py-px text-2xs font-semibold tracking-wide" style={{ background: '#FFD84D', color: '#3A2A00' }}>Enterprise</span>
          </h1>
          <p className="whitespace-pre-line text-2xs leading-relaxed text-ink-2">{t('enterprise.lead')}</p>
        </div>
        {/* Feature blocks: icon + name + one sentence; clicking opens that feature's intro page */}
        <div className="grid gap-3 sm:grid-cols-2">
          {SECTIONS.map((slug) => {
            const { icon: Icon } = ENTERPRISE_FEATURES[slug];
            return (
              <Link key={slug} to={featurePath(slug)} className="flex items-start gap-3 rounded-lg border border-hair bg-surface px-4 py-3 transition-colors hover:border-strong focus:outline-none focus-visible:ring-1 focus-visible:ring-accent">
                <span className="inline-flex size-7 shrink-0 items-center justify-center rounded-lg bg-accent-soft text-accent">
                  <Icon size={16} weight="duotone" aria-hidden="true" />
                </span>
                <span className="flex min-w-0 flex-col gap-0.5">
                  <span className="text-xs font-medium text-ink">{t(`edition.${slug}.title`)}</span>
                  <span className="text-2xs leading-relaxed text-ink-3">{t(`edition.${slug}.lead`)}</span>
                </span>
              </Link>
            );
          })}
        </div>
        <div className="flex flex-col gap-1 border-t border-hair pt-4 text-2xs text-ink-3">
          <p>{t('enterprise.support')}</p>
          <p>
            <Trans
              i18nKey="enterprise.contact"
              values={{ email: CONTACT }}
              components={{ mail: <a href={`mailto:${CONTACT}`} className="text-accent underline-offset-2 hover:underline" /> }}
            />
          </p>
        </div>
      </div>
    </div>
  );
}
