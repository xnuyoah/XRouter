import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import {
  IconActivity,
  IconChevronDown,
  IconChevronUp,
  IconRefreshCw,
} from '@/components/ui/icons';
import {
  channelStatusApi,
  webSearchApi,
  type ChannelStatusBucket,
  type ChannelStatusChannel,
  type ChannelStatusModel,
  type ChannelStatusSnapshot,
  type WebSearchAccount,
  type WebSearchChannel,
  type WebSearchQuotaBalance,
  type WebSearchQuotaHistoryPoint,
  type WebSearchStatusComponent,
  type WebSearchStatusSnapshot,
} from '@/services/api';
import {
  ACCOUNT_STATUS_META,
  CHANNEL_STATUS_META,
  QUOTA_KIND_LABEL_KEYS,
  QUOTA_REFRESH_META,
} from '@/features/webSearch/constants';
import { QuotaHistoryChart } from './components/QuotaHistoryChart';
import {
  bucketTotal,
  channelStatusTone,
  formatBucketRange,
  formatChannelNumber,
  formatChannelRate,
  formatLatency,
  formatObservedAt,
  latestNonEmptyBucketIndex,
  type ChannelStatusTone,
} from './utils';
import styles from './ChannelStatusPage.module.scss';

type TabId = 'llm' | 'web_search';

const TABS: { id: TabId; labelKey: string }[] = [
  { id: 'llm', labelKey: 'channelStatus.tabs.llm' },
  { id: 'web_search', labelKey: 'channelStatus.tabs.webSearch' },
];

const STATUS_LABEL_KEYS: Record<ChannelStatusTone, string> = {
  good: 'channelStatus.status.good',
  warning: 'channelStatus.status.warning',
  critical: 'channelStatus.status.critical',
  idle: 'channelStatus.status.idle',
};

