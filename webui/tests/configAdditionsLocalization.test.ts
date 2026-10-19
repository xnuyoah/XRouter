import { describe, expect, test } from 'bun:test';
import { CONFIG_FIELD_SEARCH_INDEX } from '@/features/config/searchIndex';

const addedFieldIds = [
  'routingSessionAffinitySubagents',
  'saveCooldownStatus',
  'transientErrorCooldownSeconds',
];

function resolveKey(root: unknown, path: string): unknown {
  return path.split('.').reduce<unknown>((node, key) => {
    if (node && typeof node === 'object') return (node as Record<string, unknown>)[key];
    return undefined;
  }, root);
}

describe('added configuration field localization', () => {
  for (const locale of ['en', 'zh-CN']) {
    test(`${locale} includes searchable labels and help`, async () => {
      const messages: unknown = await Bun.file(`src/i18n/locales/${locale}.json`).json();
      for (const fieldId of addedFieldIds) {
        const entry = CONFIG_FIELD_SEARCH_INDEX.find((item) => item.fieldId === fieldId);
        expect(entry).toBeDefined();
        expect(entry?.labelKey).toBe(`config_management.visual.additions.${fieldId}.label`);
        expect(entry?.hintKey).toBe(`config_management.visual.additions.${fieldId}.hint`);
        expect(resolveKey(messages, entry!.labelKey)).toBeString();
        expect(resolveKey(messages, entry!.hintKey!)).toBeString();
      }
      for (const fieldId of addedFieldIds) {
        const group = resolveKey(messages, `config_management.visual.additions.${fieldId}`);
        expect(group).toBeTypeOf('object');
        expect(resolveKey(group, 'label')).toBeString();
        expect(resolveKey(group, 'hint')).toBeString();
      }
    });
  }
});
