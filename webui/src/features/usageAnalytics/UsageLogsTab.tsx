import { useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { Pagination } from '@/components/ui/Pagination/Pagination';
import { usageAnalyticsApi, type UsageEvent, type UsageFacets } from '@/services/api';
import { downloadBlob } from '@/utils/download';

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
  onKindChange: (kind: 'all' | 'llm' | 'search') => void;
  preset: string;
  onPresetChange: (preset: string) => void;
  customFrom: string;
  customTo: string;
  onCustomFromChange: (value: string) => void;
  onCustomToChange: (value: string) => void;
  baseFilters: UsageBaseFilters;
  onBaseFiltersChange: (patch: Partial<UsageBaseFilters>) => void;
  onResetBaseFilters: () => void;
  onRefreshAll: () => void;
  facets?: UsageFacets;
  currency?: string;
  pageSize: number;
  onPageSizeChange: (size: number) => void;
  columns: UsageLogColumnKey[];
  onColumnsChange: (columns: UsageLogColumnKey[]) => void;
  refreshToken: number;
  total: number;
  rangeFrom?: string;
  rangeTo?: string;
  metrics: {
    cost: number | null;
    currency?: string;
    rpm: number;
    tpm: number;
  };
}

export function UsageLogsTab({
  kind,
  onKindChange,
  preset,
  onPresetChange,
  customFrom,
  customTo,
  onCustomFromChange,
  onCustomToChange,
  baseFilters,
  onBaseFiltersChange,
  onResetBaseFilters,
  onRefreshAll,
  facets,
  currency,
  pageSize,
  onPageSizeChange,
  columns,
  onColumnsChange,
  refreshToken,
  total,
  rangeFrom,
  rangeTo,
  metrics,
}: UsageLogsTabProps) {
  const { t } = useTranslation();
  const [filters, setFilters] = useState<UsageLogFilterValues>(EMPTY_USAGE_LOG_FILTERS);
  const [appliedFilters, setAppliedFilters] = useState<UsageLogFilterValues>(
    EMPTY_USAGE_LOG_FILTERS
  );
  const [events, setEvents] = useState<UsageEvent[]>([]);
  const [pageIndex, setPageIndex] = useState(0);
  const [hasMore, setHasMore] = useState(false);
  const [loading, setLoading] = useState(false);
  const [exporting, setExporting] = useState(false);
  const [error, setError] = useState('');
  const [selected, setSelected] = useState<UsageEvent | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const sequence = useRef(0);

  // Freeze the time window for the current analytics load so newly arriving
  // events cannot shift offset pages while the operator is paging.
  const useFixedWindow = preset !== 'custom' && Boolean(rangeFrom && rangeTo);
  const queryPreset = useFixedWindow ? 'custom' : preset;
  const queryFrom = useFixedWindow ? (rangeFrom ?? '') : customFrom;
  const queryTo = useFixedWindow ? (rangeTo ?? '') : customTo;
  const query = useMemo(
    () =>
      buildUsageEventQuery({
        kind,
        preset: queryPreset,
        customFrom: queryFrom,
        customTo: queryTo,
        baseFilters,
        logFilters: appliedFilters,
        limit: pageSize,
      }),
    [
      kind,
      queryPreset,
      queryFrom,
      queryTo,
      baseFilters,
      appliedFilters,
      pageSize,
    ]
  );

  useEffect(() => {
    const seq = ++sequence.current;
    if (preset === 'custom' && (!customFrom || !customTo)) {
      setLoading(false);
      setError('');
      setEvents([]);
      setHasMore(false);
      return;
    }
    setLoading(true);
    setError('');
    usageAnalyticsApi
      .getEvents({ ...query, offset: pageIndex * pageSize })
      .then((result) => {
        if (seq !== sequence.current) return;
        setEvents(result.events ?? []);
        setHasMore(Boolean(result.has_more));
        setSelected(null);
      })
      .catch((err: unknown) => {
        if (seq !== sequence.current) return;
        setError(err instanceof Error ? err.message : t('usage_analytics.load_failed'));
      })
      .finally(() => {
        if (seq === sequence.current) setLoading(false);
      });
  }, [query, pageIndex, pageSize, preset, customFrom, customTo, refreshToken, reloadToken, t]);

  const applyFilters = () => {
    setPageIndex(0);
    setAppliedFilters(filters);
    setReloadToken((value) => value + 1);
  };

  const resetFilters = () => {
    setFilters(EMPTY_USAGE_LOG_FILTERS);
    setAppliedFilters(EMPTY_USAGE_LOG_FILTERS);
    setPageIndex(0);
    onResetBaseFilters();
    onKindChange('all');
    setReloadToken((value) => value + 1);
  };

  const handlePresetChange = (value: string) => {
    setPageIndex(0);
    onPresetChange(value);
  };

  const handleKindChange = (value: 'all' | 'llm' | 'search') => {
    setPageIndex(0);
    onKindChange(value);
  };

  const handleBaseFiltersChange = (patch: Partial<UsageBaseFilters>) => {
    setPageIndex(0);
    onBaseFiltersChange(patch);
  };

  const handleCustomFromChange = (value: string) => {
    setPageIndex(0);
    onCustomFromChange(value);
  };

  const handleCustomToChange = (value: string) => {
    setPageIndex(0);
    onCustomToChange(value);
  };

  const handlePageSizeChange = (value: number) => {
    setPageIndex(0);
    onPageSizeChange(value);
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

  const hasEventFilters = Object.values(appliedFilters).some((value) => value.trim() !== '');
  const observedTotal = events.length + pageIndex * pageSize + (hasMore ? 1 : 0);
  const effectiveTotal = hasEventFilters ? observedTotal : total > 0 ? total : observedTotal;

  return (
    <div className={styles.tab}>
      <UsageLogFilters
        values={filters}
        onChange={(patch) => setFilters((current) => ({ ...current, ...patch }))}
        onReset={resetFilters}
        onApply={applyFilters}
        onRefresh={() => {
          setReloadToken((value) => value + 1);
          onRefreshAll();
        }}
        loading={loading}
        exporting={exporting}
        onExport={() => void exportCsv()}
        facets={facets}
        columns={columns}
        onColumnsChange={onColumnsChange}
        kind={kind}
        onKindChange={handleKindChange}
        preset={preset}
        onPresetChange={handlePresetChange}
        customFrom={customFrom}
        customTo={customTo}
        onCustomFromChange={handleCustomFromChange}
        onCustomToChange={handleCustomToChange}
        baseFilters={baseFilters}
        onBaseFiltersChange={handleBaseFiltersChange}
        rangeFrom={rangeFrom}
        rangeTo={rangeTo}
        metrics={metrics}
      />
      {error && <div className={styles.error}>{error}</div>}
      <UsageLogsTable
        events={events}
        columns={columns}
        currency={currency}
        loading={loading}
        emptyAction={
          preset !== '24h' && !hasEventFilters ? (
            <Button size="sm" variant="secondary" onClick={() => handlePresetChange('24h')}>
              {t('usage_analytics.log_try_24h')}
            </Button>
          ) : undefined
        }
        onSelect={setSelected}
      />
      <div className={styles.paginationBar}>
        <Pagination
          page={pageIndex + 1}
          pageSize={pageSize}
          total={effectiveTotal}
          pageSizeOptions={[25, 50, 100, 200]}
          onPageChange={(page) => {
            setPageIndex(Math.max(0, page - 1));
          }}
          onPageSizeChange={handlePageSizeChange}
        />
      </div>
      <UsageLogDetailSheet event={selected} onClose={() => setSelected(null)} />
    </div>
  );
}
