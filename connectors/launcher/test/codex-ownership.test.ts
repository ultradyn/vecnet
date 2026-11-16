import { nodeCommand } from './fixtures/platform.ts';
import { cliEnvironment, commandInvocation } from '../../hub-client/src/platform.ts';
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
    const fixtureBin = nodeCommand(bin, `process.env.FAKE_VECNET_PLAN=process.env.VECNET_CONFIG; import(${JSON.stringify(new URL('./fake-vecnet.ts', import.meta.url).href)});`);
    const env = { ...cliEnvironment(process.env), HOME: dir, VECNET_CONFIG: config };
    const exec = (args: string[]) => { const c = commandInvocation([fixtureBin, ...args], env); return execFileSync(c.file, c.args, { ...c, env, stdio: 'pipe', windowsHide: true }); };
    const guard = await acquireGuard(fixtureBin, env, owner, claim, attempt);
    assert.equal(guard.valid(), true);
    assert.deepEqual(JSON.parse(readFileSync(trace + '.pending', 'utf8')), { owner, claim, attempt });
    if (outcome === 'lost') await assert.rejects(guard.finish('accepted'), /ownership_guard_lost/);
    else await guard.finish(outcome);
    assert.equal(guard.valid(), false);
    assert.equal(existsSync(trace + '.pending'), outcome === 'ambiguous' || outcome === 'lost');
    if (outcome !== 'rejected') await assert.rejects(acquireGuard(fixtureBin, env, owner, claim, attempt), /ownership_guard_refused/);
    if (outcome === 'ambiguous' || outcome === 'lost') {
      assert.throws(() => exec(['--net', 'p', 'codex', 'settle', ...ownerArgs(owner), '--claim', claim,
        '--attempt', 'c'.repeat(32), '--outcome', 'rejected']));
      assert.equal(existsSync(trace + '.pending'), true, 'stale attempt cannot remove a newer ambiguity marker');
      exec(['--net', 'p', 'codex', 'settle', ...ownerArgs(owner), '--claim', claim, '--attempt', attempt, '--outcome', 'accepted']);
      assert.equal(existsSync(trace + '.pending'), false);
    }
    if (outcome !== 'rejected') assert.equal(JSON.parse(readFileSync(claims + '.accepted', 'utf8'))[claim], true);
    else {
      const retry = await acquireGuard(fixtureBin, env, owner, claim, attempt);
      await retry.finish('rejected');
    }
  });
}
