import test from 'node:test';
import assert from 'node:assert/strict';
import { PRIORITY_HELP, ago, bytes, clean, deltaLabel, deltaTitle, fullWhen, kindLabel, kindShort, kindTitle, plural, quotaLabel, short, standInTitle, title, when } from '../static/format.js';

test('when is 24-hour local today, with the date on other days, and survives midnight', () => {
  const noon = new Date(2026, 10, 2, 14, 5, 0).getTime();
  assert.equal(when(new Date(2026, 10, 2, 14, 5, 0).toISOString(), noon), '14:05');
  assert.equal(when(new Date(2026, 10, 2, 0, 7, 0).toISOString(), noon), '00:07');
  assert.equal(when(new Date(2026, 9, 31, 9, 30, 0).toISOString(), noon), '31 Oct 09:30');
  assert.equal(when('not a time', noon), '');
  assert.equal(when(null, noon), '');
});

test('ago counts back from the supplied clock', () => {
  const now = Date.parse('2026-11-02T10:00:00.000Z');
  const back = ms => new Date(now - ms).toISOString();
  assert.equal(ago(back(0), now), 'now');
  assert.equal(ago(back(-5000), now), 'now');
  assert.equal(ago(back(42_000), now), '42s ago');
  assert.equal(ago(back(5 * 60_000), now), '5m ago');
  assert.equal(ago(back(3 * 3_600_000), now), '3h ago');
  assert.equal(ago(back(2 * 86_400_000), now), '2d ago');
  assert.equal(ago('nope', now), '');
  assert.equal(ago(null, now), '');
});

test('bytes uses binary units', () => {
  assert.equal(bytes(0), '0 B');
  assert.equal(bytes(1023), '1023 B');
  assert.equal(bytes(1536), '1.5 KiB');
  assert.equal(bytes(5 << 20), '5.0 MiB');
});

test('short keeps the type prefix and the tail', () => {
  assert.equal(short('peer_01JD3Q2K8M4N6P8R0T2V4X6Z8B'), 'peer_…X6Z8B');
  assert.equal(short('thr_1'), 'thr_1');
  assert.equal(short(''), '?');
  assert.equal(short(undefined), '?');
});

test('title prefers the thread title, then the other participants, then the id', () => {
  const nameOf = id => ({ peer_me: 'max', peer_a: 'codex', peer_b: 'grok' })[id];
  assert.equal(title({ title: 'Hosting', participants: ['peer_a'], thread_id: 'thr_1' }, 'peer_me', nameOf), 'Hosting');
  assert.equal(title({ title: '', participants: ['peer_me', 'peer_a', 'peer_b'], thread_id: 'thr_1' }, 'peer_me', nameOf), 'codex, grok');
  assert.equal(title({ title: null, participants: ['peer_me'], thread_id: 'thr_01JD3S2C4E6G8J0M2P4R6T8V0X' }, 'peer_me', nameOf), 'thr_…T8V0X');
  assert.equal(title(null, 'peer_me', nameOf), '');
  // An untitled thread takes its stand-in.
  assert.equal(title({ title: null, participants: ['peer_a', 'peer_b'], thread_id: 'thr_1' }, 'peer_me', nameOf), 'codex ↔ grok');
});

const PEOPLE = { peer_me: 'max', peer_a: 'codex-hark', peer_b: 'grok-reviewer', peer_c: 'pag-sleuth', peer_d: 'claw-ops', peer_e: 'dot-research' };
const nameOf = id => PEOPLE[id];
const thr = (...participants) => ({ thread_id: 'thr_01JD3S2C4E6G8J0M2P4R6T8V0X', title: null, participants });

test('standInTitle: two peers read A ↔ B, a group names two and counts the rest', () => {
  assert.equal(standInTitle(thr('peer_a', 'peer_b'), 'peer_me', nameOf), 'codex-hark ↔ grok-reviewer');
  assert.equal(standInTitle(thr('peer_a', 'peer_b', 'peer_c'), 'peer_me', nameOf), 'codex-hark, grok-reviewer, pag-sleuth');
  assert.equal(standInTitle(thr('peer_a', 'peer_b', 'peer_c', 'peer_d'), 'peer_me', nameOf), 'codex-hark, grok-reviewer +2');
  assert.equal(standInTitle(thr('peer_a', 'peer_b', 'peer_c', 'peer_d', 'peer_e'), 'peer_me', nameOf), 'codex-hark, grok-reviewer +3');
  // One peer alone (a thread whose creator was its only participant).
  assert.equal(standInTitle(thr('peer_a'), 'peer_me', nameOf), 'codex-hark');
});

