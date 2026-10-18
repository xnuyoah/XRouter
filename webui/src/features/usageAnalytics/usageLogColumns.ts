export type UsageLogColumnKey =
  | 'time'
  | 'kind'
  | 'provider'
  | 'account'
  | 'token'
  | 'model'
  | 'request'
  | 'tokens'
  | 'cost'
  | 'latency'
  | 'detail'
  | 'plan'
  | 'request_id';

export interface UsageLogColumn {
  key: UsageLogColumnKey;
  labelKey: string;
}

export const USAGE_LOG_COLUMNS: UsageLogColumn[] = [
  { key: 'time', labelKey: 'usage_analytics.log_col_time' },
  { key: 'provider', labelKey: 'usage_analytics.log_col_provider' },
  { key: 'account', labelKey: 'usage_analytics.log_col_account' },
  { key: 'token', labelKey: 'usage_analytics.log_col_token' },
  { key: 'model', labelKey: 'usage_analytics.log_col_model' },
  { key: 'request', labelKey: 'usage_analytics.log_col_request' },
  { key: 'tokens', labelKey: 'usage_analytics.log_col_tokens' },
  { key: 'cost', labelKey: 'usage_analytics.log_col_cost' },
  { key: 'latency', labelKey: 'usage_analytics.log_col_latency' },
  { key: 'detail', labelKey: 'usage_analytics.log_col_detail' },
  { key: 'kind', labelKey: 'usage_analytics.log_col_kind' },
  { key: 'plan', labelKey: 'usage_analytics.log_col_plan' },
  { key: 'request_id', labelKey: 'usage_analytics.log_col_request_id' },
];

const COLUMN_KEYS = new Set<string>(USAGE_LOG_COLUMNS.map((column) => column.key));

export const DEFAULT_LOG_COLUMNS: UsageLogColumnKey[] = [
  'time',
  'provider',
  'account',
  'token',
  'model',
  'request',
  'tokens',
  'cost',
  'latency',
  'detail',
];

const LEGACY_DEFAULT_COLUMNS: UsageLogColumnKey[] = [
  'time',
  'kind',
  'provider',
  'model',
  'account',
  'plan',
  'tokens',
  'cost',
  'latency',
  'request',
  'request_id',
];

export function normalizeLogColumns(values: string[] | undefined): UsageLogColumnKey[] {
  if (!values || values.length === 0) return [...DEFAULT_LOG_COLUMNS];
  const seen = new Set<UsageLogColumnKey>();
  for (const value of values) {
    if (COLUMN_KEYS.has(value)) seen.add(value as UsageLogColumnKey);
  }
  if (
    seen.size === LEGACY_DEFAULT_COLUMNS.length &&
    LEGACY_DEFAULT_COLUMNS.every((column) => seen.has(column))
  ) {
    return [...DEFAULT_LOG_COLUMNS];
  }
  const result = USAGE_LOG_COLUMNS.filter((column) => seen.has(column.key)).map(
    (column) => column.key
  );
  return result.length > 0 ? result : [...DEFAULT_LOG_COLUMNS];
}

export function logColumnLabelKey(key: UsageLogColumnKey): string {
  return USAGE_LOG_COLUMNS.find((column) => column.key === key)?.labelKey ?? key;
}