export function ChannelStatusPage() {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<ChannelStatusSnapshot | null>(null);
  const [webChannels, setWebChannels] = useState<WebSearchChannel[]>([]);
  const [accounts, setAccounts] = useState<WebSearchAccount[]>([]);
  const [webStatus, setWebStatus] = useState<WebSearchStatusSnapshot | null>(null);
  const [tab, setTab] = useState<TabId>('llm');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);
  const [expandedModels, setExpandedModels] = useState<Record<string, boolean>>({});
  const [collapsedAccounts, setCollapsedAccounts] = useState<Record<string, boolean>>({});
  const [expandedHistory, setExpandedHistory] = useState<Record<string, boolean>>({});
  const [selectedBuckets, setSelectedBuckets] = useState<Record<string, number>>({});
  const [quotaHistory, setQuotaHistory] = useState<Record<string, WebSearchQuotaHistoryPoint[]>>({});
  const [historyLoading, setHistoryLoading] = useState<Record<string, boolean>>({});
  const pollRef = useRef<number | null>(null);

  const loadAll = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [nextSnapshot, nextChannels, nextAccounts, nextStatus] = await Promise.all([
        channelStatusApi.get(),
        webSearchApi.listChannels().catch(() => [] as WebSearchChannel[]),
        webSearchApi.listAccounts().catch(() => [] as WebSearchAccount[]),
        webSearchApi.getStatus().catch(() => null),
      ]);
      setSnapshot(nextSnapshot);
      setWebChannels(nextChannels);
      setAccounts(nextAccounts);
      setWebStatus(nextStatus);
      setError(null);
    } catch (errLoad) {
      setError(errLoad instanceof Error ? errLoad.message : String(errLoad));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadAll();
  }, [loadAll]);

  useEffect(() => {
    pollRef.current = window.setInterval(() => {
      void loadAll(true);
    }, 60_000);
    return () => {
      if (pollRef.current !== null) window.clearInterval(pollRef.current);
    };
  }, [loadAll]);

  // Refresh faster while a status check is running.
  useEffect(() => {
    if (!webStatus?.check_running) return;
    const timer = window.setInterval(() => {
      void webSearchApi
        .getStatus()
        .then(setWebStatus)
        .catch(() => undefined);
    }, 2_000);
    return () => window.clearInterval(timer);
  }, [webStatus?.check_running]);

  const llmChannels = useMemo(
    () => (snapshot?.channels ?? []).filter((channel) => channel.kind === 'llm'),
    [snapshot]
  );
  const searchChannels = useMemo(
    () => (snapshot?.channels ?? []).filter((channel) => channel.kind === 'web_search'),
    [snapshot]
  );
  const accountsByChannel = useMemo(() => {
    const grouped = new Map<string, WebSearchAccount[]>();
    for (const account of accounts) {
      const entry = grouped.get(account.channel) ?? [];
      entry.push(account);
      grouped.set(account.channel, entry);
    }
    return grouped;
  }, [accounts]);
  const statusByChannel = useMemo(() => {
    const map = new Map<string, WebSearchStatusComponent>();
    for (const channel of webStatus?.channels ?? []) map.set(channel.channel, channel);
    return map;
  }, [webStatus]);

  const refreshQuotaHistory = useCallback(async (account: WebSearchAccount) => {
    const key = accountKey(account);
    setHistoryLoading((current) => ({ ...current, [key]: true }));
    try {
      const response = await webSearchApi.getQuotaHistory({
        channel: account.channel,
        account_id: account.account_id,
        hours: 720,
        limit: 1000,
      });
      setQuotaHistory((current) => ({ ...current, [key]: response.points ?? [] }));
    } catch {
      setQuotaHistory((current) => ({ ...current, [key]: [] }));
    } finally {
      setHistoryLoading((current) => ({ ...current, [key]: false }));
    }
  }, []);

  const toggleHistory = useCallback(
    (account: WebSearchAccount) => {
      const key = accountKey(account);
      const next = !expandedHistory[key];
      setExpandedHistory((current) => ({ ...current, [key]: next }));
      if (next && quotaHistory[key] === undefined) {
        void refreshQuotaHistory(account);
      }
    },
    [expandedHistory, quotaHistory, refreshQuotaHistory]
  );

  const probeAccount = useCallback(
    async (account: WebSearchAccount) => {
      const key = accountKey(account);
      setBusy(`probe:${key}`);
      setMessage(null);
      try {
        const summary = await webSearchApi.probe({ account_ids: [account.account_id] });
        setMessage(
          t('channelStatus.actionSummary', {
            succeeded: summary.succeeded,
            failed: summary.failed,
          })
        );
        await loadAll(true);
      } catch (errProbe) {
        setError(errProbe instanceof Error ? errProbe.message : String(errProbe));
      } finally {
        setBusy(null);
      }
    },
    [loadAll, t]
  );

  const probeChannel = useCallback(
    async (channel: WebSearchChannel) => {
      setBusy(`probe-channel:${channel.id}`);
      setMessage(null);
      try {
        const summary = await webSearchApi.probe({ channel: channel.id });
        setMessage(
          t('channelStatus.actionSummary', {
            succeeded: summary.succeeded,
            failed: summary.failed,
          })
        );
        await loadAll(true);
      } catch (errProbe) {
        setError(errProbe instanceof Error ? errProbe.message : String(errProbe));
      } finally {
        setBusy(null);
      }
    },
    [loadAll, t]
  );

  const checkChannel = useCallback(
    async (channel: WebSearchChannel) => {
      setBusy(`check:${channel.id}`);
      setMessage(null);
      try {
        await webSearchApi.startStatusCheck(channel.id);
        const next = await webSearchApi.getStatus();
        setWebStatus(next);
      } catch (errCheck) {
        setError(errCheck instanceof Error ? errCheck.message : String(errCheck));
      } finally {
        setBusy(null);
      }
    },
    []
  );

  const refreshAccountQuota = useCallback(
    async (account: WebSearchAccount) => {
      const key = accountKey(account);
      setBusy(`quota:${key}`);
      setMessage(null);
      try {
        await webSearchApi.refreshQuota({
          channel: account.channel,
          account_ids: [account.account_id],
        });
        setQuotaHistory((current) => {
          const next = { ...current };
          delete next[key];
          return next;
        });
        await loadAll(true);
        if (expandedHistory[key]) {
          await refreshQuotaHistory(account);
        }
      } catch (errQuota) {
        setError(errQuota instanceof Error ? errQuota.message : String(errQuota));
      } finally {
        setBusy(null);
      }
    },
    [expandedHistory, loadAll, refreshQuotaHistory]
  );

  const currentChannels = tab === 'llm' ? llmChannels : searchChannels;

  if (loading && !snapshot) {
    return (
      <div className={styles.page}>
        <Skeleton height={28} width={180} />
        <Skeleton height={120} />
        <Skeleton height={240} />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <div className={styles.header}>
        <div>
          <h1>{t('channelStatus.title')}</h1>
          <p className={styles.subtitle}>{t('channelStatus.subtitle')}</p>
        </div>
        <div className={styles.headerActions}>
          <span className={styles.updatedPill}>
            {t('channelStatus.lastUpdated')}: {formatObservedAt(snapshot?.observed_at)}
          </span>
          <Button size="sm" variant="secondary" onClick={() => void loadAll()}>
            <IconRefreshCw size={14} />
            {t('channelStatus.refresh')}
          </Button>
        </div>
      </div>

      {error && <div className={styles.error}>{error}</div>}
      {message && <div className={styles.message}>{message}</div>}

      <div className={styles.tabs} role="tablist">
        {TABS.map((item) => (
          <button
            key={item.id}
            type="button"
            role="tab"
            aria-selected={tab === item.id}
            className={`${styles.tab} ${tab === item.id ? styles.tabActive : ''}`}
            onClick={() => setTab(item.id)}
          >
            {t(item.labelKey)}
            <span className={styles.tabCount}>
              {item.id === 'llm' ? llmChannels.length : searchChannels.length}
            </span>
          </button>
        ))}
      </div>

      <div className={styles.legend}>
        {(['good', 'warning', 'critical', 'idle'] as ChannelStatusTone[]).map((tone) => (
          <span key={tone}>
            <i className={styles[`dot_${tone}`]} />
            {t(`channelStatus.legend.${tone}`)}
          </span>
        ))}
      </div>

      {currentChannels.length === 0 ? (
        <EmptyState
          title={t('channelStatus.empty.title')}
          description={t('channelStatus.empty.description')}
        />
      ) : (
        <div className={styles.cards}>
          {currentChannels.map((channel) => (
            <ChannelCard
              key={`${channel.kind}:${channel.id}`}
              channel={channel}
              webChannel={webChannels.find((item) => item.id === channel.id)}
              accounts={accountsByChannel.get(channel.id) ?? []}
              probeStatus={statusByChannel.get(channel.id)}
              bucketMinutes={snapshot?.bucket_minutes ?? 20}
              expandedModels={expandedModels[`${channel.kind}:${channel.id}`] === true}
              onToggleModels={() =>
                setExpandedModels((current) => ({
                  ...current,
                  [`${channel.kind}:${channel.id}`]: !current[`${channel.kind}:${channel.id}`],
                }))
              }
              accountsCollapsed={collapsedAccounts[channel.id] === true}
              onToggleAccounts={() =>
                setCollapsedAccounts((current) => ({
                  ...current,
                  [channel.id]: !current[channel.id],
                }))
              }
              selectedBuckets={selectedBuckets}
              onSelectBucket={(key, index) =>
                setSelectedBuckets((current) => ({ ...current, [key]: index }))
              }
              busy={busy}
              expandedHistory={expandedHistory}
              quotaHistory={quotaHistory}
              historyLoading={historyLoading}
              onProbeAccount={(account) => void probeAccount(account)}
              onProbeChannel={(webChannel) => void probeChannel(webChannel)}
              onCheckChannel={(webChannel) => void checkChannel(webChannel)}
              onRefreshQuota={(account) => void refreshAccountQuota(account)}
              onToggleHistory={toggleHistory}
            />
          ))}
        </div>
      )}
    </div>
  );
}

