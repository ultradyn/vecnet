import { definePlugin } from '../src/backend.ts';
import { createFakeBackend } from './fake-backend.ts';
import type { FakeBackendOptions } from './fake-backend.ts';

export default definePlugin({
  id:'fake',configSchema:{},
  create:async (cfg: FakeBackendOptions, _ctx) => createFakeBackend(cfg),
});
