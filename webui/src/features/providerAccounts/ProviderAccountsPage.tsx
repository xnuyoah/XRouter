import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { ChannelLogo } from '@/components/common/ChannelLogo';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Pagination } from '@/components/ui/Pagination/Pagination';
import { clampPage, paginate } from '@/components/ui/Pagination/paginationUtils';
import { Skeleton } from '@/components/ui/Skeleton';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import {
  channelStatusApi,
  type ChannelStatusSnapshot,
} from '@/services/api';
import { useAuthStore, useConfigStore } from '@/stores';
import { providerLabel } from '@/features/dashboard/utils';

import styles from './ProviderAccountsPage.module.scss';

const PAGE_SIZES = [25, 50, 100] as const;
const DEFAULT_BASE_URLS: Record<string, string> = {
  nvidia: 'https://integrate.api.nvidia.com/v1',
  agnes: 'https://apihub.agnes-ai.com/v1',
  openai: 'https://api.openai.com/v1',
  deepseek: 'https://api.deepseek.com/v1',
};
const PROVIDER_ORDER = [
  'nvidia',
  'agnes',
  'openai',
  'deepseek',
  'openai-compatibility',
  'gemini',
  'claude',
  'codex',
];

interface ProviderKeyRow {
  id: string;
  provider: string;
  group: string;
  key: string;
  keyMasked: string;
  baseURL: string;
  models: string[];
  disabled: boolean;
}

interface ProviderSummary {
  provider: string;
  keys: number;
  groups: number;
  models: number;
}

const isRecord = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);

function maskKey(value: string): string {
  if (!value) return '—';
  if (value.length <= 12) return `${value.slice(0, 4)}…`;
  return `${value.slice(0, 5)}…${value.slice(-4)}`;
}

function modelNames(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  return value.flatMap((entry) => {
    if (!isRecord(entry)) return [];
    const name = String(entry.alias || entry.name || '').trim();
    return name ? [name] : [];
  });
}

function providerSort(left: string, right: string): number {
  const leftIndex = PROVIDER_ORDER.indexOf(left);
  const rightIndex = PROVIDER_ORDER.indexOf(right);
  if (leftIndex !== -1 || rightIndex !== -1) {
    if (leftIndex === -1) return 1;
    if (rightIndex === -1) return -1;
    if (leftIndex !== rightIndex) return leftIndex - rightIndex;
  }
  return left.localeCompare(right);
}

function readProviderRows(
  groupsByProvider: Record<string, unknown>,
  providerModels: Map<string, string[]>
): { rows: ProviderKeyRow[]; summaries: ProviderSummary[] } {
  const rows: ProviderKeyRow[] = [];
  const summaryMap = new Map<string, { keys: number; groups: number; models: Set<string> }>();

  Object.entries(groupsByProvider).forEach(([provider, value]) => {
    const groups = Array.isArray(value) ? value : isRecord(value) ? [value] : [];
    if (groups.length === 0) return;
    const summary = summaryMap.get(provider) ?? { keys: 0, groups: 0, models: new Set<string>() };
    summary.groups += groups.length;

    groups.forEach((groupValue, groupIndex) => {
      if (!isRecord(groupValue)) return;
      const keys = Array.isArray(groupValue.keys) ? groupValue.keys : [];
      const groupName =
        String(groupValue.name || '').trim() || providerLabel(provider, provider);
      const baseURL =
        String(groupValue['base-url'] || '').trim() || DEFAULT_BASE_URLS[provider] || '';
      const declaredModels = modelNames(groupValue.models);
      const models =
        declaredModels.length > 0
          ? declaredModels
          : (providerModels.get(provider) ?? []);
      models.forEach((model) => summary.models.add(model));
      const groupDisabled = Boolean(groupValue.disabled);
      summary.keys += keys.length;

      keys.forEach((keyValue, keyIndex) => {
        if (!isRecord(keyValue)) return;
        const key = String(keyValue['api-key'] || '').trim();
        if (!key) return;
        rows.push({
          id: `${provider}\x1f${groupIndex}\x1f${keyIndex}`,
          provider,
          group: groupName,
          key,
          keyMasked: maskKey(key),
          baseURL,
          models,
          disabled: groupDisabled || Boolean(keyValue.disabled),
        });
      });
    });
    summaryMap.set(provider, summary);
  });

  rows.sort(
    (left, right) =>
      providerSort(left.provider, right.provider) ||
      left.group.localeCompare(right.group) ||
      left.id.localeCompare(right.id)
  );
  return {
    rows,
    summaries: [...summaryMap.entries()]
      .map(([provider, summary]) => ({
        provider,
        keys: summary.keys,
        groups: summary.groups,
        models: summary.models.size,
      }))
      .sort((left, right) => providerSort(left.provider, right.provider)),
  };
}

