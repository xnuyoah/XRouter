import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { ChannelLogo } from '@/components/common/ChannelLogo';
import { Pagination } from '@/components/ui/Pagination/Pagination';
import { clampPage, paginate } from '@/components/ui/Pagination/paginationUtils';
import { QuotaHistoryChart } from '@/features/channelStatus/components/QuotaHistoryChart';
import { ACCOUNT_STATUS_META, QUOTA_KIND_LABEL_KEYS } from '@/features/webSearch/constants';
import {
  webSearchApi,
  type UsageGroup,
  type WebSearchAccount,
  type WebSearchQuotaHistoryPoint,
} from '@/services/api';
import { formatCompactNumber, formatPercent } from '@/utils/format';

import styles from './SearchAccountsPanel.module.scss';
import { accountWarnings, LOW_BALANCE_OPTIONS, remainingBalance } from './searchAccountWarnings';

interface SearchAccountsPanelProps {
  accounts: WebSearchAccount[];
  groups: UsageGroup[];
  historyHours: number;
  historyTo?: string;
  lowBalanceThreshold: number;
  onLowBalanceThresholdChange: (value: number) => void;
  pageSize: number;
  onPageSizeChange: (value: number) => void;
}

function balanceText(account: WebSearchAccount, t: (key: string) => string): string {
  const balances = account.quota?.balances ?? [];
  if (balances.length === 0) return '—';
  return balances
    .map((balance) => {
      const label = QUOTA_KIND_LABEL_KEYS[balance.kind]
        ? t(QUOTA_KIND_LABEL_KEYS[balance.kind])
        : balance.kind;
      const remaining = remainingBalance(balance);
      return `${label}: ${remaining == null ? '—' : formatCompactNumber(remaining)}`;
    })
    .join(' · ');
}

function probeText(account: WebSearchAccount): string {
  if (!account.last_checked_at) return '—';
  const at = new Date(account.last_checked_at);
  const time = Number.isNaN(at.getTime()) ? account.last_checked_at : at.toLocaleString();
  const latency =
    account.last_latency_ms == null ? '—' : `${Math.round(account.last_latency_ms)} ms`;
  const status = account.last_http_status ? `HTTP ${account.last_http_status}` : 'HTTP —';
  return `${time} · ${latency} · ${status}`;
}

