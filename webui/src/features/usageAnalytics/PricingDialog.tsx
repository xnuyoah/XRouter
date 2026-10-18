import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { Modal } from '@/components/ui/Modal';
import { Select, type SelectOption } from '@/components/ui/Select';
import {
  usageAnalyticsApi,
  type UsageModelPrice,
  type UsagePricingSnapshot,
  type UsageSearchPrice,
} from '@/services/api';

import styles from './PricingDialog.module.scss';

interface PricingDialogProps {
  open: boolean;
  onClose: () => void;
  onSaved: () => void;
}

interface ModelDraft {
  provider: string;
  model: string;
  plan: string;
  service_tier: string;
  input: string;
  output: string;
  cache_read: string;
  cache_write: string;
  reasoning: string;
}

interface SearchDraft {
  channel: string;
  operation: string;
  unit: string;
  price: string;
}

const EMPTY_MODEL: ModelDraft = {
  provider: '',
  model: '',
  plan: '',
  service_tier: '',
  input: '0',
  output: '0',
  cache_read: '0',
  cache_write: '0',
  reasoning: '0',
};

const EMPTY_SEARCH: SearchDraft = {
  channel: 'tavily',
  operation: 'search',
  unit: 'request',
  price: '0',
};

const numberValue = (value: string): number => {
  const parsed = Number(value);
  return Number.isFinite(parsed) && parsed >= 0 ? parsed : 0;
};

