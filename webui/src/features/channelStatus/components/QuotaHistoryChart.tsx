import { useMemo } from 'react';
import { useTranslation } from 'react-i18next';
import type {
  WebSearchQuotaHistoryPoint,
  WebSearchQuotaKind,
} from '@/services/api/webSearch';
import { QUOTA_KIND_LABEL_KEYS } from '@/features/webSearch/constants';
import { formatBucketStart } from '../utils';
import styles from './QuotaHistoryChart.module.scss';

const SERIES_COLORS = [
  'var(--viz-success)',
  'var(--warning-color)',
  'var(--viz-failure)',
  'var(--primary-color)',
  'var(--text-secondary)',
];

interface QuotaHistoryChartProps {
  points: WebSearchQuotaHistoryPoint[];
  loading?: boolean;
}

interface SeriesPoint {
  time: number;
  value: number;
}

interface Series {
  kind: WebSearchQuotaKind;
  label: string;
  color: string;
  points: SeriesPoint[];
}

const WIDTH = 640;
const HEIGHT = 180;
const PADDING_X = 28;
const PADDING_Y = 18;

export function QuotaHistoryChart({ points, loading }: QuotaHistoryChartProps) {
  const { t } = useTranslation();

  const series = useMemo(() => buildSeries(points, t), [points, t]);
  if (loading) {
    return <div className={styles.empty}>{t('channelStatus.quota.loading')}</div>;
  }
  if (series.length === 0) {
    return <div className={styles.empty}>{t('channelStatus.quota.empty')}</div>;
  }

  const allPoints = series.flatMap((entry) => entry.points);
  if (allPoints.length === 0) {
    return <div className={styles.empty}>{t('channelStatus.quota.empty')}</div>;
  }
  const times = allPoints.map((point) => point.time);
  const values = allPoints.map((point) => point.value);
  const minTime = Math.min(...times);
  const maxTime = Math.max(...times);
  const minValue = Math.min(...values);
  const maxValue = Math.max(...values);
  const timeSpan = maxTime - minTime || 1;
  const valueSpan = maxValue - minValue || Math.max(1, Math.abs(maxValue) * 0.1);
  const chartWidth = WIDTH - PADDING_X * 2;
  const chartHeight = HEIGHT - PADDING_Y * 2;
  const xFor = (time: number) => PADDING_X + ((time - minTime) / timeSpan) * chartWidth;
  const yFor = (value: number) => PADDING_Y + chartHeight - ((value - minValue) / valueSpan) * chartHeight;

  const firstTime = new Date(minTime).toISOString();
  const lastTime = new Date(maxTime).toISOString();

  return (
    <div className={styles.chart}>
      <div className={styles.legend}>
        {series.map((entry) => (
          <span key={entry.kind}>
            <i style={{ backgroundColor: entry.color }} />
            {entry.label}
          </span>
        ))}
      </div>
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} role="img" aria-label={t('channelStatus.quota.chart')}>
        <line
          x1={PADDING_X}
          y1={HEIGHT - PADDING_Y}
          x2={WIDTH - PADDING_X}
          y2={HEIGHT - PADDING_Y}
          className={styles.axis}
        />
        <line
          x1={PADDING_X}
          y1={PADDING_Y}
          x2={PADDING_X}
          y2={HEIGHT - PADDING_Y}
          className={styles.axis}
        />
        <text x={4} y={PADDING_Y + 4} className={styles.label}>
          {formatValue(maxValue)}
        </text>
        <text x={4} y={HEIGHT - PADDING_Y + 4} className={styles.label}>
          {formatValue(minValue)}
        </text>
        <text x={PADDING_X} y={HEIGHT - 2} className={styles.label}>
          {formatBucketStart(firstTime)}
        </text>
        <text
          x={WIDTH - PADDING_X}
          y={HEIGHT - 2}
          textAnchor="end"
          className={styles.label}
        >
          {formatBucketStart(lastTime)}
        </text>
        {series.map((entry) => (
          <g key={entry.kind}>
            {buildPathSegments(entry.points).map((segment, index) => (
              <polyline
                key={index}
                fill="none"
                stroke={entry.color}
                strokeWidth={2}
                strokeLinecap="round"
                strokeLinejoin="round"
                points={segment
                  .map((point) => `${xFor(point.time)},${yFor(point.value)}`)
                  .join(' ')}
              />
            ))}
            {entry.points.map((point) => (
              <circle
                key={`${entry.kind}-${point.time}`}
                cx={xFor(point.time)}
                cy={yFor(point.value)}
                r={entry.points.length > 120 ? 1.2 : 2.2}
                fill={entry.color}
              >
                <title>
                  {`${formatBucketStart(new Date(point.time).toISOString())} · ${entry.label} ${formatValue(point.value)}`}
                </title>
              </circle>
            ))}
          </g>
        ))}
      </svg>
    </div>
  );
}

function buildSeries(
  points: WebSearchQuotaHistoryPoint[],
  t: (key: string) => string
): Series[] {
  const byKind = new Map<WebSearchQuotaKind, SeriesPoint[]>();
  for (const point of points) {
    const time = new Date(point.checked_at).getTime();
    if (Number.isNaN(time)) continue;
    for (const balance of point.balances ?? []) {
      if (balance.remaining === undefined || balance.remaining === null) continue;
      const entry = byKind.get(balance.kind) ?? [];
      entry.push({ time, value: balance.remaining });
      byKind.set(balance.kind, entry);
    }
  }
  return Array.from(byKind.entries())
    .filter(([, values]) => values.length > 0)
    .map(([kind, values], index) => ({
      kind,
      label: t(QUOTA_KIND_LABEL_KEYS[kind] ?? 'webSearch.quotaKind.credits'),
      color: SERIES_COLORS[index % SERIES_COLORS.length],
      points: values.sort((left, right) => left.time - right.time),
    }));
}

function buildPathSegments(points: SeriesPoint[]): SeriesPoint[][] {
  if (points.length === 0) return [];
  if (points.length === 1) return [[points[0], points[0]]];
  return [points];
}

function formatValue(value: number): string {
  if (!Number.isFinite(value)) return '--';
  if (Math.abs(value) >= 1000) return value.toLocaleString(undefined, { maximumFractionDigits: 0 });
  return value.toLocaleString(undefined, { maximumFractionDigits: 2 });
}

