import { describe, expect, test } from 'bun:test';

import {
  clampPage,
  paginate,
  paginationItems,
  totalPageCount,
} from '@/components/ui/Pagination/paginationUtils';

describe('pagination helpers', () => {
  test('computes page count with a single empty page', () => {
    expect(totalPageCount(0, 25)).toBe(1);
    expect(totalPageCount(861, 25)).toBe(35);
    expect(totalPageCount(25, 25)).toBe(1);
    expect(totalPageCount(26, 25)).toBe(2);
  });

  test('clamps pages to the valid range', () => {
    expect(clampPage(0, 861, 25)).toBe(1);
    expect(clampPage(2, 861, 25)).toBe(2);
    expect(clampPage(99, 861, 25)).toBe(35);
    expect(clampPage(-1, 0, 25)).toBe(1);
    expect(clampPage(Number.NaN, 861, 25)).toBe(1);
  });

  test('slices only the requested page', () => {
    const rows = Array.from({ length: 60 }, (_, index) => index + 1);
    expect(paginate(rows, 1, 25)).toEqual(rows.slice(0, 25));
    expect(paginate(rows, 3, 25)).toEqual(rows.slice(50, 60));
    expect(paginate(rows, 99, 25)).toEqual(rows.slice(50, 60));
    expect(paginate([], 1, 25)).toEqual([]);
  });

  test('renders compact numbered pages with middle ellipses', () => {
    expect(paginationItems(1, 35)).toEqual([
      { type: 'page', page: 1 },
      { type: 'page', page: 2 },
      { type: 'page', page: 3 },
      { type: 'page', page: 4 },
      { type: 'page', page: 5 },
      { type: 'ellipsis', key: 'ellipsis-5-35' },
      { type: 'page', page: 35 },
    ]);
    expect(paginationItems(18, 35)).toEqual([
      { type: 'page', page: 1 },
      { type: 'ellipsis', key: 'ellipsis-1-17' },
      { type: 'page', page: 17 },
      { type: 'page', page: 18 },
      { type: 'page', page: 19 },
      { type: 'ellipsis', key: 'ellipsis-19-35' },
      { type: 'page', page: 35 },
    ]);
    expect(paginationItems(1, 1)).toEqual([{ type: 'page', page: 1 }]);
  });
});