test('standInTitle leaves the operator out: a 1:1 reads as the other peer', () => {
  assert.equal(standInTitle(thr('peer_me', 'peer_a'), 'peer_me', nameOf), 'codex-hark');
  // With the operator in, two others are a group of three, not a pair.
  assert.equal(standInTitle(thr('peer_a', 'peer_me', 'peer_b'), 'peer_me', nameOf), 'codex-hark, grok-reviewer');
  assert.equal(standInTitle(thr('peer_me', 'peer_a', 'peer_b', 'peer_c'), 'peer_me', nameOf), 'codex-hark, grok-reviewer, pag-sleuth');
  assert.equal(standInTitle(thr('peer_me', 'peer_a', 'peer_b', 'peer_c', 'peer_d'), 'peer_me', nameOf), 'codex-hark, grok-reviewer +2');
  // Only the operator: the thread's short id.
  assert.equal(standInTitle(thr('peer_me'), 'peer_me', nameOf), 'thr_…T8V0X');
  assert.equal(standInTitle(thr(), 'peer_me', nameOf), 'thr_…T8V0X');
  assert.equal(standInTitle(null, 'peer_me', nameOf), '');
});

test('standInTitle falls back to short ids and keeps peer names inert', () => {
  assert.equal(standInTitle(thr('peer_01JD3S2C4E6G8J0M2P4R6T8V0X', 'peer_a'), 'peer_me', nameOf), 'peer_…T8V0X ↔ codex-hark');
  // An empty or blank name is no name.
  assert.equal(standInTitle(thr('peer_a', 'peer_01JD3S2C4E6G8J0M2P4R6T8VZZ'), 'peer_me', id => (id === 'peer_a' ? 'codex' : ' \t ')), 'codex ↔ peer_…T8VZZ');
  // Controls and bidi overrides go; line breaks and runs of space fold to one space.
  const hostile = { peer_a: '\u202egrok\u0007 bot', peer_b: 'line\none\u2028two  three' };
  assert.equal(standInTitle(thr('peer_a', 'peer_b'), 'peer_me', id => hostile[id]), 'grok bot ↔ line one two three');
  // Markup stays text: the view sets it as textContent.
  assert.equal(standInTitle(thr('peer_a', 'peer_b'), 'peer_me', id => (id === 'peer_a' ? '<img src=x>' : 'b')), '<img src=x> ↔ b');
});

test('quotaLabel reads a *_pct value as a percentage, an empty quota as none, anything else as k=v', () => {
  assert.equal(quotaLabel({ weekly_remaining_pct: 61 }), '61%');
  assert.equal(quotaLabel({ weekly_remaining_pct: 0 }), '0%');
  assert.equal(quotaLabel({ weekly_remaining_pct: 60.6 }), '61%');
  assert.equal(quotaLabel({ weekly_remaining_pct: 3, runs_this_hour: '0/200' }), '3%');
  assert.equal(quotaLabel({ daily_used_pct: 12, burst: 4 }), '12%');
  assert.equal(quotaLabel({ a_pct: 9, weekly_remaining_pct: 40 }), '40%');
  assert.equal(quotaLabel({}), 'none');
  assert.equal(quotaLabel(null), 'none');
  assert.equal(quotaLabel(undefined), 'none');
  assert.equal(quotaLabel({ runs_this_hour: '0/200', tier: 2 }), 'runs_this_hour=0/200 tier=2');
  assert.equal(quotaLabel({ weekly_remaining_pct: 'lots' }), 'weekly_remaining_pct=lots');
});

