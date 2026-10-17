/**
 * Shared constants and display mappings for the web tools module.
 *
 * Colors reuse the panel's own theme variables so light and dark themes both
 * follow automatically.
 */

import type {
  WebSearchAccountStatus,
  WebSearchChannelStatusValue,
  WebSearchQuotaKind,
  WebSearchQuotaRefreshStatus,
} from '@/services/api/webSearch';

/** Tabs inside the page. */
export type WebSearchTabId = 'overview' | 'accounts' | 'access';

export const WEB_SEARCH_TABS: { id: WebSearchTabId; labelKey: string }[] = [
  { id: 'overview', labelKey: 'webSearch.tabs.overview' },
  { id: 'accounts', labelKey: 'webSearch.tabs.accounts' },
  { id: 'access', labelKey: 'webSearch.tabs.access' },
];

/** Status tones used by pills and progress strips. */
export type ToneId = 'green' | 'orange' | 'red' | 'gray' | 'blue';

/** Account status to label key and tone. */
export const ACCOUNT_STATUS_META: Record<
  WebSearchAccountStatus,
  { labelKey: string; tone: ToneId }
> = {
  available: { labelKey: 'webSearch.accountStatus.available', tone: 'green' },
  limited: { labelKey: 'webSearch.accountStatus.limited', tone: 'orange' },
  expired: { labelKey: 'webSearch.accountStatus.expired', tone: 'red' },
  disabled: { labelKey: 'webSearch.accountStatus.disabled', tone: 'gray' },
  pending: { labelKey: 'webSearch.accountStatus.pending', tone: 'blue' },
};

/** Channel overall status to label key, tone and symbol. */
export const CHANNEL_STATUS_META: Record<
  WebSearchChannelStatusValue,
  { labelKey: string; tone: ToneId; symbol: string }
> = {
  operational: { labelKey: 'webSearch.channelStatus.operational', tone: 'green', symbol: '●' },
  degraded: { labelKey: 'webSearch.channelStatus.degraded', tone: 'orange', symbol: '◐' },
  down: { labelKey: 'webSearch.channelStatus.down', tone: 'red', symbol: '○' },
  unknown: { labelKey: 'webSearch.channelStatus.unknown', tone: 'gray', symbol: '·' },
};

/** Quota refresh status to label key and tone. */
export const QUOTA_REFRESH_META: Record<
  WebSearchQuotaRefreshStatus,
  { labelKey: string; tone: ToneId }
> = {
  success: { labelKey: 'webSearch.quotaRefresh.success', tone: 'green' },
  failed: { labelKey: 'webSearch.quotaRefresh.failed', tone: 'red' },
  unsupported: { labelKey: 'webSearch.quotaRefresh.unsupported', tone: 'gray' },
  never: { labelKey: 'webSearch.quotaRefresh.never', tone: 'blue' },
};

/** Quota dimension to label key. */
export const QUOTA_KIND_LABEL_KEYS: Record<WebSearchQuotaKind, string> = {
  credits: 'webSearch.quotaKind.credits',
  key: 'webSearch.quotaKind.key',
  plan: 'webSearch.quotaKind.plan',
  paygo: 'webSearch.quotaKind.paygo',
};

/** Documented gateway paths per channel. */
export function gatewayExample(channel: string): string {
  switch (channel) {
    case 'tavily':
      return 'POST /gateway/tavily/search';
    case 'exa':
      return 'POST /gateway/exa/search';
    case 'anysearch':
      return 'GET  /gateway/anysearch/v1/sub-domains?domain=code';
    case 'firecrawl':
      return 'POST /gateway/firecrawl/v2/scrape';
    case 'tinyfish':
      return 'POST /gateway/tinyfish/';
    default:
      return `POST /gateway/${channel}/...`;
  }
}

/** Managed MCP endpoint per channel (display only; requests are not proxied). */
export function mcpExample(channel: string): string {
  return `POST /mcp/${channel}`;
}

/** Polling intervals in ms. Probing is synchronous; these cover status refresh only. */
export const WEB_SEARCH_POLL_IDLE_MS = 60_000;
export const WEB_SEARCH_POLL_ACTIVE_MS = 2_000;

/** Maximum number of days rendered in the history strip. */
export const HISTORY_STRIP_DAYS = 90;

/** Status auto-refresh interval in ms. */
export function statusPollInterval(running: boolean): number {
  return running ? WEB_SEARCH_POLL_ACTIVE_MS : WEB_SEARCH_POLL_IDLE_MS;
}
