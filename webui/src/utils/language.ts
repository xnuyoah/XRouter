import type { Language } from '@/types';
import { STORAGE_KEY_LANGUAGE, SUPPORTED_LANGUAGES } from '@/utils/constants';

export const isSupportedLanguage = (value: string): value is Language =>
  SUPPORTED_LANGUAGES.includes(value as Language);

const parseStoredLanguage = (value: string): Language | null => {
  try {
    const parsed = JSON.parse(value);
    const candidate = parsed?.state?.language ?? parsed?.language ?? parsed;
    if (typeof candidate === 'string' && isSupportedLanguage(candidate)) {
      return candidate;
    }
  } catch {
    if (isSupportedLanguage(value)) {
      return value;
    }
  }
  return null;
};

const getStoredLanguage = (): Language | null => {
  if (typeof window === 'undefined') {
    return null;
  }
  try {
    const stored = localStorage.getItem(STORAGE_KEY_LANGUAGE);
    if (!stored) {
      return null;
    }
    return parseStoredLanguage(stored);
  } catch {
    return null;
  }
};

// 默认使用中文：用户语言解析不出中文时也回退到 zh-CN，
// 仅在用户手动切换语言后（存在存储值）才跟随其选择。
export const getInitialLanguage = (): Language => getStoredLanguage() ?? 'zh-CN';
