import { describe, expect, test } from 'bun:test';

import type { WebSearchAccount } from '@/services/api';
import {
  accountWarnings,
  countAccountWarnings,
} from '@/features/usageAnalytics/searchAccountWarnings';

const t = ((key: string, options?: Record<string, unknown>) => {
  if (options?.percent != null) return `${key}:${options.percent}`;
  if (options?.status != null) return `${key}:${options.status}`;
  return key;
}) as unknown as Parameters<typeof accountWarnings>[2];

function account(overrides: Partial<WebSearchAccount>): WebSearchAccount {
  return {
    channel: 'tavily',
    account_id: 'account-1',
    name: 'Account',
    api_key_masked: 'tvly-****',
    has_login: false,
    status: 'available',
    quota_refresh_status: 'success',
    cooldown: false,
    disabled: false,
    weight: 1,
    ...overrides,
  };
}

describe('search account warnings', () => {
  test('flags a remaining balance at or below the configured threshold', () => {
    const warnings = accountWarnings(
      account({
        quota: {
          checked_at: new Date().toISOString(),
          balances: [{ kind: 'key', remaining: 10, limit: 100 }],
        },
      }),
      10,
      t
    );
    expect(warnings).toEqual([{ tone: 'warning', label: 'usage_analytics.issue_low_balance:10' }]);
  });

  test('keeps balances above the threshold quiet', () => {
    const warnings = accountWarnings(
      account({
        quota: {
          checked_at: new Date().toISOString(),
          balances: [{ kind: 'key', remaining: 11, limit: 100 }],
        },
      }),
      10,
      t
    );
    expect(warnings).toEqual([]);
  });

  test('derives remaining from limit minus used when remaining is absent', () => {
    const warnings = accountWarnings(
      account({
        quota: {
          checked_at: new Date().toISOString(),
          balances: [{ kind: 'key', used: 95, limit: 100 }],
        },
      }),
      10,
      t
    );
    expect(warnings).toEqual([{ tone: 'warning', label: 'usage_analytics.issue_low_balance:10' }]);
  });

  test('treats a zero or negative balance as exhausted', () => {
    for (const remaining of [0, -3]) {
      const warnings = accountWarnings(
        account({
          quota: {
            checked_at: new Date().toISOString(),
            balances: [{ kind: 'credits', remaining }],
          },
        }),
        10,
        t
      );
      expect(warnings).toEqual([{ tone: 'critical', label: 'usage_analytics.issue_exhausted' }]);
    }
  });

  test('does not duplicate exhausted and limited states', () => {
    const warnings = accountWarnings(
      account({
        status: 'limited',
        quota: {
          checked_at: new Date().toISOString(),
          balances: [{ kind: 'credits', remaining: 0 }],
        },
      }),
      10,
      t
    );
    expect(warnings).toEqual([{ tone: 'critical', label: 'usage_analytics.issue_limited' }]);
  });

  test('surfaces cooldown, quota refresh failure and the last probe status', () => {
    const warnings = accountWarnings(
      account({
        cooldown: true,
        quota_refresh_status: 'failed',
        last_http_status: 429,
      }),
      10,
      t
    );
    expect(warnings).toEqual([
      { tone: 'warning', label: 'usage_analytics.issue_cooldown' },
      { tone: 'warning', label: 'usage_analytics.issue_quota_failed' },
      { tone: 'warning', label: 'usage_analytics.issue_probe_failed:429' },
    ]);
  });

  test('expired credentials do not also report the probe status', () => {
    const warnings = accountWarnings(account({ status: 'expired', last_http_status: 401 }), 10, t);
    expect(warnings).toEqual([{ tone: 'critical', label: 'usage_analytics.issue_expired' }]);
  });
});

describe('search account warning counts', () => {
  test('counts critical accounts once and separates warnings', () => {
    const counts = countAccountWarnings(
      [account({ status: 'limited' }), account({ cooldown: true }), account()],
      10,
      t
    );
    expect(counts).toEqual({ critical: 1, warning: 1, total: 2 });
  });
});
