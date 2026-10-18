/**
 * 搜索渠道管理接口（相对路径 /web-search/*，由 apiClient 拼上 /v8/management 前缀）。
 *
 * 渠道与账号池的唯一事实来源是 config.yaml 的 web-search 段：这里的写操作会由后端
 * 写回配置文件并触发热重载。
 */

import { apiClient } from './client';

/** 额度维度。 */
export type WebSearchQuotaKind = 'credits' | 'key' | 'plan' | 'paygo';

/** 单个额度维度的快照；remaining 允许为负（Firecrawl 透支）。 */
export interface WebSearchQuotaBalance {
  kind: WebSearchQuotaKind;
  remaining?: number;
  used?: number;
  limit?: number;
}

/** 账号额度快照。 */
export interface WebSearchQuotaSnapshot {
  checked_at: string;
  balances?: WebSearchQuotaBalance[];
  reset_at?: string;
  message?: string;
}

/** 账号可用状态。 */
export type WebSearchAccountStatus =
  | 'pending'
  | 'available'
  | 'limited'
  | 'expired'
  | 'disabled';

/** 额度刷新状态。 */
export type WebSearchQuotaRefreshStatus = 'never' | 'success' | 'failed' | 'unsupported';

/** 渠道视图。 */
export interface WebSearchChannel {
  id: string;
  name: string;
  enabled: boolean;
  base_url?: string;
  proxy_url?: string;
  proxy_ready: boolean;
  supports_mcp: boolean;
  mcp_endpoint?: string;
  gateway_path: string;
  mcp_path?: string;
  accounts: number;
  available: number;
  limited: number;
  expired: number;
  pending: number;
  disabled: number;
}

/** 账号视图（密钥只返回掩码）。 */
export interface WebSearchAccount {
  channel: string;
  account_id: string;
  name: string;
  api_key_masked: string;
  has_login: boolean;
  status: WebSearchAccountStatus;
  status_message?: string;
  quota?: WebSearchQuotaSnapshot;
  quota_refresh_status: WebSearchQuotaRefreshStatus;
  quota_refresh_message?: string;
  last_checked_at?: string;
  last_latency_ms?: number;
  last_http_status?: number;
  last_used_at?: string;
  cooldown_until?: string;
  cooldown: boolean;
  disabled: boolean;
  weight: number;
  note?: string;
}

/** 渠道整体状态。 */
export type WebSearchChannelStatusValue = 'operational' | 'degraded' | 'down' | 'unknown';

/** 渠道状态的按日聚合点。 */
export interface WebSearchStatusHistoryPoint {
  date: string;
  status: WebSearchChannelStatusValue;
  samples: number;
  successes: number;
}

/** 单个渠道的状态视图。 */
export interface WebSearchStatusComponent {
  channel: string;
  name: string;
  status: WebSearchChannelStatusValue;
  message?: string;
  checked_at?: string;
  latency_ms?: number;
  http_status?: number;
  proxy_used: boolean;
  proxy_configured: boolean;
  success_rate: number;
  sample_count: number;
  successes: number;
  stale: boolean;
  history: WebSearchStatusHistoryPoint[];
}

/** 渠道状态快照。 */
export interface WebSearchStatusSnapshot {
  overall_status: WebSearchChannelStatusValue;
  proxy_configured: boolean;
  checked_at?: string;
  history_days: number;
  history_start: string;
  history_end: string;
  check_running: boolean;
  completed_checks: number;
  total_checks: number;
  check_error?: string;
  channels: WebSearchStatusComponent[];
}

/** 额度刷新概览。 */
export interface WebSearchQuotaOverview {
  accounts: number;
  fresh: number;
  stale: number;
  failed: number;
  unsupported: number;
  never: number;
}

/** 监控汇总。 */
export interface WebSearchMonitoring {
  quota: WebSearchQuotaOverview;
  status: WebSearchStatusSnapshot;
  enabled: boolean;
}

/** 单个账号的测活结果。 */
export interface WebSearchProbeItem {
  channel: string;
  account_id: string;
  name: string;
  status: WebSearchAccountStatus | '';
  message?: string;
  success: boolean;
  latency_ms?: number;
  http_status?: number;
}

/** 一轮测活的汇总。 */
export interface WebSearchProbeSummary {
  total: number;
  succeeded: number;
  failed: number;
  by_status: Record<string, number>;
  results: WebSearchProbeItem[];
  error?: string;
}

/** 调用日志条目。 */
export interface WebSearchRequestEntry {
  id: string;
  channel: string;
  account_id: string;
  account_name: string;
  method: string;
  path: string;
  status_code: number;
  attempts: number;
  duration_ms: number;
  created_at: string;
  error_code?: string;
  proxy_used: boolean;
}

/** 账号额度历史点。 */
export interface WebSearchQuotaHistoryPoint {
  checked_at: string;
  status: WebSearchQuotaRefreshStatus;
  balances?: WebSearchQuotaBalance[];
  reset_at?: string;
  message?: string;
}

/** 账号额度历史响应。 */
export interface WebSearchQuotaHistoryResponse {
  channel: string;
  account_id: string;
  points: WebSearchQuotaHistoryPoint[];
}

