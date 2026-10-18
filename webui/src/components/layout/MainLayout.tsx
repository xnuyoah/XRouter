import {
  ReactNode,
  RefObject,
  SVGProps,
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { NavLink, useLocation, useNavigate } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { PageTransition } from '@/components/common/PageTransition';
import { MainRoutes } from '@/router/MainRoutes';
import { pluginsApi } from '@/services/api';
import {
  IconActivity,
  IconDollarSign,
  IconSidebarConfig,
  IconSidebarDashboard,
  IconSidebarLogs,
  IconSidebarPlugins,
  IconSidebarSearch,
  IconChevronDown,
} from '@/components/ui/icons';
import { INLINE_LOGO } from '@/assets/logoInline';
import {
  useAuthStore,
  useConfigStore,
  useLanguageStore,
  useNotificationStore,
  useThemeStore,
} from '@/stores';
import {
  collectPluginResourceEntries,
  PLUGIN_RESOURCES_REFRESH_EVENT,
  resolvePluginAssetURL,
  type PluginResourceEntry,
} from '@/features/plugins/pluginResources';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { LANGUAGE_LABEL_KEYS, LANGUAGE_ORDER } from '@/utils/constants';
import { isSupportedLanguage } from '@/utils/language';
import type { Theme } from '@/types';

const sidebarIcons: Record<string, ReactNode> = {
  dashboard: <IconSidebarDashboard size={17} />,
  channelStatus: <IconActivity size={17} />,
  usageAnalytics: <IconDollarSign size={17} />,
  webSearch: <IconSidebarSearch size={17} />,
  plugins: <IconSidebarPlugins size={17} />,
  config: <IconSidebarConfig size={17} />,
  logs: <IconSidebarLogs size={17} />,
};

interface SidebarNavLinkItem {
  kind?: 'link';
  path: string;
  labelKey?: string;
  metaKey?: string;
  label?: string;
  meta?: string;
  badge?: number;
  badgeLabel?: string;
  icon: ReactNode;
}

interface SidebarNavDrawerItem {
  kind: 'drawer';
  id: string;
  label: string;
  meta?: string;
  icon: ReactNode;
  children: SidebarNavLinkItem[];
}

type SidebarNavItem = SidebarNavLinkItem | SidebarNavDrawerItem;

interface SidebarNavGroup {
  id: string;
  labelKey: string;
  items: SidebarNavItem[];
}

const flattenNavItems = (items: SidebarNavItem[]): SidebarNavLinkItem[] =>
  items.flatMap((item) => (item.kind === 'drawer' ? item.children : [item]));

/** 点击菜单外或按下 Escape 时关闭弹出菜单 */
function useMenuDismiss(
  open: boolean,
  menuRef: RefObject<HTMLElement | null>,
  onClose: () => void
) {
  useEffect(() => {
    if (!open) {
      return;
    }

    const handlePointerDown = (event: MouseEvent) => {
      if (!menuRef.current?.contains(event.target as Node)) {
        onClose();
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        onClose();
      }
    };

    document.addEventListener('mousedown', handlePointerDown);
    document.addEventListener('keydown', handleEscape);

    return () => {
      document.removeEventListener('mousedown', handlePointerDown);
      document.removeEventListener('keydown', handleEscape);
    };
  }, [open, menuRef, onClose]);
}

function PluginSidebarIcon({ src }: { src: string }) {
  const [failed, setFailed] = useState(false);
  const showImage = Boolean(src) && !failed;

  return showImage ? (
    <img src={src} alt="" onError={() => setFailed(true)} />
  ) : (
    <IconSidebarPlugins size={17} />
  );
}

// Topbar action icons
const headerIconProps: SVGProps<SVGSVGElement> = {
  width: 16,
  height: 16,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': 'true',
  focusable: 'false',
};

const headerIcons = {
  refresh: (
    <svg {...headerIconProps}>
      <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8" />
      <path d="M21 3v5h-5" />
    </svg>
  ),
  menu: (
    <svg {...headerIconProps}>
      <path d="M4 7h16" />
      <path d="M4 12h16" />
      <path d="M4 17h16" />
    </svg>
  ),
  close: (
    <svg {...headerIconProps}>
      <path d="M18 6 6 18" />
      <path d="m6 6 12 12" />
    </svg>
  ),
  search: (
    <svg {...headerIconProps}>
      <circle cx="11" cy="11" r="8" />
      <path d="m21 21-4.35-4.35" />
    </svg>
  ),
  language: (
    <svg {...headerIconProps}>
      <circle cx="12" cy="12" r="10" />
      <path d="M2 12h20" />
      <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
    </svg>
  ),
  sun: (
    <svg {...headerIconProps}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2" />
      <path d="M12 20v2" />
      <path d="m4.93 4.93 1.41 1.41" />
      <path d="m17.66 17.66 1.41 1.41" />
      <path d="M2 12h2" />
      <path d="M20 12h2" />
      <path d="m6.34 17.66-1.41 1.41" />
      <path d="m19.07 4.93-1.41 1.41" />
    </svg>
  ),
  moon: (
    <svg {...headerIconProps}>
      <path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9z" />
    </svg>
  ),
  logout: (
    <svg {...headerIconProps}>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <path d="m16 17 5-5-5-5" />
      <path d="M21 12H9" />
    </svg>
  ),
};

/** 主题切换菜单项：浅色 / 深色（「跟随系统」「纯白」档已移除） */
const THEME_OPTIONS: Array<{ key: Theme; labelKey: string }> = [
  { key: 'light', labelKey: 'theme.light' },
  { key: 'dark', labelKey: 'theme.dark' },
];

export function MainLayout() {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const location = useLocation();
  const navigate = useNavigate();

  const logout = useAuthStore((state) => state.logout);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const apiBase = useAuthStore((state) => state.apiBase);
  const supportsPlugin = useAuthStore((state) => state.supportsPlugin);

  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const clearCache = useConfigStore((state) => state.clearCache);

  const theme = useThemeStore((state) => state.theme);
  const setTheme = useThemeStore((state) => state.setTheme);
  const language = useLanguageStore((state) => state.language);
  const setLanguage = useLanguageStore((state) => state.setLanguage);

  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [languageMenuOpen, setLanguageMenuOpen] = useState(false);
  const [themeMenuOpen, setThemeMenuOpen] = useState(false);
  const [searchOpen, setSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState('');
  const [pluginResources, setPluginResources] = useState<PluginResourceEntry[]>([]);
  const [expandedPluginResourceIDs, setExpandedPluginResourceIDs] = useState<Set<string>>(
    () => new Set()
  );
  const contentRef = useRef<HTMLDivElement | null>(null);
  const languageMenuRef = useRef<HTMLDivElement | null>(null);
  const themeMenuRef = useRef<HTMLDivElement | null>(null);
  const searchMenuRef = useRef<HTMLDivElement | null>(null);
  const searchInputRef = useRef<HTMLInputElement | null>(null);

  const brandName = 'XRouter';
  const isLogsPage = location.pathname.startsWith('/logs');
  const isPluginResourcePage = location.pathname.startsWith('/plugin-pages');

  // Keep header height available to sticky mobile elements and overlays.
  useLayoutEffect(() => {
    document.documentElement.style.setProperty('--header-height', '72px');
    return () => {
      document.documentElement.style.removeProperty('--header-height');
    };
  }, []);

  // Keep the content center available to bottom overlays that align with the main area.
  useLayoutEffect(() => {
    const updateContentCenter = () => {
      const el = contentRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      const centerX = rect.left + rect.width / 2;
      document.documentElement.style.setProperty('--content-center-x', `${centerX}px`);
    };

    updateContentCenter();

    const resizeObserver =
      typeof ResizeObserver !== 'undefined' && contentRef.current
        ? new ResizeObserver(updateContentCenter)
        : null;

    if (resizeObserver && contentRef.current) {
      resizeObserver.observe(contentRef.current);
    }

    window.addEventListener('resize', updateContentCenter);

    return () => {
      if (resizeObserver) {
        resizeObserver.disconnect();
      }
      window.removeEventListener('resize', updateContentCenter);
      document.documentElement.style.removeProperty('--content-center-x');
    };
  }, []);

  const closeLanguageMenu = useCallback(() => setLanguageMenuOpen(false), []);
  const closeThemeMenu = useCallback(() => setThemeMenuOpen(false), []);
  useMenuDismiss(languageMenuOpen, languageMenuRef, closeLanguageMenu);
  useMenuDismiss(themeMenuOpen, themeMenuRef, closeThemeMenu);

  const toggleLanguageMenu = useCallback(() => {
    setLanguageMenuOpen((prev) => !prev);
    setThemeMenuOpen(false);
  }, []);

  const toggleThemeMenu = useCallback(() => {
    setThemeMenuOpen((prev) => !prev);
    setLanguageMenuOpen(false);
  }, []);

  const handleThemeSelect = useCallback(
    (nextTheme: Theme) => {
      setTheme(nextTheme);
      setThemeMenuOpen(false);
    },
    [setTheme]
  );

  const handleLanguageSelect = useCallback(
    (nextLanguage: string) => {
      if (!isSupportedLanguage(nextLanguage)) {
        return;
      }
      setLanguage(nextLanguage);
      setLanguageMenuOpen(false);
    },
    [setLanguage]
  );

  useEffect(() => {
    fetchConfig().catch(() => {
      // Ignore the initial failure; the login flow shows the user-facing prompt.
    });
  }, [fetchConfig]);

  const loadPluginResources = useCallback(async () => {
    if (connectionStatus !== 'connected' || !supportsPlugin) {
      setPluginResources([]);
      return;
    }

    try {
      const plugins = await pluginsApi.list();
      setPluginResources(collectPluginResourceEntries(plugins.plugins, t));
    } catch {
      setPluginResources([]);
    }
  }, [connectionStatus, supportsPlugin, t]);

  useEffect(() => {
    const timer = window.setTimeout(() => {
      void loadPluginResources();
    }, 0);

    window.addEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, loadPluginResources);

    return () => {
      window.clearTimeout(timer);
      window.removeEventListener(PLUGIN_RESOURCES_REFRESH_EVENT, loadPluginResources);
    };
  }, [apiBase, loadPluginResources]);

  const pluginResourceGroups = pluginResources.reduce<
    Array<{ pluginID: string; pluginTitle: string; entries: PluginResourceEntry[] }>
  >((groups, resource) => {
    const group = groups.find((item) => item.pluginID === resource.pluginID);
    if (group) {
      group.entries.push(resource);
      return groups;
    }

    groups.push({
      pluginID: resource.pluginID,
      pluginTitle: resource.pluginTitle,
      entries: [resource],
    });
    return groups;
  }, []);

  const pluginPageNavItems: SidebarNavItem[] = supportsPlugin
    ? pluginResourceGroups.flatMap((group): SidebarNavItem[] => {
        if (group.entries.length === 1) {
          const resource = group.entries[0];
          const pluginLogo = resolvePluginAssetURL(resource.pluginLogo, apiBase);
          return [
            {
              path: resource.route,
              label: resource.label,
              meta: resource.description,
              icon: <PluginSidebarIcon src={pluginLogo} />,
            },
          ];
        }

        const pluginLogo = resolvePluginAssetURL(group.entries[0]?.pluginLogo ?? '', apiBase);
        return [
          {
            kind: 'drawer',
            id: `plugin-pages-${group.pluginID}`,
            label: group.pluginTitle,
            meta: t('plugin_resource.page_count', { count: group.entries.length }),
            icon: <PluginSidebarIcon src={pluginLogo} />,
            children: group.entries.map((resource) => ({
              path: resource.route,
              label: resource.label,
              meta: resource.description,
              icon: <span className="nav-sub-dot" aria-hidden="true" />,
            })),
          },
        ];
      })
    : [];

  const navGroups: SidebarNavGroup[] = [
    {
      id: 'operate',
      labelKey: 'nav_groups.operate',
      items: [
        {
          path: '/',
          labelKey: 'nav.dashboard',
          metaKey: 'nav_meta.dashboard',
          icon: sidebarIcons.dashboard,
        },
      ],
    },
    {
      id: 'observe',
      labelKey: 'nav_groups.observe',
      items: [
        {
          path: '/usage',
          labelKey: 'nav.usage_analytics',
          metaKey: 'nav_meta.usage_analytics',
          icon: sidebarIcons.usageAnalytics,
        },
        {
          path: '/channel-status',
          labelKey: 'nav.channel_status',
          metaKey: 'nav_meta.channel_status',
          icon: sidebarIcons.channelStatus,
        },
        {
          path: '/web-search',
          labelKey: 'nav.web_search',
          metaKey: 'nav_meta.web_search',
          icon: sidebarIcons.webSearch,
        },
        {
          path: '/logs',
          labelKey: 'nav.logs',
          metaKey: 'nav_meta.logs',
          icon: sidebarIcons.logs,
        },
      ],
    },
    {
      id: 'control',
      labelKey: 'nav_groups.control',
      items: [
        {
          path: '/config',
          labelKey: 'nav.config_management',
          metaKey: 'nav_meta.config_management',
          icon: sidebarIcons.config,
        },
      ],
    },
    ...(pluginPageNavItems.length > 0
      ? [
          {
            id: 'plugin-pages',
            labelKey: 'nav_groups.plugin_pages',
            items: pluginPageNavItems,
          },
        ]
      : []),
  ];
  const navItems = navGroups.flatMap((group) => flattenNavItems(group.items));
  const navOrder = navItems.map((item) => item.path);
  const getRouteOrder = (pathname: string) => {
    const trimmedPath =
      pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname;
    const normalizedPath = trimmedPath === '/dashboard' ? '/' : trimmedPath;

    const exactIndex = navOrder.indexOf(normalizedPath);
    if (exactIndex !== -1) return exactIndex;
    const nestedIndex = navOrder.findIndex(
      (path) => path !== '/' && normalizedPath.startsWith(`${path}/`)
    );
    return nestedIndex === -1 ? null : nestedIndex;
  };

  const getTransitionVariant = useCallback(() => 'vertical', []);

  const handleRefreshAll = async () => {
    clearCache();
    const results = await Promise.allSettled([
      fetchConfig(true),
      loadPluginResources(),
      triggerHeaderRefresh(),
    ]);
    const rejected = results.find((result) => result.status === 'rejected');
    if (rejected && rejected.status === 'rejected') {
      const reason = rejected.reason;
      const message =
        typeof reason === 'string' ? reason : reason instanceof Error ? reason.message : '';
      showNotification(
        `${t('notification.refresh_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
      return;
    }
    showNotification(t('notification.data_refreshed'), 'success');
  };

  const togglePluginResourceDrawer = useCallback((drawerID: string) => {
    setExpandedPluginResourceIDs((current) => {
      const next = new Set(current);
      if (next.has(drawerID)) {
        next.delete(drawerID);
      } else {
        next.add(drawerID);
      }
      return next;
    });
  }, []);

  // 全局搜索：⌘K / Ctrl+K 聚焦，Escape 关闭；纯客户端过滤导航条目。
  const searchCandidates = useMemo(
    () => {
      const fromNav = navItems.map((item) => ({
        path: item.path,
        label: item.label ?? (item.labelKey ? t(item.labelKey) : ''),
        meta: item.meta ?? (item.metaKey ? t(item.metaKey) : ''),
      }));
      // 扩展管理不在侧边栏，但路由始终可达（页面自身渲染 no-CGO /
      // 平台不匹配诊断）。动态扩展分组为空时这里是唯一 UI 入口，
      // 保证全局搜索始终可达。
      if (!fromNav.some((item) => item.path === '/plugins')) {
        fromNav.push({
          path: '/plugins',
          label: t('nav.plugins'),
          meta: t('nav_meta.plugins'),
        });
      }
      return fromNav;
    },
    // navItems 每次渲染都是新建数组，这里依赖 t 与导航内容本身即可。
    // eslint-disable-next-line react-hooks/exhaustive-deps -- navItems is rebuilt every render; key on the translated strings instead.
    [t, pluginPageNavItems]
  );
  const searchResults = useMemo(() => {
    const normalized = searchQuery.trim().toLowerCase();
    if (!normalized) return searchCandidates;
    return searchCandidates.filter(
      (item) =>
        item.label.toLowerCase().includes(normalized) ||
        item.meta.toLowerCase().includes(normalized) ||
        item.path.toLowerCase().includes(normalized)
    );
  }, [searchCandidates, searchQuery]);

  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === 'k') {
        event.preventDefault();
        setSearchOpen(true);
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, []);

  useEffect(() => {
    if (searchOpen) {
      setSearchQuery('');
      searchInputRef.current?.focus();
    }
  }, [searchOpen]);

  const closeSearch = useCallback(() => setSearchOpen(false), []);
  useMenuDismiss(searchOpen, searchMenuRef, closeSearch);

  const isMac = useMemo(() => {
    if (typeof navigator === 'undefined') return false;
    const platform =
      (navigator as unknown as { userAgentData?: { platform?: string } }).userAgentData?.platform ||
      navigator.platform ||
      navigator.userAgent ||
      '';
    return /(Mac|iPhone|iPod|iPad)/i.test(platform);
  }, []);
  const searchShortcut = isMac ? '⌘K' : 'Ctrl K';

  const renderNavBadge = (badge?: number, badgeLabel?: string) =>
    typeof badge === 'number' ? (
      <>
        {badge > 0 ? (
          <span className="nav-badge" aria-hidden="true">
            {badge}
          </span>
        ) : null}
        {badgeLabel ? <span className="nav-badge-sr-only">{badgeLabel}</span> : null}
      </>
    ) : null;

  const renderNavLink = (item: SidebarNavLinkItem, className = 'nav-item') => {
    const itemLabel = item.label ?? (item.labelKey ? t(item.labelKey) : '');

    return (
      <NavLink
        key={item.path}
        to={item.path}
        className={({ isActive }) => `${className} ${isActive ? 'active' : ''}`}
        onClick={() => setSidebarOpen(false)}
        aria-label={item.badgeLabel ? `${itemLabel}, ${item.badgeLabel}` : undefined}
      >
        <span className="nav-icon">{item.icon}</span>
        <span className="nav-text">
          <span className="nav-label">{itemLabel}</span>
        </span>
        {renderNavBadge(item.badge, item.badgeLabel)}
      </NavLink>
    );
  };

  const renderNavItem = (item: SidebarNavItem) => {
    if (item.kind !== 'drawer') {
      return renderNavLink(item);
    }

    const isActive = item.children.some((child) => child.path === location.pathname);
    const isOpen = isActive || expandedPluginResourceIDs.has(item.id);

    return (
      <div className={`nav-drawer ${isOpen ? 'open' : ''}`} key={item.id}>
        <button
          type="button"
          className={`nav-item nav-drawer-toggle ${isActive ? 'active' : ''} ${
            isOpen ? 'open' : ''
          }`}
          onClick={() => togglePluginResourceDrawer(item.id)}
          aria-expanded={isOpen}
        >
          <span className="nav-icon">{item.icon}</span>
          <span className="nav-text">
            <span className="nav-label">{item.label}</span>
          </span>
          <span className="nav-drawer-caret" aria-hidden="true">
            <IconChevronDown size={14} />
          </span>
        </button>
        {isOpen ? (
          <div className="nav-sub-list">
            {item.children.map((child) => renderNavLink(child, 'nav-item nav-sub-item'))}
          </div>
        ) : null}
      </div>
    );
  };

  const mobileSidebarToggleLabel = sidebarOpen
    ? t('sidebar.toggle_collapse', { defaultValue: 'Close navigation' })
    : t('sidebar.toggle_expand', { defaultValue: 'Open navigation' });

  // 顶栏面包屑：当前页面名（复用 nav i18n key）
  const activeNavItem = navItems.find((item) => {
    const normalized = location.pathname === '/dashboard' ? '/' : location.pathname;
    if (item.path === '/') return normalized === '/';
    return normalized === item.path || normalized.startsWith(`${item.path}/`);
  });
  const currentPageLabel = activeNavItem
    ? (activeNavItem.label ?? (activeNavItem.labelKey ? t(activeNavItem.labelKey) : ''))
    : '';

  return (
    <div className={`app-shell ${isPluginResourcePage ? 'plugin-resource-shell' : ''}`}>
      <header className="topbar">
        <button
          type="button"
          className="icon-btn mobile-menu"
          onClick={() => setSidebarOpen((prev) => !prev)}
          aria-label={mobileSidebarToggleLabel}
          title={mobileSidebarToggleLabel}
        >
          {sidebarOpen ? headerIcons.close : headerIcons.menu}
        </button>

        <nav className="breadcrumbs" aria-label={t('common.close', { defaultValue: '' }) || 'Breadcrumb'}>
          <span className="workspace-name">{brandName}</span>
          <span className="slash" aria-hidden="true">
            /
          </span>
          {currentPageLabel ? <strong>{currentPageLabel}</strong> : null}
        </nav>

        <div className="topbar-right">
          <div className={`global-search ${searchOpen ? 'open' : ''}`} ref={searchMenuRef}>
            <button
              type="button"
              className="global-search-trigger"
              onClick={() => setSearchOpen(true)}
              aria-haspopup="dialog"
              aria-expanded={searchOpen}
            >
              {headerIcons.search}
              <span>{t('header.search_placeholder', { defaultValue: '搜索页面…' })}</span>
              <kbd>{searchShortcut}</kbd>
            </button>
            {searchOpen && (
              <div className="global-search-popover" role="dialog" aria-label={t('header.search_placeholder', { defaultValue: '搜索页面…' })}>
                <div className="global-search-input-row">
                  {headerIcons.search}
                  <input
                    ref={searchInputRef}
                    type="text"
                    value={searchQuery}
                    onChange={(event) => setSearchQuery(event.target.value)}
                    placeholder={t('header.search_placeholder', { defaultValue: '搜索页面…' })}
                  />
                </div>
                <div className="global-search-results">
                  {searchResults.length === 0 ? (
                    <div className="global-search-empty">
                      {t('header.search_empty', { defaultValue: '没有匹配的页面' })}
                    </div>
                  ) : (
                    searchResults.map((result) => (
                      <button
                        key={result.path}
                        type="button"
                        className="global-search-result"
                        onClick={() => {
                          setSearchOpen(false);
                          if (result.path !== location.pathname) {
                            navigate(result.path);
                          }
                        }}
                      >
                        <span className="global-search-result-label">{result.label}</span>
                        {result.meta ? (
                          <span className="global-search-result-meta">{result.meta}</span>
                        ) : null}
                      </button>
                    ))
                  )}
                </div>
              </div>
            )}
          </div>

          <div className={`language-menu ${languageMenuOpen ? 'open' : ''}`} ref={languageMenuRef}>
            <Button
              variant="ghost"
              size="sm"
              onClick={toggleLanguageMenu}
              title={t('language.switch')}
              aria-label={t('language.switch')}
              aria-haspopup="menu"
              aria-expanded={languageMenuOpen}
            >
              {headerIcons.language}
            </Button>
            {languageMenuOpen && (
              <div
                className="notification entering language-menu-popover"
                role="menu"
                aria-label={t('language.switch')}
              >
                {LANGUAGE_ORDER.map((lang) => (
                  <button
                    key={lang}
                    type="button"
                    className={`language-menu-option ${language === lang ? 'active' : ''}`}
                    onClick={() => handleLanguageSelect(lang)}
                    role="menuitemradio"
                    aria-checked={language === lang}
                  >
                    <span>{t(LANGUAGE_LABEL_KEYS[lang])}</span>
                    {language === lang ? <span className="language-menu-check">✓</span> : null}
                  </button>
                ))}
              </div>
            )}
          </div>

          <div className={`theme-menu ${themeMenuOpen ? 'open' : ''}`} ref={themeMenuRef}>
            <Button
              variant="ghost"
              size="sm"
              onClick={toggleThemeMenu}
              title={t('theme.switch')}
              aria-label={t('theme.switch')}
              aria-haspopup="menu"
              aria-expanded={themeMenuOpen}
            >
              {theme === 'dark' ? headerIcons.moon : headerIcons.sun}
            </Button>
            {themeMenuOpen && (
              <div
                className="notification entering theme-menu-popover"
                role="menu"
                aria-label={t('theme.switch')}
              >
                {THEME_OPTIONS.map((option) => (
                  <button
                    key={option.key}
                    type="button"
                    className={`theme-option ${theme === option.key ? 'active' : ''}`}
                    onClick={() => handleThemeSelect(option.key)}
                    role="menuitemradio"
                    aria-checked={theme === option.key}
                  >
                    <span className="theme-option-icon">
                      {option.key === 'dark' ? headerIcons.moon : headerIcons.sun}
                    </span>
                    <span>{t(option.labelKey)}</span>
                  </button>
                ))}
              </div>
            )}
          </div>

          <Button variant="ghost" size="sm" onClick={handleRefreshAll} title={t('header.refresh_all')}>
            {headerIcons.refresh}
          </Button>
          <Button variant="ghost" size="sm" onClick={logout} title={t('header.logout')}>
            {headerIcons.logout}
          </Button>
        </div>
      </header>

      <div className="main-body">
        <button
          type="button"
          className={`nav-backdrop ${sidebarOpen ? 'visible' : ''}`}
          onClick={() => setSidebarOpen(false)}
          aria-label={t('common.close')}
          aria-hidden={!sidebarOpen}
          tabIndex={sidebarOpen ? 0 : -1}
        />

        <aside className={`sidebar ${sidebarOpen ? 'open' : ''}`}>
          <div className="brand">
            <span className="brand-mark">
              <img src={INLINE_LOGO} alt="XRouter logo" />
            </span>
            <span className="brand-text">
              <span className="brand-title">{brandName}</span>
              <span className="brand-subtitle">{t('sidebar.subtitle')}</span>
            </span>
          </div>

          <div className="nav-scroll">
            <p className="nav-workspace-label">
              {t('nav_groups.workspace', { defaultValue: '工作空间' })}
            </p>
            <nav className="nav" aria-label={t('sidebar.subtitle')}>
              {navGroups.map((group) => (
                <div className="nav-group" key={group.id}>
                  <div className="nav-group-label">{t(group.labelKey)}</div>
                  {group.items.map((item) => renderNavItem(item))}
                </div>
              ))}
            </nav>
          </div>
        </aside>

        <div
          className={`content${isLogsPage ? ' content-logs' : ''}${
            isPluginResourcePage ? ' content-plugin-resource' : ''
          }`}
          ref={contentRef}
        >
          <main
            className={`main-content${isLogsPage ? ' main-content-logs' : ''}${
              isPluginResourcePage ? ' main-content-plugin-resource' : ''
            }`}
          >
            <PageTransition
              render={(location) => <MainRoutes location={location} />}
              getRouteOrder={getRouteOrder}
              getTransitionVariant={getTransitionVariant}
              scrollContainerRef={contentRef}
            />
          </main>
        </div>
      </div>
    </div>
  );
}
