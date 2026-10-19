/** Client access keys are a direct config list, not upstream provider groups. */
import { getConfigValue } from './configValue';

const PATH = '/config/access/api-keys';

export const apiKeysApi = {
  async list(): Promise<string[]> {
    const data = await getConfigValue<unknown>(PATH, []);
    return Array.isArray(data) ? data.map((key) => String(key)) : [];
  },
};
