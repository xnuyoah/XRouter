import { useEffect, useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { configApi } from '@/services/api';
import {
  apiKeyNameFingerprint,
  readApiKeyNames,
  saveApiKeyName,
} from '@/features/config/apiKeyNames';
import { useAuthStore, useConfigStore, useNotificationStore } from '@/stores';
import { copyToClipboard } from '@/utils/clipboard';

import styles from './ApiTokensPage.module.scss';

type TokenPurpose = 'model' | 'search' | 'shared';

const PURPOSE_KEYS: Record<TokenPurpose, string> = {
  model: 'apiTokens.purposeModel',
  search: 'apiTokens.purposeSearch',
  shared: 'apiTokens.purposeShared',
};

function generateToken(purpose: Exclude<TokenPurpose, 'shared'>): string {
  const bytes = crypto.getRandomValues(new Uint8Array(24));
  const body = Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('');
  return `sk-xr-${purpose}-${body}`;
}

function purposeFromKey(key: string): TokenPurpose {
  if (key.startsWith('sk-xr-model-')) return 'model';
  if (key.startsWith('sk-xr-search-')) return 'search';
  return 'shared';
}

function maskToken(key: string): string {
  if (key.length <= 6) return '••••••';
  return `${key.slice(0, 4)}…${key.slice(-4)}`;
}

export function ApiTokensPage() {
  const { t } = useTranslation();
  const connected = useAuthStore((state) => state.connectionStatus === 'connected');
  const apiBase = useAuthStore((state) => state.apiBase);
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const showNotification = useNotificationStore((state) => state.showNotification);
  const [name, setName] = useState('');
  const [purpose, setPurpose] = useState<Exclude<TokenPurpose, 'shared'>>('model');
  const [saving, setSaving] = useState(false);
  const [configReady, setConfigReady] = useState(false);
  const [error, setError] = useState('');
  const [createdKey, setCreatedKey] = useState('');
  const [revealed, setRevealed] = useState<Record<string, boolean>>({});
  const [names, setNames] = useState<Record<string, string>>({});

  useEffect(() => {
    setNames(readApiKeyNames(apiBase));
  }, [apiBase]);

  useEffect(() => {
    if (!connected) {
      setConfigReady(false);
      return;
    }
    let cancelled = false;
    void fetchConfig()
      .then(() => {
        if (!cancelled) setConfigReady(true);
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setConfigReady(false);
          setError(err instanceof Error ? err.message : String(err));
        }
      });
    return () => {
      cancelled = true;
    };
  }, [connected, fetchConfig]);

  const keys = useMemo(() => config?.apiKeys ?? [], [config?.apiKeys]);
  const counts = useMemo(() => {
    const totals: Record<TokenPurpose, number> = { model: 0, search: 0, shared: 0 };
    keys.forEach((key) => {
      totals[purposeFromKey(key)] += 1;
    });
    return totals;
  }, [keys]);

  const refresh = async () => {
    setError('');
    try {
      await fetchConfig(true);
      setConfigReady(true);
    } catch (errRefresh) {
      setConfigReady(false);
      setError(errRefresh instanceof Error ? errRefresh.message : String(errRefresh));
    }
  };
  useHeaderRefresh(refresh, connected);

  const tokenName = (key: string) => names[apiKeyNameFingerprint(apiBase, key)] ?? '';

  const createToken = async () => {
    if (!configReady) {
      setError(t('apiTokens.configNotReady'));
      return;
    }
    const key = generateToken(purpose);
    setSaving(true);
    setError('');
    try {
      const latest = await fetchConfig(true);
      await configApi.updateAccessApiKeys([...(latest.apiKeys ?? []), key]);
      if (name.trim()) {
        saveApiKeyName(apiBase, key, name.trim());
        setNames(readApiKeyNames(apiBase));
      }
      setCreatedKey(key);
      setName('');
      await fetchConfig(true);
      showNotification(t('apiTokens.created'), 'success');
    } catch (errCreate) {
      setError(errCreate instanceof Error ? errCreate.message : String(errCreate));
    } finally {
      setSaving(false);
    }
  };

  const deleteToken = async (key: string) => {
    if (!configReady) {
      setError(t('apiTokens.configNotReady'));
      return;
    }
    const label = tokenName(key) || maskToken(key);
    if (!window.confirm(t('apiTokens.deleteConfirm', { name: label }))) return;
    setSaving(true);
    setError('');
    try {
      const latest = await fetchConfig(true);
      const latestKeys = latest.apiKeys ?? [];
      if (!latestKeys.includes(key)) {
        setError(t('apiTokens.tokenMissing'));
        return;
      }
      await configApi.updateAccessApiKeys(latestKeys.filter((entry) => entry !== key));
      await fetchConfig(true);
      if (createdKey === key) setCreatedKey('');
      showNotification(t('apiTokens.deleted'), 'success');
    } catch (errDelete) {
      setError(errDelete instanceof Error ? errDelete.message : String(errDelete));
    } finally {
      setSaving(false);
    }
  };

  const copyToken = async (key: string) => {
    const ok = await copyToClipboard(key);
    showNotification(
      t(ok ? 'apiTokens.copied' : 'apiTokens.copyFailed'),
      ok ? 'success' : 'error'
    );
  };

  const origin = typeof window === 'undefined' ? 'http://127.0.0.1:11016' : window.location.origin;
  const modelExample = `curl ${origin}/v1/chat/completions \\
  -H "Authorization: Bearer $XROUTER_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"model":"glm-5.3-flash","messages":[{"role":"user","content":"Hello"}]}'`;
  const searchExample = `curl ${origin}/gateway/tavily/search \\
  -H "Authorization: Bearer $XROUTER_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{"query":"golang","max_results":3}'`;

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1>{t('apiTokens.title')}</h1>
          <p>{t('apiTokens.subtitle')}</p>
        </div>
        <Button variant="secondary" size="sm" onClick={() => void refresh()} disabled={saving}>
          {t('common.refresh')}
        </Button>
      </header>

      {error && <div className={styles.error}>{error}</div>}

      <section className={styles.metrics}>
        <article>
          <span>{t('apiTokens.total')}</span>
          <strong>{keys.length}</strong>
        </article>
        <article>
          <span>{t('apiTokens.purposeModel')}</span>
          <strong>{counts.model}</strong>
        </article>
        <article>
          <span>{t('apiTokens.purposeSearch')}</span>
          <strong>{counts.search}</strong>
        </article>
        <article>
          <span>{t('apiTokens.purposeShared')}</span>
          <strong>{counts.shared}</strong>
        </article>
      </section>

      <section className={styles.panel}>
        <div className={styles.panelHead}>
          <h2>{t('apiTokens.createTitle')}</h2>
          <p>{t('apiTokens.scopeNote')}</p>
        </div>
        <div className={styles.createRow}>
          <input
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder={t('apiTokens.namePlaceholder')}
            aria-label={t('apiTokens.namePlaceholder')}
          />
          <select
            value={purpose}
            onChange={(event) =>
              setPurpose(event.target.value === 'search' ? 'search' : 'model')
            }
            aria-label={t('apiTokens.purpose')}
          >
            <option value="model">{t('apiTokens.purposeModel')}</option>
            <option value="search">{t('apiTokens.purposeSearch')}</option>
          </select>
          <Button onClick={() => void createToken()} loading={saving} disabled={!configReady}>
            {t('apiTokens.create')}
          </Button>
        </div>
        {createdKey && (
          <div className={styles.createdBox}>
            <div>
              <strong>{t('apiTokens.createdTitle')}</strong>
              <span>{t('apiTokens.createdHint')}</span>
            </div>
            <code>{createdKey}</code>
            <Button size="sm" variant="secondary" onClick={() => void copyToken(createdKey)}>
              {t('apiTokens.copy')}
            </Button>
          </div>
        )}
      </section>

      <section className={styles.panel}>
        <div className={styles.panelHead}>
          <h2>{t('apiTokens.listTitle')}</h2>
          <p>{t('apiTokens.listHint')}</p>
        </div>
        {keys.length === 0 ? (
          <EmptyState title={t('apiTokens.empty')} description={t('apiTokens.emptyHint')} />
        ) : (
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('apiTokens.colName')}</th>
                  <th>{t('apiTokens.colPurpose')}</th>
                  <th>{t('apiTokens.colKey')}</th>
                  <th>{t('apiTokens.colActions')}</th>
                </tr>
              </thead>
              <tbody>
                {keys.map((key) => {
                  const tokenPurpose = purposeFromKey(key);
                  const visible = revealed[key] === true;
                  return (
                    <tr key={key}>
                      <td>{tokenName(key) || '—'}</td>
                      <td>
                        <span className={styles[`purpose_${tokenPurpose}`]}>
                          {t(PURPOSE_KEYS[tokenPurpose])}
                        </span>
                      </td>
                      <td className={styles.mono}>{visible ? key : maskToken(key)}</td>
                      <td>
                        <div className={styles.actions}>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() =>
                              setRevealed((current) => ({
                                ...current,
                                [key]: !current[key],
                              }))
                            }
                          >
                            {t(visible ? 'apiTokens.hide' : 'apiTokens.reveal')}
                          </Button>
                          <Button size="sm" variant="ghost" onClick={() => void copyToken(key)}>
                            {t('apiTokens.copy')}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => void deleteToken(key)}
                            disabled={saving}
                          >
                            {t('common.delete')}
                          </Button>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className={styles.examples}>
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <h2>{t('apiTokens.modelExample')}</h2>
          </div>
          <pre>{modelExample}</pre>
        </div>
        <div className={styles.panel}>
          <div className={styles.panelHead}>
            <h2>{t('apiTokens.searchExample')}</h2>
          </div>
          <pre>{searchExample}</pre>
        </div>
      </section>
    </div>
  );
}
