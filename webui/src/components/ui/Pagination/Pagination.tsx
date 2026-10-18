import { useTranslation } from 'react-i18next';

import styles from './Pagination.module.scss';
import { clampPage, paginationItems, totalPageCount } from './paginationUtils';

export interface PaginationProps {
  page: number;
  pageSize: number;
  total: number;
  pageSizeOptions?: number[];
  onPageChange: (page: number) => void;
  onPageSizeChange: (size: number) => void;
  compact?: boolean;
  className?: string;
}

export function Pagination({
  page,
  pageSize,
  total,
  pageSizeOptions = [25, 50, 100],
  onPageChange,
  onPageSizeChange,
  compact = false,
  className,
}: PaginationProps) {
  const { t } = useTranslation();
  const totalPages = totalPageCount(total, pageSize);
  const current = clampPage(page, total, pageSize);
  const from = total === 0 ? 0 : (current - 1) * pageSize + 1;
  const to = total === 0 ? 0 : Math.min(total, current * pageSize);
  const showPages = totalPages > 1;
  const items = showPages ? paginationItems(current, totalPages) : [];

  return (
    <nav
      className={`${styles.pagination} ${compact ? styles.compact : ''} ${className ?? ''}`}
      aria-label={t('common.pagination.aria')}
    >
      <span className={styles.summary}>
        {t('common.pagination.range', { from, to, total })}
      </span>
      <div className={styles.controls}>
        <label className={styles.pageSize}>
          <span>{t('common.pagination.page_size')}</span>
          <select
            value={String(pageSize)}
            onChange={(event) => onPageSizeChange(Number(event.target.value))}
            aria-label={t('common.pagination.page_size')}
          >
            {pageSizeOptions.map((option) => (
              <option key={option} value={option}>
                {t('common.pagination.size_option', { size: option })}
              </option>
            ))}
          </select>
        </label>
        {showPages && (
          <>
            <div className={styles.pageNav}>
              <button
                type="button"
                className={styles.pageButton}
                onClick={() => onPageChange(current - 1)}
                disabled={current <= 1}
                aria-label={t('common.pagination.previous')}
              >
                ‹
              </button>
              <div className={styles.pages}>
                {items.map((item) =>
                  item.type === 'ellipsis' ? (
                    <span key={item.key} className={styles.ellipsis} aria-hidden="true">
                      ...
                    </span>
                  ) : (
                    <button
                      key={item.page}
                      type="button"
                      className={`${styles.pageButton} ${
                        item.page === current ? styles.active : ''
                      }`}
                      onClick={() => onPageChange(item.page)}
                      aria-current={item.page === current ? 'page' : undefined}
                      aria-label={t('common.pagination.page', { page: item.page })}
                    >
                      {item.page}
                    </button>
                  )
                )}
              </div>
              <button
                type="button"
                className={styles.pageButton}
                onClick={() => onPageChange(current + 1)}
                disabled={current >= totalPages}
                aria-label={t('common.pagination.next')}
              >
                ›
              </button>
            </div>
            <span className={styles.mobileStatus}>
              {t('common.pagination.page_of', { page: current, total: totalPages })}
            </span>
          </>
        )}
      </div>
    </nav>
  );
}
