#!/usr/bin/env node
import { readFileSync, writeFileSync, appendFileSync, existsSync, unlinkSync } from 'node:fs';

// Every path comes from the test's private environment; no home defaults.
const file = process.env.FAKE_VECNET_PLAN;
if (!file) throw new Error('fake vecnet requires a plan');
const read = () => JSON.parse(readFileSync(file, 'utf8'));
const plan = read(), argv = process.argv.slice(2);
const acknowledge = (data: object) => appendFileSync(plan.trace, JSON.stringify({ ...data, ack: true }) + '\n');
appendFileSync(plan.trace, JSON.stringify({ argv, env: process.env }) + '\n');
const flag = (s: string) => argv[argv.indexOf(s) + 1];
const waitGate = async (gate: string | undefined) => {
  if (!gate) return;
  await new Promise<void>(resolve => {
    const timer = setInterval(() => { if (existsSync(gate)) { clearInterval(timer); resolve(); } }, 10);
  });
};
// Response controls model the approved Go interface, including silent refusal
// and committed-response loss. They never supply an upstream receipt to Codex.
const rawResponse = (mode: string | undefined): string | undefined => ({
  'capacity-refusal': '', empty: '', truncated: '{"released":', malformed: '{',
  object: '{}', null: 'null', array: '[]', 'boolean-count': '{"released":true}',
  'string-count': '{"released":"1"}', 'negative-count': '{"released":-1}',
  'fractional-count': '{"released":0.5}', 'numeric-zero': '{"released":0}',
  'refused-count': '{"released":0,"refused":"capacity"}',
} as Record<string, string>)[mode ?? ''];
if (argv.includes('watch')) {
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
  let displayed: string[] = [];
  try { displayed = JSON.parse(readFileSync(plan.claims + '.displayed', 'utf8')); } catch {}
  const nonce = flag('--claim');
  if (argv.includes('--receipt-key')) {
    await waitGate(plan.receiptGate);
    if (plan.receiptFailure === 'exit') process.exit(1);
    const deliveries = flag('--delivery-ids') === '' ? [] : flag('--delivery-ids').split(',');
    const released = claims[nonce]?.length ?? 0;
    delete claims[nonce]; writeFileSync(plan.claims, JSON.stringify(claims));
    writeFileSync(plan.claims + '.displayed', JSON.stringify([...new Set([...displayed, ...deliveries])]));
    acknowledge({ receiptCommitted: flag('--receipt-key'), nonce, deliveries });
    if (plan.receiptFailure === 'commit-output-lost') process.exit(0);
    const response: Record<string, unknown> = { version: 1, receipt_key: flag('--receipt-key'), deliveries, released, lifecycle: 'receipt-display-v1' };
    switch (plan.receiptResponse) {
      case 'missing-marker': delete response.lifecycle; break;
      case 'wrong-marker': response.lifecycle = 'old'; break;
      case 'missing-key': delete response.receipt_key; break;
      case 'wrong-key': response.receipt_key = 'vn.wrong'; break;
      case 'missing-ids': delete response.deliveries; break;
      case 'subset': response.deliveries = deliveries.slice(1); break;
      case 'superset': response.deliveries = [...deliveries, 'extra']; break;
      case 'duplicate': response.deliveries = [...deliveries, deliveries[0]]; break;
      case 'wrong-id-type': response.deliveries = [1]; break;
      case 'refused': response.refused = 'identity_changed'; break;
      case 'reverse': response.deliveries = [...deliveries].reverse(); break;
      case 'wrong-version': response.version = 2; break;
    }
    const raw = rawResponse(plan.receiptResponse);
    process.stdout.write(raw === undefined ? JSON.stringify(response) + '\n' : raw);
  } else if (argv.includes('--release')) {
    await waitGate(plan.releaseGate);
    if (plan.releaseFailure) process.exit(1);
    const raw = rawResponse(plan.releaseResponse);
    if (raw !== undefined && plan.releaseResponse !== 'numeric-zero') process.stdout.write(raw);
    else {
      const released = claims[nonce]?.length ?? 0;
      delete claims[nonce]; writeFileSync(plan.claims, JSON.stringify(claims));
      console.log(JSON.stringify({ released: plan.releaseResponse === 'numeric-zero' ? 0 : released }));
    }
  } else if (flag('--event') === 'user-prompt') {
    const claimed = new Set([...Object.values(claims).flat(), ...displayed]);
    console.log(JSON.stringify({ deliveries: (plan.items ?? []).filter((i: { id: string }) => !claimed.has(i.id)).map((i: { id: string }) => i.id) }));
  } else if (plan.refused) {
    console.log(JSON.stringify({ version: 1, refused: plan.refused, deliveries: [] }));
  } else {
    const priorities = flag('--priorities').split(',').filter(p => p !== 'hold');
    const claimed = new Set([...Object.values(claims).flat(), ...displayed]);
    const items = (plan.items ?? []).filter((i: { id: string; priority: string }) => !claimed.has(i.id) && priorities.includes(i.priority)).slice(0, 8);
    const summary = plan.summary && !Object.keys(claims).length && !existsSync(plan.claims + '.summary');
    if (summary) writeFileSync(plan.claims + '.summary', nonce);
    const deliveries = items.map((i: { id: string }) => i.id);
    if (deliveries.length || summary) claims[nonce] = deliveries;
    writeFileSync(plan.claims, JSON.stringify(claims));
    acknowledge({ hookClaimed: nonce, hookDeliveries: deliveries });
    if (plan.hookFailure === 'exit') process.exit(1);
    if (plan.hookFailure === 'invalid') { console.log('{'); process.exit(0); }
    await waitGate(plan.hookGate);
    console.log(JSON.stringify({ version: 1, stub: plan.stub, pointer: false,
      ...(plan.claimLifecycle === 'missing' ? {} : { lifecycle: plan.claimLifecycle === 'wrong' ? 'old' : 'receipt-display-v1' }),
      deliveries, entries: summary ? [{ key: 'vecnet_summary', value: plan.summary }] : items.map((i: { id: string; value?: string }) => ({ key: 'vecnet_' + i.id, value: i.value ?? '<vecnet-inbox untrusted="true">peer</vecnet-inbox>' })),
      more: 0 }));
  }
  acknowledge({ hook: argv, nonce, released: argv.includes('--release') });
} else process.exitCode = 2;
