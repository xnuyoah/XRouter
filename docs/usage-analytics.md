# Native Usage Analytics

XRouter includes a local usage analytics store for LLM requests and web-search
gateway/MCP requests. It is served by the management panel under
`Observe -> Usage Analytics`.

## Enable

Analytics is disabled by default and is independent of
`observability.usage.usage-statistics-enabled` (which controls the Redis queue).

```yaml
observability:
  usage:
    analytics:
      enabled: true
      db: ""                 # default: WRITABLE_PATH/usage-analytics/usage.db
      retention-days: 90     # per-request detail
      rollup-retention-days: 365
      flush-interval: 5s
      max-db-bytes: 0        # soft logical data cap; 0 disables
      # Display-only labels for blank LLM plan/quota fields. Existing upstream
      # plan values are preserved.
      plan-display:
        - provider: nvidia
          plan: "NIM Free"
          quota-kind: credits
        - provider: agnes
          plan: "Agnes Free"
          quota-kind: plan
      pricing:
        sync-enabled: false  # refresh on startup and every 24 hours
        source-url: "https://models.dev/api.json"
        provider-priority: [openai, google, anthropic]
        ignored-suffixes: ["-thinking", "-preview", "-high", "-low", "(thinking)", "(xhigh)", "(high)", "(medium)", "(low)", "(max)", "(auto)", "(none)"]
        currency: "USD"
```

A restart is required after changing `enabled`.

`plan-display` fills the request-log “Plan / quota” column for providers whose
credentials do not report a plan. It is display-only and never changes routing
or upstream quotas. An entry without `model` applies to every model served by
that provider; a `model` entry matches the client model, upstream model, or
alias and takes precedence over the provider default. Labels are also applied
when querying existing event rows, so historical request logs pick up a new
display mapping without rewriting the database. Rolled-up aggregates created
before the mapping was added are not recalculated. Restart after changing this
mapping.

Local analytics is skipped in Home mode because Home already provides usage
observability and local management endpoints are unavailable. This avoids
duplicate storage and double counting.

Search-channel requests are still forwarded to Home through the existing usage
stream, encoded as zero-token usage events with:

- `provider=web-search`
- `event_type=web_search`
- `model=<channel>/<operation>`
- `endpoint=/gateway/<channel>` or `/mcp/<channel>`
- latency, failure status, credits and upstream cost in the `web_search` payload

This lets Home show search request counts, latency and failures without polluting
LLM token totals or requiring a new Home protocol. The Home side must recognize
`event_type=web_search` and the `web_search` payload; without a matching Home
release, these events appear as generic zero-token usage records.

## Data

LLM events are produced from the host usage pipeline:

- provider, executor, model, alias, upstream response model
- input/output/reasoning/cache-read/cache-write tokens
- plan (`plan_type` from the selected credential)
- latency, P95 latency, TTFT, TPS, success/failure
- request source, auth type, service tier, reasoning effort
- a hashed/masked client API key (never the raw key)

Search events are produced by the web-search gateway and MCP mirror:

- channel, operation, method, path, account, attempts, latency, status, proxy use
- quota kinds such as Tavily `key/plan/paygo`, AnySearch `plan`,
  Firecrawl/Tinyfish `credits`
- per-request credits when the upstream reports them:
  - Tavily: `usage.credits`
  - Firecrawl: `creditsUsed` (crawl status polling is excluded to avoid double counting)
  - Exa: `costDollars.total`, stored as an upstream dollar cost
  - Tinyfish search/fetch are free; credits are only recorded if the response reports them
- credits and upstream cost are extracted from gateway HTTP responses; MCP
  `tools/call` events currently record request units unless the upstream result
  exposes billing metadata
- a search-account detail table joins analytics with the existing account
  status, quota, last probe, credits and P95 views
- the search trend chart plots success/failure bars per bucket with a credits
  line, and each account row can expand its quota-balance history for the
  selected range
- `/web-search/requests` is served from analytics events when they exist and
  falls back to the legacy JSONL log otherwise; the response `source` field
  reports which backend was used
- the search account table highlights low balances, exhausted accounts,
  cooldowns, quota-refresh failures and failed probes; a warning-only filter
  sorts critical accounts first, the KPI row shows critical/warning totals, and
  the low-balance threshold is persisted in preferences

