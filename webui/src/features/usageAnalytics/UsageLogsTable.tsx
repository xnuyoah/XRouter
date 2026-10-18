import type { ReactNode } from 'react';
import { useTranslation } from 'react-i18next';

import { ChannelLogo } from '@/components/common/ChannelLogo';
import { EmptyState } from '@/components/ui/EmptyState';
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/Table/Table';
import { IconKey } from '@/components/ui/icons';
import type { UsageEvent } from '@/services/api';
import { formatCompactNumber } from '@/utils/format';

import {
  formatLogDuration,
  formatLogTableTime,
  formatUsageCost,
  formatUsageTokens,
  formatUsageTps,
} from './format';
import type { UsageLogColumnKey } from './usageLogColumns';
import styles from './UsageLogsTable.module.scss';

interface UsageLogsTableProps {
  events: UsageEvent[];
  columns: UsageLogColumnKey[];
  currency?: string;
  loading?: boolean;
  emptyAction?: ReactNode;
  onSelect: (event: UsageEvent) => void;
}

function shortId(value: string): string {
  return value.length > 14 ? `${value.slice(0, 14)}…` : value;
}

/**
 * Search events carry `provider=web-search`, so their real brand is the
 * channel. LLM events use the provider and keep the channel as a sub-label.
 */
function eventBrand(event: UsageEvent): { value: string; label: string; secondary?: string } {
  if (event.kind === 'search') {
    return {
      value: event.channel || event.provider || '',
      label: event.channel || event.provider || '—',
      secondary:
        event.channel && event.provider && event.provider !== event.channel
          ? event.provider
          : undefined,
    };
  }
  return {
    value: event.provider || event.channel || '',
    label: event.provider || event.channel || '—',
    secondary: event.channel && event.channel !== event.provider ? event.channel : undefined,
  };
}

function accountName(event: UsageEvent): string {
  return event.account_name || event.account_id || '—';
}

function accountInitial(event: UsageEvent): string {
  const name = accountName(event);
  const initial = name.trim().charAt(0);
  return initial ? initial.toUpperCase() : '?';
}

function latencyTone(event: UsageEvent): 'good' | 'warning' | 'critical' | 'idle' {
  const value = event.latency_ms ?? 0;
  if (value <= 0) return 'idle';
  if (value < 5000) return 'good';
  if (value < 20000) return 'warning';
  return 'critical';
}

function StatusText({ event }: { event: UsageEvent }) {
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
        <span className={styles.modelName}>
          <ChannelLogo value={event.channel || event.provider || ''} size={18} />
          <span>{event.operation || event.path || '—'}</span>
        </span>
        {event.path && <span className={styles.muted}>{event.path}</span>}
      </div>
    );
  }
  const model = event.alias || event.model || '—';
  const upstream =
    event.upstream_model && event.upstream_model !== model ? event.upstream_model : '';
  return (
    <div className={styles.stack}>
      <span className={styles.modelName}>
        <ChannelLogo value={event.provider || event.channel || ''} size={18} />
        <span>{model}</span>
      </span>
      {upstream && <span className={styles.muted}>↳ {upstream}</span>}
    </div>
  );
}

function TokenCell({ event }: { event: UsageEvent }) {
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
    <span className={styles.tokenPair}>
      {formatUsageTokens(tokens.input, 'full')} / {formatUsageTokens(tokens.output, 'full')}
    </span>
  );
}

function CostCell({ event, currency }: { event: UsageEvent; currency?: string }) {
  return (
    <div className={styles.stack}>
      <span className={styles.costPill}>{formatUsageCost(event.cost, event.currency || currency)}</span>
      {event.upstream_cost != null && (
        <span className={styles.muted}>
          upstream {formatUsageCost(event.upstream_cost, event.upstream_currency || currency)}
        </span>
      )}
    </div>
  );
}

function RequestCell({ event }: { event: UsageEvent }) {
  const { t } = useTranslation();
  return (
    <div className={styles.stack}>
      <span>{event.stream ? t('usage_analytics.log_stream') : t('usage_analytics.log_non_stream')}</span>
      <span className={styles.muted}>
        {event.tps ? formatUsageTps(event.tps) : '—'}
        {(event.attempts ?? 0) > 1 ? ` · ×${event.attempts}` : ''}
        {event.proxy_used ? ` · ${t('usage_analytics.proxy_short')}` : ''}
      </span>
    </div>
  );
}

function DetailCell({ event }: { event: UsageEvent }) {
  const { t } = useTranslation();
  if (!event.success) {
    const reason =
      event.error_code ||
      ((event.status_code ?? 0) > 0 ? `HTTP ${event.status_code}` : t('usage_analytics.failed'));
    return (
      <div className={styles.stack}>
        <span className={styles.detailError} title={reason}>
          {reason}
        </span>
        {event.path && <span className={styles.muted}>{event.path}</span>}
      </div>
    );
  }
  const plan = event.plan || event.service_tier || t('usage_analytics.log_standard');
  return (
    <div className={styles.stack}>
      <span className={styles.detailPlan}>{plan}</span>
      <span className={styles.muted}>
        {event.quota_kind && event.quota_kind !== plan
          ? event.quota_kind
          : event.service_tier && event.service_tier !== plan
            ? event.service_tier
            : event.reasoning_effort || '—'}
      </span>
    </div>
  );
}

