import type { PluginListEntry, PluginMenu } from '@/types';
import { normalizeApiBase } from '@/utils/connection';

export const PLUGIN_RESOURCES_REFRESH_EVENT = 'plugin-resources-refresh';

export const notifyPluginResourcesChanged = () => {
  window.dispatchEvent(new Event(PLUGIN_RESOURCES_REFRESH_EVENT));
};

export interface PluginResourceEntry {
  pluginID: string;
  pluginTitle: string;
  pluginLogo: string;
  menuIndex: number;
  menu: PluginMenu;
  label: string;
  description: string;
  route: string;
}

// Built-in extension display names override the plugin binary's own
// metadata.name and menu label so the sidebar and management page show
// the productized name. Resolved through i18n so the label follows the
// active language.
const PLUGIN_DISPLAY_NAME_KEYS: Record<string, string> = {
  'tokens-statistic': 'plugin_display_names.tokens-statistic',
};

export const getPluginDisplayName = (
  pluginID: string,
  t: (key: string) => string
): string | undefined => {
  const key = PLUGIN_DISPLAY_NAME_KEYS[pluginID.trim()];
  if (!key) return undefined;
  const translated = t(key);
  // Missing translations echo the key itself; fall back to no override.
  return translated === key ? undefined : translated;
};

export const getPluginTitle = (plugin: PluginListEntry, t: (key: string) => string) =>
  getPluginDisplayName(plugin.id, t) ?? (plugin.metadata?.name.trim() || plugin.id);

export const buildPluginResourceRoute = (pluginID: string, menuIndex: number) =>
  `/plugin-pages/${encodeURIComponent(pluginID)}/${menuIndex}`;

export const resolvePluginAssetURL = (value: string, apiBase: string) => {
  const trimmed = value.trim();
  if (!trimmed) return '';
  if (/^(https?:|data:|blob:)/i.test(trimmed)) return trimmed;
  if (!trimmed.startsWith('/')) return trimmed;
  const base = normalizeApiBase(apiBase);
  return base ? `${base}${trimmed}` : trimmed;
};

export const collectPluginResourceEntries = (
  plugins: PluginListEntry[],
  t: (key: string) => string
): PluginResourceEntry[] =>
  plugins.flatMap((plugin) => {
    if (!plugin.effectiveEnabled) return [];

    const pluginTitle = getPluginTitle(plugin, t);
    const pluginLogo = plugin.logo || plugin.metadata?.logo || '';

    return plugin.menus
      .map((menu, menuIndex): PluginResourceEntry | null => {
        const path = menu.path.trim();
        if (!path) return null;

        const menuLabel = getPluginDisplayName(plugin.id, t) ?? menu.menu.trim();
        return {
          pluginID: plugin.id,
          pluginTitle,
          pluginLogo,
          menuIndex,
          menu: { ...menu, path },
          label: menuLabel || pluginTitle,
          description: menu.description.trim() || pluginTitle,
          route: buildPluginResourceRoute(plugin.id, menuIndex),
        };
      })
      .filter((entry): entry is PluginResourceEntry => Boolean(entry));
  });
