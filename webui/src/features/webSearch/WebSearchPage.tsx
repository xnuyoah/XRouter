/**
 * Web tools page: centralizes tavily / exa / anysearch / firecrawl / tinyfish
 * account pools into two tabs: account pool and integration.
 *
 * Behavior contract:
 * - Channels and accounts are owned by the web-search section of config.yaml;
 *   adding or removing accounts writes back through the management API.
 * - Probing is a synchronous endpoint: one call returns one result set.
 * - The integration tab documents /gateway and /mcp access; one client key from
 *   access.api-keys can call every search tool and MCP endpoint.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { ChannelLogo } from '@/components/common/ChannelLogo';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { Pagination } from '@/components/ui/Pagination/Pagination';
import { clampPage, paginate } from '@/components/ui/Pagination/paginationUtils';
import { Skeleton } from '@/components/ui/Skeleton';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import {
  webSearchApi,
  type WebSearchAccount,
  type WebSearchChannel,
  type WebSearchProbeSummary,
  type WebSearchQuotaBalance,
  type WebSearchImportSummary,
} from '@/services/api';
import {
  ACCOUNT_STATUS_META,
  QUOTA_KIND_LABEL_KEYS,
  QUOTA_REFRESH_META,
  WEB_SEARCH_TABS,
  gatewayExample,
  mcpExample,
  type ToneId,
  type WebSearchTabId,
} from './constants';
import styles from './WebSearchPage.module.scss';

const ACCOUNT_FILTERS = ['all', 'available', 'limited', 'expired', 'pending', 'disabled'] as const;
type AccountFilter = (typeof ACCOUNT_FILTERS)[number];
const ACCOUNT_PAGE_SIZES = [25, 50, 100] as const;

function formatNumber(value: number | undefined): string {
  if (value === undefined || value === null) return '—';
  if (Number.isInteger(value)) return String(value);
  return value.toFixed(2);
}

function formatTime(value: string | undefined): string {
  if (!value) return '—';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return '—';
  return parsed.toLocaleString();
}

/** Account quota breakdown; negative Firecrawl balances render in red. */
function quotaText(account: WebSearchAccount, t: (key: string) => string): string {
  const balances = account.quota?.balances ?? [];
  if (balances.length === 0) return t('webSearch.quotaUnknown');
  return balances
    .map((balance: WebSearchQuotaBalance) => {
      const label = t(QUOTA_KIND_LABEL_KEYS[balance.kind] ?? 'webSearch.quotaKind.credits');
      return `${label} ${formatNumber(balance.remaining)}`;
    })
    .join(' · ');
}

function hasNegativeQuota(account: WebSearchAccount): boolean {
  return (account.quota?.balances ?? []).some(
    (balance) => balance.remaining !== undefined && balance.remaining < 0
  );
}