function LogRow({
  event,
  columns,
  currency,
  onSelect,
}: {
  event: UsageEvent;
  columns: UsageLogColumnKey[];
  currency?: string;
  onSelect: (event: UsageEvent) => void;
}) {
  const { t } = useTranslation();
  const cellFor = (column: UsageLogColumnKey) => {
    switch (column) {
      case 'time':
        return (
          <div className={styles.stack}>
            <span className={styles.timeValue}>{formatLogTableTime(event.time)}</span>
            <span className={styles.rowMeta}>
              <StatusText event={event} />
              {(event.status_code ?? 0) > 0 && <span>HTTP {event.status_code}</span>}
            </span>
          </div>
        );
      case 'kind':
        return (
          <span className={styles.kindBadge}>
            {event.kind === 'llm'
              ? t('usage_analytics.kind_llm')
              : t('usage_analytics.kind_search')}
          </span>
        );
      case 'provider': {
        const brand = eventBrand(event);
        return (
          <div className={styles.stack}>
            <span className={styles.identity}>
              <ChannelLogo value={brand.value} label={brand.label} size={18} />
              <span className={styles.channelLabel}>{brand.label}</span>
            </span>
            {brand.secondary && <span className={styles.muted}>{brand.secondary}</span>}
            {event.executor_type && <span className={styles.muted}>{event.executor_type}</span>}
          </div>
        );
      }
      case 'account':
        return (
          <div className={styles.accountCell}>
            <span className={styles.avatar} aria-hidden="true">
              {accountInitial(event)}
            </span>
            <div className={styles.stack}>
              <span>{accountName(event)}</span>
              {event.account_id && event.account_id !== event.account_name && (
                <span className={styles.muted}>{shortId(event.account_id)}</span>
              )}
            </div>
          </div>
        );
      case 'token':
        return (
          <div className={styles.stack}>
            <span className={styles.tokenName}>
              <IconKey size={15} />
              <span>{event.api_key_masked || '—'}</span>
            </span>
            <span className={styles.muted}>
              {event.plan || event.quota_kind || t('usage_analytics.log_token_default')}
            </span>
          </div>
        );
      case 'model':
        return <ModelCell event={event} />;
      case 'tokens':
        return <TokenCell event={event} />;
      case 'cost':
        return <CostCell event={event} currency={currency} />;
      case 'latency':
        return (
          <div className={styles.latencyCell}>
            <span
              className={`${styles.latencyBar} ${styles[`latency_${latencyTone(event)}`]}`}
              aria-hidden="true"
            />
            <div className={styles.stack}>
              <span>{formatLogDuration(event.latency_ms)}</span>
              {event.ttft_ms ? (
                <span className={styles.muted}>TTFT {formatLogDuration(event.ttft_ms)}</span>
              ) : null}
            </div>
          </div>
        );
      case 'request':
        return <RequestCell event={event} />;
      case 'detail':
        return <DetailCell event={event} />;
      case 'plan':
        return (
          <div className={`${styles.stack} ${styles.planCell}`}>
            <span>{event.plan || event.quota_kind || '—'}</span>
            {event.plan && event.quota_kind && event.quota_kind !== event.plan && (
              <span className={styles.muted}>{event.quota_kind}</span>
            )}
          </div>
        );
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
    <TableRow
      className={styles.rowClickable}
      tabIndex={0}
      onClick={() => onSelect(event)}
      onKeyDown={(keyboardEvent) => {
        if (keyboardEvent.key === 'Enter' || keyboardEvent.key === ' ') {
          keyboardEvent.preventDefault();
          onSelect(event);
        }
      }}
    >
      {columns.map((column) => (
        <TableCell key={column}>{cellFor(column)}</TableCell>
      ))}
    </TableRow>
  );
}

export function UsageLogsTable({
  events,
  columns,
  currency,
  loading,
  emptyAction,
  onSelect,
}: UsageLogsTableProps) {
  const { t } = useTranslation();
  if (events.length === 0) {
    if (loading) return <div className={styles.loading}>{t('usage_analytics.log_loading')}</div>;
    return (
      <EmptyState
        title={t('usage_analytics.log_empty_title')}
        description={t('usage_analytics.log_empty_hint')}
        action={emptyAction}
      />
    );
  }

  return (
    <>
      <div className={styles.desktopTable}>
        <Table className={styles.logTable}>
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
                currency={currency}
                onSelect={onSelect}
              />
            ))}
          </TableBody>
        </Table>
      </div>
      <div className={styles.mobileList}>
        {events.map((event) => {
          const brand = eventBrand(event);
          return (
            <button
              key={event.id}
              type="button"
              className={styles.mobileCard}
              onClick={() => onSelect(event)}
            >
              <span className={styles.mobileTop}>
                <span>{formatLogTableTime(event.time)}</span>
                <StatusText event={event} />
              </span>
              <span className={styles.mobileModel}>
                {event.alias || event.model || event.operation || event.channel || '—'}
              </span>
              <span className={styles.mobileMeta}>
                <ChannelLogo value={brand.value} label={brand.label} size={14} />
                <span>{brand.label}</span>
                {columns.includes('account') && (
                  <span>· {event.account_name || event.account_id || '—'}</span>
                )}
              </span>
              <span className={styles.mobileMetrics}>
                {columns.includes('tokens') && (
                  <span>
                    {event.kind === 'search'
                      ? event.credits
                        ? `${formatCompactNumber(event.credits)} ${t('usage_analytics.credits')}`
                        : formatCompactNumber(event.units ?? 0)
                      : `${formatUsageTokens(event.tokens.input, 'full')} / ${formatUsageTokens(
                          event.tokens.output,
                          'full'
                        )}`}
                  </span>
                )}
                {columns.includes('cost') && (
                  <span>{formatUsageCost(event.cost, event.currency || currency)}</span>
                )}
                {columns.includes('latency') && (
                  <span>{formatLogDuration(event.latency_ms)}</span>
                )}
              </span>
            </button>
          );
        })}
      </div>
    </>
  );
}
