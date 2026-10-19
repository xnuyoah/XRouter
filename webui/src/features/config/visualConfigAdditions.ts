import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import type { VisualConfigValues, VisualConfigValidationErrors } from '@/types/visualConfig';
import { readConfigBoolean } from './visualConfigBoolean';

// Source: backend config_v8.go/config.go. Only fields with real backend
// Config mappings are listed here.
export const ADDITION_FIELDS = [
  {
    key: 'routingSessionAffinitySubagents',
    path: 'routing.session-affinity-subagents'.split('.'),
    kind: 'boolean',
  },
  {
    key: 'saveCooldownStatus',
    path: 'routing.cooldown.save-cooldown-status'.split('.'),
    kind: 'boolean',
  },
  {
    key: 'transientErrorCooldownSeconds',
    path: 'routing.cooldown.transient-error-cooldown-seconds'.split('.'),
    kind: 'integer',
  },
] as const;

type Doc = import('yaml').Document;

export function readVisualAdditions(doc: Doc) {
  const values = {} as Pick<VisualConfigValues, (typeof ADDITION_FIELDS)[number]['key']>;
  for (const { key, path, kind } of ADDITION_FIELDS) {
    const raw = doc.getIn(path);
    Object.assign(values, {
      [key]:
        kind === 'boolean' ? readConfigBoolean(raw, DEFAULT_VISUAL_VALUES[key]) : String(raw ?? ''),
    });
  }
  return values;
}


export function writeVisualAdditions(
  doc: Doc,
  values: VisualConfigValues,
  dirty: ReadonlySet<string>
) {
  for (const { key, path, kind } of ADDITION_FIELDS) {
    if (!dirty.has(key)) continue;
    const value = values[key];
    if (typeof value === 'string' && !value.trim()) {
      doc.deleteIn(path);
      continue;
    }
    if (
      kind === 'integer' &&
      (!/^-?\d+$/.test(String(value).trim()) || !Number.isSafeInteger(Number(value)))
    )
      continue;
    doc.setIn(path, kind === 'integer' ? Number(value) : value);
  }
}

export function validateVisualAdditions(
  values: VisualConfigValues,
  dirtyFields?: ReadonlySet<string>
): VisualConfigValidationErrors {
  void dirtyFields;
  const errors: VisualConfigValidationErrors = {
    transientErrorCooldownSeconds: undefined,
  };
  const value = String(values.transientErrorCooldownSeconds).trim();
  if (value) {
    const signed = true;
    if (!/^-?\d+$/.test(value) || !Number.isSafeInteger(Number(value))) {
      Object.assign(errors, { transientErrorCooldownSeconds: signed ? 'integer' : 'non_negative_integer' });
    }
  }
  return errors;
}
