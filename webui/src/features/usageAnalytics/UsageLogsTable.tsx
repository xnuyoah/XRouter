import { useTranslation } from 'react-i18next';

import { EmptyState } from '@/components/ui/EmptyState';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/Table/Table';
import type { UsageEvent } from '@/services/api';
import { formatCompactNumber } from '@/utils/format';

import {
  formatUsageCost,
  formatUsageDuration,
  formatUsageTime,
  formatUsageTokens,
  formatUsageTps,
  type TokenDisplayMode,
} from './format';
import type { UsageLogColumnKey } from './usageLogColumns';
import styles from './UsageLogsTable.module.scss';

interface UsageLogsTableProps {
  events: UsageEvent[];
  columns: UsageLogColumnKey[];
  tokenDisplayMode: TokenDisplayMode;
  currency?: string;
  loading?: boolean;
  onSelect: (event: UsageEvent) => void;
}

function kindLabel(kind: UsageEvent['kind'], t: (key: string) => string) {
  return kind === 'llm' ? t('usage_analytics.kind_llm') : t('usage_analytics.kind_search');
}

function shortId(value: string): string {
  return value.length > 14 ? `${value.slice(0, 14)}…` : value;
}

function StatusBadge({ event }: { event: UsageEvent }) {
  const { t } = useTranslation();
  return (
    <span className={event.success ? styles.statusSuccess : styles.statusFailed}>
      {event.success ? t('usage_analytics.success') : t('usage_analytics.failed')}
    </span>
  );
}

function ModelCell({ event }: { event: UsageEvent }) {
  if (event.kind === 'search') {
    return (
      <div className={styles.stack}>
        <span>{event.operation || event.path || '—'}</span>
        {event.path && <span className={styles.muted}>{event.path}</span>}
      </div>
    );
  }
  const model = event.alias || event.model || '—';
  const upstream =
    event.upstream_model && event.upstream_model !== model ? event.upstream_model : '';
  return (
    <div className={styles.stack}>
      <span>{model}</span>
      {upstream && <span className={styles.muted}>↳ {upstream}</span>}
    </div>
  );
}

function TokenCell({ event, mode }: { event: UsageEvent; mode: TokenDisplayMode }) {
  const { t } = useTranslation();
  if (event.kind === 'search') {
    if (event.credits) {
      return (
        <span>
          {formatCompactNumber(event.credits)} {t('usage_analytics.credits')}
        </span>
      );
    }
    if (event.units) return <span>{formatCompactNumber(event.units)}</span>;
    return <span className={styles.muted}>—</span>;
  }
  const tokens = event.tokens;
  return (
    <div className={styles.stack}>
      <span>
        {formatUsageTokens(tokens.input, mode)} / {formatUsageTokens(tokens.output, mode)}
      </span>
      {(tokens.cache_read > 0 || tokens.cache_write > 0 || tokens.reasoning > 0) && (
        <span className={styles.muted}>
          {tokens.cache_read > 0 ? `R ${formatUsageTokens(tokens.cache_read, mode)}` : ''}
          {tokens.cache_write > 0 ? ` W ${formatUsageTokens(tokens.cache_write, mode)}` : ''}
          {tokens.reasoning > 0 ? ` · think ${formatUsageTokens(tokens.reasoning, mode)}` : ''}
        </span>
      )}
    </div>
  );
}

function CostCell({ event, currency }: { event: UsageEvent; currency?: string }) {
  return (
    <div className={styles.stack}>
      <span>{formatUsageCost(event.cost, event.currency || currency)}</span>
      {event.upstream_cost != null && (
        <span className={styles.muted}>
          upstream {formatUsageCost(event.upstream_cost, event.upstream_currency || currency)}
        </span>
      )}
    </div>
  );
}

function RequestTraits({ event }: { event: UsageEvent }) {
  const { t } = useTranslation();
  const traits = [];
  if (event.stream) traits.push('stream');
  if ((event.attempts ?? 0) > 1) traits.push(`×${event.attempts}`);
  if (event.proxy_used) traits.push('proxy');
  if (traits.length === 0) return <span className={styles.muted}>—</span>;
  return (
    <div className={styles.traits}>
      {traits.map((trait) => (
        <span key={trait} className={styles.badge}>
          {trait === 'proxy' ? t('usage_analytics.proxy_short') : trait}
        </span>
      ))}
    </div>
  );
}

