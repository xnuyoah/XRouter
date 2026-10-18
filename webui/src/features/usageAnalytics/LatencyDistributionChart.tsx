import { useTranslation } from 'react-i18next';

import type { UsageLatencyBucket } from '@/services/api';
import { formatCompactNumber, formatPercent } from '@/utils/format';

import styles from './LatencyDistributionChart.module.scss';

interface LatencyDistributionChartProps {
  buckets: UsageLatencyBucket[];
  p95: number;
}

function formatBound(ms: number): string {
  if (ms >= 1000) {
    const seconds = ms / 1000;
    return `${Number.isInteger(seconds) ? seconds : seconds.toFixed(1)}s`;
  }
  return `${ms}ms`;
}

function bucketLabel(bucket: UsageLatencyBucket): string {
  if (!bucket.upper_ms) {
    return `>${formatBound(bucket.lower_ms)}`;
  }
  if (bucket.lower_ms <= 0) {
    return `≤${formatBound(bucket.upper_ms)}`;
  }
  return `${formatBound(bucket.lower_ms)}–${formatBound(bucket.upper_ms)}`;
}

export function LatencyDistributionChart({ buckets, p95 }: LatencyDistributionChartProps) {
  const { t } = useTranslation();
  const total = buckets.reduce((sum, bucket) => sum + bucket.count, 0);
  if (total <= 0) return null;

  const p95Target = Math.ceil((total * 95) / 100);
  let cumulative = 0;
  let p95Index = -1;
  for (let index = 0; index < buckets.length; index += 1) {
    cumulative += buckets[index].count;
    if (cumulative >= p95Target) {
      p95Index = index;
      break;
    }
  }

  return (
    <div className={styles.wrap}>
      <span className={styles.samples}>
        {t('usage_analytics.latency_samples', { count: formatCompactNumber(total) })}
      </span>
      <div className={styles.list}>
        {buckets.map((bucket, index) => {
          const percent = total > 0 ? (bucket.count / total) * 100 : 0;
          const isP95 = index === p95Index;
          return (
            <div
              key={`${bucket.lower_ms}:${bucket.upper_ms ?? 0}`}
              className={`${styles.row} ${isP95 ? styles.rowP95 : ''}`}
            >
              <span className={styles.label}>
                {bucketLabel(bucket)}
                {isP95 && (
                  <em title={t('usage_analytics.latency_p95_marker')}>
                    {t('usage_analytics.latency_p95_marker')} · {formatBound(Math.round(p95))}
                  </em>
                )}
              </span>
              <div className={styles.track}>
                <span
                  className={styles.bar}
                  style={{ width: bucket.count > 0 ? `${Math.max(1.5, percent)}%` : '0%' }}
                />
              </div>
              <span className={styles.count}>{formatCompactNumber(bucket.count)}</span>
              <span className={styles.percent}>{formatPercent(percent)}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}
