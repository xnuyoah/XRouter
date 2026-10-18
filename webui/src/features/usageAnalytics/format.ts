export function formatUsageCost(
  value: number | null | undefined,
  currency?: string
): string {
  if (value == null || !Number.isFinite(value)) return '—';
  const symbol = currency === 'CNY' ? '¥' : '$';
  return `${symbol}${value.toFixed(6).replace(/0+$/, '').replace(/\.$/, '')}`;
}
