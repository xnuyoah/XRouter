import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';

import type { UsageSeriesPoint } from '@/services/api';

import { formatUsageCost } from './format';
import styles from './CostTrendChart.module.scss';

interface CostTrendChartProps {
  series: UsageSeriesPoint[];
  currency?: string;
}

interface CostBucket {
  start: string;
  llm: number | null;
  search: number | null;
  total: number;
}

const WIDTH = 900;
const HEIGHT = 220;
const PADDING = { top: 20, right: 24, bottom: 30, left: 58 };

function aggregateCosts(series: UsageSeriesPoint[]): CostBucket[] {
  const buckets = new Map<string, CostBucket>();
  for (const point of series) {
    if (point.cost == null || !Number.isFinite(point.cost)) continue;
    const bucket = buckets.get(point.start) ?? {
      start: point.start,
      llm: null,
      search: null,
      total: 0,
    };
    if (point.kind === 'llm') {
      bucket.llm = (bucket.llm ?? 0) + point.cost;
    } else {
      bucket.search = (bucket.search ?? 0) + point.cost;
    }
    bucket.total += point.cost;
    buckets.set(point.start, bucket);
  }
  return Array.from(buckets.values()).sort((left, right) => left.start.localeCompare(right.start));
}

function formatTime(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function CostTrendChart({ series, currency }: CostTrendChartProps) {
  const { t } = useTranslation();
  const buckets = useMemo(() => aggregateCosts(series), [series]);
  if (buckets.length === 0) return null;

  const totalLlm = buckets.reduce((sum, bucket) => sum + (bucket.llm ?? 0), 0);
  const totalSearch = buckets.reduce((sum, bucket) => sum + (bucket.search ?? 0), 0);
  const totalCost = buckets.reduce((sum, bucket) => sum + bucket.total, 0);
  const hasLlm = buckets.some((bucket) => bucket.llm != null);
  const hasSearch = buckets.some((bucket) => bucket.search != null);
  const maxCost = Math.max(0, ...buckets.map((bucket) => bucket.total));
  const scale = maxCost > 0 ? maxCost : 1;

  const innerWidth = WIDTH - PADDING.left - PADDING.right;
  const innerHeight = HEIGHT - PADDING.top - PADDING.bottom;
  const step = buckets.length > 1 ? innerWidth / (buckets.length - 1) : 0;
  const xFor = (index: number) => PADDING.left + index * step;
  const yFor = (value: number) => PADDING.top + innerHeight - (value / scale) * innerHeight;

  const totalPoints = buckets.map((bucket, index) => `${xFor(index)},${yFor(bucket.total)}`);
  const area = [
    `M ${xFor(0)},${PADDING.top + innerHeight}`,
    ...totalPoints.map((point) => `L ${point}`),
    `L ${xFor(buckets.length - 1)},${PADDING.top + innerHeight}`,
    'Z',
  ].join(' ');
  const kindPoints = (key: 'llm' | 'search') =>
    buckets
      .map((bucket, index) => (bucket[key] == null ? null : `${xFor(index)},${yFor(bucket[key])}`))
      .filter((point): point is string => point != null)
      .join(' ');

  return (
    <div className={styles.wrap}>
      <div className={styles.legend}>
        <span>
          <i className={styles.totalDot} />
          {t('usage_analytics.cost_trend_total')} {formatUsageCost(totalCost, currency)}
        </span>
        {hasLlm && (
          <span>
            <i className={styles.llmDot} />
            {t('usage_analytics.kind_llm')} {formatUsageCost(totalLlm, currency)}
          </span>
        )}
        {hasSearch && (
          <span>
            <i className={styles.searchDot} />
            {t('usage_analytics.kind_search')} {formatUsageCost(totalSearch, currency)}
          </span>
        )}
      </div>
      <svg
        className={styles.chart}
        viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
        role="img"
        aria-label={t('usage_analytics.cost_trend_title')}
      >
        <defs>
          <linearGradient id="usage-cost-fill" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stopColor="var(--primary-color)" stopOpacity="0.24" />
            <stop offset="100%" stopColor="var(--primary-color)" stopOpacity="0.02" />
          </linearGradient>
        </defs>
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
          {formatUsageCost(maxCost, currency)}
        </text>
        <text x={4} y={HEIGHT - PADDING.bottom + 4} className={styles.axisLabel}>
          {formatUsageCost(0, currency)}
        </text>
        <path d={area} fill="url(#usage-cost-fill)" />
        <polyline points={totalPoints.join(' ')} className={styles.totalLine} />
        {hasLlm && <polyline points={kindPoints('llm')} className={styles.llmLine} />}
        {hasSearch && <polyline points={kindPoints('search')} className={styles.searchLine} />}
        {buckets.map((bucket, index) => (
          <circle
            key={bucket.start}
            cx={xFor(index)}
            cy={yFor(bucket.total)}
            r={buckets.length > 120 ? 1.5 : 3}
            className={styles.point}
          >
            <title>
              {`${formatTime(bucket.start)} · ${formatUsageCost(bucket.total, currency)}${bucket.llm != null ? ` · ${t('usage_analytics.kind_llm')} ${formatUsageCost(bucket.llm, currency)}` : ''}${bucket.search != null ? ` · ${t('usage_analytics.kind_search')} ${formatUsageCost(bucket.search, currency)}` : ''}`}
            </title>
          </circle>
        ))}
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
