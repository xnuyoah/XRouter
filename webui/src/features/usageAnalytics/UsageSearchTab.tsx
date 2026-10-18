import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/ui/EmptyState';

import type { UsageAnalyticsResponse, WebSearchAccount, WebSearchMonitoring } from '@/services/api';

import { SearchAccountsPanel } from './SearchAccountsPanel';
import { SearchTrendChart } from './SearchTrendChart';
import styles from './UsageSearchTab.module.scss';

interface UsageSearchTabProps {
  monitoring: WebSearchMonitoring | null;
  accounts: WebSearchAccount[];
  accountUsage: UsageAnalyticsResponse | null;
  historyHours: number;
  historyTo?: string;
  lowBalanceThreshold: number;
  onLowBalanceThresholdChange: (value: number) => void;
}

export function UsageSearchTab({
  monitoring,
  accounts,
  accountUsage,
  historyHours,
  historyTo,
  lowBalanceThreshold,
  onLowBalanceThresholdChange,
}: UsageSearchTabProps) {
  const { t } = useTranslation();
  const series = accountUsage?.series ?? [];
  const hasTrend = series.some((point) => point.kind === 'search' && point.requests > 0);
  if (!monitoring?.quota && !hasTrend && accounts.length === 0) {
    return (
      <EmptyState
        title={t('usage_analytics.search_tab_empty')}
        description={t('usage_analytics.search_tab_empty_hint')}
      />
    );
  }
  return (
    <div className={styles.tab}>
      {monitoring?.quota && (
        <section className={styles.panel}>
          <div className={styles.panelHeader}>
            <h2>{t('usage_analytics.search_quota_title')}</h2>
            <span>{t('usage_analytics.search_quota_hint')}</span>
          </div>
          <div className={styles.quotaRow}>
            <span>
              {t('usage_analytics.search_quota_accounts')}:{' '}
              <strong>{monitoring.quota.accounts}</strong>
            </span>
            <span>
              {t('usage_analytics.search_quota_fresh')}: <strong>{monitoring.quota.fresh}</strong>
            </span>
            <span>
              {t('usage_analytics.search_quota_stale')}: <strong>{monitoring.quota.stale}</strong>
            </span>
            <span>
              {t('usage_analytics.search_quota_failed')}: <strong>{monitoring.quota.failed}</strong>
            </span>
          </div>
        </section>
      )}

      {series.some((point) => point.kind === 'search' && point.requests > 0) && (
        <section className={styles.panel}>
          <div className={styles.panelHeader}>
            <h2>{t('usage_analytics.search_trend_title')}</h2>
            <span>{t('usage_analytics.search_trend_hint')}</span>
          </div>
          <SearchTrendChart series={series} />
        </section>
      )}

      <SearchAccountsPanel
        accounts={accounts}
        groups={accountUsage?.groups ?? []}
        historyHours={historyHours}
        historyTo={historyTo}
        lowBalanceThreshold={lowBalanceThreshold}
        onLowBalanceThresholdChange={onLowBalanceThresholdChange}
      />
    </div>
  );
}
