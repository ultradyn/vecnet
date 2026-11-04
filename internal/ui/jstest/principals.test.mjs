import test from 'node:test';
import assert from 'node:assert/strict';
import { principalRows } from '../static/principals-model.js';

// prn is one GET /v1/admin/principals entry (opapi.PrincipalRow, §18.4).
function prn(over = {}) {
  return {
    principal_id: 'prn_x', name: 'x', max_peers: 0, creates_per_hour: 60, kinds: [],
    active: 0, disabled: 0, creates_last_hour: 0, scoped_peers: 0, default_allow: [], default_deny: [],
    ...over,
  };
}

test('principalRows: unlimited principals with no created peers are left out', () => {
  const rows = principalRows([
    prn({ principal_id: 'prn_none', name: 'household' }),
    prn({ principal_id: 'prn_lim', name: 'pagchat', max_peers: 3 }),
    prn({ principal_id: 'prn_old', name: 'retired', active: 0, disabled: 2 }),
    prn({ principal_id: 'prn_act', name: 'lowered', active: 1 }),
  ]);
  assert.deepEqual(rows.map(r => r.id).sort(), ['prn_act', 'prn_lim', 'prn_old']);
  assert.deepEqual(principalRows([prn(), prn({ principal_id: 'prn_2' })]), []);
  assert.deepEqual(principalRows(undefined), []);
  assert.deepEqual(principalRows(null), []);
});

test('principalRows: sorted by name', () => {
  const rows = principalRows([
    prn({ principal_id: 'prn_c', name: 'zeta', max_peers: 1 }),
    prn({ principal_id: 'prn_a', name: 'alpha', max_peers: 1 }),
    prn({ principal_id: 'prn_b', name: 'mid', max_peers: 1 }),
  ]);
  assert.deepEqual(rows.map(r => r.name), ['alpha', 'mid', 'zeta']);
});

test('principalRows: fields, full at the cap, scopes formatted', () => {
  const [atCap, under] = principalRows([
    prn({ principal_id: 'prn_full', name: 'a-full', max_peers: 2, active: 2, disabled: 1, creates_last_hour: 3, creates_per_hour: 10, kinds: ['pagchat-bot', 'launcher'] }),
    prn({ principal_id: 'prn_under', name: 'b-under', max_peers: 5, active: 4, default_allow: ['peer_a', 'team:x'], default_deny: ['peer_b'] }),
  ]);
  assert.deepEqual(atCap, {
    id: 'prn_full', name: 'a-full', active: 2, max: 2, disabled: 1, createsLastHour: 3, createsPerHour: 10,
    kinds: ['pagchat-bot', 'launcher'], scopes: 'none', unscopedRisk: false, full: true,
  });
  assert.equal(under.full, false);
  assert.equal(under.scopes, 'allow 2 · deny 1');
  assert.equal(under.unscopedRisk, false);
  // Over the cap (the operator lowered max_peers) is full too.
  assert.equal(principalRows([prn({ max_peers: 1, active: 3 })])[0].full, true);
  // One side of the defaults set is still scoped.
  assert.equal(principalRows([prn({ max_peers: 1, default_deny: ['peer_z'] })])[0].scopes, 'allow 0 · deny 1');
});

test('principalRows: unscopedRisk only when defaults are empty and other peers are scoped', () => {
  const rows = principalRows([
    prn({ principal_id: 'prn_risk', name: 'a', max_peers: 2, scoped_peers: 1 }),
    prn({ principal_id: 'prn_ok', name: 'b', max_peers: 2, scoped_peers: 0 }),
    prn({ principal_id: 'prn_set', name: 'c', max_peers: 2, scoped_peers: 4, default_allow: ['peer_a'] }),
  ]);
  const by = Object.fromEntries(rows.map(r => [r.id, r]));
  assert.equal(by.prn_risk.scopes, 'none');
  assert.equal(by.prn_risk.unscopedRisk, true);
  assert.equal(by.prn_ok.scopes, 'none');
  assert.equal(by.prn_ok.unscopedRisk, false);
  assert.equal(by.prn_set.unscopedRisk, false);
});

test('principalRows: a bidi-override name is cleaned', () => {
  const [row] = principalRows([prn({ max_peers: 1, name: 'evil‮gnp.exe⁦\u0007' })]);
  assert.equal(row.name, 'evilgnp.exe');
  assert.doesNotMatch(row.name, /[‪-‮⁦-⁩\u0000-\u001f]/);
});
