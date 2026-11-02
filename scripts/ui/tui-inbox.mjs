// Task 18: the TUI inbox against a real demo hub, in tmux.
import { assert, cleanBuild, run } from './lib.mjs';
import { withTui } from './tmux.mjs';

// tmux.mjs drives one private server and names it after this process. Reads
// that it does not expose go to the same server.
const tmux = (...args) => run('tmux', ['-L', `oui-${process.pid}`, ...args], { env: { ...process.env, TMUX: '' } });
const screen = () => tmux('capture-pane', '-t', 'ui:0.0', '-p');
const paneTitle = async () => (await tmux('display', '-p', '-t', 'ui:0.0', '#{pane_title}')).trimEnd();

async function until(what, check) {
  const deadline = Date.now() + 60000;
  while (!await check()) {
    assert(Date.now() < deadline, `timed out: ${what}`);
    await new Promise(resolve => setTimeout(resolve, 50));
  }
}
// A closed approval stays in the history as an ordinary WAKE row, so what goes
// is its APR row.
const noApr = text => until(`the APR row to go: ${text}`, async () => !(await screen()).split('\n').some(line => line.includes('APR') && line.includes(text)));

try {
  await withTui({}, async ({ keys, waitFor, shot, asPeer }) => {
    // Taken before anything a test sends can reach the screen.
    const title = await paneTitle();
    const inbox = async () => {
      const response = await asPeer('operator', 'GET', '/v1/admin/inbox');
      assert(response.ok, `inbox: ${response.status}`);
      return response.json();
    };
    const json = async (peer, method, path, body) => {
      const response = await asPeer(peer, method, path, body);
      assert(response.ok, `${method} ${path}: ${response.status}`);
      return response.status === 204 ? undefined : response.json();
    };
    // The raw log holds what the operator's log holds. Presence needs an opt-in
    // (section 8), and a receipt needs a message of the operator's to acknowledge.
    const opening = await inbox();
    await json('operator', 'PATCH', '/v1/peers/me', { subscribe_presence: 'all' });
    await json('codex', 'POST', '/v1/presence', { state: 'blocked', at: opening.now });
    const sent = await json('operator', 'POST', '/v1/messages', {
      idempotency_key: 'event-log', recipients: [opening.approvals[0].requester], priority: 'hold',
      content: { text: 'Please acknowledge.' }, thread_title: 'Event log',
    });
    await json('codex', 'POST', '/v1/receipts', { delivery_id: sent.deliveries[0].delivery_id, state: 'processed' });

    await waitFor('NORMAL');
    await waitFor(' live ');
    await waitFor('your event log');
    await waitFor('apr 2');
    await waitFor('dlq 3');
    await waitFor('codex-hark'); // peer names have landed, not bare ids
    await waitFor('press a to answer');
    await waitFor('SUB');
    await waitFor('WAKE');
    await waitFor('HOLD');
    await waitFor('exp ');
    await waitFor('codex-hark blocked');
    await waitFor('✓✓ processed');
    await shot('18-tui-inbox-initial');

    const [first, second] = (await inbox()).approvals;
    assert(first && second, 'the demo seeds two open approvals');

    await keys('a');
    await waitFor('any other key cancels');
    await waitFor('[3] yes but no release');
    await shot('18-tui-inbox-progress');

    // The toast is gone in seconds, and a loaded slice can miss it, so what is
    // waited on is the state that lasts. Go tests pin the toast.
    await keys('1');
    await waitFor('apr 1');
    await noApr('Push classifier fix');
    await shot('18-tui-inbox-success');
    assert(!(await inbox()).approvals.some(a => a.approval_id === first.approval_id), 'the hub still holds the approval open');

    // Another client answers while this client's prompt is open. Its
    // operator_view refresh must close the prompt without sending an answer
    // (Review Focus 2). Go tests also pin the late-send 422 race.
    await waitFor('Proceed with the hosting plan?');
    await keys('a');
    await waitFor('any other key cancels');
    const other = await asPeer('operator', 'POST', '/v1/messages', {
      idempotency_key: 'other-client', thread_id: second.thread_id, in_reply_to: second.message_id, recipients: [second.requester],
      kind: 'approval_response', priority: 'wake', content: { text: 'no' },
      approval_response: { approval_id: second.approval_id, decision: 'no' },
    });
    assert(other.ok, `other client: ${other.status}`);
    await waitFor('approval closed before you answered: nothing sent');
    await shot('18-tui-inbox-error');
    await waitFor('apr 0');
    await noApr('Proceed with the hosting plan?');
    assert(!(await screen()).includes('answered: ' + second.options[0]), 'the operator was told a closed approval landed');

    await keys('p');
    await waitFor('max@vecnet · idle');

    // The list starts on the third line of the pane, and the cursor is on its
    // first row, so the row's line gives the number of presses.
    const row = (await screen()).split('\n').findIndex(line => line.includes('HOLD') && line.includes('onerror=alert(1)>pwn'));
    assert(row >= 2, 'the hostile thread is not on the list');
    await keys(...Array(row - 2).fill('j'));
    await waitFor('thread   <img src=x onerror=alert(1)>pwn');
    await keys('Enter');
    await until('the chat tab', async () => !(await screen()).includes('your event log'));
    await keys('1');
    await waitFor('your event log');

    // The seed's hostile text sets no title, so one that does arrives here. A
    // stronger seed is Task 2's to change.
    const probe = await asPeer('hostile', 'POST', '/v1/messages', {
      idempotency_key: 'title-probe', recipients: [opening.me.peer_id], priority: 'hold', thread_title: 'Title probe',
      content: { text: 'title-probe\x1b]0;pwned\x07\x1b]2;pwned\x07 ends' },
    });
    assert(probe.ok, `title probe: ${probe.status}`);
    await waitFor('title-probe ends');
    assert((await inbox()).deliveries.some(d => d.excerpt.includes('\x1b]0;pwned')), 'the title sequence did not reach this client');
    assert(!(await screen()).includes('pwned'), 'a title sequence was drawn as text');

    assert(await paneTitle() === title, 'a peer changed the pane title');
    const lines = (await screen()).split('\n');
    assert(lines[0].includes('vecnet | 1:inbox 2:chat 3:dash 4:subs'), 'a peer moved the top bar');
    assert(lines.some(line => line.includes('NORMAL')), 'a peer moved the status line');
    const raw = await tmux('capture-pane', '-t', 'ui:0.0', '-p', '-e');
    assert(!/\x1b\][\s\S]|‮|\]52;/.test(raw), 'a control sequence reached the pane');
    const alternate = await tmux('display', '-p', '-t', 'ui:0.0', '#{alternate_on}');
    assert(alternate.trim() === '1', 'the alternate screen was left');
  });
  console.log('TUI inbox passed: list, answer, closed approval, presence, hostile thread inert');
} finally { await cleanBuild(); }
