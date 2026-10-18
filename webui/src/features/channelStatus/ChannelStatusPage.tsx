import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Skeleton } from '@/components/ui/Skeleton';
import { IconRefreshCw } from '@/components/ui/icons';
import {
  channelStatusApi,
  usageAnalyticsApi,
  webSearchApi,
  type ChannelStatusChannel,
  type ChannelStatusSnapshot,
  type UsageAnalyticsResponse,
  type UsageGroup,
  type WebSearchChannel,
  type WebSearchStatusComponent,
  type WebSearchStatusSnapshot,
} from '@/services/api';
import {
  formatBucketRange,
  formatChannelNumber,
  formatChannelRate,
  formatObservedAt,
  type ChannelStatusTone,
} from './utils';
import styles from './ChannelStatusPage.module.scss';

export function ChannelStatusPage() {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<ChannelStatusSnapshot | null>(null);
  const [webChannels, setWebChannels] = useState<WebSearchChannel[]>([]);
  const [webStatus, setWebStatus] = useState<WebSearchStatusSnapshot | null>(null);
  const [modelUsage, setModelUsage] = useState<UsageAnalyticsResponse | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const pollRef = useRef<number | null>(null);

  const loadAll = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    try {
      const [nextSnapshot, nextChannels, nextStatus, nextModelUsage] =
        await Promise.all([
          channelStatusApi.get(),
          webSearchApi.listChannels().catch(() => [] as WebSearchChannel[]),
          webSearchApi.getStatus().catch(() => null),
          usageAnalyticsApi
            .getAnalytics({ range: '24h', group_by: 'model' })
            .catch(() => null),
        ]);
      setSnapshot(nextSnapshot);
      setWebChannels(nextChannels);
      setWebStatus(nextStatus);
      setModelUsage(nextModelUsage);
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
  const statusByChannel = useMemo(() => {
    const map = new Map<string, WebSearchStatusComponent>();
    for (const channel of webStatus?.channels ?? []) map.set(channel.channel, channel);
    return map;
  }, [webStatus]);

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

      <div className={styles.legend}>
        {(['good', 'warning', 'critical', 'idle'] as ChannelStatusTone[]).map((tone) => (
          <span key={tone}>
            <i className={styles[`dot_${tone}`]} />
            {t(`channelStatus.legend.${tone}`)}
          </span>
        ))}
      </div>

      <section className={styles.statusSection}>
        <div className={styles.sectionHeading}>
          <div>
            <h2>{t('channelStatus.sections.models.title')}</h2>
            <p>{t('channelStatus.sections.models.description')}</p>
          </div>
          <span className={styles.sectionCount}>{llmChannels.length}</span>
        </div>
        <ModelStatusPanel
          channels={llmChannels}
          usage={modelUsage}
          bucketMinutes={snapshot?.bucket_minutes ?? 20}
        />
      </section>

      <section className={styles.statusSection}>
        <div className={styles.sectionHeading}>
          <div>
            <h2>{t('channelStatus.sections.search.title')}</h2>
            <p>{t('channelStatus.sections.search.description')}</p>
          </div>
          <span className={styles.sectionCount}>{searchChannels.length}</span>
        </div>
        <SearchStatusPanel
          channels={searchChannels}
          webChannels={webChannels}
          statusByChannel={statusByChannel}
          bucketMinutes={snapshot?.bucket_minutes ?? 20}
        />
      </section>
    </div>
  );
}

type ModelStatusTone = 'good' | 'warning' | 'critical' | 'idle';

interface ModelStatusRow {
  id: string;
  name: string;
  provider: string;
  protocols: string[];
  total: number;
  success: number;
  failed: number;
  successRate: number | null;
  ttftMS: number;
  latencyMS: number;
  buckets: ChannelStatusChannel['buckets'];
  tone: ModelStatusTone;
}

function modelProtocols(provider: string): string[] {
  switch (provider.toLowerCase()) {
    case 'claude':
      return ['claude_native'];
    case 'codex':
      return ['responses'];
    case 'openai':
      return ['chat', 'responses'];
    default:
      return ['chat'];
  }
}

function formatModelLatency(value: number): string {
  if (!value || !Number.isFinite(value) || value <= 0) return '—';
  if (value < 1000) return `${Math.round(value)} ms`;
  return `${(value / 1000).toFixed(2)} s`;
}

function modelTone(total: number, rate: number | null): ModelStatusTone {
  if (total === 0 || rate === null) return 'idle';
  if (rate >= 85) return 'good';
  if (rate >= 50) return 'warning';
  return 'critical';
}

