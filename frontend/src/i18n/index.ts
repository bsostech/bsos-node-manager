import i18n from 'i18next';
import { initReactI18next } from 'react-i18next';
import zh from './zh.json';
import en from './en.json';
import type { ErrorReason } from '@/api/client';

// Every value of the contract's Error.reason needs a sentence in both languages; a missing one fails the type check on this line (npm run build)
void ([zh.apiError, en.apiError] satisfies Record<ErrorReason, string>[]);

const KEY = 'bsos-lang';
const saved = localStorage.getItem(KEY);
const fallback = navigator.language?.startsWith('en') ? 'en' : 'zh';

void i18n.use(initReactI18next).init({
  resources: { zh: { translation: zh }, en: { translation: en } },
  lng: saved || fallback,
  fallbackLng: 'zh',
  interpolation: { escapeValue: false },
});

// Enterprise strings are layered on top of Core, with Enterprise winning on duplicate keys; the open-source edition has no src/enterprise/ so the glob is empty.
const enterpriseStrings = import.meta.glob<Record<string, unknown>>('../enterprise/i18n/*.json', { eager: true, import: 'default' });
for (const [path, bundle] of Object.entries(enterpriseStrings)) {
  const lng = path.endsWith('/en.json') ? 'en' : 'zh';
  i18n.addResourceBundle(lng, 'translation', bundle, true, true);
}

// <html lang> and the tab title: index.html hardcodes Chinese, so set them to the actual language on load and again on switch (screen readers and browser translation rely on lang)
const setHtmlLang = (lng: string) => {
  document.documentElement.lang = lng.startsWith('en') ? 'en' : 'zh-Hant';
  document.title = i18n.t('app.name', { lng });
};
setHtmlLang(i18n.language);

export function setLang(lng: 'zh' | 'en') {
  void i18n.changeLanguage(lng);
  localStorage.setItem(KEY, lng);
  setHtmlLang(lng);
}

export default i18n;