export function WebSearchPage() {
  const { t } = useTranslation();
  const [tab, setTab] = useState<WebSearchTabId>('accounts');
  const [channels, setChannels] = useState<WebSearchChannel[]>([]);
  const [accounts, setAccounts] = useState<WebSearchAccount[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useState<AccountFilter>('all');
  const [accountPage, setAccountPage] = useState(1);
  const [storedAccountPageSize, setStoredAccountPageSize] = useLocalStorage(
    'webSearch.accounts.pageSize',
    25
  );
  const accountPageSize = ACCOUNT_PAGE_SIZES.some((size) => size === storedAccountPageSize)
    ? storedAccountPageSize
    : 25;
  const [pendingAccount, setPendingAccount] = useState<string | null>(null);
  const [probeResult, setProbeResult] = useState<WebSearchProbeSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [form, setForm] = useState({ channel: '', name: '', api_key: '', username: '', password: '' });
  const [importing, setImporting] = useState(false);
  const [importSummary, setImportSummary] = useState<WebSearchImportSummary | null>(null);
  const [exporting, setExporting] = useState(false);
  const [exportFormat, setExportFormat] = useState<'csv' | 'json'>('csv');
  const [includeCredentials, setIncludeCredentials] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const loadAll = useCallback(async () => {
    try {
      const [nextChannels, nextAccounts] = await Promise.all([
        webSearchApi.listChannels(),
        webSearchApi.listAccounts(),
      ]);
      setChannels(nextChannels);
      setAccounts(nextAccounts);
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

  const filteredAccounts = useMemo(
    () => (filter === 'all' ? accounts : accounts.filter((account) => account.status === filter)),
    [accounts, filter]
  );
  const pagedAccounts = useMemo(
    () => paginate(filteredAccounts, accountPage, accountPageSize),
    [filteredAccounts, accountPage, accountPageSize]
  );

  useEffect(() => {
    setAccountPage((current) =>
      clampPage(current, filteredAccounts.length, accountPageSize)
    );
  }, [filteredAccounts.length, accountPageSize]);

  const changeFilter = (nextFilter: AccountFilter) => {
    setFilter(nextFilter);
    setAccountPage(1);
  };

  const changeAccountPageSize = (size: number) => {
    setStoredAccountPageSize(size);
    setAccountPage(1);
  };

  const probeAll = useCallback(
    async (channel?: string, accountIds?: string[]) => {
      setBusy(true);
      setError(null);
      try {
        const summary = await webSearchApi.probe(
          channel || accountIds ? { channel, account_ids: accountIds } : {}
        );
        setProbeResult(summary);
        await loadAll();
      } catch (errProbe) {
        setError(errProbe instanceof Error ? errProbe.message : String(errProbe));
      } finally {
        setBusy(false);
      }
    },
    [loadAll]
  );

  const probeOne = useCallback(
    async (account: WebSearchAccount) => {
      setPendingAccount(account.account_id);
      try {
        const summary = await webSearchApi.probe({ account_ids: [account.account_id] });
        setProbeResult(summary);
        await loadAll();
      } catch (errProbe) {
        setError(errProbe instanceof Error ? errProbe.message : String(errProbe));
      } finally {
        setPendingAccount(null);
      }
    },
    [loadAll]
  );

  const refreshQuota = useCallback(async () => {
    setBusy(true);
    try {
      await webSearchApi.refreshQuota();
      await loadAll();
    } catch (errQuota) {
      setError(errQuota instanceof Error ? errQuota.message : String(errQuota));
    } finally {
      setBusy(false);
    }
  }, [loadAll]);

  // Config writes respond before the file watcher rebuilds the search service.
  // Reload after a short settle so the account list reflects the new config.
  const reloadAfterConfigWrite = useCallback(async () => {
    await new Promise((resolve) => window.setTimeout(resolve, 800));
    await loadAll();
  }, [loadAll]);

  const importAccounts = useCallback(
    async (file: File) => {
      setImporting(true);
      setError(null);
      try {
        const summary = await webSearchApi.importAccounts(await file.text());
        setImportSummary(summary);
        await reloadAfterConfigWrite();
      } catch (errImport) {
        setError(errImport instanceof Error ? errImport.message : String(errImport));
      } finally {
        setImporting(false);
        if (fileInputRef.current) fileInputRef.current.value = '';
      }
    },
    [reloadAfterConfigWrite]
  );

  const exportAccounts = useCallback(async () => {
    if (
      includeCredentials &&
      !window.confirm(t('webSearch.importExport.confirmCredentials'))
    ) {
      return;
    }
    setExporting(true);
    try {
      const blob = await webSearchApi.exportAccounts({
        format: exportFormat,
        include_credentials: includeCredentials,
      });
      const url = URL.createObjectURL(blob);
      const anchor = document.createElement('a');
      anchor.href = url;
      anchor.download = `xrouter-accounts.${exportFormat}`;
      anchor.click();
      window.setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (errExport) {
      setError(errExport instanceof Error ? errExport.message : String(errExport));
    } finally {
      setExporting(false);
    }
  }, [exportFormat, includeCredentials, t]);

  const submitAccount = useCallback(async () => {
    if (!form.channel || !form.api_key.trim()) return;
    setBusy(true);
    setError(null);
    try {
      await webSearchApi.upsertAccount({
        channel: form.channel,
        api_key: form.api_key.trim(),
        name: form.name.trim() || undefined,
        username: form.username.trim() || undefined,
        password: form.password || undefined,
      });
      setForm({ channel: form.channel, name: '', api_key: '', username: '', password: '' });
      await reloadAfterConfigWrite();
    } catch (errSave) {
      setError(errSave instanceof Error ? errSave.message : String(errSave));
    } finally {
      setBusy(false);
    }
  }, [form, reloadAfterConfigWrite]);

  const removeAccount = useCallback(
    async (account: WebSearchAccount) => {
      setBusy(true);
      try {
        await webSearchApi.deleteAccount(account.channel, account.account_id);
        await reloadAfterConfigWrite();
      } catch (errDelete) {
        setError(errDelete instanceof Error ? errDelete.message : String(errDelete));
      } finally {
        setBusy(false);
      }
    },
    [reloadAfterConfigWrite]
  );

  const pill = (tone: ToneId, label: string) => (
    <span className={`${styles.pill} ${styles[tone]}`}>{label}</span>
  );

  /** Probe result panel shown above the account pool. */
  const renderProbeSummary = (titleKey: string) => {
    if (!probeResult) return null;
    return (
      <div className={styles.panel}>
        <div className={styles.panelHead}>
          <strong>{t(titleKey)}</strong>
          <span className={styles.muted}>
            {t('webSearch.probes.summary', {
              total: probeResult.total,
              succeeded: probeResult.succeeded,
              failed: probeResult.failed,
            })}
          </span>
        </div>
        {probeResult.error && <div className={styles.error}>{probeResult.error}</div>}
        <div className={styles.tableScroll}><table className={styles.table}>
          <thead>
            <tr>
              <th>{t('webSearch.columns.channel')}</th>
              <th>{t('webSearch.columns.account')}</th>
              <th>{t('webSearch.columns.status')}</th>
              <th>{t('webSearch.columns.message')}</th>
            </tr>
          </thead>
          <tbody>
            {probeResult.results.map((item) => {
              const meta = item.status ? ACCOUNT_STATUS_META[item.status] : null;
              return (
                <tr key={`${item.channel}-${item.account_id}`}>
                  <td>
                    <span className={styles.brand}>
                      <ChannelLogo value={item.channel} size={17} />
                      {item.channel}
                    </span>
                  </td>
                  <td>{item.name}</td>
                  <td>{meta ? pill(meta.tone, t(meta.labelKey)) : '—'}</td>
                  <td className={styles.muted}>{item.message || '—'}</td>
                </tr>
              );
            })}
          </tbody>
        </table></div>
      </div>
    );
  };

  const renderAccounts = () => (
    <div className={styles.stack}>
      <div className={styles.panel}>
        <div className={styles.panelHead}>
          <strong>{t('webSearch.accounts.add')}</strong>
          <span className={styles.muted}>{t('webSearch.accounts.addHint')}</span>
        </div>
        <div className={styles.formRow}>
          <label>
            <span>{t('webSearch.columns.channel')}</span>
            <select
              value={form.channel}
              onChange={(event) => setForm({ ...form, channel: event.target.value })}
            >
              <option value="">{t('webSearch.accounts.selectChannel')}</option>
              {channels.map((channel) => (
                <option key={channel.id} value={channel.id}>
                  {channel.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            <span>{t('webSearch.accounts.name')}</span>
            <input
              value={form.name}
              onChange={(event) => setForm({ ...form, name: event.target.value })}
              placeholder={t('webSearch.accounts.namePlaceholder')}
            />
          </label>
          <label>
            <span>{t('webSearch.accounts.apiKey')}</span>
            <input
              value={form.api_key}
              onChange={(event) => setForm({ ...form, api_key: event.target.value })}
              placeholder="tvly-..."
            />
          </label>
          <label>
            <span>{t('webSearch.accounts.loginId')}</span>
            <input
              value={form.username}
              onChange={(event) => setForm({ ...form, username: event.target.value })}
              placeholder={t('webSearch.accounts.loginOptional')}
            />
          </label>
          <label>
            <span>{t('webSearch.accounts.password')}</span>
            <input
              type="password"
              value={form.password}
              onChange={(event) => setForm({ ...form, password: event.target.value })}
            />
          </label>
          <Button
            onClick={() => void submitAccount()}
            disabled={busy || !form.channel || !form.api_key.trim()}
          >
            {t('webSearch.actions.save')}
          </Button>
        </div>
      </div>

      <div className={styles.toolbar}>
        <div className={styles.filters}>
          {ACCOUNT_FILTERS.map((item) => (
            <button
              key={item}
              type="button"
              className={`${styles.filterButton} ${filter === item ? styles.filterActive : ''}`}
              onClick={() => changeFilter(item)}
            >
              {item === 'all' ? t('webSearch.filters.all') : t(ACCOUNT_STATUS_META[item].labelKey)}
            </button>
          ))}
        </div>
        <div className={styles.rowActions}>
          <input
            ref={fileInputRef}
            type="file"
            accept=".csv,text/csv"
            hidden
            onChange={(event) => {
              const file = event.target.files?.[0];
              if (file) void importAccounts(file);
            }}
          />
          <Button
            size="sm"
            variant="secondary"
            loading={importing}
            onClick={() => fileInputRef.current?.click()}
          >
            {t('webSearch.importExport.import')}
          </Button>
          <select
            value={exportFormat}
            onChange={(event) => setExportFormat(event.target.value === 'json' ? 'json' : 'csv')}
            aria-label={t('webSearch.importExport.format')}
          >
            <option value="csv">CSV</option>
            <option value="json">JSON</option>
          </select>
          <label className={styles.exportCredentials}>
            <input
              type="checkbox"
              checked={includeCredentials}
              onChange={(event) => setIncludeCredentials(event.target.checked)}
            />
            {t('webSearch.importExport.includeCredentials')}
          </label>
          <Button size="sm" variant="secondary" loading={exporting} onClick={() => void exportAccounts()}>
            {t('webSearch.importExport.export')}
          </Button>
          <Button size="sm" variant="secondary" onClick={() => void refreshQuota()} disabled={busy}>
            {t('webSearch.actions.refreshQuota')}
          </Button>
          {channels.map((channel) => (
            <Button
              key={channel.id}
              size="sm"
              variant="ghost"
              onClick={() => void probeAll(channel.id)}
              disabled={busy}
            >
              {channel.name}
            </Button>
          ))}
          <Button
            size="sm"
            variant="secondary"
            onClick={() => void probeAll(undefined, filteredAccounts.map((item) => item.account_id))}
            disabled={busy || filteredAccounts.length === 0}
          >
            {t('webSearch.actions.probeAll')}
          </Button>
        </div>
      </div>

      {importSummary && (
        <div className={styles.importSummary}>
          <strong>{t('webSearch.importExport.summaryTitle')}</strong>
          <span>
            {t('webSearch.importExport.summary', {
              added: importSummary.added,
              updated: importSummary.updated,
              duplicate: importSummary.skipped_duplicate,
              other: importSummary.skipped_other_channel,
              invalid: importSummary.skipped_invalid,
            })}
          </span>
          <span className={styles.muted}>{t('webSearch.importExport.llmNote')}</span>
          {importSummary.errors && importSummary.errors.length > 0 && (
            <ul className={styles.importErrors}>
              {importSummary.errors.map((entry) => (
                <li key={`${entry.row}-${entry.reason}`}>
                  {entry.row}: {entry.reason}
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      {renderProbeSummary('webSearch.probes.lastSummary')}

      <div className={styles.panel}>
        <div className={styles.panelHead}>
          <strong>{t('webSearch.accounts.title')}</strong>
          <span className={styles.muted}>{t('webSearch.accounts.source')}</span>
        </div>
        {filteredAccounts.length === 0 ? (
          <EmptyState
            title={t('webSearch.empty.noAccounts')}
            description={t('webSearch.empty.noAccountsDesc')}
          />
        ) : (
          <>
            <div className={styles.tableScroll}><table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('webSearch.columns.channel')}</th>
                  <th>{t('webSearch.columns.account')}</th>
                  <th>{t('webSearch.columns.key')}</th>
                  <th>{t('webSearch.columns.status')}</th>
                  <th>{t('webSearch.columns.quota')}</th>
                  <th>{t('webSearch.columns.quotaReset')}</th>
                  <th>{t('webSearch.columns.lastCheck')}</th>
                  <th>{t('webSearch.columns.actions')}</th>
                </tr>
              </thead>
              <tbody>
                {pagedAccounts.map((account) => {
                  const meta = ACCOUNT_STATUS_META[account.status];
                  const quotaMeta = QUOTA_REFRESH_META[account.quota_refresh_status];
                  return (
                    <tr key={`${account.channel}-${account.account_id}`}>
                      <td>
                        <span className={styles.brand}>
                          <ChannelLogo value={account.channel} size={17} />
                          {account.channel}
                        </span>
                      </td>
                      <td>
                        <div>{account.name}</div>
                        {account.note && <div className={styles.muted}>{account.note}</div>}
                      </td>
                      <td className={styles.mono}>{account.api_key_masked}</td>
                      <td>
                        {pill(meta.tone, t(meta.labelKey))}
                        {account.cooldown && (
                          <div className={styles.muted}>
                            {t('webSearch.accounts.cooldownUntil')} {formatTime(account.cooldown_until)}
                          </div>
                        )}
                      </td>
                      <td>
                        <span
                          className={
                            hasNegativeQuota(account) ? styles.negativeQuota : styles.quotaValue
                          }
                        >
                          {quotaText(account, t)}
                        </span>
                        <div className={`${styles.pill} ${styles[quotaMeta.tone]}`}>
                          {t(quotaMeta.labelKey)}
                        </div>
                      </td>
                      <td className={styles.muted}>{formatTime(account.quota?.reset_at)}</td>
                      <td className={styles.muted}>{formatTime(account.last_checked_at)}</td>
                      <td>
                        <div className={styles.rowActions}>
                          <Button
                            size="sm"
                            variant="ghost"
                            loading={pendingAccount === account.account_id}
                            onClick={() => void probeOne(account)}
                          >
                            {t('webSearch.actions.probe')}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => void removeAccount(account)}
                            disabled={busy}
                          >
                            {t('webSearch.actions.remove')}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table></div>
            <Pagination
              page={accountPage}
              pageSize={accountPageSize}
              total={filteredAccounts.length}
              pageSizeOptions={[...ACCOUNT_PAGE_SIZES]}
              onPageChange={setAccountPage}
              onPageSizeChange={changeAccountPageSize}
            />
          </>
        )}
      </div>

    </div>
  );

  const renderAccess = () => (
    <div className={styles.stack}>
      <div className={styles.panel}>
        <div className={styles.panelHead}>
          <strong>{t('webSearch.access.title')}</strong>
          <span className={styles.muted}>{t('webSearch.access.hint')}</span>
        </div>
        <p className={styles.muted}>{t('webSearch.access.authNote')}</p>
        <div className={styles.tableScroll}><table className={styles.table}>
          <thead>
            <tr>
              <th>{t('webSearch.columns.channel')}</th>
              <th>{t('webSearch.columns.gateway')}</th>
              <th>{t('webSearch.columns.mcp')}</th>
            </tr>
          </thead>
          <tbody>
            {channels.map((channel) => (
              <tr key={channel.id}>
                <td>
                  <span className={styles.brand}>
                    <ChannelLogo value={channel.id} label={channel.name} size={17} />
                    {channel.name}
                  </span>
                </td>
                <td className={styles.mono}>{gatewayExample(channel.id)}</td>
                <td className={styles.mono}>
                  {channel.supports_mcp ? mcpExample(channel.id) : t('webSearch.access.noMcp')}
                </td>
              </tr>
            ))}
          </tbody>
        </table></div>
      </div>

      <div className={styles.panel}>
        <div className={styles.panelHead}>
          <strong>{t('webSearch.access.examples')}</strong>
        </div>
        <pre className={styles.code}>{`# One XROUTER_KEY calls every search tool.
curl -s http://127.0.0.1:11016/gateway/tavily/search \\
  -H "Authorization: Bearer $XROUTER_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"query":"golang","max_results":3}'

curl -s http://127.0.0.1:11016/gateway/firecrawl/v2/scrape \\
  -H "Authorization: Bearer $XROUTER_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"url":"https://example.com"}'

# MCP client (Streamable HTTP)
{
  "mcpServers": {
    "xrouter-tavily": {
      "url": "http://127.0.0.1:11016/mcp/tavily",
      "headers": { "Authorization": "Bearer $XROUTER_KEY" }
    }
  }
}`}</pre>
      </div>

    </div>
  );

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('webSearch.title')}</h1>
          <p className={styles.muted}>{t('webSearch.subtitle')}</p>
        </div>
        <Button variant="secondary" size="sm" onClick={() => void loadAll()} disabled={busy}>
          {t('webSearch.actions.refresh')}
        </Button>
      </header>

      <nav className={styles.tabs}>
        {WEB_SEARCH_TABS.map((item) => (
          <button
            key={item.id}
            type="button"
            className={`${styles.tab} ${tab === item.id ? styles.tabActive : ''}`}
            onClick={() => setTab(item.id)}
          >
            {t(item.labelKey)}
          </button>
        ))}
      </nav>

      {error && <div className={styles.error}>{error}</div>}

      {loading ? (
        <Skeleton />
      ) : (
        <>
          {tab === 'accounts' && renderAccounts()}
          {tab === 'access' && renderAccess()}
        </>
      )}
    </div>
  );
}
