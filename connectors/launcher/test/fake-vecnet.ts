#!/usr/bin/env node
import { readFileSync, writeFileSync, appendFileSync, existsSync, unlinkSync, mkdirSync, rmdirSync } from 'node:fs';

import { createInterface } from 'node:readline';
import { setTimeout as delay } from 'node:timers/promises';

// Every path comes from the test's private environment; no home defaults.
const file = process.env.FAKE_VECNET_PLAN;
if (!file) throw new Error('fake vecnet requires a plan');
const read = () => JSON.parse(readFileSync(file, 'utf8'));
const plan = read(), argv = process.argv.slice(2);
const acknowledge = (data: object) => appendFileSync(plan.trace, JSON.stringify({ ...data, ack: true }) + '\n');
appendFileSync(plan.trace, JSON.stringify({ argv, env: process.env }) + '\n');
const flag = (s: string) => argv[argv.indexOf(s) + 1];
const marker = plan.trace + '.pending';
const lock = plan.trace + '.lock';
const acceptedPath = plan.claims + '.accepted';
const accepted = (): Record<string, boolean> => existsSync(acceptedPath) ? JSON.parse(readFileSync(acceptedPath, 'utf8')) : {};
const accept = (claim: string) => writeFileSync(acceptedPath, JSON.stringify({ ...accepted(), [claim]: true }));
const matchesOwner = () => {
  const o = read().owner;
  return o && o.configPath === process.env.VECNET_CONFIG && o.profile === flag('--net') &&
    (o.as ?? '') === (argv.includes('--as') ? flag('--as') : '') &&
    o.threadId === flag('--thread') && o.codexHome === flag('--codex-home') &&
    o.generation === flag('--expect-generation') && /^[a-f0-9]{32}$/.test(o.generation) &&
    o.hub === flag('--expect-hub') && o.caller === flag('--expect-caller');
};
if (argv.includes('codex') && argv.includes('verify')) {
  if (!matchesOwner() || plan.verifyFailure) process.exitCode = 1;
  else console.log(JSON.stringify({ verified: true }));
} else if (argv.includes('codex') && (argv.includes('guard') || argv.includes('settle'))) {
  // Serialize independently spawned CLI processes, as the Go binding lock does.
  while (true) {
    try { mkdirSync(lock); break; } catch (e) { if ((e as NodeJS.ErrnoException).code !== 'EEXIST') throw e; await delay(2); }
  }
  try {
    if (!matchesOwner() || !/^[a-f0-9]{32}$/.test(flag('--attempt'))) process.exitCode = 1;
    else if (argv.includes('settle')) {
      if (!['accepted', 'rejected'].includes(flag('--outcome'))) process.exitCode = 1;
      else if (read().settleFailure) process.exitCode = 1;
      else if (existsSync(marker)) {
        const pending = JSON.parse(readFileSync(marker, 'utf8'));
        if (pending.claim !== flag('--claim') || pending.owner.generation !== read().owner.generation || pending.attempt !== flag('--attempt')) process.exitCode = 1;
        else { if (flag('--outcome') === 'accepted') accept(flag('--claim')); unlinkSync(marker); }
      } else { /* Missing marker cannot accept another attempt's claim. */ }
    } else if (accepted()[flag('--claim')] || existsSync(marker) || !(JSON.parse(readFileSync(plan.claims, 'utf8'))[flag('--claim')]?.length)) process.exitCode = 1;
    else {
      const claim = flag('--claim');
      writeFileSync(marker, JSON.stringify({ owner: read().owner, claim, attempt: flag('--attempt') }));
      const reader = createInterface({ input: process.stdin });
      const outcome = new Promise<string>(resolve => { reader.once('line', resolve); reader.once('close', () => resolve('')); });
      acknowledge({ guardReady: claim });
      console.log(JSON.stringify({ ready: true }));
      const result = await outcome;
      reader.close();
      if (read().guardFinishFailure) process.exitCode = 1;
      else {
        if (result === 'accepted') accept(claim);
        if (result === 'accepted' || result === 'rejected') unlinkSync(marker);
      }
      acknowledge({ guardFinished: claim, outcome: result });
    }
  } finally { rmdirSync(lock); }
} else if (argv.includes('watch')) {
  let seen = 0, revision = -1, ending = false;
  const tick = () => {
    if (ending) return;
    const p = read();
    // A one-shot crash: a recoverable process failure with no stop line.
    if (p.watchCrash && existsSync(p.watchCrash)) { ending = true; unlinkSync(p.watchCrash); process.exit(1); }
    // A terminal hub answer (401/403/204): the Go watch's last line, then exit 2.
    if (p.watchStop) {
      ending = true;
      process.stdout.write(JSON.stringify({ type: 'watch_stopped', cursor: '', at: '', data: { text: 'not reconnecting: hub answered HTTP ' + p.watchStop, status: p.watchStop } }) + '\n', () => process.exit(2));
      return;
    }
    for (const e of (p.events ?? []).slice(seen)) process.stdout.write(JSON.stringify(e) + '\n');
    seen = (p.events ?? []).length;
    if (revision !== p.revision) {
      revision = p.revision;
      const published = revision;
      process.stdout.write('', () => acknowledge({ watchRevision: published }));
    }
  };
  tick(); setInterval(tick, 10);
} else if (argv.includes('hook')) {
  let claims: Record<string, string[]> = {};
  try { claims = JSON.parse(readFileSync(plan.claims, 'utf8')); } catch {}
  const nonce = flag('--claim');
  if (flag('--event') !== 'user-prompt' && !matchesOwner()) process.exit(1);
  if (argv.includes('--release')) {
    if (plan.releaseFailure) process.exit(1);
    if (plan.releaseRefused) { console.log(JSON.stringify({ refused: 'ownership_changed' })); process.exit(0); }
    const released = !accepted()[nonce] && !existsSync(marker) ? (claims[nonce]?.length ?? 0) : 0;
    if (released) delete claims[nonce];
    writeFileSync(plan.claims, JSON.stringify(claims));
    console.log(JSON.stringify({ version: 1, released, remaining: claims[nonce]?.length ?? 0 }));
  } else if (flag('--event') === 'user-prompt') {
    const claimed = new Set(Object.values(claims).flat());
    console.log(JSON.stringify({ deliveries: (plan.items ?? []).filter((i: { id: string }) => !claimed.has(i.id)).map((i: { id: string }) => i.id) }));
  } else if (plan.refused) {
    console.log(JSON.stringify({ version: 1, refused: plan.refused, deliveries: [] }));
  } else {
    const priorities = flag('--priorities').split(',').filter(p => p !== 'hold');
    const claimed = new Set(Object.values(claims).flat());
    const items = (plan.items ?? []).filter((i: { id: string; priority: string }) => !claimed.has(i.id) && priorities.includes(i.priority)).slice(0, 8);
    claims[nonce] = items.map((i: { id: string }) => i.id);
    writeFileSync(plan.claims, JSON.stringify(claims));
    acknowledge({ hookClaimed: nonce });
    if (plan.hookFailure === 'exit') process.exit(1);
    if (plan.hookFailure === 'invalid') { console.log('{'); process.exit(0); }
    if (plan.hookGate) await new Promise<void>(resolve => {
      const gate = setInterval(() => { if (existsSync(plan.hookGate)) { clearInterval(gate); resolve(); } }, 10);
    });
    if (items.length && plan.handoffAfterClaim) {
      const changed = read(); changed.owner = { ...changed.owner, ...plan.handoffAfterClaim };
      writeFileSync(file, JSON.stringify(changed));
    }
    console.log(JSON.stringify({ version: 1, stub: plan.stub, pointer: false,
      deliveries: claims[nonce], entries: plan.summaryOnly && !items.length ? [{ key: 'vecnet_summary', value: 'backlog' }] : items.map((i: { id: string; value?: string }) => ({ key: 'vecnet_' + i.id, value: i.value ?? '<vecnet-inbox untrusted="true">peer</vecnet-inbox>' })),
      more: 0 }));
  }
  acknowledge({ hook: argv, nonce, released: argv.includes('--release') });
} else process.exitCode = 2;