export function ProviderAccountsPage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [providerFilter, setProviderFilter] = useState('all');
  const [statusFilter, setStatusFilter] = useState<'all' | 'enabled' | 'disabled'>('all');
  const [search, setSearch] = useState('');
  const [page, setPage] = useState(1);
  const [storedPageSize, setStoredPageSize] = useLocalStorage(
    'providerAccounts.pageSize',
    25
  );
  const pageSize = PAGE_SIZES.some((size) => size === storedPageSize) ? storedPageSize : 25;
  const [channelSnapshot, setChannelSnapshot] = useState<ChannelStatusSnapshot | null>(null);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [, nextSnapshot] = await Promise.all([
        fetchConfig(true),
        channelStatusApi.get().catch(() => null),
      ]);
      setChannelSnapshot(nextSnapshot);
    } catch (errRefresh) {
      setError(errRefresh instanceof Error ? errRefresh.message : String(errRefresh));
    } finally {
      setLoading(false);
    }
  }, [fetchConfig]);

  useEffect(() => {
    void refresh();
  }, [refresh]);
  useHeaderRefresh(refresh, connected);

  const providerModels = useMemo(() => {
    const map = new Map<string, string[]>();
    for (const channel of channelSnapshot?.channels ?? []) {
      if (channel.kind !== 'llm') continue;
      map.set(
        channel.id.toLowerCase(),
        (channel.models ?? []).map((model) => model.name || model.id).filter(Boolean)
      );
    }
    return map;
  }, [channelSnapshot]);

  const { rows, summaries } = useMemo(
    () =>
      readProviderRows(
        isRecord(config?.providerGroups) ? config.providerGroups : {},
        providerModels
      ),
    [config, providerModels]
  );

  const filteredRows = useMemo(() => {
    const query = search.trim().toLowerCase();
    return rows.filter((row) => {
      if (providerFilter !== 'all' && row.provider !== providerFilter) return false;
      if (statusFilter === 'enabled' && row.disabled) return false;
      if (statusFilter === 'disabled' && !row.disabled) return false;
      if (!query) return true;
      return [row.provider, row.group, row.key, row.baseURL, ...row.models]
        .join(' ')
        .toLowerCase()
        .includes(query);
    });
  }, [providerFilter, rows, search, statusFilter]);

  useEffect(() => {
    setPage((current) => clampPage(current, filteredRows.length, pageSize));
  }, [filteredRows.length, pageSize]);

  const pagedRows = useMemo(
    () => paginate(filteredRows, page, pageSize),
    [filteredRows, page, pageSize]
  );
  const keyCount = rows.length;
  const modelCount = useMemo(
    () => new Set(rows.flatMap((row) => row.models)).size,
    [rows]
  );

  const changePageSize = (size: number) => {
    setStoredPageSize(size);
    setPage(1);
  };

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('providerAccounts.title')}</h1>
          <p>{t('providerAccounts.subtitle')}</p>
        </div>
        <div className={styles.headerActions}>
          <Link to="/config" className={styles.secondaryLink}>
            {t('providerAccounts.editConfig')}
          </Link>
          <Button variant="secondary" size="sm" onClick={() => void refresh()} loading={loading}>
            {t('common.refresh')}
          </Button>
        </div>
      </header>

      {error && <div className={styles.error}>{error}</div>}

      <section className={styles.metrics}>
        <article>
          <span>{t('providerAccounts.metricProviders')}</span>
          <strong>{summaries.length}</strong>
        </article>
        <article>
          <span>{t('providerAccounts.metricKeys')}</span>
          <strong>{keyCount.toLocaleString()}</strong>
        </article>
        <article>
          <span>{t('providerAccounts.metricGroups')}</span>
          <strong>{summaries.reduce((sum, provider) => sum + provider.groups, 0)}</strong>
        </article>
        <article>
          <span>{t('providerAccounts.metricModels')}</span>
          <strong>{modelCount}</strong>
        </article>
      </section>

      <section className={styles.panel}>
        <div className={styles.toolbar}>
          <input
            value={search}
            onChange={(event) => {
              setSearch(event.target.value);
              setPage(1);
            }}
            placeholder={t('providerAccounts.searchPlaceholder')}
            aria-label={t('providerAccounts.searchPlaceholder')}
          />
          <select
            value={providerFilter}
            aria-label={t('providerAccounts.providerFilter')}
            onChange={(event) => {
              setProviderFilter(event.target.value);
              setPage(1);
            }}
          >
            <option value="all">{t('providerAccounts.allProviders')}</option>
            {summaries.map((provider) => (
              <option key={provider.provider} value={provider.provider}>
                {providerLabel(provider.provider, provider.provider)} ({provider.keys})
              </option>
            ))}
          </select>
          <select
            value={statusFilter}
            aria-label={t('providerAccounts.statusFilter')}
            onChange={(event) => {
              setStatusFilter(event.target.value as typeof statusFilter);
              setPage(1);
            }}
          >
            <option value="all">{t('providerAccounts.allStatuses')}</option>
            <option value="enabled">{t('providerAccounts.enabled')}</option>
            <option value="disabled">{t('providerAccounts.disabled')}</option>
          </select>
        </div>

        {loading && rows.length === 0 ? (
          <Skeleton height={240} />
        ) : filteredRows.length === 0 ? (
          <EmptyState
            title={t('providerAccounts.empty')}
            description={t('providerAccounts.emptyHint')}
          />
        ) : (
          <>
            <div className={styles.tableWrap}>
              <table className={styles.table}>
                <thead>
                  <tr>
                    <th>{t('providerAccounts.colProvider')}</th>
                    <th>{t('providerAccounts.colGroup')}</th>
                    <th>{t('providerAccounts.colKey')}</th>
                    <th>{t('providerAccounts.colBaseUrl')}</th>
                    <th>{t('providerAccounts.colModels')}</th>
                    <th>{t('providerAccounts.colStatus')}</th>
                  </tr>
                </thead>
                <tbody>
                  {pagedRows.map((row) => (
                    <tr key={row.id}>
                      <td>
                        <span className={styles.provider}>
                          <ChannelLogo value={row.provider} size={17} />
                          {providerLabel(row.provider, row.provider)}
                        </span>
                      </td>
                      <td>{row.group}</td>
                      <td className={styles.mono}>{row.keyMasked}</td>
                      <td className={styles.url} title={row.baseURL}>
                        {row.baseURL || '—'}
                      </td>
                      <td>
                        {row.models.length > 0
                          ? t('providerAccounts.modelCount', { count: row.models.length })
                          : '—'}
                      </td>
                      <td>
                        <span className={row.disabled ? styles.disabled : styles.enabled}>
                          {row.disabled
                            ? t('providerAccounts.disabled')
                            : t('providerAccounts.enabled')}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
            <Pagination
              page={page}
              pageSize={pageSize}
              total={filteredRows.length}
              pageSizeOptions={[...PAGE_SIZES]}
              onPageChange={setPage}
              onPageSizeChange={changePageSize}
            />
          </>
        )}
      </section>
    </div>
  );
}