export function PricingDialog({ open, onClose, onSaved }: PricingDialogProps) {
  const { t } = useTranslation();
  const [snapshot, setSnapshot] = useState<UsagePricingSnapshot | null>(null);
  const [loading, setLoading] = useState(false);
  const [syncing, setSyncing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [query, setQuery] = useState('');
  const [error, setError] = useState('');
  const [modelDraft, setModelDraft] = useState<ModelDraft>(EMPTY_MODEL);
  const [searchDraft, setSearchDraft] = useState<SearchDraft>(EMPTY_SEARCH);
  const queryRef = useRef('');

  useEffect(() => {
    queryRef.current = query;
  }, [query]);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const result = await usageAnalyticsApi.getPricing({ q: queryRef.current, limit: 200 });
      setSnapshot(result);
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.pricing_load_failed'));
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    if (open) {
      void load();
    }
  }, [open, load]);

  const channelOptions: SelectOption[] = useMemo(
    () =>
      ['tavily', 'exa', 'anysearch', 'firecrawl', 'tinyfish'].map((value) => ({
        value,
        label: value,
      })),
    []
  );

  const unitOptions: SelectOption[] = [
    { value: 'request', label: t('usage_analytics.pricing_unit_request') },
    { value: 'credit', label: t('usage_analytics.pricing_unit_credit') },
  ];

  const saveModels = async (models: UsageModelPrice[], search: UsageSearchPrice[]) => {
    setSaving(true);
    setError('');
    try {
      const result = await usageAnalyticsApi.putPricing({ models, search });
      setSnapshot(result);
      onSaved();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.pricing_save_failed'));
    } finally {
      setSaving(false);
    }
  };

  const addModelOverride = async () => {
    if (!modelDraft.model.trim() || !snapshot) return;
    const entry: UsageModelPrice = {
      provider: modelDraft.provider.trim(),
      model: modelDraft.model.trim(),
      plan: modelDraft.plan.trim(),
      service_tier: modelDraft.service_tier.trim(),
      input: numberValue(modelDraft.input),
      output: numberValue(modelDraft.output),
      cache_read: numberValue(modelDraft.cache_read),
      cache_write: numberValue(modelDraft.cache_write),
      reasoning: numberValue(modelDraft.reasoning),
      currency: snapshot.currency,
    };
    const key = (value: UsageModelPrice) =>
      `${value.provider ?? ''}|${value.model}|${value.plan ?? ''}|${value.service_tier ?? ''}`.toLowerCase();
    const models = [
      ...(snapshot.manual_models ?? []).filter((value) => key(value) !== key(entry)),
      entry,
    ];
    await saveModels(models, snapshot.search);
    setModelDraft(EMPTY_MODEL);
  };

  const removeModelOverride = async (entry: UsageModelPrice) => {
    if (!snapshot) return;
    const key = (value: UsageModelPrice) =>
      `${value.provider ?? ''}|${value.model}|${value.plan ?? ''}|${value.service_tier ?? ''}`.toLowerCase();
    await saveModels(
      (snapshot.manual_models ?? []).filter((value) => key(value) !== key(entry)),
      snapshot.search
    );
  };

  const addSearchOverride = async () => {
    if (!searchDraft.channel.trim() || !snapshot) return;
    const entry: UsageSearchPrice = {
      channel: searchDraft.channel.trim().toLowerCase(),
      operation: searchDraft.operation.trim().toLowerCase(),
      unit: searchDraft.unit,
      price: numberValue(searchDraft.price),
      currency: snapshot.currency,
    };
    const key = (value: UsageSearchPrice) =>
      `${value.channel}|${value.operation ?? ''}|${value.unit ?? ''}`.toLowerCase();
    const search = [...(snapshot.search ?? []).filter((value) => key(value) !== key(entry)), entry];
    await saveModels(snapshot.manual_models ?? [], search);
    setSearchDraft(EMPTY_SEARCH);
  };

  const removeSearchOverride = async (entry: UsageSearchPrice) => {
    if (!snapshot) return;
    const key = (value: UsageSearchPrice) =>
      `${value.channel}|${value.operation ?? ''}|${value.unit ?? ''}`.toLowerCase();
    await saveModels(
      snapshot.manual_models,
      (snapshot.search ?? []).filter((value) => key(value) !== key(entry))
    );
  };

  const sync = async () => {
    setSyncing(true);
    setError('');
    try {
      await usageAnalyticsApi.syncPricing();
      await load();
      onSaved();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : t('usage_analytics.pricing_sync_failed'));
    } finally {
      setSyncing(false);
    }
  };

  return (
    <Modal open={open} onClose={onClose} title={t('usage_analytics.pricing_title')} width={980}>
      <div className={styles.dialog}>
        <div className={styles.toolbar}>
          <Input
            value={query}
            onChange={(event) => setQuery(event.target.value)}
            placeholder={t('usage_analytics.pricing_search')}
          />
          <Button variant="secondary" onClick={() => void load()} loading={loading}>
            {t('usage_analytics.refresh')}
          </Button>
          <Button variant="secondary" onClick={() => void sync()} loading={syncing}>
            {t('usage_analytics.pricing_sync')}
          </Button>
        </div>

        {snapshot && (
          <div className={styles.meta}>
            <span>
              {t('usage_analytics.pricing_revision')}: {snapshot.revision}
            </span>
            <span>
              {t('usage_analytics.pricing_updated')}:{' '}
              {new Date(snapshot.updated_at).toLocaleString()}
            </span>
            <span>
              {t('usage_analytics.pricing_currency')}: {snapshot.currency}
            </span>
            <span>
              {t('usage_analytics.pricing_total')}: {snapshot.total_models}
            </span>
            <span>
              {t('usage_analytics.pricing_sync_status')}:{' '}
              {snapshot.sync?.running
                ? t('usage_analytics.pricing_syncing')
                : snapshot.sync?.last_completed_at
                  ? new Date(snapshot.sync.last_completed_at).toLocaleString()
                  : t('usage_analytics.pricing_never_synced')}
            </span>
            {snapshot.sync?.last_error && (
              <span className={styles.syncError}>{snapshot.sync.last_error}</span>
            )}
          </div>
        )}

        {error && <div className={styles.error}>{error}</div>}

        <section className={styles.section}>
          <h3>{t('usage_analytics.pricing_manual_models')}</h3>
          <div className={styles.formGrid}>
            <Input
              value={modelDraft.provider}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, provider: event.target.value }))
              }
              placeholder={t('usage_analytics.provider')}
            />
            <Input
              value={modelDraft.model}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, model: event.target.value }))
              }
              placeholder={t('usage_analytics.model')}
            />
            <Input
              value={modelDraft.plan}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, plan: event.target.value }))
              }
              placeholder={t('usage_analytics.plan')}
            />
            <Input
              value={modelDraft.service_tier}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, service_tier: event.target.value }))
              }
              placeholder={t('usage_analytics.pricing_service_tier')}
            />
            <Input
              type="number"
              min={0}
              step="any"
              value={modelDraft.input}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, input: event.target.value }))
              }
              placeholder="input / 1M"
            />
            <Input
              type="number"
              min={0}
              step="any"
              value={modelDraft.output}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, output: event.target.value }))
              }
              placeholder="output / 1M"
            />
            <Input
              type="number"
              min={0}
              step="any"
              value={modelDraft.cache_read}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, cache_read: event.target.value }))
              }
              placeholder="cache read / 1M"
            />
            <Input
              type="number"
              min={0}
              step="any"
              value={modelDraft.cache_write}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, cache_write: event.target.value }))
              }
              placeholder="cache write / 1M"
            />
            <Input
              type="number"
              min={0}
              step="any"
              value={modelDraft.reasoning}
              onChange={(event) =>
                setModelDraft((current) => ({ ...current, reasoning: event.target.value }))
              }
              placeholder="reasoning / 1M"
            />
            <Button onClick={() => void addModelOverride()} loading={saving}>
              {t('usage_analytics.pricing_add_override')}
            </Button>
          </div>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('usage_analytics.provider')}</th>
                  <th>{t('usage_analytics.model')}</th>
                  <th>{t('usage_analytics.plan')}</th>
                  <th>Input</th>
                  <th>Output</th>
                  <th>Cache R</th>
                  <th>Cache W</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(snapshot?.manual_models ?? []).map((entry) => (
                  <tr key={`${entry.provider}:${entry.model}:${entry.plan}:${entry.service_tier}`}>
                    <td>{entry.provider || '—'}</td>
                    <td>{entry.model}</td>
                    <td>{entry.plan || entry.service_tier || '—'}</td>
                    <td>{entry.input}</td>
                    <td>{entry.output}</td>
                    <td>{entry.cache_read}</td>
                    <td>{entry.cache_write}</td>
                    <td>
                      <Button
                        variant="danger"
                        size="sm"
                        onClick={() => void removeModelOverride(entry)}
                      >
                        {t('usage_analytics.pricing_remove')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {snapshot && (snapshot.manual_models?.length ?? 0) === 0 && (
              <div className={styles.empty}>{t('usage_analytics.pricing_no_manual')}</div>
            )}
          </div>
        </section>

        <section className={styles.section}>
          <h3>{t('usage_analytics.pricing_manual_search')}</h3>
          <div className={styles.formGrid}>
            <Select
              value={searchDraft.channel}
              options={channelOptions}
              onChange={(value) => setSearchDraft((current) => ({ ...current, channel: value }))}
              ariaLabel={t('usage_analytics.channel')}
            />
            <Input
              value={searchDraft.operation}
              onChange={(event) =>
                setSearchDraft((current) => ({ ...current, operation: event.target.value }))
              }
              placeholder={t('usage_analytics.operation')}
            />
            <Select
              value={searchDraft.unit}
              options={unitOptions}
              onChange={(value) => setSearchDraft((current) => ({ ...current, unit: value }))}
              ariaLabel={t('usage_analytics.units')}
            />
            <Input
              type="number"
              min={0}
              step="any"
              value={searchDraft.price}
              onChange={(event) =>
                setSearchDraft((current) => ({ ...current, price: event.target.value }))
              }
              placeholder={t('usage_analytics.pricing_price')}
            />
            <Button onClick={() => void addSearchOverride()} loading={saving}>
              {t('usage_analytics.pricing_add_override')}
            </Button>
          </div>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('usage_analytics.channel')}</th>
                  <th>{t('usage_analytics.operation')}</th>
                  <th>{t('usage_analytics.units')}</th>
                  <th>{t('usage_analytics.pricing_price')}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {(snapshot?.search ?? []).map((entry) => (
                  <tr key={`${entry.channel}:${entry.operation}:${entry.unit}`}>
                    <td>{entry.channel}</td>
                    <td>{entry.operation || '*'}</td>
                    <td>{entry.unit || 'request'}</td>
                    <td>{entry.price}</td>
                    <td>
                      <Button
                        variant="danger"
                        size="sm"
                        onClick={() => void removeSearchOverride(entry)}
                      >
                        {t('usage_analytics.pricing_remove')}
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            {snapshot && (snapshot.search?.length ?? 0) === 0 && (
              <div className={styles.empty}>{t('usage_analytics.pricing_no_manual')}</div>
            )}
          </div>
        </section>

        <section className={styles.section}>
          <h3>{t('usage_analytics.pricing_effective')}</h3>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>{t('usage_analytics.provider')}</th>
                  <th>{t('usage_analytics.model')}</th>
                  <th>{t('usage_analytics.plan')}</th>
                  <th>Input</th>
                  <th>Output</th>
                  <th>Cache R</th>
                  <th>Cache W</th>
                  <th>{t('usage_analytics.pricing_source')}</th>
                </tr>
              </thead>
              <tbody>
                {(snapshot?.models ?? []).map((entry) => (
                  <tr
                    key={`${entry.provider}:${entry.model}:${entry.plan}:${entry.service_tier}:${entry.source}`}
                  >
                    <td>{entry.provider || '—'}</td>
                    <td>{entry.model}</td>
                    <td>{entry.plan || entry.service_tier || '—'}</td>
                    <td>{entry.input}</td>
                    <td>{entry.output}</td>
                    <td>{entry.cache_read}</td>
                    <td>{entry.cache_write}</td>
                    <td>{entry.source || '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </Modal>
  );
}
