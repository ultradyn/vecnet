import { test } from 'node:test';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync, readFileSync, existsSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { acquireGuard, ownerArgs } from '../src/codex/ownership.ts';
import type { Owner } from '../src/codex/ownership.ts';

for (const outcome of ['accepted', 'rejected', 'ambiguous', 'lost'] as const) {
  test(`guard ${outcome} preserves the correct durable recovery state`, async t => {
    const dir = mkdtempSync(join(tmpdir(), 'vn-owner-'));
    t.after(() => rmSync(dir, { recursive: true, force: true }));
    const config = join(dir, 'config'), bin = join(dir, 'vecnet'), claims = join(dir, 'claims'), trace = join(dir, 'trace');
    const owner: Owner = { configPath: config, profile: 'p', as: '', hub: 'https://hub.invalid', caller: 'peer',
      codexHome: dir, threadId: '11111111-1111-4111-8111-111111111111', generation: 'a'.repeat(32) };
    const claim = 'a'.repeat(24), attempt = 'b'.repeat(32);
    writeFileSync(config, JSON.stringify({ trace, claims, owner, guardFinishFailure: outcome === 'lost' }));
    writeFileSync(claims, JSON.stringify({ [claim]: ['delivery'] }));
    writeFileSync(bin, `#!/bin/sh\nexport FAKE_VECNET_PLAN="$VECNET_CONFIG"\nexec '${process.execPath}' '${fileURLToPath(new URL('./fake-vecnet.ts', import.meta.url))}' "$@"\n`, { mode: 0o700 });
    const env = { HOME: dir, VECNET_CONFIG: config };
    const guard = await acquireGuard(bin, env, owner, claim, attempt);
    assert.equal(guard.valid(), true);
    assert.deepEqual(JSON.parse(readFileSync(trace + '.pending', 'utf8')), { owner, claim, attempt });
    if (outcome === 'lost') await assert.rejects(guard.finish('accepted'), /ownership_guard_lost/);
    else await guard.finish(outcome);
    assert.equal(guard.valid(), false);
    assert.equal(existsSync(trace + '.pending'), outcome === 'ambiguous' || outcome === 'lost');
    if (outcome !== 'rejected') await assert.rejects(acquireGuard(bin, env, owner, claim, attempt), /ownership_guard_refused/);
    if (outcome === 'ambiguous' || outcome === 'lost') {
      assert.throws(() => execFileSync(bin, ['--net', 'p', 'codex', 'settle', ...ownerArgs(owner), '--claim', claim,
        '--attempt', 'c'.repeat(32), '--outcome', 'rejected'], { env, stdio: 'pipe' }));
      assert.equal(existsSync(trace + '.pending'), true, 'stale attempt cannot remove a newer ambiguity marker');
      execFileSync(bin, ['--net', 'p', 'codex', 'settle', ...ownerArgs(owner), '--claim', claim, '--attempt', attempt, '--outcome', 'accepted'], { env });
      assert.equal(existsSync(trace + '.pending'), false);
    }
    if (outcome !== 'rejected') assert.equal(JSON.parse(readFileSync(claims + '.accepted', 'utf8'))[claim], true);
    else {
      const retry = await acquireGuard(bin, env, owner, claim, attempt);
      await retry.finish('rejected');
    }
  });
}
