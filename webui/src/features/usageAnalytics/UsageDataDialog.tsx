import { useTranslation } from 'react-i18next';

import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import type { UsageAnalyticsStatus } from '@/services/api';
import { formatCompactNumber, formatFileSize } from '@/utils/format';

import { formatUsageTime } from './format';
import styles from './UsageDataDialog.module.scss';

interface UsageDataDialogProps {
  open: boolean;
  onClose: () => void;
  status: UsageAnalyticsStatus | null;
  disabled: boolean;
  onBackup: () => void;
  onRestore: (file: File) => void;
  onImport: (file: File) => void;
  onReset: () => void;
}

export function UsageDataDialog({
  open,
  onClose,
  status,
  disabled,
  onBackup,
  onRestore,
  onImport,
  onReset,
}: UsageDataDialogProps) {
  const { t } = useTranslation();
  return (
    <Modal open={open} onClose={onClose} title={t('usage_analytics.data_title')} width={720}>
      <div className={styles.content}>
        <p className={styles.hint}>{t('usage_analytics.data_hint')}</p>
        {status ? (
          <div className={styles.statusGrid}>
            <span>
              {t('usage_analytics.status_db')}: <strong>{formatFileSize(status.db_bytes)}</strong>
              {status.max_db_bytes > 0 ? ` / ${formatFileSize(status.max_db_bytes)}` : ''}
            </span>
            <span>
              {t('usage_analytics.status_queue')}:{' '}
              <strong>
                {status.queue_depth} / {status.queue_capacity}
              </strong>
            </span>
            <span>
              {t('usage_analytics.status_dropped')}:{' '}
              <strong className={status.dropped_events > 0 ? styles.danger : undefined}>
                {formatCompactNumber(status.dropped_events)}
              </strong>
            </span>
            <span>
              {t('usage_analytics.status_retention')}: <strong>{status.retention_days} d</strong> /{' '}
              {status.rollup_retention_days} d {t('usage_analytics.status_rollups')}
            </span>
            {status.oldest_event && status.newest_event && (
              <span>
                {t('usage_analytics.status_events_range')}:{' '}
                <strong>
                  {formatUsageTime(status.oldest_event)} → {formatUsageTime(status.newest_event)}
                </strong>
              </span>
            )}
            {status.last_flush && (
              <span>
                {t('usage_analytics.status_last_flush')}:{' '}
                <strong>{formatUsageTime(status.last_flush)}</strong>
              </span>
            )}
            <span>
              {t('usage_analytics.status_price_revision')}: <strong>{status.price_revision}</strong>
              {status.price_currency ? ` · ${status.price_currency}` : ''}
            </span>
          </div>
        ) : (
          <div className={styles.hint}>{t('usage_analytics.disabled')}</div>
        )}

        <div className={styles.actions}>
          <Button variant="secondary" onClick={onBackup} disabled={disabled}>
            {t('usage_analytics.backup')}
          </Button>
          <label className={styles.fileButton}>
            <span>{t('usage_analytics.restore')}</span>
            <input
              type="file"
              accept=".db,application/octet-stream"
              disabled={disabled}
              onChange={(event) => {
                const file = event.target.files?.[0];
                event.target.value = '';
                if (file) onRestore(file);
              }}
            />
          </label>
          <label className={styles.fileButton}>
            <span>{t('usage_analytics.import_legacy')}</span>
            <input
              type="file"
              accept=".db,application/octet-stream"
              disabled={disabled}
              onChange={(event) => {
                const file = event.target.files?.[0];
                event.target.value = '';
                if (file) onImport(file);
              }}
            />
          </label>
        </div>

        <div className={styles.dangerZone}>
          <div>
            <strong>{t('usage_analytics.reset')}</strong>
            <p>{t('usage_analytics.reset_hint')}</p>
          </div>
          <Button variant="danger" size="sm" onClick={onReset} disabled={disabled}>
            {t('usage_analytics.reset')}
          </Button>
        </div>
      </div>
    </Modal>
  );
}