/** CSV 导入结果。 */
export interface WebSearchImportSummary {
  added: number;
  updated: number;
  skipped_duplicate: number;
  skipped_other_channel: number;
  skipped_invalid: number;
  errors?: { row: number; reason: string }[];
}

/** 账号新增/修改请求体。 */
export interface WebSearchAccountPayload {
  channel: string;
  api_key: string;
  name?: string;
  username?: string;
  password?: string;
  note?: string;
  disabled?: boolean;
  weight?: number;
}

// apiClient 的 baseURL 已经包含 /v8/management，这里只写相对路径。
const BASE = '/web-search';

export const webSearchApi = {
  /** 渠道列表（含账号统计）。 */
  async listChannels(): Promise<WebSearchChannel[]> {
    const data = await apiClient.get<{ channels: WebSearchChannel[] }>(`${BASE}/channels`);
    return data.channels ?? [];
  },

  /** 账号池；可按渠道与状态过滤。 */
  async listAccounts(params?: { channel?: string; status?: string }): Promise<WebSearchAccount[]> {
    const query = new URLSearchParams();
    if (params?.channel) query.set('channel', params.channel);
    if (params?.status) query.set('status', params.status);
    const suffix = query.toString() ? `?${query.toString()}` : '';
    const data = await apiClient.get<{ accounts: WebSearchAccount[] }>(`${BASE}/accounts${suffix}`);
    return data.accounts ?? [];
  },

  /** 新增或更新账号（写回 config.yaml）。 */
  upsertAccount(payload: WebSearchAccountPayload): Promise<unknown> {
    return apiClient.put(`${BASE}/accounts`, payload);
  },

  /** 删除账号（写回 config.yaml）。 */
  deleteAccount(channel: string, accountId: string): Promise<unknown> {
    const query = new URLSearchParams({ channel, account_id: accountId });
    return apiClient.delete(`${BASE}/accounts?${query.toString()}`);
  },

  /** 触发一轮测活（同步返回逐账号结果）。 */
  probe(payload?: { channel?: string; account_ids?: string[] }): Promise<WebSearchProbeSummary> {
    return apiClient.post<WebSearchProbeSummary>(`${BASE}/probes`, payload ?? {});
  },

  /** 渠道状态快照。 */
  getStatus(): Promise<WebSearchStatusSnapshot> {
    return apiClient.get<WebSearchStatusSnapshot>(`${BASE}/status`);
  },

  /** 触发渠道状态检测（异步）；可只检测一个渠道。 */
  startStatusCheck(channel?: string): Promise<unknown> {
    const query = channel ? `?channel=${encodeURIComponent(channel)}` : '';
    return apiClient.post(`${BASE}/status/check${query}`, {});
  },

  /** 监控汇总（额度概览 + 渠道状态）。 */
  getMonitoring(): Promise<WebSearchMonitoring> {
    return apiClient.get<WebSearchMonitoring>(`${BASE}/monitoring`);
  },

  /** 立即刷新额度；可按渠道或账号过滤。 */
  refreshQuota(params?: { channel?: string; account_ids?: string[] }): Promise<{ quota: WebSearchQuotaOverview }> {
    const body = params ?? {};
    return apiClient.post<{ quota: WebSearchQuotaOverview }>(`${BASE}/quota/refresh`, body);
  },

  /** 单个账号的余额历史。 */
  getQuotaHistory(params: {
    channel: string;
    account_id: string;
    hours?: number;
    limit?: number;
    to?: string;
  }): Promise<WebSearchQuotaHistoryResponse> {
    const query = new URLSearchParams({
      channel: params.channel,
      account_id: params.account_id,
    });
    if (params.hours) query.set('hours', String(params.hours));
    if (params.limit) query.set('limit', String(params.limit));
    if (params.to) query.set('to', params.to);
    return apiClient.get<WebSearchQuotaHistoryResponse>(`${BASE}/quota/history?${query.toString()}`);
  },

  /** 导入 OpenRegister CSV（搜索账号与 NVIDIA/Agnes key）。 */
  importAccounts(csv: string): Promise<WebSearchImportSummary> {
    return apiClient.post<WebSearchImportSummary>(`${BASE}/accounts/import`, csv, {
      headers: { 'Content-Type': 'text/csv' },
    });
  },

  /** 导出账号；默认脱敏，include_credentials 显式返回凭据。 */
  async exportAccounts(params: {
    format: 'csv' | 'json';
    include_credentials?: boolean;
    channel?: string;
  }): Promise<Blob> {
    const query = new URLSearchParams({ format: params.format });
    if (params.include_credentials) query.set('include_credentials', 'true');
    if (params.channel) query.set('channel', params.channel);
    const response = await apiClient.getRaw(`${BASE}/accounts/export?${query.toString()}`, {
      responseType: 'blob',
    });
    return response.data as Blob;
  },

  /** 最近的调用日志。 */
  async listRequests(params?: { channel?: string; limit?: number }): Promise<WebSearchRequestEntry[]> {
    const query = new URLSearchParams();
    if (params?.channel) query.set('channel', params.channel);
    if (params?.limit) query.set('limit', String(params.limit));
    const suffix = query.toString() ? `?${query.toString()}` : '';
    const data = await apiClient.get<{ requests: WebSearchRequestEntry[] }>(
      `${BASE}/requests${suffix}`
    );
    return data.requests ?? [];
  },
};