export function SearchAccountsPanel({
  accounts,
  groups,
  historyHours,
  historyTo,
  lowBalanceThreshold,
  onLowBalanceThresholdChange,
  pageSize,
  onPageSizeChange,
}: SearchAccountsPanelProps) {
  const { t } = useTranslation();
  const [expandedKey, setExpandedKey] = useState('');
  const [onlyWarnings, setOnlyWarnings] = useState(false);
  const [page, setPage] = useState(1);
  const [history, setHistory] = useState<Record<string, WebSearchQuotaHistoryPoint[]>>({});
  const [historyLoading, setHistoryLoading] = useState<Record<string, boolean>>({});
  const [historyError, setHistoryError] = useState<Record<string, boolean>>({});
  const historySeq = useRef<Record<string, number>>({});

  useEffect(() => {
    setExpandedKey('');
    setHistory({});
    setHistoryLoading({});
    setHistoryError({});
    historySeq.current = {};
  }, [historyHours, historyTo]);

  const loadHistory = useCallback(
    async (account: WebSearchAccount, key: string) => {
      const seq = (historySeq.current[key] ?? 0) + 1;
      historySeq.current[key] = seq;
      setHistoryLoading((current) => ({ ...current, [key]: true }));
      setHistoryError((current) => ({ ...current, [key]: false }));
      try {
        const result = await webSearchApi.getQuotaHistory({
          channel: account.channel,
          account_id: account.account_id,
          hours: historyHours,
          limit: 500,
          to: historyTo,
        });
        if (historySeq.current[key] !== seq) return;
        setHistory((current) => ({ ...current, [key]: result.points ?? [] }));
      } catch {
        if (historySeq.current[key] !== seq) return;
        setHistoryError((current) => ({ ...current, [key]: true }));
      } finally {
        if (historySeq.current[key] === seq) {
          setHistoryLoading((current) => ({ ...current, [key]: false }));
        }
      }
    },
    [historyHours, historyTo]
  );

  const toggleHistory = (account: WebSearchAccount) => {
    const key = `${account.channel}:${account.account_id}`;
    if (expandedKey === key) {
      setExpandedKey('');
      return;
    }
    setExpandedKey(key);
    if (!history[key] && !historyLoading[key]) {
      void loadHistory(account, key);
    }
  };

  const usageByAccount = useMemo(() => {
    const map = new Map<string, UsageGroup>();
    for (const group of groups) {
      const id = group.account_id || group.key;
      if (id) map.set(id, group);
    }
    return map;
  }, [groups]);

  const rows = useMemo(() => {
    const severity = (warnings: ReturnType<typeof accountWarnings>) =>
      warnings.some((warning) => warning.tone === 'critical') ? 0 : warnings.length > 0 ? 1 : 2;
    return accounts
      .map((account) => ({
        account,
        warnings: accountWarnings(account, lowBalanceThreshold, t),
      }))
      .sort((left, right) => {
        const bySeverity = severity(left.warnings) - severity(right.warnings);
        if (bySeverity !== 0) return bySeverity;
        if (left.account.channel !== right.account.channel) {
          return left.account.channel.localeCompare(right.account.channel);
        }
        return (left.account.name || left.account.account_id).localeCompare(
          right.account.name || right.account.account_id
        );
      });
  }, [accounts, lowBalanceThreshold, t]);
  const warningCount = rows.filter((row) => row.warnings.length > 0).length;
  const visibleRows = onlyWarnings ? rows.filter((row) => row.warnings.length > 0) : rows;
  const pagedRows = useMemo(
    () => paginate(visibleRows, page, pageSize),
    [visibleRows, page, pageSize]
  );

  useEffect(() => {
    setPage((current) => clampPage(current, visibleRows.length, pageSize));
  }, [visibleRows.length, pageSize]);

  const changeWarningsFilter = (checked: boolean) => {
    setOnlyWarnings(checked);
    setPage(1);
    setExpandedKey('');
  };

  const changeThreshold = (value: number) => {
    onLowBalanceThresholdChange(value);
    setPage(1);
    setExpandedKey('');
  };

  const changePage = (nextPage: number) => {
    setPage(nextPage);
    setExpandedKey('');
  };

  const changePageSize = (size: number) => {
    onPageSizeChange(size);
    setPage(1);
    setExpandedKey('');
  };

  if (accounts.length === 0) {
    return null;
  }

  return (
    <section className={styles.panel}>
      <div className={styles.panelHeader}>
        <div className={styles.titleBlock}>
          <h2>{t('usage_analytics.search_accounts_title')}</h2>
          <span>{t('usage_analytics.search_accounts_hint')}</span>
        </div>
        <div className={styles.headerControls}>
          <label className={styles.warningFilter}>
            <input
              type="checkbox"
              checked={onlyWarnings}
              onChange={(event) => changeWarningsFilter(event.target.checked)}
            />
            {t('usage_analytics.only_warnings', { count: warningCount })}
          </label>
          <label className={styles.thresholdControl}>
            {t('usage_analytics.low_balance_threshold')}
            <select
              value={lowBalanceThreshold}
              onChange={(event) => changeThreshold(Number(event.target.value))}
            >
              {LOW_BALANCE_OPTIONS.map((value) => (
                <option key={value} value={value}>
                  {value}%
                </option>
              ))}
            </select>
          </label>
        </div>
      </div>
      <div className={styles.tableWrap}>
        <table className={styles.table}>
          <thead>
            <tr>
              <th>{t('usage_analytics.channel')}</th>
              <th>{t('usage_analytics.account')}</th>
              <th>{t('usage_analytics.status')}</th>
              <th>{t('usage_analytics.warnings')}</th>
              <th>{t('usage_analytics.requests')}</th>
              <th>{t('usage_analytics.success_rate')}</th>
              <th>{t('usage_analytics.credits')}</th>
              <th>{t('usage_analytics.p95')}</th>
              <th>{t('usage_analytics.balance')}</th>
              <th>{t('usage_analytics.last_probe')}</th>
              <th aria-label={t('usage_analytics.quota_trend')} />
            </tr>
          </thead>
          <tbody>
            {pagedRows.map(({ account, warnings }) => {
              const usage = usageByAccount.get(account.account_id);
              const status = ACCOUNT_STATUS_META[account.status];
              const rowKey = `${account.channel}:${account.account_id}`;
              const expanded = expandedKey === rowKey;
              const rowTone = warnings.some((warning) => warning.tone === 'critical')
                ? styles.rowCritical
                : warnings.length > 0
                  ? styles.rowWarning
                  : '';
              return (
                <Fragment key={rowKey}>
                  <tr className={rowTone}>
                    <td>
                      <span className={styles.channelCell}>
                        <ChannelLogo value={account.channel} size={18} />
                        {account.channel}
                      </span>
                    </td>
                    <td>
                      <span>{account.name || account.account_id}</span>
                      {account.api_key_masked && (
                        <span className={styles.meta}>{account.api_key_masked}</span>
                      )}
                    </td>
                    <td>
                      <span
                        className={`${styles.statusPill} ${styles[`status_${status?.tone ?? 'gray'}`]}`}
                      >
                        {status ? t(status.labelKey) : account.status}
                      </span>
                      {account.cooldown && (
                        <span className={styles.meta}>{t('usage_analytics.cooldown')}</span>
                      )}
                    </td>
                    <td>
                      {warnings.length === 0 ? (
                        <span className={styles.meta}>—</span>
                      ) : (
                        <span className={styles.warningList}>
                          {warnings.map((warning) => (
                            <span
                              key={`${warning.tone}:${warning.label}`}
                              className={`${styles.warningChip} ${
                                warning.tone === 'critical'
                                  ? styles.warningCritical
                                  : styles.warningWarning
                              }`}
                            >
                              {warning.label}
                            </span>
                          ))}
                        </span>
                      )}
                    </td>
                    <td>{usage ? formatCompactNumber(usage.requests) : '—'}</td>
                    <td>{usage ? formatPercent(usage.success_rate * 100) : '—'}</td>
                    <td>
                      {usage?.credits
                        ? formatCompactNumber(usage.credits)
                        : usage?.units
                          ? formatCompactNumber(usage.units)
                          : '—'}
                    </td>
                    <td>
                      {usage?.p95_latency_ms ? `${Math.round(usage.p95_latency_ms)} ms` : '—'}
                    </td>
                    <td>{balanceText(account, t)}</td>
                    <td>{probeText(account)}</td>
                    <td>
                      <button
                        type="button"
                        className={`${styles.trendButton} ${expanded ? styles.trendButtonActive : ''}`}
                        aria-expanded={expanded}
                        onClick={() => toggleHistory(account)}
                      >
                        {t('usage_analytics.quota_trend')}
                      </button>
                    </td>
                  </tr>
                  {expanded && (
                    <tr>
                      <td colSpan={11} className={styles.chartCell}>
                        {historyError[rowKey] ? (
                          <div className={styles.chartEmpty}>
                            {t('usage_analytics.quota_trend_failed')}
                          </div>
                        ) : (
                          <QuotaHistoryChart
                            points={history[rowKey] ?? []}
                            loading={historyLoading[rowKey] === true}
                          />
                        )}
                      </td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
            {visibleRows.length === 0 && (
              <tr>
                <td colSpan={11} className={styles.empty}>
                  {t('usage_analytics.no_warnings')}
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
      <Pagination
        page={page}
        pageSize={pageSize}
        total={visibleRows.length}
        pageSizeOptions={[10, 25, 50, 100]}
        onPageChange={changePage}
        onPageSizeChange={changePageSize}
      />
    </section>
  );
}
