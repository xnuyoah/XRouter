import { apiClient } from './client';

export type UsageKind = 'llm' | 'search';

export interface UsageTokenCounts {
  input: number;
  uncached_input?: number;
  output: number;
  reasoning: number;
  cache_read: number;
  cache_write: number;
  total: number;
  unclassified?: number;
}

export interface UsageSummary {
  requests: number;
  success: number;
  failed: number;
  success_rate: number;
  tokens: UsageTokenCounts;
  search_units?: number;
  search_unit?: string;
  avg_latency_ms: number;
  p95_latency_ms: number;
  avg_ttft_ms: number;
  tps?: number;
  estimated_cost: number | null;
  currency?: string;
  coverage: number;
  unpriced_models?: string[];
  failure_statuses?: Array<{ status_code: number; count: number }>;
  latency_buckets?: UsageLatencyBucket[];
}

export interface UsageLatencyBucket {
  lower_ms: number;
  upper_ms?: number;
  count: number;
}

export interface UsageSeriesPoint {
  start: string;
  kind: UsageKind;
  requests: number;
  success: number;
  failed: number;
  tokens: UsageTokenCounts;
  units?: number;
  credits?: number;
  cost: number | null;
}

export interface UsageGroup {
  kind: UsageKind;
  key: string;
  provider?: string;
  model?: string;
  channel?: string;
  operation?: string;
  plan?: string;
  quota_kind?: string;
  account_id?: string;
  account_name?: string;
  source?: string;
  auth_type?: string;
  executor_type?: string;
  service_tier?: string;
  reasoning_effort?: string;
  status?: string;
  status_code?: number;
  requests: number;
  success: number;
  failed: number;
  success_rate: number;
  tokens: UsageTokenCounts;
  units?: number;
  unit?: string;
  credits?: number;
  avg_latency_ms: number;
  p95_latency_ms: number;
  avg_ttft_ms: number;
  cost: number | null;
  coverage: number;
}

export interface UsageFacets {
  providers?: string[];
  models?: string[];
  channels?: string[];
  operations?: string[];
  plans?: string[];
  quota_kinds?: string[];
  sources?: string[];
  auth_types?: string[];
  executors?: string[];
  service_tiers?: string[];
  reasoning_efforts?: string[];
  statuses?: string[];
  status_codes?: number[];
}

export interface UsageAnalyticsResponse {
  range: { from: string; to: string; bucket: string };
  summary: UsageSummary;
  series: UsageSeriesPoint[];
  groups: UsageGroup[];
  facets: UsageFacets;
  price_revision?: number;
  price_updated_at?: string;
  currency?: string;
}

export interface UsageAnalyticsStatus {
  path: string;
  db_bytes: number;
  dropped_events: number;
  queue_depth: number;
  queue_capacity: number;
  retention_days: number;
  rollup_retention_days: number;
  flush_interval_ms: number;
  max_db_bytes: number;
  last_flush?: string;
  oldest_event?: string;
  newest_event?: string;
  price_revision: number;
  price_currency?: string;
}

export interface UsageEvent {
  id: string;
  trace_id?: string;
  time: string;
  kind: UsageKind;
  provider?: string;
  channel?: string;
  model?: string;
  upstream_model?: string;
  alias?: string;
  operation?: string;
  plan?: string;
  quota_kind?: string;
  account_id?: string;
  account_name?: string;
  api_key_hash?: string;
  api_key_masked?: string;
  source?: string;
  auth_type?: string;
  executor_type?: string;
  service_tier?: string;
  reasoning_effort?: string;
  stream?: boolean;
  tokens: UsageTokenCounts;
  units?: number;
  unit?: string;
  credits?: number;
  upstream_cost?: number;
  upstream_currency?: string;
  cost: number | null;
  currency?: string;
  price_revision?: number;
  latency_ms?: number;
  ttft_ms?: number;
  tps?: number;
  success: boolean;
  status_code?: number;
  error_code?: string;
  attempts?: number;
  proxy_used?: boolean;
  generate: boolean;
}

export interface UsageEventsResponse {
  events: UsageEvent[];
  next_cursor?: string;
  has_more: boolean;
}

export interface UsageQueryParams {
  kind?: UsageKind | 'all';
  range?: string;
  from?: string;
  to?: string;
  bucket?: string;
  group_by?: string;
  provider?: string;
  model?: string;
  channel?: string;
  operation?: string;
  plan?: string;
  quota_kind?: string;
  source?: string;
  auth_type?: string;
  executor?: string;
  service_tier?: string;
  reasoning_effort?: string;
  status?: string;
  api_key_hash?: string;
  limit?: number;
  offset?: number;
  cursor?: string;
}

export interface UsageModelPriceTier {
  threshold: number;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  reasoning?: number;
}

