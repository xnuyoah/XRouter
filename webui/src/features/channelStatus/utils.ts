import type { ChannelStatusBucket } from '@/services/api/channelStatus';

export type ChannelStatusTone = 'good' | 'warning' | 'critical' | 'idle';

export const CHANNEL_STATUS_GOOD_RATE = 0.85;
export const CHANNEL_STATUS_WARNING_RATE = 0.5;

export function channelStatusTone(success: number, failed: number): ChannelStatusTone {
  const total = success + failed;
  if (total <= 0) return 'idle';
  const rate = success / total;
  if (rate >= CHANNEL_STATUS_GOOD_RATE) return 'good';
  if (rate >= CHANNEL_STATUS_WARNING_RATE) return 'warning';
  return 'critical';
}

export function channelStatusRate(success: number, failed: number): number {
  const total = success + failed;
  return total > 0 ? success / total : 0;
}

export function formatChannelRate(success: number, failed: number): string {
  const total = success + failed;
  if (total <= 0) return '--';
  return `${(channelStatusRate(success, failed) * 100).toFixed(2)}%`;
}

const numberFormatter = new Intl.NumberFormat();

export function formatChannelNumber(value: number): string {
  return numberFormatter.format(Number.isFinite(value) ? value : 0);
}

const dateTimeFormatter = new Intl.DateTimeFormat(undefined, {
  month: '2-digit',
  day: '2-digit',
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
});

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  month: '2-digit',
  day: '2-digit',
});

const timeFormatter = new Intl.DateTimeFormat(undefined, {
  hour: '2-digit',
  minute: '2-digit',
  hour12: false,
});

export function formatBucketStart(start: string): string {
  const parsed = new Date(start);
  if (Number.isNaN(parsed.getTime())) return '--';
  return dateTimeFormatter.format(parsed);
}

export function formatBucketRange(start: string, bucketMinutes: number): string {
  const parsed = new Date(start);
  if (Number.isNaN(parsed.getTime())) return '--';
  const end = new Date(parsed.getTime() + bucketMinutes * 60_000);
  const startLabel = dateTimeFormatter.format(parsed);
  const endDate = dateFormatter.format(end);
  const endLabel = dateFormatter.format(parsed) === endDate
    ? timeFormatter.format(end)
    : dateTimeFormatter.format(end);
  return `${startLabel}–${endLabel}`;
}

export function formatObservedAt(value: string | undefined): string {
  if (!value) return '--';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return '--';
  return parsed.toLocaleString();
}

export function formatLatency(value: number | undefined): string {
  if (value === undefined || value === null || value <= 0) return '--';
  return `${value} ms`;
}

export function latestNonEmptyBucketIndex(buckets: ChannelStatusBucket[]): number {
  for (let index = buckets.length - 1; index >= 0; index -= 1) {
    if (buckets[index].success + buckets[index].failed > 0) return index;
  }
  return -1;
}

export function bucketTotal(bucket: ChannelStatusBucket | undefined): number {
  if (!bucket) return 0;
  return bucket.success + bucket.failed;
}
