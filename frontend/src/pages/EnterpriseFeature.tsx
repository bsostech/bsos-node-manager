import { Navigate, useParams } from 'react-router-dom';
import { Trans, useTranslation } from 'react-i18next';
import { FeatureBlurb } from '@/components/EnterpriseTeaser';
import { GoLink } from '@/components/TextLinks';
import { ENTERPRISE_INFO_PATH, ENTERPRISE_CONTACT } from '@/pages/EnterpriseInfo';
import { isEnterpriseFeature } from '@/lib/enterpriseFeatures';

/**
 * Intro page (/enterprise/<slug>) opened from a locked Enterprise item in the Core sidebar.
 * The layout is deliberately simple: icon, name, one-line value proposition, three short points, then learn-more and contact; no details, leave room for imagination.
 */
export function EnterpriseFeature() {
  const { t } = useTranslation();
  const { slug } = useParams();
  if (!isEnterpriseFeature(slug)) return <Navigate to={ENTERPRISE_INFO_PATH} replace />;
  return (
    <div className="flex flex-col items-center pt-16 pb-8">
      <div className="flex w-full max-w-xl flex-col items-center gap-8">
        <FeatureBlurb slug={slug} />
        <div className="flex flex-col items-center gap-2 text-xs text-ink-3">
          <GoLink to={ENTERPRISE_INFO_PATH}>{t('enterprise.learnMore')}</GoLink>
          <p>
            <Trans
              i18nKey="enterprise.contact"
              values={{ email: ENTERPRISE_CONTACT }}
              components={{ mail: <a href={`mailto:${ENTERPRISE_CONTACT}`} className="text-accent underline-offset-2 hover:underline" /> }}
            />
          </p>
        </div>
      </div>
    </div>
  );
}
