import test from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { ID, newUlid, newApprovalId } from '../src/ids.ts';
import type { Delivery, SendRequest } from '../src/wire.ts';

const example = (name: string) => JSON.parse(readFileSync(new URL(`../../../docs/protocol/examples/${name}.json`, import.meta.url), 'utf8'));
test('examples parse into wire types and ids match the schema', () => {
  for (const name of ['02-delivery-direct', '03-delivery-group-hold']) {
    const d: Delivery = example(name);
    assert.match(d.delivery_id, ID.delivery);
    assert.match(d.recipient, ID.peer);
    assert.match(d.message.message_id, ID.message);
    assert.match(d.thread.thread_id, ID.thread);
    assert.match(d.message.thread_id, ID.thread);
    if (d.via) assert.match(d.via, ID.grp);
    assert.equal(typeof d.message.metadata, 'object');
  }
  const approval: SendRequest = example('04-approval-request');
  assert.match(approval.approval_request!.approval_id, ID.approval);
  const status: SendRequest = example('05-job-status');
  assert.equal(status.job_status!.sequence, 3);
  assert.equal(status.job_status!.state, 'succeeded');
  assert.match(status.job_status!.job_id, /^job_[0-9A-HJKMNP-TV-Z]{26}$/);
  const schema = JSON.parse(readFileSync(new URL('../../../docs/protocol/envelope.schema.json', import.meta.url), 'utf8'));
  for (const [key, schemaKey] of Object.entries({peer:'peerId', team:'groupId', grp:'groupId', thread:'threadId', message:'messageId', delivery:'deliveryId', source:'sourceId', approval:'approvalId'})) {
    const defs = schema.$defs;
    const definition = defs[schemaKey] ?? Object.values(defs).find((v: any) => v.pattern === ID[key as keyof typeof ID].source);
    assert.ok(definition, `${key} regex exists in schema`);
    assert.equal(ID[key as keyof typeof ID].source, (definition as {pattern:string}).pattern);
  }
  assert.equal(newUlid(1).length, 26);
  assert.match(newUlid(1), /^[0-9A-HJKMNP-TV-Z]{26}$/);
  assert.ok(newUlid(2) > newUlid(1));
  assert.match(newApprovalId(1), ID.approval);
  assert.equal(ID.peer.test('peer_not-a-ulid'), false);
});
