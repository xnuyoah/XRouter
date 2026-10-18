import { useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select, type SelectOption } from '@/components/ui/Select';
import type { UsageFacets } from '@/services/api';

import type { UsageLogFilterValues } from './usageQuery';
import { USAGE_LOG_COLUMNS, type UsageLogColumnKey } from './usageLogColumns';
import styles from './UsageLogFilters.module.scss';

interface UsageLogFiltersProps {
  values: UsageLogFilterValues;
  onChange: (patch: Partial<UsageLogFilterValues>) => void;
  onReset: () => void;
  onRefresh: () => void;
  onExport: () => void;
  loading: boolean;
  exporting: boolean;
  facets?: UsageFacets;
  columns: UsageLogColumnKey[];
  onColumnsChange: (columns: UsageLogColumnKey[]) => void;
  pageSize: number;
  onPageSizeChange: (size: number) => void;
}

function options(values: string[] | undefined, allLabel: string): SelectOption[] {
  return [
    { value: '', label: allLabel },
    ...(values ?? []).map((value) => ({ value, label: value })),
  ];
}

export function UsageLogFilters({
  values,
  onChange,
  onReset,
  onRefresh,
  onExport,
  loading,
  exporting,
  facets,
  columns,
  onColumnsChange,
  pageSize,
  onPageSizeChange,
}: UsageLogFiltersProps) {
  const { t } = useTranslation();
  const [columnsOpen, setColumnsOpen] = useState(false);
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
      <div className={styles.primaryRow}>
        <Input
          className={styles.search}
          value={values.q}
          onChange={(event) => onChange({ q: event.target.value })}
          placeholder={t('usage_analytics.log_search_placeholder')}
          aria-label={t('usage_analytics.log_search')}
          maxLength={200}
        />
        <Input
          className={styles.monoInput}
          value={values.requestId}
          onChange={(event) => onChange({ requestId: event.target.value })}
          placeholder={t('usage_analytics.request_id')}
          aria-label={t('usage_analytics.request_id')}
        />
        <Input
          className={styles.accountInput}
          value={values.accountId}
          onChange={(event) => onChange({ accountId: event.target.value })}
          placeholder={t('usage_analytics.log_filter_account')}
          aria-label={t('usage_analytics.log_filter_account')}
        />
        <Input
          className={styles.statusInput}
          type="number"
          min={0}
          value={values.statusCode}
          onChange={(event) => onChange({ statusCode: event.target.value })}
          placeholder={t('usage_analytics.log_filter_status_code')}
          aria-label={t('usage_analytics.log_filter_status_code')}
        />
        <Select
          value={String(pageSize)}
          options={[25, 50, 100, 200].map((value) => ({
            value: String(value),
            label: `${value} / ${t('usage_analytics.page_size')}`,
          }))}
          onChange={(value) => onPageSizeChange(Number(value))}
          ariaLabel={t('usage_analytics.page_size')}
        />
        <Button variant="secondary" size="sm" onClick={onRefresh} loading={loading}>
          {t('usage_analytics.refresh')}
        </Button>
        <Button variant="secondary" size="sm" onClick={() => setColumnsOpen(true)}>
          {t('usage_analytics.log_columns')}
        </Button>
        <Button variant="secondary" size="sm" onClick={onExport} loading={exporting}>
          {t('usage_analytics.export_csv')}
        </Button>
        <Button variant="ghost" size="sm" onClick={onReset}>
          {t('usage_analytics.reset_filters')}
        </Button>
      </div>

      <details className={styles.advanced}>
        <summary>{t('usage_analytics.log_filter_advanced')}</summary>
        <div className={styles.advancedGrid}>
          <Input
            className={styles.monoInput}
            value={values.traceId}
            onChange={(event) => onChange({ traceId: event.target.value })}
            placeholder={t('usage_analytics.trace_id')}
            aria-label={t('usage_analytics.trace_id')}
          />
          <Select
            value={values.source}
            options={options(facets?.sources, allLabel)}
            onChange={(value) => onChange({ source: value })}
            ariaLabel={t('usage_analytics.source')}
          />
          <Select
            value={values.authType}
            options={options(facets?.auth_types, allLabel)}
            onChange={(value) => onChange({ authType: value })}
            ariaLabel={t('usage_analytics.auth_type')}
          />
          <Select
            value={values.executor}
            options={options(facets?.executors, allLabel)}
            onChange={(value) => onChange({ executor: value })}
            ariaLabel={t('usage_analytics.executor')}
          />
          <Select
            value={values.serviceTier}
            options={options(facets?.service_tiers, allLabel)}
            onChange={(value) => onChange({ serviceTier: value })}
            ariaLabel={t('usage_analytics.service_tier')}
          />
          <Select
            value={values.reasoningEffort}
            options={options(facets?.reasoning_efforts, allLabel)}
            onChange={(value) => onChange({ reasoningEffort: value })}
            ariaLabel={t('usage_analytics.reasoning_effort')}
          />
        </div>
      </details>

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