test('clean drops controls and bidi overrides, like tui.Clean, and keeps newlines and tabs', () => {
  assert.equal(clean('evil\u202Egnp.exe'), 'evilgnp.exe');
  assert.equal(clean('\u2066a\u2069\u202Ab\u202C'), 'ab');
  assert.equal(clean('a\u001b]52;c;aGk=\u0007b'), 'a]52;c;aGk=b');
  assert.equal(clean('x\u0085y\u009bz\u007f'), 'xyz');
  assert.equal(clean('line1\nline2\tend'), 'line1\nline2\tend');
  assert.equal(clean('a\u2028b\u2029c'), 'a\nb\nc');
  assert.equal(clean('<img src=x onerror=alert(1)>'), '<img src=x onerror=alert(1)>');
  assert.equal(clean(42), '42');
});

test('plural counts its noun', () => {
  assert.equal(plural(1, 'message'), '1 message');
  assert.equal(plural(0, 'message'), '0 messages');
  assert.equal(plural(2, 'recipient'), '2 recipients');
});

test('deltaLabel is the gap since the previous message', () => {
  const t = ms => new Date(Date.UTC(2026, 10, 3) + ms).toISOString();
  assert.equal(deltaLabel(t(0), t(0)), '+0.0s');
  assert.equal(deltaLabel(t(0), t(3800)), '+3.8s');
  assert.equal(deltaLabel(t(0), t(9940)), '+9.9s');
  assert.equal(deltaLabel(t(0), t(10_000)), '+10s');
  assert.equal(deltaLabel(t(0), t(59_000)), '+59s');
  assert.equal(deltaLabel(t(0), t(60_000)), '+1m 0s');
  assert.equal(deltaLabel(t(0), t(252_000)), '+4m 12s');
  assert.equal(deltaLabel(t(0), t(3_600_000)), '+1h 0m');
  assert.equal(deltaLabel(t(0), t(7_500_000)), '+2h 5m');
  assert.equal(deltaLabel(t(0), t(3 * 86_400_000)), '+3d');
  assert.equal(deltaLabel(t(5000), t(0)), '', 'out of order shows nothing');
  assert.equal(deltaLabel(null, t(0)), '');
  assert.equal(deltaLabel(t(0), 'nope'), '');
});

test('deltaTitle spells the gap out for a tooltip', () => {
  const t = ms => new Date(Date.UTC(2026, 10, 3) + ms).toISOString();
  assert.equal(deltaTitle(t(0), t(3800)), '3.8 s after the previous message');
  assert.equal(deltaTitle(t(0), t(42_000)), '42 s after the previous message');
  assert.equal(deltaTitle(t(0), t(252_000)), '4m 12s after the previous message');
  assert.equal(deltaTitle(t(0), t(7_500_000)), '2h 5m after the previous message');
  assert.equal(deltaTitle(t(5000), t(0)), '');
  assert.equal(deltaTitle(undefined, t(0)), '');
});

test('fullWhen is the full local date and time', () => {
  assert.equal(fullWhen(new Date(2026, 10, 3, 14, 5, 9).toISOString()), 'Tue, 3 Nov 2026, 14:05:09');
  assert.equal(fullWhen(new Date(2026, 10, 3, 0, 7, 0).toISOString()), 'Tue, 3 Nov 2026, 00:07:00');
  assert.equal(fullWhen('nope'), '');
  assert.equal(fullWhen(''), '');
});

test('PRIORITY_HELP covers every priority', () => {
  assert.deepEqual(Object.keys(PRIORITY_HELP).sort(), ['hold', 'wake', 'wake_idle']);
  for (const v of Object.values(PRIORITY_HELP)) assert.ok(v.length > 20 && !/[<>]/.test(v));
});

test('peer kinds read as people say them; the raw value stays for hovers and unknowns', () => {
  assert.equal(kindLabel('session'), 'Agent session (Claude Code, Codex, CLI)');
  assert.equal(kindShort('session'), 'Agent session');
  assert.equal(kindShort('grokbot'), 'Grok Bot');
  assert.equal(kindShort('pagchat-bot'), 'pagchat bot');
  assert.equal(kindShort('dot'), 'ChatGPT dot');
  assert.equal(kindTitle('dot'), 'ChatGPT dot · dot');
  // Unknown kinds, and inherited property names, show as themselves.
  assert.equal(kindLabel('webhookish'), 'webhookish');
  assert.equal(kindShort('constructor'), 'constructor');
  assert.equal(kindTitle('peer'), 'peer');
  assert.equal(kindLabel(''), '');
  assert.equal(kindShort(undefined), '');
});
