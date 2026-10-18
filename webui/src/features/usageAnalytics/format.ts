export type TokenDisplayMode = 'compact' | 'full' | 'k' | 'm' | 'b';

export function formatUsageCost(value: number | null | undefined, currency?: string): string {
  if (value == null || !Number.isFinite(value)) return '—';
  const symbol = currency === 'CNY' ? '¥' : '$';
  return `${symbol}${value.toFixed(6).replace(/0+$/, '').replace(/\.$/, '')}`;
}

export function formatUsageTime(value: string | undefined): string {
  if (!value) return '—';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function formatUsageDuration(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value <= 0) return '—';
  if (value < 1000) return `${Math.round(value)} ms`;
  return `${(value / 1000).toFixed(2)} s`;
}

export function formatUsageTokens(value: number, mode: TokenDisplayMode): string {
  if (!Number.isFinite(value)) return '—';
  switch (mode) {
    case 'full':
      return value.toLocaleString();
    case 'k':
      return `${(value / 1000).toFixed(2)}K`;
    case 'm':
      return `${(value / 1_000_000).toFixed(2)}M`;
    case 'b':
      return `${(value / 1_000_000_000).toFixed(2)}B`;
    default:
      return new Intl.NumberFormat(undefined, {
        notation: 'compact',
        maximumFractionDigits: 1,
      }).format(value);
  }
}

export function formatUsageTps(value: number | null | undefined): string {
  if (value == null || !Number.isFinite(value) || value <= 0) return '—';
  return `${value.toFixed(1)} t/s`;
}
