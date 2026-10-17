import { Navigate, useRoutes, type Location } from 'react-router-dom';
import { DashboardPage } from '@/features/dashboard/DashboardPage';
import { ChannelStatusPage } from '@/features/channelStatus/ChannelStatusPage';
import { WebSearchPage } from '@/features/webSearch/WebSearchPage';
import { PluginResourcePage } from '@/features/plugins/PluginResourcePage';
import { PluginsPage } from '@/features/plugins/PluginsPage';
import { ConfigPage } from '@/features/config/ConfigPage';
import { LogsPage } from '@/features/logs/LogsPage';

const mainRoutes = [
  { path: '/', element: <DashboardPage /> },
  { path: '/dashboard', element: <DashboardPage /> },
  { path: '/settings', element: <Navigate to="/config" replace /> },
  { path: '/api-keys', element: <Navigate to="/config" replace /> },
  { path: '/web-search', element: <WebSearchPage /> },
  { path: '/channel-status', element: <ChannelStatusPage /> },
  // /plugins is always reachable: the page itself renders the no-CGO /
  // platform-mismatch diagnostics from the plugin environment report.
  { path: '/plugin-pages/:pluginId/:menuIndex', element: <PluginResourcePage /> },
  { path: '/plugins', element: <PluginsPage /> },
  { path: '/plugins/*', element: <Navigate to="/plugins" replace /> },
  { path: '/config', element: <ConfigPage /> },
  { path: '/logs', element: <LogsPage /> },
  { path: '*', element: <Navigate to="/" replace /> },
];

export function MainRoutes({ location }: { location?: Location }) {
  return useRoutes(mainRoutes, location);
}
