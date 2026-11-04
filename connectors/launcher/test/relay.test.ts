import test from 'node:test';
import assert from 'node:assert/strict';
import { relayDecision, formatRelayed } from '../src/relay.ts';

test('relayDecision: requester and operator relayed; relay_from relayed; other peer not_relayed; own echo self', () => {
  const job = { requester: 'req', input: {backend:'fake',project:'repo',preset:'test',permission_mode:'ask' as const,relay_from:['extra']} };
  const operator = (id: string) => id === 'op';
  assert.deepEqual(relayDecision('req',job,'self',operator),{relay:true,role:'requester'});
  assert.deepEqual(relayDecision('op',job,'self',operator),{relay:true,role:'operator'});
  assert.deepEqual(relayDecision('extra',job,'self',operator),{relay:true,role:'relay_from'});
  assert.deepEqual(relayDecision('other',job,'self',operator),{relay:false,note:'not_relayed'});
  assert.deepEqual(relayDecision('self',job,'self',() => true),{relay:false,note:'self'});
});
test('formatRelayed quotes lines, strips controls from names, prefixes operator, lists unforwarded attachments', () => {
  const item = {role:'requester' as const,name:'A\n\r\t\x1b\x7f\u202eB',peerId:'peer_req',messageId:'msg_one',text:'hello\nworld\n',attachments:[{name:'file.txt',sha256:'1234567890abcdef',size:1,media_type:'text/plain'}]};
  assert.equal(formatRelayed(item),'[vecnet] from AB (peer_req), msg_one:\n> hello\n> world\n> \n[attachments not forwarded: file.txt (1234567890ab)]');
  assert.equal(formatRelayed({...item,role:'operator',attachments:[]}).split('\n')[0],'[vecnet] From: operator AB (peer_req), msg_one:');
});
