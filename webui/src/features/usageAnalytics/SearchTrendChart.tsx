import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import type { UsageSeriesPoint } from '@/services/api';
import { formatCompactNumber, formatPercent } from '@/utils/format';

import styles from './SearchTrendChart.module.scss';

interface SearchTrendChartProps {
  series: UsageSeriesPoint[];
}

interface SearchBucket {
  start: string;
  success: number;
  failed: number;
  credits: number;
}

const WIDTH = 900;
const HEIGHT = 230;
const PADDING = { top: 20, right: 58, bottom: 30, left: 48 };

function aggregateSearch(series: UsageSeriesPoint[]): SearchBucket[] {
  const buckets = new Map<string, SearchBucket>();
  for (const point of series) {
    if (point.kind !== 'search') continue;
    const bucket = buckets.get(point.start) ?? {
      start: point.start,
      success: 0,
      failed: 0,
      credits: 0,
    };
    bucket.success += point.success ?? 0;
    bucket.failed += point.failed ?? 0;
    bucket.credits += point.credits ?? 0;
    buckets.set(point.start, bucket);
  }
  return Array.from(buckets.values()).sort((left, right) => left.start.localeCompare(right.start));
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function SearchTrendChart({ series }: SearchTrendChartProps) {
  const { t } = useTranslation();
  const buckets = useMemo(() => aggregateSearch(series), [series]);
  if (buckets.length === 0) return null;

  const totalSuccess = buckets.reduce((sum, bucket) => sum + bucket.success, 0);
  const totalFailed = buckets.reduce((sum, bucket) => sum + bucket.failed, 0);
  const totalRequests = totalSuccess + totalFailed;
  if (totalRequests === 0) return null;
  const totalCredits = buckets.reduce((sum, bucket) => sum + bucket.credits, 0);

  const innerWidth = WIDTH - PADDING.left - PADDING.right;
  const innerHeight = HEIGHT - PADDING.top - PADDING.bottom;
  const maxRequests = Math.max(1, ...buckets.map((bucket) => bucket.success + bucket.failed));
  const maxCredits = Math.max(0, ...buckets.map((bucket) => bucket.credits));
  const step = innerWidth / buckets.length;
  const barWidth = Math.max(2, Math.min(26, step * 0.58));
  const yForCredits = (value: number) =>
    PADDING.top + innerHeight - (value / maxCredits) * innerHeight;

  const creditPoints =
    maxCredits > 0
      ? buckets
          .map((bucket, index) => {
            const x = PADDING.left + step * index + step / 2;
            return `${x},${yForCredits(bucket.credits)}`;
          })
          .join(' ')
      : '';

  return (
    <div className={styles.wrap}>
      <div className={styles.legend}>
        <span>
          <i className={styles.successDot} />
          {t('usage_analytics.success')}
        </span>
        <span>
          <i className={styles.failedDot} />
          {t('usage_analytics.failed')}
        </span>
        {maxCredits > 0 && (
          <span>
            <i className={styles.creditsDot} />
            {t('usage_analytics.credits')}
          </span>
        )}
        <span className={styles.summary}>
          {t('usage_analytics.search_trend_summary', {
            requests: formatCompactNumber(totalRequests),
            success: formatCompactNumber(totalSuccess),
            failed: formatCompactNumber(totalFailed),
            rate: formatPercent(totalRequests > 0 ? (totalSuccess / totalRequests) * 100 : 0),
          })}
        </span>
        {totalCredits > 0 && (
          <span className={styles.summary}>
            {t('usage_analytics.search_trend_credits', {
              credits: formatCompactNumber(totalCredits),
            })}
          </span>
        )}
      </div>
      <svg
        className={styles.chart}
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label={t('usage_analytics.search_trend_title')}
      >
        {[0, 0.25, 0.5, 0.75, 1].map((ratio) => {
          const y = PADDING.top + innerHeight * ratio;
          return (
            <line
              key={ratio}
              x1={PADDING.left}
              y1={y}
              x2={WIDTH - PADDING.right}
              y2={y}
              className={styles.gridLine}
            />
          );
        })}
        <text x={4} y={PADDING.top + 4} className={styles.axisLabel}>
          {formatCompactNumber(maxRequests)}
        </text>
        <text x={4} y={HEIGHT - PADDING.bottom + 4} className={styles.axisLabel}>
          0
        </text>
        {maxCredits > 0 && (
          <text x={WIDTH - 4} y={PADDING.top + 4} textAnchor="end" className={styles.axisLabel}>
            {formatCompactNumber(maxCredits)}
          </text>
        )}
        {buckets.map((bucket, index) => {
          const center = PADDING.left + step * index + step / 2;
          const successHeight = (bucket.success / maxRequests) * innerHeight;
          const failedHeight = (bucket.failed / maxRequests) * innerHeight;
          const successY = PADDING.top + innerHeight - successHeight;
          const failedY = successY - failedHeight;
          return (
            <g key={bucket.start}>
              {bucket.success > 0 && (
                <rect
                  x={center - barWidth / 2}
                  y={successY}
                  width={barWidth}
                  height={Math.max(1, successHeight)}
                  rx={2}
                  className={styles.successBar}
                />
              )}
              {bucket.failed > 0 && (
                <rect
                  x={center - barWidth / 2}
                  y={failedY}
                  width={barWidth}
                  height={Math.max(1, failedHeight)}
                  rx={2}
                  className={styles.failedBar}
                />
              )}
              <rect
                x={center - step / 2}
                y={PADDING.top}
                width={step}
                height={innerHeight}
                fill="transparent"
              >
                <title>
                  {`${formatTime(bucket.start)} · ${t('usage_analytics.success')} ${formatCompactNumber(bucket.success)} · ${t('usage_analytics.failed')} ${formatCompactNumber(bucket.failed)}${bucket.credits > 0 ? ` · ${t('usage_analytics.credits')} ${formatCompactNumber(bucket.credits)}` : ''}`}
                </title>
              </rect>
            </g>
          );
        })}
        {maxCredits > 0 && <polyline points={creditPoints} className={styles.creditsLine} />}
        <text x={PADDING.left} y={HEIGHT - 8} className={styles.axisLabel}>
          {formatTime(buckets[0].start)}
        </text>
        <text
          x={WIDTH - PADDING.right}
          y={HEIGHT - 8}
          textAnchor="end"
          className={styles.axisLabel}
        >
          {formatTime(buckets[buckets.length - 1].start)}
        </text>
      </svg>
    </div>
  );
}