export interface UsageModelPrice {
  provider?: string;
  model: string;
  plan?: string;
  service_tier?: string;
  input: number;
  output: number;
  cache_read: number;
  cache_write: number;
  reasoning?: number;
  currency?: string;
  source?: string;
  tiers?: UsageModelPriceTier[];
}

export interface UsageSearchPrice {
  channel: string;
  operation?: string;
  unit?: string;
  price: number;
  currency?: string;
  source?: string;
}

export interface UsagePriceMapping {
  from: string;
  to: string;
}

export interface UsagePreferences {
  kind: 'all' | 'llm' | 'search';
  range: string;
  group_by: string;
  token_display_mode: 'compact' | 'full' | 'k' | 'm' | 'b';
  page_size: number;
  low_balance_threshold_percent: number;
  updated_at?: string;
}

export interface UsageImportResult {
  total: number;
  imported: number;
  skipped: number;
  invalid: number;
}

export interface UsagePriceSyncState {
  running: boolean;
  last_started_at?: string;
  last_completed_at?: string;
  last_error?: string;
  models?: number;
  source?: string;
}

export interface UsagePricingSnapshot {
  revision: number;
  updated_at: string;
  currency: string;
  provider_priority: string[];
  ignored_suffixes: string[];
  mappings?: UsagePriceMapping[];
  models: UsageModelPrice[];
  manual_models: UsageModelPrice[];
  search: UsageSearchPrice[];
  total_models: number;
  sync: UsagePriceSyncState;
}

export interface UsagePriceSyncResult {
  source: string;
  models: number;
  revision: number;
  updated_at: string;
}

const BASE = '/observability/usage';

function queryString(params: UsageQueryParams): string {
  const search = new URLSearchParams();
  Object.entries(params).forEach(([key, value]) => {
    if (value === undefined || value === null || value === '') return;
    search.set(key, String(value));
  });
  const rendered = search.toString();
  return rendered ? `?${rendered}` : '';
}

export const usageAnalyticsApi = {
  getAnalytics(params: UsageQueryParams): Promise<UsageAnalyticsResponse> {
    return apiClient.get<UsageAnalyticsResponse>(`${BASE}/analytics${queryString(params)}`);
  },

  getStatus(): Promise<UsageAnalyticsStatus> {
    return apiClient.get<UsageAnalyticsStatus>(`${BASE}/status`);
  },

  getEvents(params: UsageQueryParams): Promise<UsageEventsResponse> {
    return apiClient.get<UsageEventsResponse>(`${BASE}/events${queryString(params)}`);
  },

  async exportCsv(params: UsageQueryParams): Promise<Blob> {
    const response = await apiClient.getRaw(`${BASE}/export${queryString(params)}`, {
      responseType: 'blob',
    });
    return response.data as Blob;
  },

  getPreferences(): Promise<UsagePreferences> {
    return apiClient.get<UsagePreferences>(`${BASE}/preferences`);
  },

  savePreferences(preferences: Omit<UsagePreferences, 'updated_at'>): Promise<UsagePreferences> {
    return apiClient.put<UsagePreferences>(`${BASE}/preferences`, preferences);
  },

  getPricing(params?: {
    provider?: string;
    q?: string;
    limit?: number;
    offset?: number;
  }): Promise<UsagePricingSnapshot> {
    const search = new URLSearchParams();
    if (params?.provider) search.set('provider', params.provider);
    if (params?.q) search.set('q', params.q);
    if (params?.limit) search.set('limit', String(params.limit));
    if (params?.offset) search.set('offset', String(params.offset));
    const suffix = search.toString() ? `?${search.toString()}` : '';
    return apiClient.get<UsagePricingSnapshot>(`${BASE}/pricing${suffix}`);
  },

  putPricing(body: {
    models: UsageModelPrice[];
    search: UsageSearchPrice[];
  }): Promise<UsagePricingSnapshot> {
    return apiClient.put<UsagePricingSnapshot>(`${BASE}/pricing`, body);
  },

  syncPricing(): Promise<UsagePriceSyncResult> {
    return apiClient.post<UsagePriceSyncResult>(`${BASE}/pricing/sync`);
  },

  async backup(): Promise<Blob> {
    const response = await apiClient.getRaw(`${BASE}/backup`, { responseType: 'blob' });
    return response.data as Blob;
  },

  restore(file: Blob): Promise<{ status: string }> {
    return apiClient.post<{ status: string }>(`${BASE}/restore`, file, {
      headers: {
        'Content-Type': 'application/octet-stream',
        'X-Confirm-Restore': 'replace',
      },
    });
  },

  importTokensStatistic(file: Blob): Promise<UsageImportResult> {
    return apiClient.post<UsageImportResult>(`${BASE}/import/tokens-statistic`, file, {
      headers: {
        'Content-Type': 'application/octet-stream',
        'X-Confirm-Import': 'merge',
      },
    });
  },

  reset(): Promise<{ status: string }> {
    return apiClient.post<{ status: string }>(`${BASE}/reset`);
  },
};
