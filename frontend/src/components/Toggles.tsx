import { useTranslation } from 'react-i18next';
import { Moon, Sun, Translate } from '@phosphor-icons/react';
import { useTheme } from '@/theme/ThemeProvider';
import { setLang } from '@/i18n';
import { Tooltip } from '@/components/Tooltip';

const iconBtn =
  'inline-flex size-8 items-center justify-center rounded-md text-ink-2 hover:bg-elevated hover:text-ink transition-colors';

export function ThemeToggle() {
  const { t } = useTranslation();
  const { theme, toggle } = useTheme();
  return (
    <Tooltip label={t('theme.toggle')} side="bottom">
      <button className={iconBtn} onClick={toggle} aria-label={t('theme.toggle')}>
        {theme === 'dark' ? <Sun size={18} /> : <Moon size={18} />}
      </button>
    </Tooltip>
  );
}

export function LangToggle() {
  const { t, i18n } = useTranslation();
  const next = i18n.language === 'zh' ? 'en' : 'zh';
  return (
    <Tooltip label={t('lang.toggle')} side="bottom">
      <button
        className={`${iconBtn} gap-1 w-auto px-2 text-xs`}
        onClick={() => setLang(next)}
        aria-label={t('lang.toggle')}
      >
        <Translate size={16} />
        {i18n.language === 'zh' ? '中' : 'EN'}
      </button>
    </Tooltip>
  );
}
