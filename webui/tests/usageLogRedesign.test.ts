import { describe, expect, test } from 'bun:test';

import {
  DEFAULT_LOG_COLUMNS,
  normalizeLogColumns,
} from '@/features/usageAnalytics/usageLogColumns';
import {
  buildUsageEventQuery,
  EMPTY_USAGE_LOG_FILTERS,
  type UsageBaseFilters,
} from '@/features/usageAnalytics/usageQuery';

const baseFilters: UsageBaseFilters = {
  provider: 'nvidia',
  model: 'model-a',
  channel: '',
  operation: '',
  plan: '',
  status: 'success',
};

describe('usage log columns', () => {
  test('defaults to every column when preferences are empty', () => {
    expect(normalizeLogColumns(undefined)).toEqual(DEFAULT_LOG_COLUMNS);
    expect(normalizeLogColumns([])).toEqual(DEFAULT_LOG_COLUMNS);
  });

  test('filters unknown keys and keeps canonical order', () => {
    expect(normalizeLogColumns(['cost', 'bogus', 'time'])).toEqual(['time', 'cost']);
    expect(normalizeLogColumns(['bogus'])).toEqual(DEFAULT_LOG_COLUMNS);
  });
});

describe('usage event query builder', () => {
  test('combines base and log filters', () => {
    const query = buildUsageEventQuery({
      kind: 'llm',
      preset: '24h',
      customFrom: '',
      customTo: '',
      baseFilters,
      logFilters: {
        ...EMPTY_USAGE_LOG_FILTERS,
        accountId: 'account-1',
        requestId: 'req-1',
        q: 'gpt',
        statusCode: '429',
      },
      limit: 50,
    });
    expect(query).toMatchObject({
      kind: 'llm',
      range: '24h',
      provider: 'nvidia',
      model: 'model-a',
      status: 'success',
      account_id: 'account-1',
      request_id: 'req-1',
      q: 'gpt',
      status_code: 429,
      limit: 50,
    });
    expect(query.channel).toBeUndefined();
    expect(query.plan).toBeUndefined();
  });

  test('uses custom range timestamps when provided', () => {
    const query = buildUsageEventQuery({
      kind: 'all',
      preset: 'custom',
      customFrom: '2026-10-18T00:00',
      customTo: '2026-10-18T12:00',
      baseFilters: { ...baseFilters, provider: '' },
      logFilters: EMPTY_USAGE_LOG_FILTERS,
      limit: 25,
    });
    expect(query.kind).toBeUndefined();
    expect(query.from).toBeDefined();
    expect(query.to).toBeDefined();
    expect(query.range).toBeUndefined();
  });
});
