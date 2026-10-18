import type { TFunction } from 'i18next';

import type { WebSearchAccount, WebSearchQuotaBalance } from '@/services/api';

export type WarningTone = 'critical' | 'warning';

export interface AccountWarning {
  tone: WarningTone;
  label: string;
}

export const LOW_BALANCE_OPTIONS = [5, 10, 20, 30];

export function remainingBalance(balance: WebSearchQuotaBalance): number | undefined {
  if (balance.remaining != null) return balance.remaining;
  if (balance.limit != null && balance.used != null) return balance.limit - balance.used;
  return undefined;
}

export function accountWarnings(
  account: WebSearchAccount,
  thresholdPercent: number,
  t: TFunction
): AccountWarning[] {
  const warnings: AccountWarning[] = [];
  let lowBalance = false;
  let exhausted = false;
  for (const balance of account.quota?.balances ?? []) {
    const remaining = remainingBalance(balance);
    if (remaining == null) continue;
    if (remaining <= 0) {
      exhausted = true;
      continue;
    }
    if (balance.limit != null && balance.limit > 0) {
      const percent = (remaining / balance.limit) * 100;
      if (percent <= thresholdPercent) lowBalance = true;
    }
  }
  if (account.cooldown) {
    warnings.push({ tone: 'warning', label: t('usage_analytics.issue_cooldown') });
  }
  if (account.status === 'limited') {
    warnings.push({ tone: 'critical', label: t('usage_analytics.issue_limited') });
  }
  if (account.status === 'expired') {
    warnings.push({ tone: 'critical', label: t('usage_analytics.issue_expired') });
  }
  if (exhausted && account.status !== 'limited') {
    warnings.push({ tone: 'critical', label: t('usage_analytics.issue_exhausted') });
  }
  if (lowBalance && !exhausted) {
    warnings.push({
      tone: 'warning',
      label: t('usage_analytics.issue_low_balance', { percent: thresholdPercent }),
    });
  }
  if (account.quota_refresh_status === 'failed') {
    warnings.push({ tone: 'warning', label: t('usage_analytics.issue_quota_failed') });
  }
  if (
    account.last_http_status != null &&
    account.last_http_status >= 400 &&
    account.status !== 'expired'
  ) {
    warnings.push({
      tone: 'warning',
      label: t('usage_analytics.issue_probe_failed', { status: account.last_http_status }),
    });
  }
  return warnings;
}

export interface WarningCounts {
  critical: number;
  warning: number;
  total: number;
}

export function countAccountWarnings(
  accounts: WebSearchAccount[],
  thresholdPercent: number,
  t: TFunction
): WarningCounts {
  let critical = 0;
  let warning = 0;
  for (const account of accounts) {
    const warnings = accountWarnings(account, thresholdPercent, t);
    if (warnings.some((entry) => entry.tone === 'critical')) {
      critical += 1;
    } else if (warnings.length > 0) {
      warning += 1;
    }
  }
  return { critical, warning, total: critical + warning };
}
