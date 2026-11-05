// The fake backend as a plugin module for end-to-end tests. The daemon loads
// it by module path (loadBackends); the test imports the same module URL, so
// both share this registry and the test can drive the instance the daemon made.
import { definePlugin } from '../../src/backend.ts';
import { createFakeBackend } from '../fake-backend.ts';
import type { FakeBackend, FakeBackendOptions } from '../fake-backend.ts';

const created = new Map<string, FakeBackend>();
const next = new Map<string, (b: FakeBackend) => void>();

/** The fake most recently created for a state file. */
export function fakeFor(stateFile: string): FakeBackend {
  const backend = created.get(stateFile);
  if (!backend) throw new Error('no fake backend created for this state file');
  return backend;
}
/** Adjust the next fake created for a state file before the daemon uses it (e.g. failReattach at startup). */
export function onNextCreate(stateFile: string, fn: (b: FakeBackend) => void): void { next.set(stateFile, fn); }

export default definePlugin({
  id: 'fake', configSchema: {},
  async create(cfg: FakeBackendOptions, _ctx) {
    const backend = createFakeBackend(cfg);
    const key = cfg.stateFile ?? '';
    created.set(key, backend);
    const adjust = next.get(key);
    next.delete(key);
    adjust?.(backend);
    return backend;
  },
});
