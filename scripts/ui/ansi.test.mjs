import { test } from 'node:test';
import assert from 'node:assert/strict';
import { ansiHTML } from './ansi.mjs';

test('hostile terminal text stays inert', () => {
  const html = ansiHTML('<img src=x onerror=alert(1)>\x1b]52;c;aGk=\x07\x1b[2Jpwn\u202e');
  assert.ok(html.includes('&lt;img src=x onerror=alert(1)&gt;'));
  assert.ok(html.includes('pwn'));
  assert.ok(!html.includes('<img'));
  assert.ok(!html.includes('aGk='));
  assert.ok(!/[\x1b\u202e]/.test(html));
});

test('SGR styles include normal, indexed and true colour and reset', () => {
  const html = ansiHTML('\x1b[1;2;4;7mA\x1b[22;27;39;49mB\x1b[38;5;208mC\x1b[48;2;1;2;3mD\x1b[0mE');
  assert.match(html, /font-weight:700;opacity:0.6;text-decoration:underline">A/);
  assert.match(html, /color:#f3f4f6;background:#0b0f19;font-weight:400;opacity:1;text-decoration:underline">B/);
  assert.match(html, /color:rgb\(255,135,0\).*?>C/);
  assert.match(html, /background:rgb\(1,2,3\).*?>D/);
  assert.match(html, /color:#f3f4f6;background:#0b0f19;font-weight:400;opacity:1;text-decoration:none">E/);
});

test('a light page swaps the default colours, and reverse follows them', () => {
  const html = ansiHTML('A\x1b[7mB\x1b[0m', { light: true });
  assert.match(html, /background:#ffffff;color:#111827;color-scheme:light/);
  assert.match(html, /color:#111827;background:#ffffff;[^>]*>A/);
  assert.match(html, /color:#ffffff;background:#111827;[^>]*>B/);
});
