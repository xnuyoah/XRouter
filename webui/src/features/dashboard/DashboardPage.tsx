import { useCallback, useEffect, useMemo } from 'react';
import { Link } from 'react-router-dom';
import { useTranslation } from 'react-i18next';

import { ChannelLogo } from '@/components/common/ChannelLogo';
import { Button } from '@/components/ui/Button';
import { IconRefreshCw } from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import type { UsageEvent } from '@/services/api';
import { useAuthStore, useConfigStore } from '@/stores';
import { formatCompactNumber, formatDateValue, formatPercent } from '@/utils/format';

import { Meter } from './components/Meter';
import { Sparkline } from './components/Sparkline';
import { ThroughputChart } from './components/ThroughputChart';
import {
  buildDashboardView,
  type DashboardProvider,
  type DashboardSearchChannel,
} from './dashboardView';
import { useDashboardTelemetry } from './hooks/useDashboardTelemetry';
import { getProviderKeyCounts } from './hooks/useDashboardOverview';
import { providerLabel, toneForSuccessRate, type MeterTone } from './utils';
import styles from './dashboard.module.scss';

const DASH = '—';

function formatLatency(value: number | undefined): string {
  if (!value || !Number.isFinite(value) || value <= 0) return DASH;
  if (value < 1000) return `${Math.round(value)} ms`;
  return `${(value / 1000).toFixed(2)} s`;
}

function formatTimeOnly(value: Date | null, language: string): string {
  if (!value) return DASH;
  return value.toLocaleTimeString(language, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  });
}

