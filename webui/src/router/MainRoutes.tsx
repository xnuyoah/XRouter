import { Navigate, useRoutes, type Location } from 'react-router-dom';
import { DashboardPage } from '@/features/dashboard/DashboardPage';
import { ChannelStatusPage } from '@/features/channelStatus/ChannelStatusPage';
import { WebSearchPage } from '@/features/webSearch/WebSearchPage';
import { ProviderAccountsPage } from '@/features/providerAccounts/ProviderAccountsPage';
import { ApiTokensPage } from '@/features/apiTokens/ApiTokensPage';
import { ConfigPage } from '@/features/config/ConfigPage';
import { LogsPage } from '@/features/logs/LogsPage';
import { UsageAnalyticsPage } from '@/features/usageAnalytics/UsageAnalyticsPage';

const mainRoutes = [
  { path: '/', element: <DashboardPage /> },
  { path: '/dashboard', element: <DashboardPage /> },
  { path: '/settings', element: <Navigate to="/config" replace /> },
  { path: '/api-keys', element: <Navigate to="/config" replace /> },
  { path: '/web-search', element: <WebSearchPage /> },
  { path: '/channel-status', element: <ChannelStatusPage /> },
  { path: '/usage', element: <UsageAnalyticsPage /> },
  { path: '/usage/overview', element: <UsageAnalyticsPage /> },
  { path: '/usage/logs', element: <UsageAnalyticsPage /> },
  { path: '/usage/search', element: <UsageAnalyticsPage /> },
  { path: '/provider-accounts', element: <ProviderAccountsPage /> },
  { path: '/api-tokens', element: <ApiTokensPage /> },
  { path: '/config', element: <ConfigPage /> },
  { path: '/logs', element: <LogsPage /> },
  { path: '*', element: <Navigate to="/" replace /> },
];

export function MainRoutes({ location }: { location?: Location }) {
  return useRoutes(mainRoutes, location);
}
