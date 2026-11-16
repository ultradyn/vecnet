import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));

export function shotsDirectory(env = process.env) {
  return resolve(root, env.SHOTS_DIR || '.scratch/evidence/ui');
}
