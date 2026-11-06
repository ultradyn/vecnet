#!/usr/bin/env node
import { readFileSync, writeFileSync, appendFileSync } from 'node:fs';

// Every path comes from the test's private environment; no home defaults.
const file = process.env.FAKE_VECNET_PLAN;
if (!file) throw new Error('fake vecnet requires a plan');
const read = () => JSON.parse(readFileSync(file, 'utf8'));
const plan = read(), argv = process.argv.slice(2);
appendFileSync(plan.trace, JSON.stringify({ argv, env: process.env }) + '\n');
const flag = (s: string) => argv[argv.indexOf(s) + 1];
if (argv.includes('watch')) {
  let seen = 0;
  const tick = () => {
    const p = read();
    for (const e of (p.events ?? []).slice(seen)) process.stdout.write(JSON.stringify(e) + '\n');
    seen = (p.events ?? []).length;
  };
  tick(); setInterval(tick, 10);
} else if (argv.includes('hook')) {
  let claims: Record<string, string[]> = {};
  try { claims = JSON.parse(readFileSync(plan.claims, 'utf8')); } catch {}
  const nonce = flag('--claim');
  if (argv.includes('--release')) {
    delete claims[nonce]; writeFileSync(plan.claims, JSON.stringify(claims));
    console.log(JSON.stringify({ version: 1, released: true }));
  } else if (plan.refused) {
    console.log(JSON.stringify({ version: 1, refused: plan.refused, deliveries: [] }));
  } else {
    const priorities = flag('--priorities').split(',').filter(p => p !== 'hold');
    const claimed = new Set(Object.values(claims).flat());
    const items = (plan.items ?? []).filter((i: { id: string; priority: string }) => !claimed.has(i.id) && priorities.includes(i.priority)).slice(0, 8);
    claims[nonce] = items.map((i: { id: string }) => i.id);
    writeFileSync(plan.claims, JSON.stringify(claims));
    console.log(JSON.stringify({ version: 1, stub: plan.stub, pointer: false,
      deliveries: claims[nonce], entries: items.map((i: { id: string; value?: string }) => ({ key: 'vecnet_' + i.id, value: i.value ?? '<vecnet-inbox untrusted="true">peer</vecnet-inbox>' })),
      more: 0 }));
  }
} else process.exitCode = 2;