function LogRow({
  event,
  columns,
  mode,
  currency,
  onSelect,
}: {
  event: UsageEvent;
  columns: UsageLogColumnKey[];
  mode: TokenDisplayMode;
  currency?: string;
  onSelect: (event: UsageEvent) => void;
}) {
  const { t } = useTranslation();
  const cellFor = (column: UsageLogColumnKey) => {
    switch (column) {
      case 'time':
        return (
          <div className={styles.stack}>
            <span>{formatUsageTime(event.time)}</span>
            <span className={styles.rowMeta}>
              <StatusBadge event={event} />
              {(event.status_code ?? 0) > 0 && <span>HTTP {event.status_code}</span>}
            </span>
          </div>
        );
      case 'kind':
        return <span className={styles.kindBadge}>{kindLabel(event.kind, t)}</span>;
      case 'provider':
        return (
          <div className={styles.stack}>
            <span>{event.provider || event.channel || '—'}</span>
            {event.channel && event.channel !== event.provider && (
              <span className={styles.muted}>{event.channel}</span>
            )}
            {event.executor_type && <span className={styles.muted}>{event.executor_type}</span>}
          </div>
        );
      case 'model':
        return <ModelCell event={event} />;
      case 'account':
        return (
          <div className={styles.stack}>
            <span>{event.account_name || event.account_id || '—'}</span>
            {event.api_key_masked && <span className={styles.muted}>{event.api_key_masked}</span>}
          </div>
        );
      case 'plan':
        return (
          <div className={styles.stack}>
            <span>{event.plan || event.quota_kind || '—'}</span>
            {event.plan && event.quota_kind && event.quota_kind !== event.plan && (
              <span className={styles.muted}>{event.quota_kind}</span>
            )}
          </div>
        );
      case 'tokens':
        return <TokenCell event={event} mode={mode} />;
      case 'cost':
        return <CostCell event={event} currency={currency} />;
      case 'latency':
        return (
          <div className={styles.stack}>
            <span>{formatUsageDuration(event.latency_ms)}</span>
            <span className={styles.muted}>
              {event.ttft_ms ? `TTFT ${formatUsageDuration(event.ttft_ms)}` : ''}
              {event.tps ? ` · ${formatUsageTps(event.tps)}` : ''}
            </span>
          </div>
        );
      case 'request':
        return <RequestTraits event={event} />;
      case 'request_id':
        return (
          <span className={styles.mono} title={event.id}>
            {shortId(event.id)}
          </span>
        );
      default:
        return null;
    }
  };
  return (
    <TableRow className={styles.rowClickable} onClick={() => onSelect(event)}>
      {columns.map((column) => (
        <TableCell key={column}>{cellFor(column)}</TableCell>
      ))}
    </TableRow>
  );
}

export function UsageLogsTable({
  events,
  columns,
  tokenDisplayMode,
  currency,
  loading,
  onSelect,
}: UsageLogsTableProps) {
  const { t } = useTranslation();
  if (events.length === 0) {
    if (loading) return <div className={styles.loading}>{t('usage_analytics.log_loading')}</div>;
    return (
      <EmptyState
        title={t('usage_analytics.no_data')}
        description={t('usage_analytics.no_data_hint')}
      />
    );
  }

  return (
    <>
      <Table className={styles.desktopTable}>
        <TableHeader>
          <tr>
            {columns.map((column) => (
              <TableHead key={column}>{t(`usage_analytics.log_col_${column}`)}</TableHead>
            ))}
          </tr>
        </TableHeader>
        <TableBody>
          {events.map((event) => (
            <LogRow
              key={event.id}
              event={event}
              columns={columns}
              mode={tokenDisplayMode}
              currency={currency}
              onSelect={onSelect}
            />
          ))}
        </TableBody>
      </Table>
      <div className={styles.mobileList}>
        {events.map((event) => (
          <button
            key={event.id}
            type="button"
            className={styles.mobileCard}
            onClick={() => onSelect(event)}
          >
            <span className={styles.mobileTop}>
              <span>{formatUsageTime(event.time)}</span>
              <StatusBadge event={event} />
            </span>
            <span className={styles.mobileModel}>
              {event.model || event.operation || event.channel || '—'}
            </span>
            <span className={styles.mobileMeta}>
              {event.provider || event.channel || '—'} · {event.account_name || '—'}
            </span>
            <span className={styles.mobileMetrics}>
              <span>
                {event.kind === 'search'
                  ? event.credits
                    ? `${formatCompactNumber(event.credits)} ${t('usage_analytics.credits')}`
                    : formatCompactNumber(event.units ?? 0)
                  : formatUsageTokens(event.tokens.total, tokenDisplayMode)}
              </span>
              <span>{formatUsageCost(event.cost, event.currency || currency)}</span>
              <span>{formatUsageDuration(event.latency_ms)}</span>
            </span>
          </button>
        ))}
      </div>
    </>
  );
}
