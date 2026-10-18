import { useCallback, useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Input } from '@/components/ui/Input';
import { Select, type SelectOption } from '@/components/ui/Select';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { useNotificationStore } from '@/stores';
import {
  usageAnalyticsApi,
  webSearchApi,
  type UsageAnalyticsResponse,
  type UsageAnalyticsStatus,
  type UsageGroup,
  type UsageQueryParams,
  type UsageSeriesPoint,
  type WebSearchAccount,
  type WebSearchMonitoring,
} from '@/services/api';
import { downloadBlob } from '@/utils/download';
import { formatCompactNumber, formatPercent } from '@/utils/format';
import { CostTrendChart } from './CostTrendChart';
import { LatencyDistributionChart } from './LatencyDistributionChart';
import { PricingDialog } from './PricingDialog';
import { exportChartPng } from './exportChartPng';
import {
  formatUsageCost,
  formatUsageDuration,
  formatUsageTime,
  formatUsageTokens,
  type TokenDisplayMode,
} from './format';
import { countAccountWarnings } from './searchAccountWarnings';
import { UsageDataDialog } from './UsageDataDialog';
import { UsageLogsTab } from './UsageLogsTab';
import { UsageSearchTab } from './UsageSearchTab';
import { UsageTabs, type UsageTabKey } from './UsageTabs';
import {
  DEFAULT_LOG_COLUMNS,
  normalizeLogColumns,
  type UsageLogColumnKey,
} from './usageLogColumns';
import type { UsageBaseFilters } from './usageQuery';

import styles from './UsageAnalyticsPage.module.scss';

type KindFilter = 'all' | 'llm' | 'search';
type RangePreset = 'today' | '24h' | '5h' | '7d' | '30d' | 'month' | 'custom';

const EMPTY_FILTERS: UsageBaseFilters = {
  provider: '',
  model: '',
  channel: '',
  operation: '',
  plan: '',
  status: '',
};

const RANGE_PRESETS: RangePreset[] = ['today', '24h', '5h', '7d', '30d', 'month', 'custom'];

function groupLabel(group: UsageGroup): string {
  if (group.key) return group.key;
  if (group.model) return group.model;
  if (group.channel) return group.operation ? `${group.channel}/${group.operation}` : group.channel;
  if (group.provider) return group.provider;
  return group.kind;
}

function kindLabel(kind: string, t: (key: string) => string): string {
  if (kind === 'llm') return t('usage_analytics.kind_llm');
  if (kind === 'search') return t('usage_analytics.kind_search');
  return kind;
}

function buildQuery(
  kind: KindFilter,
  preset: RangePreset,
  customFrom: string,
  customTo: string,
  groupBy: string,
  filters: UsageBaseFilters
): UsageQueryParams {
  const params: UsageQueryParams = {
    group_by: groupBy,
    provider: filters.provider,
    model: filters.model,
    channel: filters.channel,
    operation: filters.operation,
    plan: filters.plan,
    status: filters.status,
  };
  if (kind !== 'all') params.kind = kind;
  if (preset === 'custom') {
    if (customFrom) params.from = new Date(customFrom).toISOString();
    if (customTo) params.to = new Date(customTo).toISOString();
  } else {
    params.range = preset;
  }
  return params;
}

function selectOptions(values: string[] | undefined, allLabel: string): SelectOption[] {
  return [
    { value: '', label: allLabel },
    ...(values ?? []).map((value) => ({ value, label: value })),
  ];
}

function historyHoursForRange(preset: RangePreset, customFrom: string, customTo: string): number {
  const now = Date.now();
  switch (preset) {
    case '5h':
      return 5;
    case '24h':
      return 24;
    case '7d':
      return 24 * 7;
    case '30d':
      return 24 * 30;
    case 'today': {
      const midnight = new Date();
      midnight.setHours(0, 0, 0, 0);
      return Math.max(1, Math.ceil((now - midnight.getTime()) / 3_600_000));
    }
    case 'month': {
      const monthStart = new Date();
      monthStart.setDate(1);
      monthStart.setHours(0, 0, 0, 0);
      return Math.max(1, Math.ceil((now - monthStart.getTime()) / 3_600_000));
    }
    case 'custom': {
      const from = new Date(customFrom).getTime();
      const to = new Date(customTo).getTime();
      if (!Number.isNaN(from) && !Number.isNaN(to) && to > from) {
        return Math.max(1, Math.ceil((to - from) / 3_600_000));
      }
      return 24 * 30;
    }
    default:
      return 24 * 30;
  }
}

