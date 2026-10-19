# v8 Management API

The base path is `/v8/management`. Configuration endpoints follow the structure in
[config.example.yaml](../config.example.yaml); operational endpoints are grouped
by function. The existing `/v0/management` API remains supported. Its reference is
available in the [management API guide](https://help.router-for.me/management/api).

## Access

The v8 endpoints use the same management key and remote-access policy as v0.
Send `Authorization: Bearer <management-key>` or `X-Management-Key: <management-key>`.
Home mode disables local management endpoints for both versions.

## Configuration endpoints

All paths below are relative to `/v8/management`.

| Path | Methods | Description |
| --- | --- | --- |
| `/config` | GET, PUT, PATCH | Read, replace, or merge the complete configuration as JSON. |
| `/config.yaml` | GET, PUT | Read or replace the complete configuration as YAML. |
| `/config/<section>/<field>` | GET, PUT, PATCH, DELETE | Read or modify a section or field using its path in the v8 tree. |

GET renders the persisted configuration in the v8 layout without migrating the
file or adding omitted runtime defaults. Successful v8 configuration writes save
the latest layout; reads and rejected writes leave the file unchanged.
Historical v8 paths and request bodies remain accepted as aliases.

JSON writes accept the value directly, without a `{ "value": ... }` envelope.
PUT replaces its target. PATCH merges objects and replaces lists and scalars.
Scalar field updates retain existing comments; complete replacements use the submitted document.
DELETE removes a field. Paths identify mapping keys, not array indexes; replace
an entire list to change its entries. Optional per-key `null` overrides inherit
the corresponding group value. Legacy field names are rejected by v8 writes.

| Example path | Value or purpose |
| --- | --- |
| `/config/access/api-keys` | Client authentication keys, for example `["client-key"]`. |
| `/config/api-keys` | All upstream provider groups. |
| `/config/api-keys/openai` | Native OpenAI upstream groups. |
| `/config/api-keys/deepseek` | Native DeepSeek upstream groups. |
| `/config/api-keys/nvidia` | Native NVIDIA upstream groups. |
| `/config/observability/logs/debug` | A boolean, for example `true`. |
| `/config/routing/retry/request-retry` | A number, for example `0`. |
| `/config/plugins/configs/<id>` | A plugin configuration object. |

For example, `PATCH /v8/management/config` with this body disables additional
request retries while preserving other settings:

```json
{
  "routing": {
    "retry": {
      "request-retry": 0
    }
  }
}
```

The Home-owned revision fields
`credentials.concurrency.lifecycle-config-revision`,
`credentials.concurrency.observation-barrier-revision`, and `plugins.auth-revision`
cannot be changed through these endpoints.

## Operational endpoints

All paths below are relative to `/v8/management`. Request and response bodies
retain the corresponding business operation's fields.

| Path | Methods | Description |
| --- | --- | --- |
| `/config/upstream/<provider>` | GET, PUT, PATCH, DELETE | Manage shared provider settings. |
| `/server/latest-version` | GET | Get latest release information. |
| `/requests/api-call` | POST | Make an authenticated upstream call. |
| `/routing/cooldown/reset` | POST | Clear credential cooldown. |
| `/routing/model-definitions/<channel>` | GET | Get model definitions. |
| `/observability/logs` | GET, DELETE | Read or clear application logs. |
| `/observability/logs/errors` | GET | List error-log files. |
| `/observability/logs/errors/<name>` | GET | Download an error-log file. |
| `/observability/logs/requests/<id>` | GET | Get a request log. |
| `/observability/usage/api-keys` | GET | Get API-key usage. |
| `/observability/usage/queue` | GET | Get queued usage events. |
| `/observability/usage/analytics` | GET | Get native LLM/search usage summary, series, groups and facets. |
| `/observability/usage/status` | GET | Get analytics store health, queue depth, dropped events, retention and DB size. |
| `/observability/usage/events` | GET | Get native LLM/search per-request detail. |
| `/observability/usage/export` | GET | Export native usage events as CSV. |
| `/observability/usage/preferences` | GET, PUT | Read or save usage page defaults. |
| `/observability/usage/pricing` | GET, PUT | Read effective prices or replace manual model/search overrides. |
| `/observability/usage/pricing/sync` | POST | Refresh the models.dev reference catalog. |
| `/observability/usage/compact` | POST | Schedule a bbolt rewrite on the next restart. |
| `/observability/usage/backup` | GET | Download a bbolt snapshot of analytics data. |
| `/observability/usage/restore` | POST | Replace analytics data from a backup; requires `X-Confirm-Restore: replace`. |
| `/observability/usage/import/tokens-statistic` | POST | Merge request history from a legacy tokens-statistic bbolt database; requires `X-Confirm-Import: merge`. |
| `/observability/usage/reset` | POST | Clear native usage analytics data. |
| `/observability/channel-status` | GET | Get lifetime request counters and 24-hour, 20-minute buckets for LLM providers/models and web-search channels. |
| `/web-search/accounts/import` | POST | Import an OpenRegister CSV into search accounts and NVIDIA/Agnes native credentials. |
| `/web-search/accounts/export` | GET | Export search accounts and NVIDIA/Agnes keys as CSV or JSON; masked unless `include_credentials=true`. |
| `/web-search/quota/history` | GET | Get balance history for one web-search key (`channel` and `account_id` required; optional `hours`, `limit`, and `to`). |
| `/web-search/requests` | GET | Get recent search calls from native analytics when available, otherwise legacy JSONL; `source` reports the origin. |
| `/web-search/status/check` | POST | Start a probe-based channel status check; pass `channel` to limit it to one channel. |
| `/credentials` | GET, POST, DELETE | List, upload, or delete credential files. |
| `/credentials/models` | GET | Get credential models. |
| `/credentials/download` | GET | Download a credential file. |
| `/credentials/status` | PATCH | Change credential status. |
| `/credentials/fields` | PATCH | Change credential fields. |
| `/credentials/refresh` | POST | Refresh credentials. |
| `/plugins` | GET | List installed plugins. |
| `/plugins/<id>` | DELETE | Delete a plugin. |
| `/plugins/<id>/quota` | GET, POST, DELETE | Read, fetch, or reset plugin quota. |

OAuth login endpoints were removed from the management API. OAuth and file-backed
credentials are provisioned as credential files (`/credentials`) and refreshed
through `/credentials/refresh` or the background refresh workers.

## v0 compatibility

The `/v0/management` endpoints keep their original paths and payloads.

Legacy flat configuration endpoints such as `/debug`, `/request-retry`, and
`/claude-api-key` exist under `/v0/management` only.
`/v0/management/api-keys` continues to manage client authentication keys; the v8
equivalent is `/v8/management/config/access/api-keys`.

Legacy-only configuration files keep their layout until a successful v8
configuration write. When both layouts specify a field, the new field takes
precedence and its legacy equivalent is removed. V0 setters keep legacy-only
files in their original layout; existing v8 files are saved in the latest v8
layout. `PUT /v0/management/config.yaml` still replaces the complete file and
accepts legacy, new, or mixed layouts, normalizing v8 documents on save.

Plugin-defined HTTP extensions retain their declared `/v0/management` routes.
