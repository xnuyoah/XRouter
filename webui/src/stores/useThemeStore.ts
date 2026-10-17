/**
 * 主题状态管理
 * 仅支持浅色 / 深色两档，默认浅色（历史「auto」「纯白」存储值按浅色处理）
 */

import { create } from 'zustand';
import { persist } from 'zustand/middleware';
import type { Theme } from '@/types';
import { STORAGE_KEY_THEME } from '@/utils/constants';

type ResolvedTheme = 'light' | 'dark';

interface ThemeState {
  theme: Theme;
  resolvedTheme: ResolvedTheme;
  setTheme: (theme: Theme) => void;
  initializeTheme: () => () => void;
}

const resolveTheme = (theme: Theme): ResolvedTheme => (theme === 'dark' ? 'dark' : 'light');

const applyTheme = (resolved: ResolvedTheme) => {
  if (resolved === 'dark') {
    document.documentElement.setAttribute('data-theme', 'dark');
    return;
  }

  document.documentElement.removeAttribute('data-theme');
};

export const useThemeStore = create<ThemeState>()(
  persist(
    (set, get) => ({
      theme: 'light',
      resolvedTheme: 'light',

      setTheme: (theme) => {
        const resolved = resolveTheme(theme);
        applyTheme(resolved);
        set({ theme, resolvedTheme: resolved });
      },

      initializeTheme: () => {
        const { theme, setTheme } = get();

        // 应用已保存的主题
        setTheme(theme);

        return () => {};
      },
    }),
    {
      name: STORAGE_KEY_THEME,
      // 版本 1：历史「auto」「white」值迁移为浅色。zustand 仅在存储版本
      // 与此版本不同时调用 migrate，升级版本号才能让 0 版老数据走迁移。
      version: 1,
      migrate: (persisted) => {
        const state = persisted as { theme?: string; resolvedTheme?: ResolvedTheme };
        if (state?.theme === 'white' || state?.theme === 'auto' || !state?.theme) {
          return { ...state, theme: 'light' as const, resolvedTheme: 'light' as const };
        }
        return persisted;
      },
    }
  )
);
