import type {
  ChannelStatusChannel,
  ChannelStatusSnapshot,
  UsageAnalyticsResponse,
  UsageGroup,
  WebSearchChannel,
  WebSearchStatusSnapshot,
} from '@/services/api';

import type { TrafficWindow } from './types';
import type { MeterTone } from './utils';

export interface DashboardProvider {
  id: string;
  keys: number;
  models: number;
  requests: number;
  success: number;
  failed: number;
  successRate: number | null;
  p95LatencyMS: number;
  buckets: number[];
  tone: MeterTone;
}

export interface DashboardSearchChannel {
  id: string;
  name: string;
  accounts: number;
  available: number;
  limited: number;
  expired: number;
  status: 'operational' | 'degraded' | 'down' | 'unknown';
  tone: MeterTone;
  latencyMS: number;
  lastChecked?: string;
  message?: string;
}

export interface DashboardView {
  traffic: TrafficWindow;
  kpi: {
    requests: number;
    success: number;
    failed: number;
    successRate: number | null;
    p95LatencyMS: number;
    providerKeys: number;
    models: number;
    searchAccounts: number;
    searchAvailable: number;
  };
  providers: DashboardProvider[];
  searchChannels: DashboardSearchChannel[];
  health: {
    tone: MeterTone;
    llmHealthy: number;
    llmTotal: number;
    searchHealthy: number;
    searchTotal: number;
    proxyReady: boolean;
    failures: number;
  };
}

const EMPTY_TRAFFIC: TrafficWindow = {
  buckets: [],
  totalSuccess: 0,
  totalFailure: 0,
  total: 0,
  successRate: null,
  peakTotal: 0,
  peakIndex: -1,
  windowMinutes: 24 * 60,
};

const SEARCH_STATUS_TONE: Record<DashboardSearchChannel['status'], MeterTone> = {
  operational: 'good',
  degraded: 'warning',
  down: 'critical',
  unknown: 'idle',
};

function toneForRate(rate: number | null): MeterTone {
  if (rate === null) return 'idle';
  if (rate >= 85) return 'good';
  if (rate >= 50) return 'warning';
  return 'critical';
}

function bucketTotal(channel: ChannelStatusChannel | undefined): {
  success: number;
  failed: number;
} {
  if (!channel?.buckets) return { success: 0, failed: 0 };
  return channel.buckets.reduce(
    (total, bucket) => ({
      success: total.success + bucket.success,
      failed: total.failed + bucket.failed,
    }),
    { success: 0, failed: 0 }
  );
}

function buildTraffic(snapshot: ChannelStatusSnapshot | null, now: Date): TrafficWindow {
  if (!snapshot) return EMPTY_TRAFFIC;
  const observed = new Date(snapshot.observed_at);
  const end = new Date((Number.isNaN(observed.getTime()) ? now : observed).getTime());
  end.setMinutes(0, 0, 0);
  const start = new Date(end.getTime() - 23 * 60 * 60 * 1000);
  const hourly = new Map<number, { success: number; failed: number }>();

  snapshot.channels.forEach((channel) => {
    channel.buckets.forEach((bucket) => {
      const at = new Date(bucket.start);
      if (Number.isNaN(at.getTime())) return;
      const hour = new Date(at);
      hour.setMinutes(0, 0, 0);
      if (hour.getTime() < start.getTime() || hour.getTime() > end.getTime()) return;
      const entry = hourly.get(hour.getTime()) ?? { success: 0, failed: 0 };
      entry.success += bucket.success;
      entry.failed += bucket.failed;
      hourly.set(hour.getTime(), entry);
    });
  });

  const buckets = Array.from({ length: 24 }, (_, index) => {
    const at = new Date(start.getTime() + index * 60 * 60 * 1000);
    const value = hourly.get(at.getTime()) ?? { success: 0, failed: 0 };
    return {
      time: `${String(at.getHours()).padStart(2, '0')}:00`,
      success: value.success,
      failed: value.failed,
    };
  });

  let totalSuccess = 0;
  let totalFailure = 0;
  let peakTotal = 0;
  let peakIndex = -1;
  buckets.forEach((bucket, index) => {
    totalSuccess += bucket.success;
    totalFailure += bucket.failed;
    const total = bucket.success + bucket.failed;
    if (total > peakTotal) {
      peakTotal = total;
      peakIndex = index;
    }
  });
  const total = totalSuccess + totalFailure;
  return {
    buckets,
    totalSuccess,
    totalFailure,
    total,
    successRate: total > 0 ? (totalSuccess / total) * 100 : null,
    peakTotal,
    peakIndex,
    windowMinutes: 24 * 60,
  };
}

function analyticsProviderGroups(analytics: UsageAnalyticsResponse | null) {
  const groups = new Map<string, UsageGroup>();
  for (const group of analytics?.groups ?? []) {
    if (group.kind !== 'llm') continue;
    const id = (group.provider || group.key).toLowerCase();
    if (id) groups.set(id, group);
  }
  return groups;
}