No prompt, request body, or response body is stored.

## Cost estimation

Costs are computed when analytics is queried, using the effective price book:

- manual model overrides
- reference prices synced from `models.dev`
- context tiers (`cost.tiers`, `type: context`)
- service-tier variants from `experimental` modes
- cache read/write and reasoning rates
- per-request/per-credit search prices
- upstream-reported search costs (for example Exa `costDollars.total`)

For search, an upstream-reported cost wins when its currency matches the price
book currency. Otherwise, a `credit` price is used when credits are available;
if only a `request` price exists, cost falls back to one request per event.
Local failures that never selected or called an upstream account carry zero
units and are not included in search cost estimates.

Latency P95 is computed from a fixed histogram stored in each rollup, so it is
an estimate with bucket boundaries rather than an exact order statistic. The
same histogram is returned as `latency_buckets` and rendered as a distribution
with the P95 bucket highlighted. Old rollups without histogram data fall back
to average latency.

Token pricing uses the disjoint `TokenBreakdown` buckets. Context tiers are
selected from the average input tokens of the aggregated row, so they are an
estimate. `coverage` reports the share of requests with a matched price;
`unpriced_models` lists the labels that still need a manual price.

Manual overrides always win over reference prices. Price edits are reflected in
subsequent queries; there is no destructive recalculation step. The usage page
plots per-bucket estimated cost, with dashed lines for the LLM and search
components.

Only one catalog sync can run at a time. Manual sync requests return HTTP 409
while another sync is active. The pricing snapshot includes a `sync` object with
the last start/completion time, last error and matched model count. A sync is
canceled when the analytics store shuts down.

## Usage page

The WebUI page is split into three tabs:

- **Overview**: KPI cards, token/cost trends, latency and failure distribution,
  and the grouped breakdown table.
- **Request logs**: a high-density request table with time/status, provider,
  model mapping, account, plan, token breakdown, cost, timing, request traits
  and request ID. Filters include range, kind, provider, model, channel,
  account ID, plan, status, HTTP status, request ID, trace ID and a keyword
  search. Rows open a detail sheet; column visibility is saved in preferences.
  Request logs use server-side cursor pagination with previous/next controls.
  Mobile widths switch to a card list.
- **Search channels**: quota overview, search trend, account warnings and quota
  history. The account table paginates client-side (25 rows by default) so the
  861-account pool does not render every row at once.

Tabs use the path-based tabs (`/usage`, `/usage/logs`, `/usage/search`), for example
`#/usage/logs`.

## Management API

All endpoints are under `/v8/management` and require the management key.

| Method | Path | Description |
| --- | --- | --- |
| GET | `/observability/usage/analytics` | Summary, series, groups, facets and cost |
| GET | `/observability/usage/status` | Store health: DB bytes, queue depth, dropped events, retention and last flush |
| GET | `/observability/usage/events` | Per-request detail, newest first |
| GET | `/observability/usage/export` | CSV export of matching events |
| GET | `/observability/usage/preferences` | Saved usage page defaults |
| PUT | `/observability/usage/preferences` | Save kind, range, grouping, token display, page size and low-balance threshold |
| GET | `/observability/usage/pricing` | Effective model/search price book |
| PUT | `/observability/usage/pricing` | Replace manual model/search overrides |
| POST | `/observability/usage/pricing/sync` | Refresh the models.dev reference catalog |
| POST | `/observability/usage/compact` | Schedule a bbolt rewrite on the next restart |
| GET | `/observability/usage/backup` | Download a bbolt snapshot |
| POST | `/observability/usage/restore` | Replace data from a snapshot; requires `X-Confirm-Restore: replace` |
| POST | `/observability/usage/import/tokens-statistic` | Merge request history from a legacy tokens-statistic bbolt DB; requires `X-Confirm-Import: merge` |
| POST | `/observability/usage/reset` | Clear analytics events and rollups; prices and preferences are preserved |

Common query parameters for analytics/events:

- `kind=all|llm|search`
- `range=today|24h|5h|7d|30d|month` or `from`/`to` (RFC3339, date, or unix seconds)
- `bucket=auto|minute|hour|day`
- `group_by=kind|provider|model|channel|operation|plan|quota_kind|account|source|auth_type|executor|service_tier|reasoning_effort|status|status_code`
- `provider`, `model`, `channel`, `operation`, `plan`, `quota_kind`, `source`,
  `auth_type`, `executor`, `service_tier`, `reasoning_effort`, `status`, `status_code`
