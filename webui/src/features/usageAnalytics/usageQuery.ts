import type { UsageQueryParams } from '@/services/api';

export interface UsageBaseFilters {
  provider: string;
  model: string;
  channel: string;
  operation: string;
  plan: string;
  status: string;
}

export interface UsageLogFilterValues {
  accountId: string;
  requestId: string;
  traceId: string;
  q: string;
  statusCode: string;
  source: string;
  authType: string;
  executor: string;
  serviceTier: string;
  reasoningEffort: string;
}

export const EMPTY_USAGE_LOG_FILTERS: UsageLogFilterValues = {
  accountId: '',
  requestId: '',
  traceId: '',
  q: '',
  statusCode: '',
  source: '',
  authType: '',
  executor: '',
  serviceTier: '',
  reasoningEffort: '',
};

export interface UsageEventQueryInput {
  kind: 'all' | 'llm' | 'search';
  preset: string;
  customFrom: string;
  customTo: string;
  baseFilters: UsageBaseFilters;
  logFilters: UsageLogFilterValues;
  limit: number;
  cursor?: string;
}

function trim(value: string): string | undefined {
  const result = value.trim();
  return result === '' ? undefined : result;
}

export function buildUsageEventQuery(input: UsageEventQueryInput): UsageQueryParams {
  const params: UsageQueryParams = {
    kind: input.kind === 'all' ? undefined : input.kind,
    provider: trim(input.baseFilters.provider),
    model: trim(input.baseFilters.model),
    channel: trim(input.baseFilters.channel),
    operation: trim(input.baseFilters.operation),
    plan: trim(input.baseFilters.plan),
    status: trim(input.baseFilters.status),
    account_id: trim(input.logFilters.accountId),
    request_id: trim(input.logFilters.requestId),
    trace_id: trim(input.logFilters.traceId),
    q: trim(input.logFilters.q),
    source: trim(input.logFilters.source),
    auth_type: trim(input.logFilters.authType),
    executor: trim(input.logFilters.executor),
    service_tier: trim(input.logFilters.serviceTier),
    reasoning_effort: trim(input.logFilters.reasoningEffort),
    limit: input.limit,
    cursor: input.cursor,
  };
  const statusCode = trim(input.logFilters.statusCode);
  if (statusCode !== undefined) {
    const parsed = Number(statusCode);
    if (Number.isFinite(parsed) && parsed >= 0) {
      params.status_code = parsed;
    }
  }
  if (input.preset === 'custom') {
    if (input.customFrom) params.from = new Date(input.customFrom).toISOString();
    if (input.customTo) params.to = new Date(input.customTo).toISOString();
  } else {
    params.range = input.preset;
  }
  return params;
}