function aggregateSeries(
  series: UsageSeriesPoint[]
): Array<{ start: string; tokens: number; requests: number }> {
  const buckets = new Map<string, { start: string; tokens: number; requests: number }>();
  for (const point of series) {
    const existing = buckets.get(point.start) ?? { start: point.start, tokens: 0, requests: 0 };
    existing.tokens += point.tokens?.total ?? 0;
    existing.requests += point.requests ?? 0;
    buckets.set(point.start, existing);
  }
  return Array.from(buckets.values()).sort((left, right) => left.start.localeCompare(right.start));
}

function UsageTrendChart({
  series,
  svgRef,
}: {
  series: UsageSeriesPoint[];
  svgRef?: RefObject<SVGSVGElement | null>;
}) {
  const points = useMemo(() => aggregateSeries(series), [series]);
  if (points.length === 0) {
    return null;
  }
  const width = 900;
  const height = 220;
  const padding = { top: 18, right: 18, bottom: 28, left: 46 };
  const innerWidth = width - padding.left - padding.right;
  const innerHeight = height - padding.top - padding.bottom;
  const maxTokens = Math.max(1, ...points.map((point) => point.tokens));
  const maxRequests = Math.max(1, ...points.map((point) => point.requests));
  const step = points.length > 1 ? innerWidth / (points.length - 1) : 0;
  const tokenPoints = points.map((point, index) => {
    const x = padding.left + index * step;
    const y = padding.top + innerHeight - (point.tokens / maxTokens) * innerHeight;
    return `${x},${y}`;
  });
  const requestPoints = points.map((point, index) => {
    const x = padding.left + index * step;
    const y = padding.top + innerHeight - (point.requests / maxRequests) * innerHeight;
    return `${x},${y}`;
  });
  const area = [
    `M ${padding.left},${padding.top + innerHeight}`,
    ...tokenPoints.map((point) => `L ${point}`),
    `L ${padding.left + (points.length - 1) * step},${padding.top + innerHeight}`,
    'Z',
  ].join(' ');

  return (
    <svg
      ref={svgRef}
      className={styles.chart}
      viewBox={`0 0 ${width} ${height}`}
      role="img"
      aria-label="usage trend"
    >
      <defs>
        <linearGradient id="usage-token-fill" x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor="var(--primary-color)" stopOpacity="0.28" />
          <stop offset="100%" stopColor="var(--primary-color)" stopOpacity="0.02" />
        </linearGradient>
      </defs>
      {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
        const y = padding.top + innerHeight * ratio;
        return (
          <line
            key={ratio}
            x1={padding.left}
            y1={y}
            x2={width - padding.right}
            y2={y}
            className={styles.gridLine}
          />
        );
      })}
      <path d={area} fill="url(#usage-token-fill)" />
      <polyline points={tokenPoints.join(' ')} className={styles.tokenLine} />
      <polyline points={requestPoints.join(' ')} className={styles.requestLine} />
      <text x={padding.left} y={height - 8} className={styles.axisLabel}>
        {formatUsageTime(points[0].start)}
      </text>
      <text x={width - padding.right} y={height - 8} textAnchor="end" className={styles.axisLabel}>
        {formatUsageTime(points[points.length - 1].start)}
      </text>
      <text x={padding.left} y={padding.top - 6} className={styles.axisLabel}>
        {formatCompactNumber(maxTokens)} tokens
      </text>
    </svg>
  );
}

