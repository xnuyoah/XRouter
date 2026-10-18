import { useTranslation } from 'react-i18next';

import styles from './UsageTabs.module.scss';

export type UsageTabKey = 'overview' | 'logs' | 'search';

interface UsageTabsProps {
  active: UsageTabKey;
  onChange: (tab: UsageTabKey) => void;
}

const TABS: UsageTabKey[] = ['logs', 'overview', 'search'];

const LABEL_KEYS: Record<UsageTabKey, string> = {
  overview: 'usage_analytics.tab_overview',
  logs: 'usage_analytics.tab_logs',
  search: 'usage_analytics.tab_search',
};

export function UsageTabs({ active, onChange }: UsageTabsProps) {
  const { t } = useTranslation();
  return (
    <nav className={styles.tabs} aria-label={t('usage_analytics.tabs_aria')}>
      {TABS.map((tab) => (
        <button
          key={tab}
          type="button"
          className={`${styles.tab} ${active === tab ? styles.active : ''}`}
          aria-selected={active === tab}
          onClick={() => onChange(tab)}
        >
          {t(LABEL_KEYS[tab])}
        </button>
      ))}
    </nav>
  );
}
