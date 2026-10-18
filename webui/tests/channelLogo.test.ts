import { describe, expect, test } from 'bun:test';

import {
  channelLogoHue,
  channelLogoInitial,
  channelLogoKey,
} from '@/components/common/channelLogoUtils';

describe('channel logo identity', () => {
  test('normalizes whitespace, underscores and casing', () => {
    expect(channelLogoKey('  NVIDIA ')).toBe('nvidia');
    expect(channelLogoKey('Web_Search')).toBe('web-search');
    expect(channelLogoKey('open--router')).toBe('open-router');
  });

  test('maps provider aliases to their canonical brand', () => {
    expect(channelLogoKey('agnes-ai')).toBe('agnes');
    expect(channelLogoKey('claude-code')).toBe('claude');
    expect(channelLogoKey('gemini-cli')).toBe('gemini');
    expect(channelLogoKey('gemini-interactions')).toBe('gemini');
    expect(channelLogoKey('google-gemini')).toBe('gemini');
    expect(channelLogoKey('openai-compatible')).toBe('openai');
    expect(channelLogoKey('vertex-ai')).toBe('vertexai');
    expect(channelLogoKey('x-ai')).toBe('xai');
    expect(channelLogoKey('z-ai')).toBe('zhipu');
  });

  test('keeps unknown identities untouched and empty input empty', () => {
    expect(channelLogoKey('custom-channel')).toBe('custom-channel');
    expect(channelLogoKey(undefined)).toBe('');
    expect(channelLogoKey('   ')).toBe('');
  });

  test('falls back to a stable uppercase initial', () => {
    expect(channelLogoInitial('tavily')).toBe('T');
    expect(channelLogoInitial(' web-search ')).toBe('W');
    expect(channelLogoInitial('__')).toBe('?');
  });

  test('assigns a deterministic hue in the color wheel', () => {
    const first = channelLogoHue('anysearch');
    expect(first).toBeGreaterThanOrEqual(0);
    expect(first).toBeLessThan(360);
    expect(channelLogoHue('anysearch')).toBe(first);
    expect(channelLogoHue('tinyfish')).not.toBe(first);
  });
});
