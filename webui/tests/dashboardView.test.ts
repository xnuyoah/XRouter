import { describe, expect, test } from 'bun:test';

import type {
  ChannelStatusSnapshot,
  UsageAnalyticsResponse,
  WebSearchChannel,
  WebSearchStatusSnapshot,
} from '@/services/api';

import { buildDashboardView } from '@/features/dashboard/dashboardView';

const snapshot: ChannelStatusSnapshot = {
  observed_at: '2026-10-18T12:00:00Z',
  bucket_minutes: 20,
  window_hours: 24,
  channels: [
    {
      kind: 'llm',
      id: 'nvidia',
      name: 'NVIDIA',
      total: 112,
      success: 72,
      failed: 40,
      success_rate: 64.29,
      buckets: [
        { start: '2026-10-18T11:00:00Z', success: 7, failed: 3 },
        { start: '2026-10-18T11:20:00Z', success: 8, failed: 2 },
      ],
      models: [
        {
          id: 'nvidia/model-a',
          name: 'model-a',
          total: 10,
          success: 7,
          failed: 3,
          success_rate: 70,
          buckets: [],
        },
        {
          id: 'nvidia/model-b',
          name: 'model-b',
          total: 10,
          success: 9,
          failed: 1,
          success_rate: 90,
          buckets: [],
        },
      ],
    },
    {
      kind: 'llm',
      id: 'agnes',
      name: 'Agnes',
      total: 21,
      success: 20,
      failed: 1,
      success_rate: 95.24,
      buckets: [{ start: '2026-10-18T11:30:00Z', success: 20, failed: 1 }],
      models: [
        {
          id: 'agnes-2.5-flash',
          name: 'agnes-2.5-flash',
          total: 21,
          success: 20,
          failed: 1,
          success_rate: 95.24,
          buckets: [],
        },
      ],
    },
    {
      kind: 'web_search',
      id: 'tavily',
      name: 'Tavily',
      total: 1,
      success: 1,
      failed: 0,
      success_rate: 100,
      buckets: [{ start: '2026-10-18T11:40:00Z', success: 1, failed: 0 }],
    },
  ],
};

const analytics: UsageAnalyticsResponse = {
  range: { from: '2026-10-17T12:00:00Z', to: '2026-10-18T12:00:00Z', bucket: 'hour' },
  summary: {
    requests: 134,
    success: 93,
    failed: 41,
    success_rate: 0.6940298507462687,
    tokens: {
      input: 100,
      uncached_input: 100,
      output: 50,
      reasoning: 10,
      cache_read: 0,
      cache_write: 0,
      total: 150,
    },
    avg_latency_ms: 1000,
    p95_latency_ms: 120000,
    avg_ttft_ms: 400,
    estimated_cost: null,
    coverage: 0,
  },
  series: [],
  groups: [
    {
      kind: 'llm',
      key: 'nvidia',
      provider: 'nvidia',
      requests: 112,
      success: 72,
      failed: 40,
      success_rate: 0.642857,
      tokens: analyticsTokens(),
      avg_latency_ms: 1000,
      p95_latency_ms: 120000,
      avg_ttft_ms: 500,
      cost: null,
      coverage: 0,
    },
    {
      kind: 'llm',
      key: 'agnes',
      provider: 'agnes',
      requests: 21,
      success: 20,
      failed: 1,
      success_rate: 0.95238,
      tokens: analyticsTokens(),
      avg_latency_ms: 900,
      p95_latency_ms: 10000,
      avg_ttft_ms: 300,
      cost: null,
      coverage: 0,
    },
  ],
  facets: {},
};

function analyticsTokens() {
  return {
    input: 0,
    uncached_input: 0,
    output: 0,
    reasoning: 0,
    cache_read: 0,
    cache_write: 0,
    total: 0,
  };
}