export function buildDashboardView(input: {
  snapshot: ChannelStatusSnapshot | null;
  analytics: UsageAnalyticsResponse | null;
  searchChannels: WebSearchChannel[];
  searchStatus: WebSearchStatusSnapshot | null;
  keyCounts: Record<string, number>;
  now?: Date;
}): DashboardView {
  const traffic = buildTraffic(input.snapshot, input.now ?? new Date());
  const groups = analyticsProviderGroups(input.analytics);
  const providerMap = new Map<string, DashboardProvider>();
  const models = new Set<string>();

  const addProvider = (id: string, channel?: ChannelStatusChannel) => {
    const key = id.toLowerCase();
    if (!key) return;
    const group = groups.get(key);
    const channelBuckets = bucketTotal(channel);
    const requests = group?.requests ?? channelBuckets.success + channelBuckets.failed;
    const success = group?.success ?? channelBuckets.success;
    const failed = group?.failed ?? channelBuckets.failed;
    const rate =
      group !== undefined
        ? group.requests > 0
          ? group.success_rate * 100
          : null
        : requests > 0
          ? (success / requests) * 100
          : null;
    if (channel?.models) {
      channel.models.forEach((model) => {
        if (model.id) models.add(model.id);
      });
    }
    providerMap.set(key, {
      id: key,
      keys: input.keyCounts[key] ?? 0,
      models: channel?.models?.length ?? 0,
      requests,
      success,
      failed,
      successRate: rate,
      p95LatencyMS: group?.p95_latency_ms ?? 0,
      buckets: channel?.buckets.map((bucket) => bucket.success + bucket.failed) ?? [],
      tone: requests > 0 ? toneForRate(rate) : 'idle',
    });
  };

  (input.snapshot?.channels ?? [])
    .filter((channel) => channel.kind === 'llm')
    .forEach((channel) => addProvider(channel.id, channel));
  groups.forEach((_group, id) => {
    if (!providerMap.has(id)) addProvider(id);
  });

  const providers = [...providerMap.values()].sort(
    (left, right) => right.requests - left.requests || left.id.localeCompare(right.id)
  );
  const statusMap = new Map(
    (input.searchStatus?.channels ?? []).map((channel) => [channel.channel, channel])
  );
  const searchChannels = input.searchChannels.map<DashboardSearchChannel>((channel) => {
    const status = statusMap.get(channel.id);
    let state = status?.status ?? 'unknown';
    if (state === 'operational' && channel.limited > 0) state = 'degraded';
    return {
      id: channel.id,
      name: channel.name,
      accounts: channel.accounts,
      available: channel.available,
      limited: channel.limited,
      expired: channel.expired,
      status: state,
      tone: SEARCH_STATUS_TONE[state],
      latencyMS: status?.latency_ms ?? 0,
      lastChecked: status?.checked_at,
      message: status?.message,
    };
  });

  const requests = input.analytics?.summary.requests ?? traffic.total;
  const success = input.analytics?.summary.success ?? traffic.totalSuccess;
  const failed = input.analytics?.summary.failed ?? traffic.totalFailure;
  const successRate =
    input.analytics !== null
      ? input.analytics.summary.requests > 0
        ? input.analytics.summary.success_rate * 100
        : null
      : traffic.successRate;
  const searchAccounts = searchChannels.reduce((sum, channel) => sum + channel.accounts, 0);
  const searchAvailable = searchChannels.reduce((sum, channel) => sum + channel.available, 0);
  const llmHealthy = providers.filter((provider) => provider.tone === 'good').length;
  const searchHealthy = searchChannels.filter((channel) => channel.tone === 'good').length;
  const proxyReady =
    input.searchStatus?.proxy_configured ??
    (input.searchChannels.length > 0 &&
      input.searchChannels.every((channel) => channel.proxy_ready));

  let tone: MeterTone = 'idle';
  if (
    providers.some((provider) => provider.tone === 'critical') ||
    searchChannels.some((channel) => channel.tone === 'critical')
  ) {
    tone = 'critical';
  } else if (successRate !== null && successRate < 50 && requests > 0) {
    tone = 'critical';
  } else if (
    providers.some((provider) => provider.tone === 'warning') ||
    searchChannels.some((channel) => channel.tone === 'warning') ||
    (successRate !== null && successRate < 85 && requests > 0)
  ) {
    tone = 'warning';
  } else if (requests > 0) {
    tone = 'good';
  }

  return {
    traffic,
    kpi: {
      requests,
      success,
      failed,
      successRate,
      p95LatencyMS: input.analytics?.summary.p95_latency_ms ?? 0,
      providerKeys: Object.values(input.keyCounts).reduce((sum, count) => sum + count, 0),
      models: models.size,
      searchAccounts,
      searchAvailable,
    },
    providers,
    searchChannels,
    health: {
      tone,
      llmHealthy,
      llmTotal: providers.length,
      searchHealthy,
      searchTotal: searchChannels.length,
      proxyReady,
      failures: failed,
    },
  };
}
