import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { usageAnalyticsApi, type UsageEvent, type UsageFacets } from '@/services/api';
import { downloadBlob } from '@/utils/download';

import type { TokenDisplayMode } from './format';
import { UsageLogDetailSheet } from './UsageLogDetailSheet';
import { UsageLogFilters } from './UsageLogFilters';
import { UsageLogsTable } from './UsageLogsTable';
import {
  buildUsageEventQuery,
  EMPTY_USAGE_LOG_FILTERS,
  type UsageBaseFilters,
  type UsageLogFilterValues,
} from './usageQuery';
import type { UsageLogColumnKey } from './usageLogColumns';
import styles from './UsageLogsTab.module.scss';

interface UsageLogsTabProps {
  kind: 'all' | 'llm' | 'search';
  preset: string;
  customFrom: string;
  customTo: string;
  baseFilters: UsageBaseFilters;
  facets?: UsageFacets;
  tokenDisplayMode: TokenDisplayMode;
  currency?: string;
  pageSize: number;
  onPageSizeChange: (size: number) => void;
  columns: UsageLogColumnKey[];
  onColumnsChange: (columns: UsageLogColumnKey[]) => void;
  refreshToken: number;
}

export function UsageLogsTab({
  kind,
  preset,
  customFrom,
  customTo,
  baseFilters,
  facets,
  tokenDisplayMode,
  currency,
  pageSize,
  onPageSizeChange,
  columns,
  onColumnsChange,
  refreshToken,
}: UsageLogsTabProps) {
  const { t } = useTranslation();
  const [filters, setFilters] = useState<UsageLogFilterValues>(EMPTY_USAGE_LOG_FILTERS);
  const [appliedFilters, setAppliedFilters] = useState<UsageLogFilterValues>(EMPTY_USAGE_LOG_FILTERS);
  const [events, setEvents] = useState<UsageEvent[]>([]);
  const [cursor, setCursor] = useState('');
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<UsageEvent | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const sequence = useRef(0);

  useEffect(() => {
    const timer = window.setTimeout(() => setAppliedFilters(filters), 300);
    return () => window.clearTimeout(timer);
  }, [filters]);

  const query = useMemo(
    () =>
      buildUsageEventQuery({
        kind,
        preset,
        customFrom,
        customTo,
        baseFilters,
        logFilters: appliedFilters,
        limit: pageSize,
      }),
    [kind, preset, customFrom, customTo, baseFilters, appliedFilters, pageSize]
  );

  useEffect(() => {
    const seq = ++sequence.current;
    setLoadingMore(false);
    if (preset === 'custom' && (!customFrom || !customTo)) {
      setLoading(false);
      setError('');
      setEvents([]);
      setCursor('');
      setHasMore(false);
      return;
    }
    setLoading(true);
    setError('');
    setEvents([]);
    setCursor('');
    setHasMore(false);
    usageAnalyticsApi
      .getEvents(query)
      .then((result) => {
        if (seq !== sequence.current) return;
        setEvents(result.events ?? []);
        setCursor(result.next_cursor ?? '');
        setHasMore(Boolean(result.has_more));
      })
      .catch((err: unknown) => {
        if (seq !== sequence.current) return;
        setError(err instanceof Error ? err.message : t('usage_analytics.load_failed'));
      })
      .finally(() => {
        if (seq === sequence.current) setLoading(false);
      });
  }, [query, preset, customFrom, customTo, refreshToken, reloadToken, t]);

  const loadMore = async () => {
    if (loadingMore || !hasMore || !cursor) return;
    setLoadingMore(true);
    const seq = sequence.current;
    try {
      const result = await usageAnalyticsApi.getEvents({ ...query, cursor });
      if (seq !== sequence.current) return;
      setEvents((current) => [...current, ...(result.events ?? [])]);
      setCursor(result.next_cursor ?? '');
      setHasMore(Boolean(result.has_more));
    } catch (err: unknown) {
      if (seq === sequence.current) {
        setError(err instanceof Error ? err.message : t('usage_analytics.load_failed'));
      }
    } finally {
      if (seq === sequence.current) {
        setLoadingMore(false);
      }
    }
  };

  const exportCsv = async () => {
    setExporting(true);
    setError('');
    try {
      const blob = await usageAnalyticsApi.exportCsv({ ...query, limit: 1000 });
      downloadBlob({ filename: 'xrouter-usage.csv', blob });
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.export_failed'));
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className={styles.tab}>
      <UsageLogFilters
        values={filters}
        onChange={(patch) => setFilters((current) => ({ ...current, ...patch }))}
        onReset={() => setFilters(EMPTY_USAGE_LOG_FILTERS)}
        onRefresh={() => setReloadToken((value) => value + 1)}
        onExport={() => void exportCsv()}
        loading={loading}
        exporting={exporting}
        facets={facets}
        columns={columns}
        onColumnsChange={onColumnsChange}
        pageSize={pageSize}
        onPageSizeChange={onPageSizeChange}
      />
      {error && <div className={styles.error}>{error}</div>}
      <div className={styles.summary}>
        <span>{t('usage_analytics.log_loaded', { count: events.length })}</span>
        {hasMore && <span>{t('usage_analytics.log_more_available')}</span>}
      </div>
      <UsageLogsTable
        events={events}
        columns={columns}
        tokenDisplayMode={tokenDisplayMode}
        currency={currency}
        loading={loading}
        onSelect={setSelected}
      />
      {hasMore && (
        <div className={styles.loadMore}>
          <Button variant="secondary" onClick={() => void loadMore()} loading={loadingMore}>
            {t('usage_analytics.load_more')}
          </Button>
        </div>
      )}
      <UsageLogDetailSheet event={selected} onClose={() => setSelected(null)} />
    </div>
  );
}
