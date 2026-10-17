import { apiClient } from './client';

/** Counter families exposed by the channel status endpoint. */
export type ChannelStatusKind = 'llm' | 'web_search';

export interface ChannelStatusBucket {
  start: string;
  success: number;
  failed: number;
}

export interface ChannelStatusModel {
  id: string;
  name: string;
  total: number;
  success: number;
  failed: number;
  success_rate: number;
  buckets: ChannelStatusBucket[];
}

export interface ChannelStatusChannel {
  kind: ChannelStatusKind;
  id: string;
  name: string;
  provider?: string;
  total: number;
  success: number;
  failed: number;
  success_rate: number;
  buckets: ChannelStatusBucket[];
  models?: ChannelStatusModel[];
}

export interface ChannelStatusSnapshot {
  observed_at: string;
  bucket_minutes: number;
  window_hours: number;
  channels: ChannelStatusChannel[];
}

/** Request-level channel status statistics. */
export const channelStatusApi = {
  get(): Promise<ChannelStatusSnapshot> {
    return apiClient.get<ChannelStatusSnapshot>('/observability/channel-status');
  },
};