function shortTime(value: string | undefined): string {
  if (!value) return DASH;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return DASH;
  return date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

function statusTone(tone: MeterTone) {
  return styles[`status_${tone}`];
}

function StatusPill({ tone, label, title }: { tone: MeterTone; label: string; title?: string }) {
  return (
    <span className={`${styles.statusPill} ${statusTone(tone)}`} title={title}>
      <i className={styles.statusDot} aria-hidden="true" />
      {label}
    </span>
  );
}

function PanelHead({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description?: string;
  action?: React.ReactNode;
}) {
  return (
    <div className={styles.panelHead}>
      <div className={styles.panelHeadCopy}>
        {eyebrow && <span className={styles.eyebrow}>{eyebrow}</span>}
        <h2>{title}</h2>
        {description && <p>{description}</p>}
      </div>
      {action}
    </div>
  );
}

const METRIC_ACCENTS: Record<MeterTone, string> = {
  good: 'var(--viz-success)',
  warning: 'var(--amber-color)',
  critical: 'var(--viz-failure)',
  idle: 'var(--border-color)',
};

function MetricCard({
  label,
  value,
  hint,
  tone,
  meter,
  compact,
}: {
  label: string;
  value: string;
  hint: string;
  tone?: MeterTone;
  meter?: number | null;
  compact?: boolean;
}) {
  return (
    <article
      className={styles.metricCard}
      style={
        {
          '--metric-accent': tone ? METRIC_ACCENTS[tone] : 'var(--border-color)',
        } as React.CSSProperties
      }
    >
      <span className={styles.metricLabel}>{label}</span>
      <strong className={`${styles.metricValue} ${compact ? styles.metricValueCompact : ''}`}>
        {value}
      </strong>
      {meter !== undefined && meter !== null && (
        <Meter value={meter} tone={tone} ariaLabel={label} className={styles.metricMeter} />
      )}
      <span className={styles.metricHint}>{hint}</span>
    </article>
  );
}

function HealthPanel({ view }: { view: ReturnType<typeof buildDashboardView> }) {
  const { t } = useTranslation();
  return (
    <section className={`${styles.panel} ${styles.healthPanel}`}>
      <PanelHead eyebrow={t('dashboard.health_eyebrow')} title={t('dashboard.health_title')} />
      <div className={styles.healthList}>
        <div className={styles.healthRow}>
          <span className={styles.healthLabel}>{t('dashboard.health_llm')}</span>
          <strong>
            {view.health.llmHealthy} / {view.health.llmTotal}
          </strong>
        </div>
        <div className={styles.healthRow}>
          <span className={styles.healthLabel}>{t('dashboard.health_search')}</span>
          <strong>
            {view.health.searchHealthy} / {view.health.searchTotal}
          </strong>
        </div>
        <div className={styles.healthRow}>
          <span className={styles.healthLabel}>{t('dashboard.health_proxy')}</span>
          <strong className={view.health.proxyReady ? styles.goodText : styles.criticalText}>
            {view.health.proxyReady ? t('dashboard.health_ready') : t('dashboard.health_missing')}
          </strong>
        </div>
        <div className={styles.healthRow}>
          <span className={styles.healthLabel}>{t('dashboard.health_failures')}</span>
          <strong className={view.health.failures > 0 ? styles.criticalText : ''}>
            {view.health.failures.toLocaleString()}
          </strong>
        </div>
      </div>
      <div className={styles.healthLinks}>
        <Link to="/channel-status">{t('dashboard.health_open_channels')} →</Link>
        <Link to="/web-search">{t('dashboard.health_open_search')} →</Link>
      </div>
    </section>
  );
}

function providerSparkColor(tone: MeterTone): string {
  if (tone === 'good') return 'var(--viz-success)';
  if (tone === 'warning') return 'var(--amber-color)';
  if (tone === 'critical') return 'var(--viz-failure)';
  return 'var(--text-quaternary)';
}

function ProviderStatus({ provider }: { provider: DashboardProvider }) {
  const { t } = useTranslation();
  const label =
    provider.tone === 'good'
      ? t('dashboard.provider_status_good')
      : provider.tone === 'warning'
        ? t('dashboard.provider_status_warning')
        : provider.tone === 'critical'
          ? t('dashboard.provider_status_critical')
          : t('dashboard.provider_status_idle');
  return <StatusPill tone={provider.tone} label={label} />;
}

function ProvidersPanel({
  providers,
  unknownLabel,
}: {
  providers: DashboardProvider[];
  unknownLabel: string;
}) {
  const { t } = useTranslation();
  return (
    <section className={styles.panel}>
      <PanelHead
        eyebrow={t('dashboard.fleet_eyebrow')}
        title={t('dashboard.fleet_title')}
        description={t('dashboard.fleet_description')}
        action={
          <Link className={styles.panelLink} to="/channel-status">
            {t('dashboard.fleet_open')} →
          </Link>
        }
      />
      {providers.length === 0 ? (
        <p className={styles.emptyNote}>{t('dashboard.fleet_empty')}</p>
      ) : (
        <>
          <div className={styles.providerDesktop}>
            <table className={styles.providerTable}>
              <thead>
                <tr>
                  <th>{t('dashboard.provider_col_name')}</th>
                  <th>{t('dashboard.provider_col_keys')}</th>
                  <th>{t('dashboard.provider_col_requests')}</th>
                  <th>{t('dashboard.provider_col_rate')}</th>
                  <th>{t('dashboard.provider_col_p95')}</th>
                  <th>{t('dashboard.provider_col_trend')}</th>
                  <th>{t('dashboard.provider_col_status')}</th>
                </tr>
              </thead>
              <tbody>
                {providers.map((provider) => (
                  <tr key={provider.id}>
                    <td>
                      <span className={styles.providerName}>
                        <ChannelLogo value={provider.id} size={20} />
                        <span>
                          <strong>{providerLabel(provider.id, unknownLabel)}</strong>
                          <small>
                            {t('dashboard.provider_models', { count: provider.models })}
                          </small>
                        </span>
                      </span>
                    </td>
                    <td>{provider.keys.toLocaleString()}</td>
                    <td>{provider.requests.toLocaleString()}</td>
                    <td>
                      {provider.successRate === null ? DASH : formatPercent(provider.successRate)}
                    </td>
                    <td>{formatLatency(provider.p95LatencyMS)}</td>
                    <td>
                      <Sparkline
                        points={provider.buckets}
                        color={providerSparkColor(provider.tone)}
                        ariaLabel={t('dashboard.fleet_spark_label', {
                          provider: providerLabel(provider.id, unknownLabel),
                        })}
                        className={styles.providerSpark}
                      />
                    </td>
                    <td>
                      <ProviderStatus provider={provider} />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className={styles.providerMobile}>
            {providers.map((provider) => (
              <article key={provider.id} className={styles.providerCard}>
                <div className={styles.providerCardHead}>
                  <span className={styles.providerName}>
                    <ChannelLogo value={provider.id} size={20} />
                    <span>
                      <strong>{providerLabel(provider.id, unknownLabel)}</strong>
                      <small>{t('dashboard.provider_models', { count: provider.models })}</small>
                    </span>
                  </span>
                  <ProviderStatus provider={provider} />
                </div>
                <div className={styles.providerStats}>
                  <span>
                    {t('dashboard.provider_col_keys')} <b>{provider.keys}</b>
                  </span>
                  <span>
                    {t('dashboard.provider_col_requests')} <b>{provider.requests}</b>
                  </span>
                  <span>
                    {t('dashboard.provider_col_rate')}{' '}
                    <b>
                      {provider.successRate === null ? DASH : formatPercent(provider.successRate)}
                    </b>
                  </span>
                  <span>
                    {t('dashboard.provider_col_p95')} <b>{formatLatency(provider.p95LatencyMS)}</b>
                  </span>
                </div>
                <Sparkline
                  points={provider.buckets}
                  color={providerSparkColor(provider.tone)}
                  ariaLabel={t('dashboard.fleet_spark_label', {
                    provider: providerLabel(provider.id, unknownLabel),
                  })}
                  className={styles.providerSpark}
                />
              </article>
            ))}
          </div>
        </>
      )}
    </section>
  );
}

function SearchChannelsPanel({ channels }: { channels: DashboardSearchChannel[] }) {
  const { t } = useTranslation();
  return (
    <section className={styles.panel}>
      <PanelHead
        eyebrow={t('dashboard.search_eyebrow')}
        title={t('dashboard.search_title')}
        description={t('dashboard.search_description')}
        action={
          <Link className={styles.panelLink} to="/web-search">
            {t('dashboard.search_open')} →
          </Link>
        }
      />
      {channels.length === 0 ? (
        <p className={styles.emptyNote}>{t('dashboard.search_empty')}</p>
      ) : (
        <div className={styles.searchGrid}>
          {channels.map((channel) => {
            const statusLabel =
              channel.tone === 'good'
                ? t('dashboard.provider_status_good')
                : channel.tone === 'warning'
                  ? t('dashboard.provider_status_warning')
                  : channel.tone === 'critical'
                    ? t('dashboard.provider_status_critical')
                    : t('dashboard.provider_status_idle');
            return (
              <Link key={channel.id} to="/web-search" className={styles.searchCard}>
                <div className={styles.searchCardHead}>
                  <span className={styles.providerName}>
                    <ChannelLogo value={channel.id} label={channel.name} size={20} />
                    <strong>{channel.name}</strong>
                  </span>
                  <StatusPill
                    tone={channel.tone}
                    label={statusLabel}
                    title={t(`webSearch.channelStatus.${channel.status}`)}
                  />
                </div>
                <div className={styles.searchAccounts}>
                  <strong>{channel.available}</strong>
                  <span>/ {channel.accounts}</span>
                  <span>{t('dashboard.search_available')}</span>
                </div>
                <div className={styles.searchMeta}>
                  <span>
                    {channel.limited > 0
                      ? t('dashboard.search_limited', { count: channel.limited })
                      : t('dashboard.search_last_check', {
                          time: shortTime(channel.lastChecked),
                        })}
                  </span>
                  <span>{formatLatency(channel.latencyMS)}</span>
                </div>
              </Link>
            );
          })}
        </div>
      )}
    </section>
  );
}

function FailuresPanel({
  failures,
  analyticsAvailable,
}: {
  failures: UsageEvent[];
  analyticsAvailable: boolean;
}) {
  const { t } = useTranslation();
  return (
    <section className={styles.panel}>
      <PanelHead
        eyebrow={t('dashboard.failures_eyebrow')}
        title={t('dashboard.failures_title')}
        action={
          <Link className={styles.panelLink} to="/usage/logs">
            {t('dashboard.failures_open')} →
          </Link>
        }
      />
      {!analyticsAvailable ? (
        <p className={styles.emptyNote}>{t('dashboard.failures_unavailable')}</p>
      ) : failures.length === 0 ? (
        <p className={styles.emptyNote}>{t('dashboard.failures_empty')}</p>
      ) : (
        <ul className={styles.failureList}>
          {failures.map((event) => (
            <li key={event.id} className={styles.failureRow}>
              <span className={styles.failureTime}>{shortTime(event.time)}</span>
              <span className={styles.failureModel}>
                <ChannelLogo value={event.provider} size={15} />
                <span>{event.alias || event.model || event.provider || DASH}</span>
              </span>
              <span className={styles.failureCode}>
                {event.status_code ? `HTTP ${event.status_code}` : t('dashboard.failure_error')}
              </span>
              <span className={styles.failureLatency}>{formatLatency(event.latency_ms)}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function RuntimePanel({
  config,
  serverVersion,
  serverBuildDate,
  language,
}: {
  config: ReturnType<typeof useConfigStore.getState>['config'];
  serverVersion?: string | null;
  serverBuildDate?: string | null;
  language: string;
}) {
  const { t } = useTranslation();
  const routing = config?.routingStrategy?.trim();
  const routingLabel =
    routing === 'round-robin'
      ? t('basic_settings.routing_strategy_round_robin')
      : routing === 'weighted-round-robin'
        ? t('basic_settings.routing_strategy_weighted_round_robin')
        : routing === 'fill-first'
          ? t('basic_settings.routing_strategy_fill_first')
          : routing || DASH;
  const proxyConfigured = Boolean(config?.proxyUrl?.trim());
  const rows = [
    { label: t('dashboard.runtime_routing'), value: routingLabel },
    { label: t('dashboard.runtime_retry'), value: String(config?.requestRetry ?? 0) },
    {
      label: t('dashboard.runtime_proxy'),
      value: proxyConfigured ? t('dashboard.health_ready') : t('dashboard.health_missing'),
    },
    {
      label: t('dashboard.runtime_management_keys'),
      value: String(config?.apiKeys?.length ?? 0),
    },
    { label: t('dashboard.runtime_version'), value: serverVersion?.trim() || DASH },
    {
      label: t('dashboard.runtime_build'),
      value: formatDateValue(serverBuildDate, language) || DASH,
    },
  ];
  const toggles = [
    { label: t('dashboard.runtime_debug'), on: Boolean(config?.debug) },
    { label: t('dashboard.runtime_file_logging'), on: Boolean(config?.loggingToFile) },
    { label: t('dashboard.runtime_request_log'), on: Boolean(config?.requestLog) },
    { label: t('dashboard.runtime_ws_auth'), on: Boolean(config?.wsAuth) },
  ];

  return (
    <section className={styles.panel}>
      <PanelHead
        eyebrow={t('dashboard.runtime_eyebrow')}
        title={t('dashboard.runtime_title')}
        action={
          <Link className={styles.panelLink} to="/config">
            {t('dashboard.runtime_link')} →
          </Link>
        }
      />
      <dl className={styles.runtimeList}>
        {rows.map((row) => (
          <div key={row.label} className={styles.runtimeRow}>
            <dt>{row.label}</dt>
            <dd>{row.value}</dd>
          </div>
        ))}
      </dl>
      <div className={styles.toggleRow}>
        {toggles.map((toggle) => (
          <span
            key={toggle.label}
            className={`${styles.togglePill} ${toggle.on ? styles.toggleOn : ''}`}
          >
            {toggle.label}
            <b>{toggle.on ? t('common.yes') : t('common.no')}</b>
          </span>
        ))}
      </div>
    </section>
  );
}

export function DashboardPage() {
  const { t, i18n } = useTranslation();
  const serverVersion = useAuthStore((state) => state.serverVersion);
  const serverBuildDate = useAuthStore((state) => state.serverBuildDate);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const telemetry = useDashboardTelemetry();
  const connected = connectionStatus === 'connected';

  useEffect(() => {
    if (connected) void fetchConfig().catch(() => undefined);
  }, [connected, fetchConfig]);

  const keyCounts = useMemo(() => (config ? getProviderKeyCounts(config) : {}), [config]);
  const view = useMemo(
    () =>
      buildDashboardView({
        snapshot: telemetry.snapshot,
        analytics: telemetry.analytics,
        searchChannels: telemetry.searchChannels,
        searchStatus: telemetry.searchStatus,
        keyCounts,
      }),
    [
      telemetry.snapshot,
      telemetry.analytics,
      telemetry.searchChannels,
      telemetry.searchStatus,
      keyCounts,
    ]
  );

  const refreshTelemetry = telemetry.refresh;
  const refreshAll = useCallback(async () => {
    await Promise.allSettled([refreshTelemetry(), fetchConfig(true)]);
  }, [refreshTelemetry, fetchConfig]);
  useHeaderRefresh(refreshAll, connected);

  const initialLoading = telemetry.loading && !telemetry.snapshot && !telemetry.searchStatus;
  const connectionLabel = t(
    connectionStatus === 'connected'
      ? 'common.connected'
      : connectionStatus === 'connecting'
        ? 'common.connecting'
        : 'common.disconnected'
  );
  const versionLabel = serverVersion ? `v${serverVersion.trim().replace(/^[vV]+/, '')}` : null;
  const unknownProviderLabel = t('dashboard.provider_unknown');
  const verdictTone: MeterTone = !connected ? 'idle' : view.health.tone;
  const verdictKey = !connected
    ? connectionStatus === 'connecting'
      ? 'hero_verdict_connecting'
      : 'hero_verdict_offline'
    : view.health.tone === 'good'
      ? 'hero_verdict_good'
      : view.health.tone === 'warning'
        ? 'hero_verdict_warning'
        : view.health.tone === 'critical'
          ? 'hero_verdict_critical'
          : 'hero_verdict_idle';
  const metricCards = [
    {
      key: 'requests',
      label: t('dashboard.metric_requests'),
      value: initialLoading ? DASH : formatCompactNumber(view.kpi.requests),
      hint: t('dashboard.metric_requests_hint', {
        success: view.kpi.success.toLocaleString(),
        failed: view.kpi.failed.toLocaleString(),
      }),
    },
    {
      key: 'success',
      label: t('dashboard.success_rate'),
      value:
        initialLoading || view.kpi.successRate === null
          ? DASH
          : formatPercent(view.kpi.successRate),
      hint: t('dashboard.metric_success_hint'),
      tone: toneForSuccessRate(view.kpi.successRate),
      meter: view.kpi.successRate,
    },
    {
      key: 'p95',
      label: t('dashboard.metric_p95'),
      value: initialLoading ? DASH : formatLatency(view.kpi.p95LatencyMS),
      hint: t('dashboard.metric_p95_hint'),
    },
    {
      key: 'keys',
      label: t('dashboard.metric_provider_keys'),
      value: initialLoading ? DASH : view.kpi.providerKeys.toLocaleString(),
      hint: t('dashboard.metric_provider_keys_hint'),
    },
    {
      key: 'models',
      label: t('dashboard.metric_models'),
      value: initialLoading ? DASH : view.kpi.models.toLocaleString(),
      hint: t('dashboard.metric_models_hint'),
    },
    {
      key: 'accounts',
      label: t('dashboard.metric_search_accounts'),
      value: initialLoading ? DASH : `${view.kpi.searchAvailable}/${view.kpi.searchAccounts}`,
      hint: t('dashboard.metric_search_accounts_hint', {
        count: view.searchChannels.length,
      }),
      meter:
        view.kpi.searchAccounts > 0
          ? (view.kpi.searchAvailable / view.kpi.searchAccounts) * 100
          : null,
      tone: view.searchChannels.some((channel) => channel.limited > 0)
        ? ('warning' as MeterTone)
        : ('good' as MeterTone),
      compact: true,
    },
  ];

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div className={styles.headerCopy}>
          <div className={styles.titleRow}>
            <h1>{t('dashboard.title')}</h1>
            <StatusPill tone={verdictTone} label={t(`dashboard.${verdictKey}`)} />
          </div>
          <p>{t('dashboard.subtitle')}</p>
          <div className={styles.metaLine}>
            {[
              versionLabel,
              connectionLabel,
              t('dashboard.last_updated', {
                time: formatTimeOnly(telemetry.lastUpdated, i18n.language),
              }),
            ]
              .filter(Boolean)
              .join(' · ')}
          </div>
        </div>
        <Button
          variant="secondary"
          size="sm"
          onClick={() => void refreshAll()}
          loading={telemetry.loading}
        >
          <IconRefreshCw size={14} />
          {t('common.refresh')}
        </Button>
      </header>

      {telemetry.error && <div className={styles.error}>{telemetry.error}</div>}

      <section className={styles.metrics} aria-label={t('dashboard.stats_aria')}>
        {metricCards.map(({ key, ...card }) => (
          <MetricCard key={key} {...card} />
        ))}
      </section>

      <div className={styles.primaryGrid}>
        <section className={`${styles.panel} ${styles.trafficPanel}`}>
          <PanelHead
            eyebrow={t('dashboard.traffic_eyebrow')}
            title={t('dashboard.traffic_title_24h')}
            description={t('dashboard.traffic_description_24h')}
            action={
              <Link className={styles.panelLink} to="/usage">
                {t('dashboard.traffic_open')} →
              </Link>
            }
          />
          <ThroughputChart
            traffic={view.traffic}
            summaryLabel={t('dashboard.traffic_chart_summary_24h', {
              total: view.traffic.total,
              success: view.traffic.totalSuccess,
              failed: view.traffic.totalFailure,
            })}
          />
        </section>
        <HealthPanel view={view} />
      </div>

      <ProvidersPanel providers={view.providers} unknownLabel={unknownProviderLabel} />

      <SearchChannelsPanel channels={view.searchChannels} />

      <div className={styles.bottomGrid}>
        <FailuresPanel
          failures={telemetry.failures}
          analyticsAvailable={telemetry.analytics !== null}
        />
        <RuntimePanel
          config={config}
          serverVersion={serverVersion}
          serverBuildDate={serverBuildDate}
          language={i18n.language}
        />
      </div>
    </div>
  );
}
