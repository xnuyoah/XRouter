/**
 * 搜索渠道模块的共享常量与展示映射。
 *
 * 页面结构参考 OpenRegister 的管理台（概览 / 账号池 / 测活 / 渠道状态 / 接入说明），
 * 配色沿用面板自身的主题变量，避免引入第二套视觉语言。
 */

import type {
  WebSearchAccountStatus,
  WebSearchChannelStatusValue,
  WebSearchQuotaKind,
  WebSearchQuotaRefreshStatus,
} from '@/services/api/webSearch';

/** 页面内的标签页。 */
export type WebSearchTabId = 'overview' | 'accounts' | 'probes' | 'status' | 'access';

export const WEB_SEARCH_TABS: { id: WebSearchTabId; labelKey: string }[] = [
  { id: 'overview', labelKey: 'webSearch.tabs.overview' },
  { id: 'accounts', labelKey: 'webSearch.tabs.accounts' },
  { id: 'probes', labelKey: 'webSearch.tabs.probes' },
  { id: 'status', labelKey: 'webSearch.tabs.status' },
  { id: 'access', labelKey: 'webSearch.tabs.access' },
];

/** 状态色调，用于 pill 与进度条。 */
export type ToneId = 'green' | 'orange' | 'red' | 'gray' | 'blue';

/** 账号状态 → 文案键与色调。 */
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

/** 渠道整体状态 → 文案键与色调。 */
export const CHANNEL_STATUS_META: Record<
  WebSearchChannelStatusValue,
  { labelKey: string; tone: ToneId; symbol: string }
> = {
  operational: { labelKey: 'webSearch.channelStatus.operational', tone: 'green', symbol: '●' },
  degraded: { labelKey: 'webSearch.channelStatus.degraded', tone: 'orange', symbol: '◐' },
  down: { labelKey: 'webSearch.channelStatus.down', tone: 'red', symbol: '○' },
  unknown: { labelKey: 'webSearch.channelStatus.unknown', tone: 'gray', symbol: '·' },
};

/** 额度刷新状态 → 文案键与色调。 */
export const QUOTA_REFRESH_META: Record<
  WebSearchQuotaRefreshStatus,
  { labelKey: string; tone: ToneId }
> = {
  success: { labelKey: 'webSearch.quotaRefresh.success', tone: 'green' },
  failed: { labelKey: 'webSearch.quotaRefresh.failed', tone: 'red' },
  unsupported: { labelKey: 'webSearch.quotaRefresh.unsupported', tone: 'gray' },
  never: { labelKey: 'webSearch.quotaRefresh.never', tone: 'blue' },
};

/** 额度维度 → 文案键。 */
export const QUOTA_KIND_LABEL_KEYS: Record<WebSearchQuotaKind, string> = {
  credits: 'webSearch.quotaKind.credits',
  key: 'webSearch.quotaKind.key',
  plan: 'webSearch.quotaKind.plan',
  paygo: 'webSearch.quotaKind.paygo',
};

/** 演示/文档用的渠道接入路径。 */
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

/** 各渠道的官方托管 MCP 端点说明（面板只展示，不代理请求）。 */
export function mcpExample(channel: string): string {
  return `POST /mcp/${channel}`;
}

/** 测活轮询间隔（毫秒）。测活是同步接口，这里只用于概览页的轻量刷新。 */
export const WEB_SEARCH_POLL_IDLE_MS = 60_000;
export const WEB_SEARCH_POLL_ACTIVE_MS = 2_000;

/** 历史色条渲染的最大天数。 */
export const HISTORY_STRIP_DAYS = 90;

/** 渠道状态自动刷新间隔（毫秒）。 */
export function statusPollInterval(running: boolean): number {
  return running ? WEB_SEARCH_POLL_ACTIVE_MS : WEB_SEARCH_POLL_IDLE_MS;
}
