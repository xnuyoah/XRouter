import { useTranslation } from 'react-i18next';

import { Sheet } from '@/components/ui/Sheet/Sheet';
import type { UsageEvent } from '@/services/api';

import {
  formatUsageCost,
  formatUsageDuration,
  formatUsageTime,
  formatUsageTokens,
  formatUsageTps,
} from './format';
import styles from './UsageLogDetailSheet.module.scss';

interface UsageLogDetailSheetProps {
  event: UsageEvent | null;
  onClose: () => void;
}

interface FieldProps {
  label: string;
  value: React.ReactNode;
  wide?: boolean;
}

function Field({ label, value, wide }: FieldProps) {
  return (
    <div className={`${styles.field} ${wide ? styles.wide : ''}`}>
      <span className={styles.label}>{label}</span>
      <span className={styles.value}>{value ?? '—'}</span>
    </div>
  );
}

export function UsageLogDetailSheet({ event, onClose }: UsageLogDetailSheetProps) {
  const { t } = useTranslation();
  if (!event) {
    return <Sheet open={false} onClose={onClose} />;
  }
  const tokens = event.tokens;
  const statusLabel = event.success ? t('usage_analytics.success') : t('usage_analytics.failed');
  return (
    <Sheet
      open
      onClose={onClose}
      size="lg"
      eyebrow={t('usage_analytics.log_detail_title')}
      title={event.model || event.operation || event.id}
      description={`${formatUsageTime(event.time)} · ${statusLabel}`}
    >
      <div className={styles.content}>
        <section className={styles.section}>
          <h3>{t('usage_analytics.log_detail_request')}</h3>
          <div className={styles.grid}>
            <Field label={t('usage_analytics.request_id')} value={event.id} wide />
            <Field label={t('usage_analytics.trace_id')} value={event.trace_id || '—'} />
            <Field label={t('usage_analytics.time')} value={formatUsageTime(event.time)} />
            <Field
              label={t('usage_analytics.status')}
              value={`${statusLabel}${event.status_code ? ` · HTTP ${event.status_code}` : ''}`}
            />
            <Field label={t('usage_analytics.error_code')} value={event.error_code || '—'} />
            <Field label={t('usage_analytics.attempts')} value={event.attempts ?? 0} />
            <Field
              label={t('usage_analytics.stream')}
              value={event.stream ? t('usage_analytics.yes') : t('usage_analytics.no')}
            />
            <Field
              label={t('usage_analytics.proxy_used')}
              value={event.proxy_used ? t('usage_analytics.yes') : t('usage_analytics.no')}
            />
          </div>
        </section>

        <section className={styles.section}>
          <h3>{t('usage_analytics.log_detail_route')}</h3>
          <div className={styles.grid}>
            <Field label={t('usage_analytics.provider')} value={event.provider || '—'} />
            <Field label={t('usage_analytics.channel')} value={event.channel || '—'} />
            <Field
              label={t('usage_analytics.account')}
              value={event.account_name || event.account_id || '—'}
            />
            <Field label={t('usage_analytics.api_key')} value={event.api_key_masked || '—'} />
            <Field label={t('usage_analytics.source')} value={event.source || '—'} />
            <Field label={t('usage_analytics.auth_type')} value={event.auth_type || '—'} />
            <Field label={t('usage_analytics.executor')} value={event.executor_type || '—'} />
            <Field label={t('usage_analytics.service_tier')} value={event.service_tier || '—'} />
            <Field
              label={t('usage_analytics.reasoning_effort')}
              value={event.reasoning_effort || '—'}
            />
            <Field label={t('usage_analytics.plan')} value={event.plan || '—'} />
            <Field label={t('usage_analytics.quota_kind')} value={event.quota_kind || '—'} />
          </div>
        </section>

        <section className={styles.section}>
          <h3>{t('usage_analytics.log_detail_model')}</h3>
          <div className={styles.grid}>
            <Field label={t('usage_analytics.alias')} value={event.alias || '—'} />
            <Field label={t('usage_analytics.model')} value={event.model || '—'} />
            <Field
              label={t('usage_analytics.upstream_model')}
              value={event.upstream_model || '—'}
            />
            <Field label={t('usage_analytics.operation')} value={event.operation || '—'} />
            <Field label={t('usage_analytics.method')} value={event.method || '—'} />
            <Field label={t('usage_analytics.path')} value={event.path || '—'} wide />
          </div>
        </section>

        <section className={styles.section}>
          <h3>{t('usage_analytics.log_detail_tokens')}</h3>
          <div className={styles.grid}>
            <Field
              label={t('usage_analytics.tokens_input')}
              value={formatUsageTokens(tokens.input, 'full')}
            />
            <Field
              label={t('usage_analytics.tokens_uncached')}
              value={formatUsageTokens(tokens.uncached_input ?? tokens.input, 'full')}
            />
            <Field
              label={t('usage_analytics.tokens_output')}
              value={formatUsageTokens(tokens.output, 'full')}
            />
            <Field
              label={t('usage_analytics.tokens_reasoning')}
              value={formatUsageTokens(tokens.reasoning ?? 0, 'full')}
            />
            <Field
              label={t('usage_analytics.tokens_cache_read')}
              value={formatUsageTokens(tokens.cache_read, 'full')}
            />
            <Field
              label={t('usage_analytics.tokens_cache_write')}
              value={formatUsageTokens(tokens.cache_write, 'full')}
            />
            <Field
              label={t('usage_analytics.tokens_total')}
              value={formatUsageTokens(tokens.total, 'full')}
            />
            <Field label={t('usage_analytics.units')} value={event.units ?? '—'} />
            <Field label={t('usage_analytics.credits')} value={event.credits ?? '—'} />
          </div>
        </section>

        <section className={styles.section}>
          <h3>{t('usage_analytics.log_detail_cost_time')}</h3>
          <div className={styles.grid}>
            <Field
              label={t('usage_analytics.cost')}
              value={formatUsageCost(event.cost, event.currency)}
            />
            <Field
              label={t('usage_analytics.upstream_cost')}
              value={formatUsageCost(
                event.upstream_cost,
                event.upstream_currency || event.currency
              )}
            />
            <Field label={t('usage_analytics.currency')} value={event.currency || '—'} />
            <Field
              label={t('usage_analytics.price_revision')}
              value={event.price_revision ?? '—'}
            />
            <Field
              label={t('usage_analytics.latency')}
              value={formatUsageDuration(event.latency_ms)}
            />
            <Field label={t('usage_analytics.ttft')} value={formatUsageDuration(event.ttft_ms)} />
            <Field label={t('usage_analytics.tps')} value={formatUsageTps(event.tps)} />
          </div>
        </section>
      </div>
    </Sheet>
  );
}
