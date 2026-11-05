import test from 'node:test';
import assert from 'node:assert/strict';
import { planInput } from '../src/input-plan.ts';
import type { SessionState } from '../src/backend.ts';
test('planInput table: idle/parked act as idle; turn_running/waiting_approval/starting act as running; launching buffers; ended refuses', () => {
  const states: Array<SessionState['kind'] | 'launching'> = ['idle','parked','turn_running','waiting_approval','starting','launching','ended'];
  for (const steer of [false,true]) for (const queue of ['native','launcher'] as const) for (const s of states) {
    const queued = queue === 'native' ? 'queue-native' : 'queue-launcher';
    const expected = s === 'ended' ? ['refuse','refuse','refuse'] : s === 'launching' ? ['buffer','buffer','buffer'] : ['idle','parked'].includes(s) ? ['start','start','buffer'] : [steer ? 'steer' : queued,queued,'buffer'];
    assert.deepEqual(['wake','wake_idle','hold'].map(p => planInput(p as 'wake'|'wake_idle'|'hold',s,{steer,queue})),expected,`${s}/${steer}/${queue}`);
  }
});