function usageTabFromHash(): UsageTabKey {
  const hash = window.location.hash;
  const query = hash.includes('?') ? hash.slice(hash.indexOf('?') + 1) : '';
  const value = new URLSearchParams(query).get('tab');
  return value === 'logs' || value === 'search' ? value : 'overview';
}

export function UsageAnalyticsPage() {
  const { t } = useTranslation();
  const [kind, setKind] = useState<KindFilter>('all');
  const [preset, setPreset] = useState<RangePreset>('today');
  const [customFrom, setCustomFrom] = useState('');
  const [customTo, setCustomTo] = useState('');
  const [groupBy, setGroupBy] = useState('provider');
  const [tokenDisplayMode, setTokenDisplayMode] = useState<'compact' | 'full' | 'k' | 'm' | 'b'>(
    'compact'
  );
  const [pageSize, setPageSize] = useState(50);
  const [lowBalanceThreshold, setLowBalanceThreshold] = useState(10);
  const [logColumns, setLogColumns] = useState<UsageLogColumnKey[]>(DEFAULT_LOG_COLUMNS);
  const prefsLoaded = useRef(false);
  const [filters, setFilters] = useState<UsageBaseFilters>(EMPTY_FILTERS);
  const [analytics, setAnalytics] = useState<UsageAnalyticsResponse | null>(null);
  const [status, setStatus] = useState<UsageAnalyticsStatus | null>(null);
  const [monitoring, setMonitoring] = useState<WebSearchMonitoring | null>(null);
  const [searchAccounts, setSearchAccounts] = useState<WebSearchAccount[]>([]);
  const [searchAccountUsage, setSearchAccountUsage] = useState<UsageAnalyticsResponse | null>(null);
  const [pricingOpen, setPricingOpen] = useState(false);
  const [dataOpen, setDataOpen] = useState(false);
  const [exportingPng, setExportingPng] = useState(false);
  const chartRef = useRef<SVGSVGElement | null>(null);
  const loadSeq = useRef(0);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [loading, setLoading] = useState(true);
  const [refreshToken, setRefreshToken] = useState(0);
  const [error, setError] = useState('');
  const [disabled, setDisabled] = useState(false);

  const [tab, setTabState] = useState<UsageTabKey>(usageTabFromHash);
  const setTab = (next: UsageTabKey) => {
    setTabState(next);
    const hash = window.location.hash;
    const query = hash.includes('?') ? hash.slice(hash.indexOf('?') + 1) : '';
    const params = new URLSearchParams(query);
    if (next === 'overview') params.delete('tab');
    else params.set('tab', next);
    const base = hash.split('?')[0] || '#/usage';
    const rendered = params.toString();
    window.location.hash = rendered ? `${base}?${rendered}` : base;
  };

  useEffect(() => {
    const onHashChange = () => setTabState(usageTabFromHash());
    window.addEventListener('hashchange', onHashChange);
    return () => window.removeEventListener('hashchange', onHashChange);
  }, []);

  const params = useMemo(
    () => buildQuery(kind, preset, customFrom, customTo, groupBy, filters),
    [kind, preset, customFrom, customTo, groupBy, filters]
  );
  const historyHours = useMemo(
    () => historyHoursForRange(preset, customFrom, customTo),
    [preset, customFrom, customTo]
  );
  const searchWarningCounts = useMemo(
    () => countAccountWarnings(searchAccounts, lowBalanceThreshold, t),
    [searchAccounts, lowBalanceThreshold, t]
  );
  const series = analytics?.series ?? [];
  const historyTo = useMemo(() => {
    if (preset !== 'custom' || !customTo) return undefined;
    const parsed = new Date(customTo);
    return Number.isNaN(parsed.getTime()) ? undefined : parsed.toISOString();
  }, [preset, customTo]);

  const load = useCallback(async () => {
    const seq = ++loadSeq.current;
    if (preset === 'custom' && (!customFrom || !customTo)) {
      setLoading(false);
      return;
    }
    setLoading(true);
    setError('');
    setDisabled(false);
    try {
      const includeSearchAccounts = tab === 'search' || kind !== 'llm';
      const [analyticsResult, monitoringResult, accountUsageResult, accountResult, statusResult] =
        await Promise.all([
          usageAnalyticsApi.getAnalytics(params),
          webSearchApi.getMonitoring().catch(() => null),
          includeSearchAccounts
            ? usageAnalyticsApi
                .getAnalytics({
                  ...params,
                  kind: 'search',
                  group_by: 'account',
                  provider: '',
                  model: '',
                })
                .catch(() => null)
            : Promise.resolve(null),
          includeSearchAccounts
            ? webSearchApi.listAccounts().catch(() => [] as WebSearchAccount[])
            : Promise.resolve([] as WebSearchAccount[]),
          usageAnalyticsApi.getStatus().catch(() => null),
        ]);
      if (seq !== loadSeq.current) return;
      setAnalytics(analyticsResult);
      setStatus(statusResult);
      setMonitoring(monitoringResult);
      setSearchAccountUsage(accountUsageResult);
      setSearchAccounts(accountResult);
    } catch (err: unknown) {
      if (seq !== loadSeq.current) return;
      const status = (err as { status?: number })?.status;
      if (status === 503) {
        setDisabled(true);
        setAnalytics(null);
      } else {
        setError(err instanceof Error ? err.message : t('usage_analytics.load_failed'));
      }
    } finally {
      if (seq === loadSeq.current) {
        setLoading(false);
      }
    }
  }, [params, preset, customFrom, customTo, kind, tab, t]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    let cancelled = false;
    usageAnalyticsApi
      .getPreferences()
      .then((prefs) => {
        if (cancelled) return;
        setKind(prefs.kind);
        setPreset(
          prefs.range === 'custom' || !RANGE_PRESETS.includes(prefs.range as RangePreset)
            ? 'today'
            : (prefs.range as RangePreset)
        );
        setGroupBy(prefs.group_by || 'provider');
        setTokenDisplayMode(prefs.token_display_mode || 'compact');
        setPageSize(prefs.page_size || 50);
        setLowBalanceThreshold(prefs.low_balance_threshold_percent || 10);
        setLogColumns(normalizeLogColumns(prefs.log_columns));
      })
      .catch(() => undefined)
      .finally(() => {
        if (!cancelled) {
          prefsLoaded.current = true;
        }
      });
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    if (!prefsLoaded.current || disabled) return;
    const timer = window.setTimeout(() => {
      void usageAnalyticsApi
        .savePreferences({
          kind,
          range: preset,
          group_by: groupBy,
          token_display_mode: tokenDisplayMode,
          page_size: pageSize,
          low_balance_threshold_percent: lowBalanceThreshold,
          log_columns: logColumns,
        })
        .catch(() => undefined);
    }, 400);
    return () => window.clearTimeout(timer);
  }, [
    kind,
    preset,
    groupBy,
    tokenDisplayMode,
    pageSize,
    lowBalanceThreshold,
    logColumns,
    disabled,
  ]);

  useHeaderRefresh(() => {
    setRefreshToken((value) => value + 1);
    void load();
  }, true);

  const exportCsv = async () => {
    try {
      const blob = await usageAnalyticsApi.exportCsv({ ...params, limit: 1000 });
      downloadBlob({ filename: 'xrouter-usage.csv', blob });
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.export_failed'));
    }
  };

  const exportPng = async () => {
    if (!chartRef.current) return;
    setExportingPng(true);
    setError('');
    try {
      await exportChartPng(chartRef.current, 'xrouter-usage.png');
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.export_png_failed'));
    } finally {
      setExportingPng(false);
    }
  };

  const backup = async () => {
    try {
      const blob = await usageAnalyticsApi.backup();
      downloadBlob({ filename: 'xrouter-usage-backup.db', blob });
      showNotification(t('usage_analytics.backup_success'), 'success');
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.backup_failed'));
    }
  };

  const restore = async (file: File) => {
    if (!window.confirm(t('usage_analytics.restore_confirm'))) {
      return;
    }
    try {
      await usageAnalyticsApi.restore(file);
      showNotification(t('usage_analytics.restore_success'), 'success');
      await load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.restore_failed'));
    }
  };

  const importLegacy = async (file: File) => {
    if (!window.confirm(t('usage_analytics.import_confirm'))) {
      return;
    }
    try {
      const result = await usageAnalyticsApi.importTokensStatistic(file);
      showNotification(
        t('usage_analytics.import_success', {
          imported: result.imported,
          skipped: result.skipped,
          invalid: result.invalid,
        }),
        'success'
      );
      await load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.import_failed'));
    }
  };

  const reset = async () => {
    if (!window.confirm(t('usage_analytics.reset_confirm'))) return;
    try {
      await usageAnalyticsApi.reset();
      await load();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.reset_failed'));
    }
  };

  const summary = analytics?.summary;
  const facets = analytics?.facets;
  const groups = analytics?.groups ?? [];
  const showSearch = kind !== 'llm';
  const showLLM = kind !== 'search';

  const rangeOptions: SelectOption[] = RANGE_PRESETS.map((value) => ({
    value,
    label: t(`usage_analytics.range_${value}`),
  }));

  const groupOptions: SelectOption[] = [
    { value: 'provider', label: t('usage_analytics.group_provider') },
    { value: 'model', label: t('usage_analytics.group_model') },
    { value: 'channel', label: t('usage_analytics.group_channel') },
    { value: 'operation', label: t('usage_analytics.group_operation') },
    { value: 'plan', label: t('usage_analytics.group_plan') },
    { value: 'quota_kind', label: t('usage_analytics.group_quota_kind') },
    { value: 'account', label: t('usage_analytics.group_account') },
    { value: 'status', label: t('usage_analytics.group_status') },
    { value: 'status_code', label: t('usage_analytics.group_status_code') },
  ];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('usage_analytics.title')}</h1>
          <p>{t('usage_analytics.subtitle')}</p>
        </div>
        <div className={styles.headerActions}>
          <Button
            variant="secondary"
            onClick={() => {
              setRefreshToken((value) => value + 1);
              void load();
            }}
            loading={loading}
          >
            {t('usage_analytics.refresh')}
          </Button>
          <Button
            variant="secondary"
            onClick={() => void exportCsv()}
            disabled={disabled || !analytics}
          >
            {t('usage_analytics.export_csv')}
          </Button>
          <Button variant="secondary" onClick={() => setPricingOpen(true)} disabled={disabled}>
            {t('usage_analytics.pricing')}
          </Button>
          <Button variant="secondary" onClick={() => setDataOpen(true)}>
            {t('usage_analytics.data_title')}
          </Button>
        </div>
      </header>

      <section className={styles.controls}>
        {tab !== 'search' && (
          <div className={styles.kindTabs}>
            {(['all', 'llm', 'search'] as const).map((value) => (
              <Button
                key={value}
                size="sm"
                variant={kind === value ? 'primary' : 'secondary'}
                onClick={() => {
                  setKind(value);
                  setGroupBy(
                    value === 'search' ? 'channel' : value === 'llm' ? 'model' : 'provider'
                  );
                }}
              >
                {t(`usage_analytics.kind_${value}`)}
              </Button>
            ))}
          </div>
        )}
        <Select
          value={preset}
          options={rangeOptions}
          onChange={(value) => setPreset(value as RangePreset)}
          ariaLabel={t('usage_analytics.range')}
        />
        {preset === 'custom' && (
          <>
            <Input
              type="datetime-local"
              value={customFrom}
              onChange={(event) => setCustomFrom(event.target.value)}
              aria-label={t('usage_analytics.from')}
            />
            <Input
              type="datetime-local"
              value={customTo}
              onChange={(event) => setCustomTo(event.target.value)}
              aria-label={t('usage_analytics.to')}
            />
          </>
        )}
        {tab === 'overview' && (
          <Select
            value={groupBy}
            options={groupOptions}
            onChange={setGroupBy}
            ariaLabel={t('usage_analytics.group_by')}
          />
        )}
        {tab === 'overview' && (
          <Select
            value={tokenDisplayMode}
            options={[
              { value: 'compact', label: t('usage_analytics.token_display_compact') },
              { value: 'full', label: t('usage_analytics.token_display_full') },
              { value: 'k', label: t('usage_analytics.token_display_k') },
              { value: 'm', label: t('usage_analytics.token_display_m') },
              { value: 'b', label: t('usage_analytics.token_display_b') },
            ]}
            onChange={(value) => setTokenDisplayMode(value as TokenDisplayMode)}
            ariaLabel={t('usage_analytics.token_display')}
          />
        )}
        {tab !== 'search' && (
          <Select
            value={filters.provider}
            options={selectOptions(facets?.providers, t('usage_analytics.all'))}
            onChange={(value) => setFilters((current) => ({ ...current, provider: value }))}
            ariaLabel={t('usage_analytics.provider')}
          />
        )}
        {tab !== 'search' && showLLM && (
          <Select
            value={filters.model}
            options={selectOptions(facets?.models, t('usage_analytics.all'))}
            onChange={(value) => setFilters((current) => ({ ...current, model: value }))}
            ariaLabel={t('usage_analytics.model')}
          />
        )}
        {(tab === 'search' || showSearch) && (
          <>
            <Select
              value={filters.channel}
              options={selectOptions(facets?.channels, t('usage_analytics.all'))}
              onChange={(value) => setFilters((current) => ({ ...current, channel: value }))}
              ariaLabel={t('usage_analytics.channel')}
            />
            <Select
              value={filters.operation}
              options={selectOptions(facets?.operations, t('usage_analytics.all'))}
              onChange={(value) => setFilters((current) => ({ ...current, operation: value }))}
              ariaLabel={t('usage_analytics.operation')}
            />
          </>
        )}
        <Select
          value={filters.plan}
          options={selectOptions(facets?.plans, t('usage_analytics.all'))}
          onChange={(value) => setFilters((current) => ({ ...current, plan: value }))}
          ariaLabel={t('usage_analytics.plan')}
        />
        <Select
          value={filters.status}
          options={[
            { value: '', label: t('usage_analytics.all') },
            { value: 'success', label: t('usage_analytics.success') },
            { value: 'failed', label: t('usage_analytics.failed') },
          ]}
          onChange={(value) => setFilters((current) => ({ ...current, status: value }))}
          ariaLabel={t('usage_analytics.status')}
        />
      </section>

      <UsageTabs active={tab} onChange={setTab} />

      {disabled && (
        <div className={styles.notice}>
          <strong>{t('usage_analytics.disabled')}</strong>
          <span>{t('usage_analytics.disabled_hint')}</span>
        </div>
      )}
      {error && <div className={styles.error}>{error}</div>}

      {tab === 'overview' && (
        <>
          {!disabled && summary && (
            <section className={styles.cards}>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_requests')}</span>
                <strong>{formatCompactNumber(summary.requests)}</strong>
              </article>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_success_rate')}</span>
                <strong>{formatPercent(summary.success_rate * 100)}</strong>
              </article>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_tokens')}</span>
                <strong>{formatUsageTokens(summary.tokens?.total ?? 0, tokenDisplayMode)}</strong>
              </article>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_latency')}</span>
                <strong>{formatUsageDuration(summary.avg_latency_ms)}</strong>
              </article>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_p95')}</span>
                <strong>{formatUsageDuration(summary.p95_latency_ms)}</strong>
              </article>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_ttft')}</span>
                <strong>{formatUsageDuration(summary.avg_ttft_ms)}</strong>
              </article>
              {showSearch && (
                <article className={styles.card}>
                  <span>{t('usage_analytics.summary_search_units')}</span>
                  <strong>{formatCompactNumber(summary.search_units ?? 0)}</strong>
                </article>
              )}
              {showSearch && searchAccounts.length > 0 && (
                <article className={styles.card}>
                  <span>{t('usage_analytics.summary_search_warnings')}</span>
                  <strong
                    className={
                      searchWarningCounts.critical > 0
                        ? styles.cardDanger
                        : searchWarningCounts.warning > 0
                          ? styles.cardWarning
                          : undefined
                    }
                  >
                    {searchWarningCounts.critical} / {searchWarningCounts.warning}
                  </strong>
                </article>
              )}
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_cost')}</span>
                <strong>
                  {formatUsageCost(summary.estimated_cost, summary.currency ?? analytics?.currency)}
                </strong>
              </article>
              <article className={styles.card}>
                <span>{t('usage_analytics.summary_coverage')}</span>
                <strong>{formatPercent((summary.coverage ?? 0) * 100)}</strong>
              </article>
            </section>
          )}

          {!disabled && summary && (summary.coverage ?? 0) < 1 && (
            <div className={styles.notice}>
              <strong>{t('usage_analytics.unpriced_title')}</strong>
              <span>
                {t('usage_analytics.unpriced_hint')}
                {summary.unpriced_models?.length
                  ? `: ${summary.unpriced_models.slice(0, 8).join(', ')}`
                  : ''}
              </span>
            </div>
          )}

          {!disabled &&
            summary &&
            summary.failed > 0 &&
            (summary.failure_statuses?.length ?? 0) > 0 && (
              <section className={styles.panel}>
                <div className={styles.panelHeader}>
                  <h2>{t('usage_analytics.failure_status_title')}</h2>
                  <span>{t('usage_analytics.failure_status_hint')}</span>
                </div>
                <div className={styles.statusList}>
                  {summary.failure_statuses?.map((status) => {
                    const percent = summary.failed > 0 ? (status.count / summary.failed) * 100 : 0;
                    return (
                      <div key={status.status_code} className={styles.statusRow}>
                        <span className={styles.statusCode}>
                          {status.status_code > 0
                            ? status.status_code
                            : t('usage_analytics.unknown_status')}
                        </span>
                        <div className={styles.statusTrack}>
                          <span style={{ width: `${Math.max(2, percent)}%` }} />
                        </div>
                        <span>{formatCompactNumber(status.count)}</span>
                        <span>{formatPercent(percent)}</span>
                      </div>
                    );
                  })}
                </div>
              </section>
            )}

          {!disabled && summary && summary.latency_buckets?.some((bucket) => bucket.count > 0) && (
            <section className={styles.panel}>
              <div className={styles.panelHeader}>
                <h2>{t('usage_analytics.latency_distribution_title')}</h2>
                <span>{t('usage_analytics.latency_distribution_hint')}</span>
              </div>
              <LatencyDistributionChart
                buckets={summary.latency_buckets ?? []}
                p95={summary.p95_latency_ms}
              />
            </section>
          )}

          {!disabled && analytics && (
            <section className={styles.panel}>
              <div className={styles.panelHeader}>
                <h2>{t('usage_analytics.trend_title')}</h2>
                <span>{t('usage_analytics.trend_hint')}</span>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={() => void exportPng()}
                  loading={exportingPng}
                  disabled={series.length === 0}
                >
                  {t('usage_analytics.export_png')}
                </Button>
              </div>
              {series.length > 0 && (summary?.requests ?? 0) > 0 ? (
                <UsageTrendChart series={series} svgRef={chartRef} />
              ) : (
                <EmptyState
                  title={t('usage_analytics.no_data')}
                  description={t('usage_analytics.no_data_hint')}
                />
              )}
            </section>
          )}

          {!disabled && analytics && series.some((point) => point.cost != null) && (
            <section className={styles.panel}>
              <div className={styles.panelHeader}>
                <h2>{t('usage_analytics.cost_trend_title')}</h2>
                <span>{t('usage_analytics.cost_trend_hint')}</span>
              </div>
              <CostTrendChart series={series} currency={summary?.currency ?? analytics.currency} />
            </section>
          )}

          {!disabled && (
            <section className={styles.panel}>
              <div className={styles.panelHeader}>
                <h2>{t('usage_analytics.breakdown_title')}</h2>
                <span>{t('usage_analytics.breakdown_hint')}</span>
              </div>
              <div className={styles.tableWrap}>
                <table className={styles.table}>
                  <thead>
                    <tr>
                      <th>{t('usage_analytics.group_by')}</th>
                      <th>{t('usage_analytics.requests')}</th>
                      <th>{t('usage_analytics.success_rate')}</th>
                      <th>{t('usage_analytics.tokens_input')}</th>
                      <th>{t('usage_analytics.tokens_output')}</th>
                      <th>{t('usage_analytics.tokens_cache_read')}</th>
                      <th>{t('usage_analytics.tokens_cache_write')}</th>
                      <th>{t('usage_analytics.tokens_total')}</th>
                      <th>{t('usage_analytics.units')}</th>
                      <th>{t('usage_analytics.cost')}</th>
                      <th>{t('usage_analytics.latency')}</th>
                      <th>{t('usage_analytics.p95')}</th>
                      <th>{t('usage_analytics.ttft')}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {groups.map((group) => (
                      <tr key={`${group.kind}:${group.key}`}>
                        <td>
                          <span className={styles.kindBadge}>{kindLabel(group.kind, t)}</span>
                          <span>{groupLabel(group)}</span>
                        </td>
                        <td>{formatCompactNumber(group.requests)}</td>
                        <td>{formatPercent(group.success_rate * 100)}</td>
                        <td>{formatUsageTokens(group.tokens.input, tokenDisplayMode)}</td>
                        <td>{formatUsageTokens(group.tokens.output, tokenDisplayMode)}</td>
                        <td>{formatUsageTokens(group.tokens.cache_read, tokenDisplayMode)}</td>
                        <td>{formatUsageTokens(group.tokens.cache_write, tokenDisplayMode)}</td>
                        <td>{formatUsageTokens(group.tokens.total, tokenDisplayMode)}</td>
                        <td>{group.units ? formatCompactNumber(group.units) : '—'}</td>
                        <td>
                          {formatUsageCost(group.cost, summary?.currency ?? analytics?.currency)}
                        </td>
                        <td>{formatUsageDuration(group.avg_latency_ms)}</td>
                        <td>{formatUsageDuration(group.p95_latency_ms)}</td>
                        <td>{formatUsageDuration(group.avg_ttft_ms)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
                {groups.length === 0 && (
                  <EmptyState
                    title={t('usage_analytics.no_data')}
                    description={t('usage_analytics.no_data_hint')}
                  />
                )}
              </div>
            </section>
          )}
        </>
      )}

      {!disabled && tab === 'logs' && (
        <UsageLogsTab
          kind={kind}
          preset={preset}
          customFrom={customFrom}
          customTo={customTo}
          baseFilters={filters}
          facets={facets}
          tokenDisplayMode={tokenDisplayMode}
          currency={summary?.currency ?? analytics?.currency}
          pageSize={pageSize}
          onPageSizeChange={setPageSize}
          columns={logColumns}
          onColumnsChange={setLogColumns}
          refreshToken={refreshToken}
        />
      )}

      {!disabled && tab === 'search' && (
        <UsageSearchTab
          monitoring={monitoring}
          accounts={searchAccounts}
          accountUsage={searchAccountUsage}
          historyHours={historyHours}
          historyTo={historyTo}
          lowBalanceThreshold={lowBalanceThreshold}
          onLowBalanceThresholdChange={setLowBalanceThreshold}
        />
      )}

      <PricingDialog
        open={pricingOpen}
        onClose={() => setPricingOpen(false)}
        onSaved={() => void load()}
      />
      <UsageDataDialog
        open={dataOpen}
        onClose={() => setDataOpen(false)}
        status={status}
        disabled={disabled}
        onBackup={() => void backup()}
        onRestore={(file) => void restore(file)}
        onImport={(file) => void importLegacy(file)}
        onReset={() => void reset()}
      />
    </div>
  );
}
