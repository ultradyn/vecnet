import test from 'node:test';
import assert from 'node:assert/strict';
import { SseParser } from '../src/sse.ts';
const bytes = (s: string) => new TextEncoder().encode(s);
const parse = (s: string) => new SseParser().push(bytes(s));

test('LF, CR and CRLF line endings all parse', () => {
  for (const nl of ['\n', '\r', '\r\n']) assert.deepEqual(parse(`id: e1.1${nl}data: hello${nl}${nl}`), [{kind:'event',id:'e1.1',event:'message',data:'hello'}]);
});
test('CRLF split across two chunks is one line ending', () => {
  const p = new SseParser();
  assert.deepEqual(p.push(bytes('data: one\r')), []);
  assert.deepEqual(p.push(bytes('\ndata: two\r')), []);
  assert.deepEqual(p.push(bytes('\n\r\n')), [{kind:'event',event:'message',data:'one\ntwo'}]);
});
test('a multi-byte UTF-8 char split across chunks survives', () => {
  const p = new SseParser(), b = bytes('data: é\n\n');
  assert.deepEqual(p.push(b.subarray(0,7)), []);
  assert.deepEqual(p.push(b.subarray(7)), [{kind:'event',event:'message',data:'é'}]);
});
test('multi-line data joins with newline', () => assert.deepEqual(parse('event: delivery\ndata: a\ndata:b\ndata:  c\n\n'), [{kind:'event',event:'delivery',data:'a\nb\n c'}]));
test('priming block with id and empty data is emitted', () => assert.deepEqual(parse('id: e1.5\ndata:\n\n'), [{kind:'event',id:'e1.5',event:'message',data:''}]));
test('empty id line resets; id with NULL is ignored', () => {
  assert.deepEqual(parse('id:\ndata: a\n\nid: good\nid: bad\0id\ndata: b\n\n'), [{kind:'event',id:'',event:'message',data:'a'},{kind:'event',id:'good',event:'message',data:'b'}]);
  assert.deepEqual(parse('id: bad\0id\ndata: c\n\n'), [{kind:'event',event:'message',data:'c'}]);
});
test('comment line emits a comment frame', () => assert.deepEqual(parse(': hb\n\n'), [{kind:'comment'}]));
test('retry field is parsed; non-digit retry is ignored', () => {
  assert.deepEqual(parse('retry: 1234\n\nretry: -1\ndata: a\n\nretry: 1x\ndata: b\n\n'), [{kind:'event',event:'message',data:'',retryMs:1234},{kind:'event',event:'message',data:'a'},{kind:'event',event:'message',data:'b'}]);
});
test('leading BOM is stripped', () => assert.deepEqual(parse('\uFEFFdata: ok\n\n'), [{kind:'event',event:'message',data:'ok'}]));
test('an unterminated block is dropped by end()', () => {
  const p = new SseParser();
  assert.deepEqual(p.push(bytes('data: lost\n')), []);
  assert.deepEqual(p.end(), []);
});
