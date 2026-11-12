// Offline only: import assertions without entering the live smoke.
import assert from 'node:assert/strict';
import test from 'node:test';
import { setImmediate } from 'node:timers/promises';
import { assertNoDeliveries } from './codex-isolation-smoke.mjs';

const sent = { delivery: 'delivery-a', nonce: 'CANARY-a', receiptKeys: ['vn.owner-a', 'vn.owner-b'] };
const user = key => ({ type: 'userMessage', clientId: key });
const agent = text => ({ type: 'agentMessage', text });
const turn = (status, items = []) => ({ id: 'other-turn', status, items });

function poll(attempts = 3) {
  return async (what, check) => {
    for (let i = 0; i < attempts; i++) {
      const result = await check();
      if (result) return result;
    }
    throw new Error(`timeout: ${what}`);
  };
}

for (const status of ['inProgress', 'completed', 'failed', 'interrupted']) {
  test(`rejects a wrong-thread receipt with no assistant text (${status})`, async () => {
    await assert.rejects(assertNoDeliveries(['B'], [sent], {
      turns: async () => [turn(status, [user('vn.owner-a')])],
      until: poll(),
    }), /delivery-a.*thread B.*other-turn.*receipt/);
  });
}

test('rejects acceptance keyed to the wrong owner without assistant output', async () => {
  await assert.rejects(assertNoDeliveries(['neighbor'], [sent], {
    turns: async () => [turn('completed', [user('vn.owner-b')])],
    until: poll(),
  }), /delivery-a.*thread neighbor.*receipt/);
});

test('still rejects a leaked assistant canary without a receipt', async () => {
  await assert.rejects(assertNoDeliveries(['B'], [sent], {
    turns: async () => [turn('completed', [agent('FACT delivery-a CANARY-a')])],
    until: poll(),
  }), /delivery-a.*thread B.*assistant canary/);
});

test('allows unrelated receipts and assistant text in every isolated thread', async () => {
  const histories = new Map([
    ['A', [turn('completed', [user('vn.unrelated'), agent('READY')])]],
    ['B', []],
    ['neighbor', [turn('completed')]],
  ]);
  await assertNoDeliveries([...histories.keys()], [sent], {
    turns: async id => histories.get(id),
    until: poll(),
  });
});

test('does not declare isolation while another thread is still running', async () => {
  const changed = Promise.withResolvers();
  let completed = false, settled = false;
  const checking = assertNoDeliveries(['A', 'B', 'neighbor'], [sent], {
    turns: async id => [turn(id === 'neighbor' && !completed ? 'inProgress' : 'completed')],
    until: async (what, check) => {
      if (await check()) return true;
      await changed.promise;
      return poll()(what, check);
    },
  }).then(() => { settled = true; });
  try {
    await setImmediate();
    assert.equal(settled, false, 'negative check passed before the neighboring turn completed');
  } finally {
    completed = true;
    changed.resolve();
    await checking;
  }
});

test('rejects a canary that appears when a running turn completes', async () => {
  let snapshot = 0;
  await assert.rejects(assertNoDeliveries(['B'], [sent], {
    turns: async () => [snapshot++ === 0
      ? turn('inProgress')
      : turn('completed', [agent('FACT delivery-a CANARY-a')])],
    until: poll(),
  }), /delivery-a.*thread B.*assistant canary/);
});

test('rejects a receipt appearing during the wait without any assistant text', async () => {
  let snapshot = 0;
  await assert.rejects(assertNoDeliveries(['B'], [sent], {
    turns: async () => [turn('inProgress', snapshot++ === 0 ? [] : [user('vn.owner-a')])],
    until: poll(),
  }), /delivery-a.*thread B.*receipt/);
});

test('waits for running turns beyond the newest turn in another thread', async () => {
  let snapshot = 0;
  await assert.rejects(assertNoDeliveries(['A', 'B', 'neighbor'], [sent], {
    turns: async id => id !== 'B' ? [turn('completed')] : [
      { ...turn('completed'), id: 'newest-turn' },
      snapshot++ === 0 ? turn('inProgress') : turn('completed', [user('vn.owner-b')]),
    ],
    until: poll(),
  }), /delivery-a.*thread B.*other-turn.*receipt/);
});

test('fails isolation on timeout instead of passing with a still-running turn', async () => {
  await assert.rejects(assertNoDeliveries(['neighbor'], [sent], {
    turns: async () => [turn('inProgress')],
    until: poll(),
  }), /timeout: .*isolation/);
});

test('propagates a history-read failure rather than claiming isolation', async () => {
  await assert.rejects(assertNoDeliveries(['B'], [sent], {
    turns: async () => { throw new Error('history unavailable'); },
    until: poll(),
  }), /history unavailable/);
});

test('importing with --run in the caller arguments never enters live setup', async () => {
  const argv = [...process.argv], config = process.env.CODEX_ISOLATION_CONFIG;
  try {
    process.argv.push('--run');
    // Even a regressed guard must fail before importing connectors or starting
    // processes. The correct guard does not inspect live configuration at all.
    delete process.env.CODEX_ISOLATION_CONFIG;
    const smoke = await import('./codex-isolation-smoke.mjs?import-with-run');
    assert.equal(typeof smoke.assertNoDeliveries, 'function');
  } finally {
    process.argv = argv;
    if (config === undefined) delete process.env.CODEX_ISOLATION_CONFIG;
    else process.env.CODEX_ISOLATION_CONFIG = config;
  }
});
