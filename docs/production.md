# Production deployment checklist

This document covers the minimum steps for running XRouter as a production
service. The web UI is an administrative surface; treat it as part of the
control plane, not as a public website.

## 1. Release from a known commit

Do not deploy an image built from a dirty working tree or an untagged local
build. Use the release workflow:

1. Merge the reviewed change into `main`.
2. Create a version tag (`vX.Y.Z`).
3. Let the `docker-image` workflow publish the multi-arch manifest and the
   release workflow publish the host binaries.
4. Pin the deployment to the released tag or image digest.

The image includes OCI labels for version, revision and build time:

```bash
docker image inspect registry.cn-hangzhou.aliyuncs.com/xctr/xrouter:vX.Y.Z \
  --format '{{json .Config.Labels}}'
```

Build arguments for manual builds:

```bash
docker build \
  --build-arg VERSION=vX.Y.Z \
  --build-arg COMMIT="$(git rev-parse --short HEAD)" \
  --build-arg BUILD_DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -t xrouter:vX.Y.Z .
```

## 2. Network exposure and TLS

`docker-compose.yml` binds the service to `127.0.0.1` by default. Terminate TLS
in a reverse proxy on the same host:

```caddyfile
xrouter.example.com {
  reverse_proxy 127.0.0.1:11016
}
```

If the proxy runs in another container, put both containers on a private
network and publish only the proxy. Set `XROUTER_BIND_ADDR` only when the
traffic is already restricted by a firewall or private network.

Remote management is enabled with `management.allow-remote`. Keep the
management port behind TLS and an IP allowlist/VPN. Do not expose port `11016`
directly to the public internet.

## 3. Secrets and client keys

- Keep `config.yaml` out of git and set restrictive permissions:
  `chmod 600 config.yaml`.
- Use a strong management key. The management key protects the web UI and
  `/v8/management` routes.
- Replace any legacy unprefixed client key with scoped keys:
  - `sk-xr-model-*` can access `/v1` and Codex model routes.
  - `sk-xr-search-*` can access `/gateway` and `/mcp`.
- Do not reuse one universal client key for model and search traffic.
- Protect the proxy URL and upstream account credentials as secrets. Back up
  and restore them with the same care as the management key.

## 4. Persistence and backup

Persistent paths:

| Path | Content |
| --- | --- |
| `config.yaml` | Runtime configuration and upstream credentials |
| `websearch/` | Search account state, cooldowns and request logs |
| `channel-status/` | Request-level channel statistics |
| `usage-analytics/` | Usage events, rollups and price preferences |

Create a consistent backup with the management API:

```bash
XROUTER_MANAGEMENT_KEY='...' \
XROUTER_URL='http://127.0.0.1:11016' \
./scripts/backup.sh
```

The script writes a timestamped archive containing the config, OAuth auth
directory, search/channel state and a bbolt snapshot of usage analytics, with a
SHA-256 manifest. The archive is created with mode 0600; store it outside the
host and restrict it to administrators. The management key is passed through a
temporary curl config rather than a process-visible command-line header.

Restore procedure:

1. Stop the container.
2. Extract the archive into the data directory.
3. Restore `usage-analytics.db` to `usage-analytics/usage.db`, or use the
   management restore endpoint while the service is running.
4. Start the container and verify `/healthz`, the dashboard and recent logs.

Run a restore drill before the first production rollout and after changing the
storage layout.

## 5. Health and monitoring

- Container health check: `docker inspect --format '{{.State.Health.Status}}' xrouter`
- HTTP health endpoint: `curl -fsS http://127.0.0.1:11016/healthz`
- Analytics health: `GET /v8/management/observability/usage/status`
- Watch dropped analytics events, queue depth, disk usage and log volume.
- Alert on repeated `/healthz` failures, upstream 5xx/429 spikes, proxy
  failures and search accounts stuck in cooldown.

The image defines a health check against `/healthz`. Compose also enables
JSON log rotation and `no-new-privileges`.

## 6. Search probe load

Search probes run every 20 minutes by default and refresh quota snapshots
older than 15 minutes:

```yaml
web-search:
  probe:
    interval-seconds: 1200
  quota:
    fresh-seconds: 900
```

The scheduled refresh probes every enabled account. With a large pool this can
create substantial upstream and proxy traffic. Before production:

- Confirm the account pool and proxy usage comply with each upstream's terms.
- Watch 429, authentication and proxy failures after enabling the schedule.
- Tune `probe.concurrency`, `probe.interval-seconds` and
  `quota.fresh-seconds` for the pool size and proxy capacity.
- Keep only accounts that can legally and reliably serve production traffic.

## 7. Plugins

The web plugin management feature and the `tokens-statistic` plugin are
removed from this deployment. Plugin configuration and the plugin directory
are not mounted. The backend plugin ABI remains for SDK/Home compatibility, but
no plugin is loaded at runtime.

## 8. Preflight checklist

- [ ] Reviewed and tagged release commit; image pinned by tag or digest.
- [ ] TLS reverse proxy configured; port `11016` not publicly exposed.
- [ ] Management key rotated and stored outside the repository.
- [ ] Legacy universal client key removed; scoped model/search keys in use.
- [ ] `config.yaml` permissions restricted.
- [ ] Persistent directories backed up and a restore drill completed.
- [ ] `/healthz`, container health and log rotation verified.
- [ ] Search pool size, probe interval and proxy capacity reviewed.
- [ ] Rollback image and previous config/state backup available.
- [ ] Monitoring/alerting covers availability, upstream errors and disk space.