interface ChannelCardProps {
  channel: ChannelStatusChannel;
  webChannel?: WebSearchChannel;
  accounts: WebSearchAccount[];
  probeStatus?: WebSearchStatusComponent;
  bucketMinutes: number;
  expandedModels: boolean;
  onToggleModels: () => void;
  accountsCollapsed: boolean;
  onToggleAccounts: () => void;
  selectedBuckets: Record<string, number>;
  onSelectBucket: (key: string, index: number) => void;
  busy: string | null;
  expandedHistory: Record<string, boolean>;
  quotaHistory: Record<string, WebSearchQuotaHistoryPoint[]>;
  historyLoading: Record<string, boolean>;
  onProbeAccount: (account: WebSearchAccount) => void;
  onProbeChannel: (webChannel: WebSearchChannel) => void;
  onCheckChannel: (webChannel: WebSearchChannel) => void;
  onRefreshQuota: (account: WebSearchAccount) => void;
  onToggleHistory: (account: WebSearchAccount) => void;
}

function ChannelCard({
  channel,
  webChannel,
  accounts,
  probeStatus,
  bucketMinutes,
  expandedModels,
  onToggleModels,
  accountsCollapsed,
  onToggleAccounts,
  selectedBuckets,
  onSelectBucket,
  busy,
  expandedHistory,
  quotaHistory,
  historyLoading,
  onProbeAccount,
  onProbeChannel,
  onCheckChannel,
  onRefreshQuota,
  onToggleHistory,
}: ChannelCardProps) {
  const { t } = useTranslation();
  const tone = channelStatusTone(channel.success, channel.failed);
  const cardKey = `${channel.kind}:${channel.id}`;
  const selectedIndex = resolveSelectedIndex(selectedBuckets[cardKey], channel.buckets);
  const selectedBucket = selectedIndex >= 0 ? channel.buckets[selectedIndex] : undefined;
  const isSearch = channel.kind === 'web_search';
  const models = channel.models ?? [];

  return (
    <section className={styles.card}>
      <div className={styles.cardHead}>
        <div className={styles.identity}>
          <span className={styles.icon}>
            <IconActivity size={20} />
          </span>
          <div>
            <strong>{channel.name}</strong>
            <p>{t(isSearch ? 'channelStatus.card.allEndpoints' : 'channelStatus.card.allCredentials')}</p>
          </div>
        </div>
        <StatusPill tone={tone} />
      </div>

      <div className={styles.metrics}>
        <Metric label={t('channelStatus.metrics.total')} value={formatChannelNumber(channel.total)} />
        <Metric
          label={t('channelStatus.metrics.successRate')}
          value={formatChannelRate(channel.success, channel.failed)}
        />
        <Metric
          label={t('channelStatus.metrics.success')}
          value={formatChannelNumber(channel.success)}
          tone="good"
        />
        <Metric
          label={t('channelStatus.metrics.failed')}
          value={formatChannelNumber(channel.failed)}
          tone="critical"
        />
      </div>

      <AvailabilityStrip
        buckets={channel.buckets}
        selectedIndex={selectedIndex}
        onSelect={(index) => onSelectBucket(cardKey, index)}
        bucketMinutes={bucketMinutes}
      />
      <BucketDetail bucket={selectedBucket} bucketMinutes={bucketMinutes} />

      {!isSearch && models.length > 0 && (
        <div className={styles.section}>
          <button type="button" className={styles.sectionToggle} onClick={onToggleModels} aria-expanded={expandedModels}>
            <span>{t('channelStatus.card.models', { count: models.length })}</span>
            {expandedModels ? <IconChevronUp size={16} /> : <IconChevronDown size={16} />}
          </button>
          {expandedModels && (
            <div className={styles.modelList}>
              {models.map((model) => (
                <ModelRow
                  key={model.id}
                  model={model}
                  bucketMinutes={bucketMinutes}
                  selectedIndex={resolveSelectedIndex(
                    selectedBuckets[`${cardKey}:model:${model.id}`],
                    model.buckets
                  )}
                  onSelect={(index) =>
                    onSelectBucket(`${cardKey}:model:${model.id}`, index)
                  }
                />
              ))}
            </div>
          )}
        </div>
      )}

      {isSearch && webChannel && (
        <div className={styles.section}>
          <button type="button" className={styles.sectionToggle} onClick={onToggleAccounts} aria-expanded={!accountsCollapsed}>
            <span>{t('channelStatus.card.accounts', { count: accounts.length })}</span>
            {accountsCollapsed ? <IconChevronDown size={16} /> : <IconChevronUp size={16} />}
          </button>
          {!accountsCollapsed && (
            <div className={styles.accounts}>
              <div className={styles.accountsHead}>
                <div className={styles.probeStatus}>
                  {probeStatus ? (
                    <>
                      <StatusPill
                        tone={probeStatus.status === 'operational' ? 'good' : probeStatus.status === 'degraded' ? 'warning' : probeStatus.status === 'down' ? 'critical' : 'idle'}
                        label={t(CHANNEL_STATUS_META[probeStatus.status].labelKey)}
                      />
                      <span className={styles.muted}>
                        {t('channelStatus.probe.lastChecked')}: {formatObservedAt(probeStatus.checked_at)} ·{' '}
                        {t('channelStatus.probe.latency')}: {formatLatency(probeStatus.latency_ms)} ·{' '}
                        {t('channelStatus.probe.http')}: {probeStatus.http_status || '--'}
                      </span>
                    </>
                  ) : (
                    <span className={styles.muted}>{t('channelStatus.probe.noStatus')}</span>
                  )}
                </div>
                <div className={styles.rowActions}>
                  <Button
                    size="sm"
                    variant="secondary"
                    loading={busy === `probe-channel:${webChannel.id}`}
                    onClick={() => onProbeChannel(webChannel)}
                  >
                    {t('webSearch.actions.probeAll')}
                  </Button>
                  <Button
                    size="sm"
                    variant="secondary"
                    loading={busy === `check:${webChannel.id}`}
                    onClick={() => onCheckChannel(webChannel)}
                  >
                    {t('webSearch.actions.checkStatus')}
                  </Button>
                </div>
              </div>

              {accounts.length === 0 ? (
                <div className={styles.muted}>{t('channelStatus.account.empty')}</div>
              ) : (
                <div className={styles.accountList}>
                  {accounts.map((account) => {
                    const key = accountKey(account);
                    const meta = ACCOUNT_STATUS_META[account.status];
                    const quotaMeta = QUOTA_REFRESH_META[account.quota_refresh_status];
                    return (
                      <div key={key} className={styles.account}>
                        <div className={styles.accountHead}>
                          <div>
                            <strong>{account.name || account.account_id}</strong>
                            <span className={styles.mono}>{account.api_key_masked}</span>
                            <StatusPill
                              tone={accountPillTone(meta.tone)}
                              label={t(meta.labelKey)}
                            />
                            {account.cooldown && (
                              <span className={styles.muted}>
                                {t('channelStatus.account.cooldown')} {account.cooldown_until ?? '--'}
                              </span>
                            )}
                          </div>
                          <div className={styles.rowActions}>
                            <Button
                              size="sm"
                              variant="ghost"
                              loading={busy === `probe:${key}`}
                              onClick={() => onProbeAccount(account)}
                            >
                              {t('webSearch.actions.probe')}
                            </Button>
                            <Button
                              size="sm"
                              variant="ghost"
                              loading={busy === `quota:${key}`}
                              onClick={() => onRefreshQuota(account)}
                            >
                              {t('channelStatus.quota.refresh')}
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => onToggleHistory(account)}>
                              {t('channelStatus.quota.trend')}
                            </Button>
                          </div>
                        </div>
                        <div className={styles.accountMeta}>
                          <span>
                            {t('channelStatus.probe.lastChecked')}: {formatObservedAt(account.last_checked_at)} ·{' '}
                            {t('channelStatus.probe.latency')}: {formatLatency(account.last_latency_ms)} ·{' '}
                            {t('channelStatus.probe.http')}: {account.last_http_status || '--'}
                          </span>
                          {account.status_message && <span>{account.status_message}</span>}
                        </div>
                        <div className={styles.accountQuota}>
                          <span className={hasNegativeQuota(account) ? styles.negative : ''}>
                            {quotaText(account, t)}
                          </span>
                          <span className={styles.muted}>
                            {t('channelStatus.quota.resetAt')}: {formatObservedAt(account.quota?.reset_at)}
                          </span>
                          <span className={`${styles.pill} ${styles[`pill_${accountPillTone(quotaMeta.tone)}`]}`}>
                            {t(quotaMeta.labelKey)}
                          </span>
                        </div>
                        {expandedHistory[key] && (
                          <QuotaHistoryChart
                            points={quotaHistory[key] ?? []}
                            loading={historyLoading[key] === true}
                          />
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
            </div>
          )}
        </div>
      )}
    </section>
  );
}

function ModelRow({
  model,
  bucketMinutes,
  selectedIndex,
  onSelect,
}: {
  model: ChannelStatusModel;
  bucketMinutes: number;
  selectedIndex: number;
  onSelect: (index: number) => void;
}) {
  const { t } = useTranslation();
  const tone = channelStatusTone(model.success, model.failed);
  const selectedBucket = selectedIndex >= 0 ? model.buckets[selectedIndex] : undefined;
  return (
    <div className={styles.model}>
      <div className={styles.modelHead}>
        <div>
          <strong>{model.name}</strong>
          {model.name !== model.id && <span className={styles.mono}>{model.id}</span>}
        </div>
        <StatusPill tone={tone} />
      </div>
      <div className={styles.modelMetrics}>
        <span>{t('channelStatus.metrics.total')} {formatChannelNumber(model.total)}</span>
        <span>{t('channelStatus.metrics.successRate')} {formatChannelRate(model.success, model.failed)}</span>
        <span className={styles.success}>{t('channelStatus.metrics.success')} {formatChannelNumber(model.success)}</span>
        <span className={styles.failure}>{t('channelStatus.metrics.failed')} {formatChannelNumber(model.failed)}</span>
      </div>
      <AvailabilityStrip
        buckets={model.buckets}
        selectedIndex={selectedIndex}
        onSelect={onSelect}
        bucketMinutes={bucketMinutes}
        compact
      />
      <BucketDetail bucket={selectedBucket} bucketMinutes={bucketMinutes} compact />
    </div>
  );
}

function AvailabilityStrip({
  buckets,
  selectedIndex,
  onSelect,
  bucketMinutes,
  compact = false,
}: {
  buckets: ChannelStatusBucket[];
  selectedIndex: number;
  onSelect: (index: number) => void;
  bucketMinutes: number;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  return (
    <div className={styles.stripWrapper}>
      <div className={`${styles.strip} ${compact ? styles.stripCompact : ''}`}>
        {buckets.map((bucket, index) => {
          const tone = channelStatusTone(bucket.success, bucket.failed);
          const total = bucketTotal(bucket);
          return (
            <button
              key={`${bucket.start}-${index}`}
              type="button"
              className={`${styles.bar} ${styles[`bar_${tone}`]} ${
                index === selectedIndex ? styles.barSelected : ''
              }`}
              aria-label={`${formatBucketRange(bucket.start, bucketMinutes)}: ${bucket.success}/${bucket.failed}`}
              title={`${formatBucketRange(bucket.start, bucketMinutes)} · ${formatChannelRate(
                bucket.success,
                bucket.failed
              )} · ${total}`}
              onClick={() => onSelect(index)}
            />
          );
        })}
      </div>
      {!compact && (
        <div className={styles.stripLabels}>
          <span>{t('channelStatus.strip.past24h')}</span>
          <span>{t('channelStatus.strip.now')}</span>
        </div>
      )}
    </div>
  );
}

function BucketDetail({
  bucket,
  bucketMinutes,
  compact = false,
}: {
  bucket: ChannelStatusBucket | undefined;
  bucketMinutes: number;
  compact?: boolean;
}) {
  const { t } = useTranslation();
  if (!bucket) {
    return (
      <div className={`${styles.detail} ${compact ? styles.detailCompact : ''}`}>
        <span className={styles.muted}>{t('channelStatus.detail.noRequests')}</span>
      </div>
    );
  }
  return (
    <div className={`${styles.detail} ${compact ? styles.detailCompact : ''}`}>
      <div>
        <span>{t('channelStatus.detail.range')}</span>
        <strong>{formatBucketRange(bucket.start, bucketMinutes)}</strong>
      </div>
      <div>
        <span>{t('channelStatus.metrics.successRate')}</span>
        <strong>{formatChannelRate(bucket.success, bucket.failed)}</strong>
      </div>
      <div>
        <span>{t('channelStatus.detail.total')}</span>
        <strong>{formatChannelNumber(bucketTotal(bucket))}</strong>
      </div>
      <div>
        <span>{t('channelStatus.detail.successFailed')}</span>
        <strong>
          {formatChannelNumber(bucket.success)} / {formatChannelNumber(bucket.failed)}
        </strong>
      </div>
    </div>
  );
}

function StatusPill({ tone, label }: { tone: ChannelStatusTone; label?: string }) {
  const { t } = useTranslation();
  return (
    <span className={`${styles.pill} ${styles[`pill_${tone}`]}`}>
      {tone === 'good' ? '●' : tone === 'warning' ? '◐' : tone === 'critical' ? '○' : '·'}{' '}
      {label ?? t(STATUS_LABEL_KEYS[tone])}
    </span>
  );
}

function Metric({
  label,
  value,
  tone,
}: {
  label: string;
  value: string;
  tone?: 'good' | 'critical';
}) {
  return (
    <div className={styles.metric}>
      <span>{label}</span>
      <strong className={tone ? styles[tone] : ''}>{value}</strong>
    </div>
  );
}

function resolveSelectedIndex(selected: number | undefined, buckets: ChannelStatusBucket[]): number {
  if (selected !== undefined && selected >= 0 && selected < buckets.length) return selected;
  return latestNonEmptyBucketIndex(buckets);
}

function accountKey(account: WebSearchAccount): string {
  return `${account.channel}:${account.account_id}`;
}

function accountPillTone(tone: 'green' | 'orange' | 'red' | 'gray' | 'blue'): ChannelStatusTone {
  switch (tone) {
    case 'green':
      return 'good';
    case 'orange':
      return 'warning';
    case 'red':
      return 'critical';
    default:
      return 'idle';
  }
}

function hasNegativeQuota(account: WebSearchAccount): boolean {
  return (account.quota?.balances ?? []).some(
    (balance) => balance.remaining !== undefined && balance.remaining < 0
  );
}

function quotaText(account: WebSearchAccount, t: (key: string) => string): string {
  const balances = account.quota?.balances ?? [];
  if (balances.length === 0) return t('webSearch.quotaUnknown');
  return balances
    .map((balance: WebSearchQuotaBalance) => {
      const label = t(QUOTA_KIND_LABEL_KEYS[balance.kind] ?? 'webSearch.quotaKind.credits');
      const remaining = balance.remaining ?? '--';
      const used = balance.used ?? '--';
      const limit = balance.limit ?? '--';
      return `${label} ${remaining} (${used}/${limit})`;
    })
    .join(' · ');
}