const searchChannels: WebSearchChannel[] = [
  {
    id: 'tavily',
    name: 'Tavily',
    enabled: true,
    proxy_ready: true,
    supports_mcp: true,
    gateway_path: '/gateway/tavily',
    accounts: 409,
    available: 409,
    limited: 0,
    expired: 0,
    pending: 0,
    disabled: 0,
  },
  {
    id: 'firecrawl',
    name: 'Firecrawl',
    enabled: true,
    proxy_ready: true,
    supports_mcp: true,
    gateway_path: '/gateway/firecrawl',
    accounts: 37,
    available: 9,
    limited: 28,
    expired: 0,
    pending: 0,
    disabled: 0,
  },
];

const searchStatus: WebSearchStatusSnapshot = {
  overall_status: 'degraded',
  proxy_configured: true,
  checked_at: '2026-10-18T12:00:00Z',
  history_days: 90,
  history_start: '2026-07-20T00:00:00Z',
  history_end: '2026-10-18T00:00:00Z',
  check_running: false,
  completed_checks: 2,
  total_checks: 2,
  channels: [
    {
      channel: 'tavily',
      name: 'Tavily',
      status: 'operational',
      proxy_used: true,
      proxy_configured: true,
      success_rate: 1,
      sample_count: 1,
      successes: 1,
      stale: false,
      history: [],
      latency_ms: 2400,
      http_status: 200,
      checked_at: '2026-10-18T12:00:00Z',
    },
    {
      channel: 'firecrawl',
      name: 'Firecrawl',
      status: 'operational',
      proxy_used: true,
      proxy_configured: true,
      success_rate: 0.5,
      sample_count: 2,
      successes: 1,
      stale: false,
      history: [],
      latency_ms: 3000,
      http_status: 200,
      checked_at: '2026-10-18T12:00:00Z',
    },
  ],
};

describe('dashboard view', () => {
  test('merges channel counters and analytics into one operational view', () => {
    const view = buildDashboardView({
      snapshot,
      analytics,
      searchChannels,
      searchStatus,
      keyCounts: { nvidia: 480, agnes: 407 },
      now: new Date('2026-10-18T12:00:00Z'),
    });

    expect(view.kpi.requests).toBe(134);
    expect(view.kpi.success).toBe(93);
    expect(view.kpi.failed).toBe(41);
    expect(view.kpi.successRate).toBeCloseTo(69.402985, 5);
    expect(view.kpi.p95LatencyMS).toBe(120000);
    expect(view.kpi.providerKeys).toBe(887);
    expect(view.kpi.models).toBe(3);
    expect(view.kpi.searchAccounts).toBe(446);
    expect(view.kpi.searchAvailable).toBe(418);

    expect(view.providers.map((provider) => provider.id)).toEqual(['nvidia', 'agnes']);
    expect(view.providers[0]).toMatchObject({
      keys: 480,
      models: 2,
      requests: 112,
      p95LatencyMS: 120000,
      tone: 'warning',
    });
    expect(view.providers[1].tone).toBe('good');

    expect(view.searchChannels.map((channel) => channel.status)).toEqual([
      'operational',
      'degraded',
    ]);
    expect(view.searchChannels[1].tone).toBe('warning');

    expect(view.health).toEqual({
      tone: 'warning',
      llmHealthy: 1,
      llmTotal: 2,
      searchHealthy: 1,
      searchTotal: 2,
      proxyReady: true,
      failures: 41,
    });
    expect(view.traffic.buckets).toHaveLength(24);
    expect(view.traffic.total).toBe(42);
  });

  test('falls back to bucket counters when analytics is unavailable', () => {
    const view = buildDashboardView({
      snapshot,
      analytics: null,
      searchChannels,
      searchStatus: null,
      keyCounts: {},
      now: new Date('2026-10-18T12:00:00Z'),
    });

    expect(view.kpi.requests).toBe(42);
    expect(view.kpi.success).toBe(36);
    expect(view.kpi.failed).toBe(6);
    expect(view.kpi.p95LatencyMS).toBe(0);
    expect(view.kpi.providerKeys).toBe(0);
    expect(view.health.proxyReady).toBe(true);
    expect(view.providers.find((provider) => provider.id === 'nvidia')?.requests).toBe(20);
  });
});
