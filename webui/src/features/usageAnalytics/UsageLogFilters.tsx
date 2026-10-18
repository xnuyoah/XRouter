import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select, type SelectOption } from '@/components/ui/Select';
import {
  IconCalendar,
  IconChevronDown,
  IconDownload,
  IconEye,
  IconRefreshCw,
  IconSearch,
  IconSlidersHorizontal,
} from '@/components/ui/icons';
import type { UsageFacets } from '@/services/api';

import { formatLogTableTime, formatUsageCost } from './format';
import type { UsageBaseFilters, UsageLogFilterValues } from './usageQuery';
import { USAGE_LOG_COLUMNS, type UsageLogColumnKey } from './usageLogColumns';
import styles from './UsageLogFilters.module.scss';

type KindFilter = 'all' | 'llm' | 'search';

const RANGE_PRESETS = ['today', '24h', '5h', '7d', '30d', 'month', 'custom'] as const;

interface UsageLogMetrics {
  cost: number | null;
  currency?: string;
  rpm: number;
  tpm: number;
}

interface UsageLogFiltersProps {
  values: UsageLogFilterValues;
  onChange: (patch: Partial<UsageLogFilterValues>) => void;
  onReset: () => void;
  onApply: () => void;
  onRefresh: () => void;
  loading: boolean;
  exporting: boolean;
  onExport: () => void;
  facets?: UsageFacets;
  columns: UsageLogColumnKey[];
  onColumnsChange: (columns: UsageLogColumnKey[]) => void;
  kind: KindFilter;
  onKindChange: (kind: KindFilter) => void;
  preset: string;
  onPresetChange: (preset: string) => void;
  customFrom: string;
  customTo: string;
  onCustomFromChange: (value: string) => void;
  onCustomToChange: (value: string) => void;
  baseFilters: UsageBaseFilters;
  onBaseFiltersChange: (patch: Partial<UsageBaseFilters>) => void;
  rangeFrom?: string;
  rangeTo?: string;
  metrics: UsageLogMetrics;
}

function options(values: string[] | undefined, allLabel: string): SelectOption[] {
  return [
    { value: '', label: allLabel },
    ...(values ?? []).map((value) => ({ value, label: value })),
  ];
}

function rangeLabel(value: string | undefined): string {
  if (!value) return '—';
  return formatLogTableTime(value).slice(0, 16);
}

function formatRateMetric(value: number): string {
  if (!Number.isFinite(value)) return '—';
  if (value >= 100) return Math.round(value).toLocaleString();
  if (value >= 10) return value.toFixed(0);
  if (value >= 1) return value.toFixed(1);
  return value.toFixed(2);
}

