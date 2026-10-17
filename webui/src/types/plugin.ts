export type PluginConfigFieldType =
  | 'string'
  | 'number'
  | 'integer'
  | 'boolean'
  | 'enum'
  | 'array'
  | 'object';

export interface PluginConfigField {
  name: string;
  type: PluginConfigFieldType | string;
  enumValues: string[];
  description: string;
}

export type PluginConfigObject = Record<string, unknown>;

export interface PluginMetadata {
  name: string;
  version: string;
  author: string;
  githubRepository: string;
  logo: string;
  configFields: PluginConfigField[];
}

export interface PluginMenu {
  path: string;
  menu: string;
  description: string;
}

export interface PluginListEntry {
  id: string;
  path: string;
  configured: boolean;
  registered: boolean;
  enabled: boolean;
  effectiveEnabled: boolean;
  supportsOAuth: boolean;
  oauthProvider?: string;
  supportsQuota: boolean;
  quotaProvider?: string;
  logo: string;
  configFields: PluginConfigField[];
  menus: PluginMenu[];
  metadata: PluginMetadata | null;
}

export interface PluginListResponse {
  pluginsEnabled: boolean;
  pluginsDir: string;
  plugins: PluginListEntry[];
  environment: PluginEnvironment | null;
}

export interface PluginEnvironment {
  goos: string;
  goarch: string;
  inContainer: boolean;
  cgoPluginSupport: boolean;
  platformDir: string;
  platformMatchedFiles: number;
  skippedPlatformFiles: number;
}

export interface PluginDeleteResult {
  status: string;
  id: string;
  path: string;
  fileDeleted: boolean;
  configuredRemoved: boolean;
  restartRequired: boolean;
}
