export function totalPageCount(total: number, pageSize: number): number {
  if (!Number.isFinite(total) || total <= 0) return 1;
  if (!Number.isFinite(pageSize) || pageSize <= 0) return 1;
  return Math.max(1, Math.ceil(total / pageSize));
}

export function clampPage(page: number, total: number, pageSize: number): number {
  const pages = totalPageCount(total, pageSize);
  if (!Number.isFinite(page)) return 1;
  return Math.min(Math.max(1, Math.trunc(page)), pages);
}

export function paginate<T>(items: readonly T[], page: number, pageSize: number): T[] {
  if (items.length === 0) return [];
  const current = clampPage(page, items.length, pageSize);
  const start = (current - 1) * pageSize;
  return items.slice(start, start + pageSize);
}

export type PageItem = { type: 'page'; page: number } | { type: 'ellipsis'; key: string };

export function paginationItems(currentPage: number, totalPages: number): PageItem[] {
  if (totalPages <= 1) return totalPages === 1 ? [{ type: 'page', page: 1 }] : [];
  const current = Math.min(Math.max(1, currentPage), totalPages);
  const pages = new Set<number>([1, totalPages, current, current - 1, current + 1]);
  if (current <= 4) {
    for (let page = 2; page <= Math.min(5, totalPages - 1); page += 1) pages.add(page);
  }
  if (current >= totalPages - 3) {
    for (let page = Math.max(2, totalPages - 4); page < totalPages; page += 1) {
      pages.add(page);
    }
  }
  const sorted = [...pages].filter((page) => page >= 1 && page <= totalPages).sort((a, b) => a - b);
  const result: PageItem[] = [];
  sorted.forEach((page, index) => {
    const previous = sorted[index - 1];
    if (previous !== undefined && page - previous > 1) {
      result.push({ type: 'ellipsis', key: `ellipsis-${previous}-${page}` });
    }
    result.push({ type: 'page', page });
  });
  return result;
}