- `limit`, `offset`, or `cursor` (use the `next_cursor` from a previous page)
- `account_id`, `request_id`, `trace_id`, `q` for request-log queries
  (`request_id`, `trace_id` and `q` are rejected by the aggregate `/analytics`
  endpoint because they do not exist in rollup keys)
- queries are capped at 20,000 time buckets; use a larger bucket or a shorter range

`summary.failure_statuses` lists failed HTTP status codes and counts. Status code
`0` means the upstream did not report one.

Pricing example:

```json
{
  "models": [
    {
      "provider": "openai",
      "model": "gpt-6-luna",
      "plan": "plus",
      "service_tier": "fast",
      "input": 2.5,
      "output": 10,
      "cache_read": 0.5,
      "cache_write": 3
    }
  ],
  "search": [
    { "channel": "tavily", "operation": "search", "unit": "request", "price": 0.008 },
    { "channel": "firecrawl", "operation": "scrape", "unit": "credit", "price": 0.01 }
  ]
}
```

All prices are per 1M tokens for model entries and per unit for search entries.
Search account balances and probe state remain available through the existing
`/web-search/monitoring`, `/web-search/accounts`, and `/web-search/quota/history`
endpoints.

## Legacy tokens-statistic import

`POST /observability/usage/import/tokens-statistic` accepts a legacy
`tokens-statistic.db` (bbolt) upload with:

```http
X-Confirm-Import: merge
```

The importer reads the `requests` bucket, maps each stored `RequestDetail` into
a native LLM event, and inserts it together with its rollup. Imports are
idempotent: a deterministic event ID (`legacy:<request key>`) is skipped when it
already exists.

Notes:

- Only request details are imported. The legacy `hours` aggregates are not
  imported because the request rows are the authoritative source.
- If the legacy database has no request rows (for example after retention
  pruning), the import reports zero imported rows.
- Legacy model prices are not imported. Use models.dev sync or manual overrides
  for cost calculation after the import.
- The import is additive; it never deletes native analytics data.

## Preferences

The usage page saves these defaults server-side in the analytics database:

- `kind`: all / llm / search
- `range`: today / 24h / 5h / 7d / 30d / month (custom ranges are not persisted)
- `group_by`
- `token_display_mode`: compact / full / k / m / b
- `page_size`: 25 / 50 / 100 / 200
- `search_accounts_page_size`: 10 / 25 / 50 / 100 (defaults to 25)
- `log_columns`: request-log column visibility
- `low_balance_threshold_percent`: 5 / 10 / 20 / 30 (defaults to 10)

They are loaded on page open and saved after a short debounce when the controls
change. This keeps the view consistent across browsers and devices.

## Storage

Events, minute rollups and hour rollups are stored in bbolt. Rollup keys are
prefixed with their timestamp so range queries seek directly into the requested
window. Queries with an hour/day bucket and a span of at least two hours use the
hourly rollups; shorter or minute queries use the minute rollups. Legacy
JSON-only rollup keys are migrated on open, and hourly rollups are rebuilt once
from minute data when upgrading or restoring. Event retention and rollup
retention are independent.

`max-db-bytes` is a soft cap on logical bbolt data (not the physical file size).
When exceeded, the maintenance loop deletes the oldest time window of events and
rollups until data falls below 90% of the cap. Prices, preferences and metadata
are never pruned by the size cap.

Deleted bbolt pages are reused, but the physical file does not shrink on its
own. On startup the store compacts the database when it is significantly larger
than its logical data, and when a size-cap cleanup left a compaction flag.
Compaction runs before serving traffic. `POST /observability/usage/compact`
schedules a rewrite for the next restart. The writer batches events; if its bounded queue is
full, events are dropped and counted rather than blocking the request pipeline.
The effective price book is persisted in the same database.

## Limitations

- Tiered pricing is selected from aggregated input tokens; per-event detail
  uses the event's own token count.
- Search costs are only available when a manual search price is configured;
  quota-only plans show usage units and balances, not fabricated money.
- CSV/PNG export, bbolt backup/restore, server-side preferences and legacy
  `tokens-statistic.db` import are implemented.
