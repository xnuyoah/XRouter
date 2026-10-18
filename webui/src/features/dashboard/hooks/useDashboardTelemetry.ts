import { useCallback, useEffect, useRef, useState } from 'react';

import {
  channelStatusApi,
  usageAnalyticsApi,
  webSearchApi,
  type ChannelStatusSnapshot,
  type UsageAnalyticsResponse,
  type UsageEvent,
  type WebSearchChannel,
  type WebSearchStatusSnapshot,
} from '@/services/api';
import { useAuthStore } from '@/stores';

interface DashboardTelemetry {
  snapshot: ChannelStatusSnapshot | null;
  analytics: UsageAnalyticsResponse | null;
  searchChannels: WebSearchChannel[];
  searchStatus: WebSearchStatusSnapshot | null;
  failures: UsageEvent[];
  loading: boolean;
  error: string;
  lastUpdated: Date | null;
  refresh: () => Promise<void>;
}

export function useDashboardTelemetry(): DashboardTelemetry {
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const [snapshot, setSnapshot] = useState<ChannelStatusSnapshot | null>(null);
  const [analytics, setAnalytics] = useState<UsageAnalyticsResponse | null>(null);
  const [searchChannels, setSearchChannels] = useState<WebSearchChannel[]>([]);
  const [searchStatus, setSearchStatus] = useState<WebSearchStatusSnapshot | null>(null);
  const [failures, setFailures] = useState<UsageEvent[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [lastUpdated, setLastUpdated] = useState<Date | null>(null);
  const sequence = useRef(0);

  const load = useCallback(
    async (showLoading: boolean) => {
      if (!connected) {
        setLoading(false);
        return;
      }
      const seq = ++sequence.current;
      if (showLoading) setLoading(true);
      const [snapshotResult, monitoringResult, channelsResult, analyticsResult, failuresResult] =
        await Promise.allSettled([
          channelStatusApi.get(),
          webSearchApi.getMonitoring(),
          webSearchApi.listChannels(),
          usageAnalyticsApi.getAnalytics({ range: '24h', group_by: 'provider' }),
          usageAnalyticsApi.getEvents({ range: '24h', status: 'failed', limit: 5 }),
        ]);
      if (seq !== sequence.current) return;

      setSnapshot(snapshotResult.status === 'fulfilled' ? snapshotResult.value : null);
      setSearchStatus(
        monitoringResult.status === 'fulfilled' ? monitoringResult.value.status : null
      );
      setSearchChannels(channelsResult.status === 'fulfilled' ? channelsResult.value : []);
      setAnalytics(analyticsResult.status === 'fulfilled' ? analyticsResult.value : null);
      setFailures(
        failuresResult.status === 'fulfilled' ? failuresResult.value.events ?? [] : []
      );

      const primaryFailed =
        snapshotResult.status === 'rejected' && channelsResult.status === 'rejected';
      setError(
        primaryFailed
          ? snapshotResult.reason instanceof Error
            ? snapshotResult.reason.message
            : String(snapshotResult.reason)
          : ''
      );
      setLastUpdated(new Date());
      if (showLoading) setLoading(false);
    },
    [connected]
  );

  useEffect(() => {
    if (!connected) {
      setLoading(false);
      return;
    }
    void load(true);
    const timer = window.setInterval(() => void load(false), 60_000);
    return () => window.clearInterval(timer);
  }, [connected, load]);

  const refresh = useCallback(async () => {
    await load(true);
  }, [load]);

  return {
    snapshot,
    analytics,
    searchChannels,
    searchStatus,
    failures,
    loading,
    error,
    lastUpdated,
    refresh,
  };
}
