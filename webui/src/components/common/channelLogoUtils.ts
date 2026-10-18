const CHANNEL_LOGO_ALIASES: Record<string, string> = {
  'agnes-ai': 'agnes',
  aistudio: 'gemini',
  'anthropic-claude': 'claude',
  chatgpt: 'openai',
  'claude-code': 'claude',
  'firecrawl-dev': 'firecrawl',
  'gemini-cli': 'gemini',
  'gemini-interactions': 'gemini',
  'google-ai-studio': 'gemini',
  'google-gemini': 'gemini',
  grok: 'xai',
  'moonshot-ai': 'moonshot',
  'openai-compat': 'openai',
  'openai-compatible': 'openai',
  'openai-compatibility': 'openai',
  'vertex-ai': 'vertexai',
  'x-ai': 'xai',
  'z-ai': 'zhipu',
  zhipuai: 'zhipu',
};

/** Normalize provider/channel identifiers before looking up a brand logo. */
export function channelLogoKey(value: string | null | undefined): string {
  if (!value) return '';
  const normalized = value
    .trim()
    .toLowerCase()
    .replace(/[\s_]+/g, '-')
    .replace(/-+/g, '-');
  return CHANNEL_LOGO_ALIASES[normalized] ?? normalized;
}

/** Stable hue for the fallback tile so the same provider keeps the same color. */
export function channelLogoHue(value: string): number {
  let hash = 0;
  for (const character of value) {
    hash = (hash * 31 + character.charCodeAt(0)) % 360;
  }
  return hash;
}

/** Single-character fallback used when no brand logo is available. */
export function channelLogoInitial(value: string): string {
  const alphanumeric = value.replace(/[^a-z0-9]/gi, '');
  return (alphanumeric[0] || '?').toUpperCase();
}