function ModelStatusPanel({
  channels,
  usage,
  bucketMinutes,
}: {
  channels: ChannelStatusChannel[];
  usage: UsageAnalyticsResponse | null;
  bucketMinutes: number;
}) {
  const { t } = useTranslation();
  const usageByModel = new Map<string, UsageGroup>();
  for (const group of usage?.groups ?? []) {
    if (group.kind !== 'llm') continue;
    const key = group.model || group.key;
    if (key) usageByModel.set(key, group);
  }
  const rows: ModelStatusRow[] = [];
  for (const channel of channels) {
    for (const model of channel.models ?? []) {
      const group = usageByModel.get(model.id) ?? usageByModel.get(model.name);
      const bucketUsage = model.buckets.reduce(
        (total, bucket) => ({
          success: total.success + bucket.success,
          failed: total.failed + bucket.failed,
        }),
        { success: 0, failed: 0 }
      );
      const total = group?.requests ?? bucketUsage.success + bucketUsage.failed;
      const success = group?.success ?? bucketUsage.success;
      const failed = group?.failed ?? bucketUsage.failed;
      const rate =
        total > 0
          ? group && group.requests > 0
            ? group.success_rate * 100
            : (success / total) * 100
          : null;
      const name = model.name || model.id;
      rows.push({
        id: model.id,
        name: name.startsWith(`${channel.id}/`) ? name.slice(channel.id.length + 1) : name,
        provider: channel.id,
        protocols: modelProtocols(channel.id),
        total,
        success,
        failed,
        successRate: rate,
        ttftMS: group?.avg_ttft_ms ?? 0,
        latencyMS: group?.avg_latency_ms ?? 0,
        buckets: model.buckets,
        tone: modelTone(total, rate),
      });
    }
  }
  rows.sort((left, right) => right.total - left.total || left.name.localeCompare(right.name));
  const totalRequests = rows.reduce((sum, row) => sum + row.total, 0);
  const successRequests = rows.reduce((sum, row) => sum + row.success, 0);
  const overallRate = totalRequests > 0 ? (successRequests / totalRequests) * 100 : null;
  const activeModels = rows.filter((row) => row.total > 0).length;

  if (rows.length === 0) {
    return (
      <EmptyState
        title={t('channelStatus.empty.title')}
        description={t('channelStatus.empty.description')}
      />
    );
  }

  return (
    <div className={styles.modelStatusPanel}>
      <div className={styles.modelSummary}>
        <article>
          <span>{t('channelStatus.models.availability')}</span>
          <strong>{overallRate === null ? '—' : formatChannelRate(successRequests, totalRequests - successRequests)}</strong>
        </article>
        <article>
          <span>{t('channelStatus.models.active')}</span>
          <strong>
            {activeModels} / {rows.length}
          </strong>
        </article>
      </div>
      <div className={styles.modelStatusList}>
        {rows.map((row) => (
          <article key={`${row.provider}:${row.id}`} className={styles.modelStatusCard}>
            <div className={styles.modelStatusHead}>
              <div className={styles.modelIdentity}>
                <i className={`${styles.modelDot} ${styles[`model_${row.tone}`]}`} aria-hidden="true" />
                <strong>{row.name}</strong>
                <span className={`${styles.modelStatusPill} ${styles[`modelPill_${row.tone}`]}`}>
                  {t(`channelStatus.status.${row.tone}`)}
                </span>
                {row.protocols.map((protocol) => (
                  <span key={protocol} className={styles.protocolTag}>
                    {protocol}
                  </span>
                ))}
              </div>
              <div className={styles.modelMetrics}>
                <span>
                  {t('channelStatus.models.rate')}{' '}
                  <b>{row.successRate === null ? '—' : formatChannelRate(row.success, row.failed)}</b>
                </span>
                <span>
                  {t('channelStatus.models.ttft')} <b>{formatModelLatency(row.ttftMS)}</b>
                </span>
                <span>
                  {t('channelStatus.models.latency')} <b>{formatModelLatency(row.latencyMS)}</b>
                </span>
              </div>
            </div>
            <div className={styles.modelStrip} role="img" aria-label={row.name}>
              {row.buckets.map((bucket, index) => {
                const total = bucket.success + bucket.failed;
                const rate = total > 0 ? (bucket.success / total) * 100 : null;
                const tone = modelTone(total, rate);
                return (
                  <span
                    key={`${bucket.start}-${index}`}
                    className={`${styles.modelBar} ${styles[`modelBar_${tone}`]}`}
                    title={`${formatBucketRange(bucket.start, bucketMinutes)} · ${bucket.success}/${bucket.failed}`}
                  />
                );
              })}
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}

interface SearchStatusRow {
  id: string;
  name: string;
  total: number;
  success: number;
  failed: number;
  successRate: number | null;
  latencyMS: number;
  accounts: number;
  available: number;
  buckets: ChannelStatusChannel['buckets'];
  tone: ModelStatusTone;
}

function SearchStatusPanel({
  channels,
  webChannels,
  statusByChannel,
  bucketMinutes,
}: {
  channels: ChannelStatusChannel[];
  webChannels: WebSearchChannel[];
  statusByChannel: Map<string, WebSearchStatusComponent>;
  bucketMinutes: number;
}) {
  const { t } = useTranslation();
  const webChannelByID = new Map<string, WebSearchChannel>();
  for (const channel of webChannels) webChannelByID.set(channel.id, channel);

  const rows: SearchStatusRow[] = [];
  for (const channel of channels) {
    const webChannel = webChannelByID.get(channel.id);
    const probe = statusByChannel.get(channel.id);
    const bucketUsage = channel.buckets.reduce(
      (total, bucket) => ({
        success: total.success + bucket.success,
        failed: total.failed + bucket.failed,
      }),
      { success: 0, failed: 0 }
    );
    const total = bucketUsage.success + bucketUsage.failed;
    const rate = total > 0 ? (bucketUsage.success / total) * 100 : null;
    rows.push({
      id: channel.id,
      name: webChannel?.name ?? channel.name,
      total,
      success: bucketUsage.success,
      failed: bucketUsage.failed,
      successRate: rate,
      latencyMS: probe?.latency_ms ?? 0,
      accounts: webChannel?.accounts ?? 0,
      available: webChannel?.available ?? 0,
      buckets: channel.buckets,
      tone: modelTone(total, rate),
    });
  }
  rows.sort((left, right) => right.total - left.total || left.name.localeCompare(right.name));

  const totalRequests = rows.reduce((sum, row) => sum + row.total, 0);
  const successRequests = rows.reduce((sum, row) => sum + row.success, 0);
  const overallRate = totalRequests > 0 ? (successRequests / totalRequests) * 100 : null;
  const activeChannels = rows.filter((row) => row.total > 0).length;

  if (rows.length === 0) {
    return (
      <EmptyState
        title={t('channelStatus.empty.searchTitle')}
        description={t('channelStatus.empty.searchDescription')}
      />
    );
  }

  return (
    <div className={styles.modelStatusPanel}>
      <div className={styles.modelSummary}>
        <article>
          <span>{t('channelStatus.models.availability')}</span>
          <strong>{overallRate === null ? '—' : formatChannelRate(successRequests, totalRequests - successRequests)}</strong>
        </article>
        <article>
          <span>{t('channelStatus.search.active')}</span>
          <strong>
            {activeChannels} / {rows.length}
          </strong>
        </article>
      </div>
      <div className={styles.modelStatusList}>
        {rows.map((row) => (
          <article key={row.id} className={styles.modelStatusCard}>
            <div className={styles.modelStatusHead}>
              <div className={styles.modelIdentity}>
                <i className={`${styles.modelDot} ${styles[`model_${row.tone}`]}`} aria-hidden="true" />
                <strong>{row.name}</strong>
                <span className={`${styles.modelStatusPill} ${styles[`modelPill_${row.tone}`]}`}>
                  {t(`channelStatus.status.${row.tone}`)}
                </span>
                {row.accounts > 0 && (
                  <span
                    className={`${styles.protocolTag} ${
                      row.available < row.accounts ? styles.protocolTagWarning : ''
                    }`}
                  >
                    {t('channelStatus.search.accountsTag', {
                      available: row.available,
                      accounts: row.accounts,
                    })}
                  </span>
                )}
              </div>
              <div className={styles.modelMetrics}>
                <span>
                  {t('channelStatus.models.rate')}{' '}
                  <b>{row.successRate === null ? '—' : formatChannelRate(row.success, row.failed)}</b>
                </span>
                <span>
                  {t('channelStatus.search.requests')} <b>{formatChannelNumber(row.total)}</b>
                </span>
                <span>
                  {t('channelStatus.search.latency')} <b>{formatModelLatency(row.latencyMS)}</b>
                </span>
              </div>
            </div>
            <div className={styles.modelStrip} role="img" aria-label={row.name}>
              {row.buckets.map((bucket, index) => {
                const total = bucket.success + bucket.failed;
                const rate = total > 0 ? (bucket.success / total) * 100 : null;
                const tone = modelTone(total, rate);
                return (
                  <span
                    key={`${bucket.start}-${index}`}
                    className={`${styles.modelBar} ${styles[`modelBar_${tone}`]}`}
                    title={`${formatBucketRange(bucket.start, bucketMinutes)} · ${bucket.success}/${bucket.failed}`}
                  />
                );
              })}
            </div>
          </article>
        ))}
      </div>
    </div>
  );
}