export function UsageLogFilters({
  values,
  onChange,
  onReset,
  onApply,
  onRefresh,
  loading,
  exporting,
  onExport,
  facets,
  columns,
  onColumnsChange,
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
  rangeFrom,
  rangeTo,
  metrics,
}: UsageLogFiltersProps) {
  const { t } = useTranslation();
  const [columnsOpen, setColumnsOpen] = useState(false);
  const [advancedOpen, setAdvancedOpen] = useState(false);
  const allLabel = t('usage_analytics.all');

  const toggleColumn = (column: UsageLogColumnKey) => {
    if (columns.includes(column)) {
      if (columns.length <= 1) return;
      onColumnsChange(columns.filter((value) => value !== column));
      return;
    }
    onColumnsChange(
      USAGE_LOG_COLUMNS.filter((entry) => columns.includes(entry.key) || entry.key === column).map(
        (entry) => entry.key
      )
    );
  };

  return (
    <div className={styles.toolbar}>
      <div className={styles.filterRow}>
        <div className={styles.rangeField}>
          <IconCalendar size={16} />
          <Select
            value={preset}
            options={RANGE_PRESETS.map((value) => ({
              value,
              label: t(`usage_analytics.range_${value}`),
            }))}
            onChange={onPresetChange}
            ariaLabel={t('usage_analytics.range')}
            className={styles.rangeSelect}
            size="sm"
          />
          {preset === 'custom' ? (
            <div className={styles.customRange}>
              <Input
                type="datetime-local"
                value={customFrom}
                onChange={(event) => onCustomFromChange(event.target.value)}
                aria-label={t('usage_analytics.from')}
                className={styles.customInput}
              />
              <span>~</span>
              <Input
                type="datetime-local"
                value={customTo}
                onChange={(event) => onCustomToChange(event.target.value)}
                aria-label={t('usage_analytics.to')}
                className={styles.customInput}
              />
            </div>
          ) : (
            <span className={styles.rangeText}>
              {rangeLabel(rangeFrom)} ~ {rangeLabel(rangeTo)}
            </span>
          )}
        </div>

        <Input
          className={styles.keywordInput}
          value={values.q}
          onChange={(event) => onChange({ q: event.target.value })}
          placeholder={t('usage_analytics.log_search_placeholder')}
          aria-label={t('usage_analytics.log_search')}
          maxLength={200}
          onKeyDown={(event) => {
            if (event.key === 'Enter') onApply();
          }}
        />

        <Select
          value={kind}
          options={[
            { value: 'all', label: t('usage_analytics.log_all_types') },
            { value: 'llm', label: t('usage_analytics.kind_llm') },
            { value: 'search', label: t('usage_analytics.kind_search') },
          ]}
          onChange={(value) => onKindChange(value as KindFilter)}
          ariaLabel={t('usage_analytics.log_all_types')}
          className={styles.kindSelect}
          size="sm"
        />

        <Button
          variant="ghost"
          size="sm"
          onClick={() => setAdvancedOpen((value) => !value)}
          aria-expanded={advancedOpen}
        >
          <IconSlidersHorizontal size={15} />
          {advancedOpen ? t('usage_analytics.log_collapse') : t('usage_analytics.log_expand')}
          <IconChevronDown
            size={14}
            className={advancedOpen ? styles.chevronOpen : undefined}
          />
        </Button>
      </div>

      <div className={styles.metaRow}>
        <div className={styles.metrics}>
          <span>
            <small>{t('usage_analytics.log_metric_usage')}</small>
            <b>{formatUsageCost(metrics.cost, metrics.currency)}</b>
          </span>
          <span>
            <small>{t('usage_analytics.log_metric_rpm')}</small>
            <b>{formatRateMetric(metrics.rpm)}</b>
          </span>
          <span>
            <small>{t('usage_analytics.log_metric_tpm')}</small>
            <b>{formatRateMetric(metrics.tpm)}</b>
          </span>
        </div>
        <div className={styles.actions}>
          <Button variant="secondary" size="sm" onClick={onRefresh} loading={loading}>
            <IconRefreshCw size={14} />
            {t('usage_analytics.refresh')}
          </Button>
          <Button variant="secondary" size="sm" onClick={() => setColumnsOpen(true)}>
            <IconEye size={14} />
            {t('usage_analytics.log_columns_short')}
          </Button>
          <Button variant="secondary" size="sm" onClick={onExport} loading={exporting}>
            <IconDownload size={14} />
            {t('usage_analytics.export_csv')}
          </Button>
          <Button variant="ghost" size="sm" onClick={onReset}>
            {t('usage_analytics.reset_filters')}
          </Button>
          <Button variant="primary" size="sm" onClick={onApply}>
            <IconSearch size={14} />
            {t('usage_analytics.log_apply')}
          </Button>
        </div>
      </div>

      {advancedOpen && (
        <div className={styles.advancedPanel}>
          <div className={styles.advancedGroup}>
            <span className={styles.groupLabel}>{t('usage_analytics.log_col_provider')}</span>
            <div className={styles.advancedGrid}>
              <Select
                value={baseFilters.provider}
                options={options(facets?.providers, allLabel)}
                onChange={(value) => onBaseFiltersChange({ provider: value })}
                ariaLabel={t('usage_analytics.provider')}
                size="sm"
              />
              <Select
                value={baseFilters.model}
                options={options(facets?.models, allLabel)}
                onChange={(value) => onBaseFiltersChange({ model: value })}
                ariaLabel={t('usage_analytics.model')}
                size="sm"
              />
              <Select
                value={baseFilters.channel}
                options={options(facets?.channels, allLabel)}
                onChange={(value) => onBaseFiltersChange({ channel: value })}
                ariaLabel={t('usage_analytics.channel')}
                size="sm"
              />
              <Select
                value={baseFilters.operation}
                options={options(facets?.operations, allLabel)}
                onChange={(value) => onBaseFiltersChange({ operation: value })}
                ariaLabel={t('usage_analytics.operation')}
                size="sm"
              />
              <Select
                value={baseFilters.plan}
                options={options(facets?.plans, allLabel)}
                onChange={(value) => onBaseFiltersChange({ plan: value })}
                ariaLabel={t('usage_analytics.plan')}
                size="sm"
              />
              <Select
                value={baseFilters.status}
                options={[
                  { value: '', label: allLabel },
                  { value: 'success', label: t('usage_analytics.success') },
                  { value: 'failed', label: t('usage_analytics.failed') },
                ]}
                onChange={(value) => onBaseFiltersChange({ status: value })}
                ariaLabel={t('usage_analytics.status')}
                size="sm"
              />
            </div>
          </div>

          <div className={styles.advancedGroup}>
            <span className={styles.groupLabel}>{t('usage_analytics.log_filter_advanced')}</span>
            <div className={styles.advancedGrid}>
              <Input
                value={values.accountId}
                onChange={(event) => onChange({ accountId: event.target.value })}
                placeholder={t('usage_analytics.log_filter_account')}
                aria-label={t('usage_analytics.log_filter_account')}
                className={styles.monoInput}
              />
              <Input
                value={values.requestId}
                onChange={(event) => onChange({ requestId: event.target.value })}
                placeholder={t('usage_analytics.request_id')}
                aria-label={t('usage_analytics.request_id')}
                className={styles.monoInput}
              />
              <Input
                value={values.traceId}
                onChange={(event) => onChange({ traceId: event.target.value })}
                placeholder={t('usage_analytics.trace_id')}
                aria-label={t('usage_analytics.trace_id')}
                className={styles.monoInput}
              />
              <Input
                type="number"
                min={0}
                value={values.statusCode}
                onChange={(event) => onChange({ statusCode: event.target.value })}
                placeholder={t('usage_analytics.log_filter_status_code')}
                aria-label={t('usage_analytics.log_filter_status_code')}
              />
              <Select
                value={values.source}
                options={options(facets?.sources, allLabel)}
                onChange={(value) => onChange({ source: value })}
                ariaLabel={t('usage_analytics.source')}
                size="sm"
              />
              <Select
                value={values.authType}
                options={options(facets?.auth_types, allLabel)}
                onChange={(value) => onChange({ authType: value })}
                ariaLabel={t('usage_analytics.auth_type')}
                size="sm"
              />
              <Select
                value={values.executor}
                options={options(facets?.executors, allLabel)}
                onChange={(value) => onChange({ executor: value })}
                ariaLabel={t('usage_analytics.executor')}
                size="sm"
              />
              <Select
                value={values.serviceTier}
                options={options(facets?.service_tiers, allLabel)}
                onChange={(value) => onChange({ serviceTier: value })}
                ariaLabel={t('usage_analytics.service_tier')}
                size="sm"
              />
              <Select
                value={values.reasoningEffort}
                options={options(facets?.reasoning_efforts, allLabel)}
                onChange={(value) => onChange({ reasoningEffort: value })}
                ariaLabel={t('usage_analytics.reasoning_effort')}
                size="sm"
              />
            </div>
          </div>
        </div>
      )}

      <Modal
        open={columnsOpen}
        onClose={() => setColumnsOpen(false)}
        title={t('usage_analytics.log_columns')}
        width={420}
      >
        <p className={styles.columnsHint}>{t('usage_analytics.log_columns_hint')}</p>
        <div className={styles.columnList}>
          {USAGE_LOG_COLUMNS.map((column) => (
            <label key={column.key} className={styles.columnItem}>
              <input
                type="checkbox"
                checked={columns.includes(column.key)}
                onChange={() => toggleColumn(column.key)}
              />
              {t(column.labelKey)}
            </label>
          ))}
        </div>
      </Modal>
    </div>
  );
}
