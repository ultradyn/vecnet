// New message (#new): pick peers by name, send to their ids (PROTOCOL
// §14.4), and open the thread the hub creates. compose-model.js holds the
// rules; this file only draws them. Every peer string goes through h().
// Attachments (picker, drop onto the form, paste into the text) are
// attach.js's chip row; the send carries their hashes once all are uploaded.
// Another screen may open it with a draft (prefill): the peer view's message
// actions pick the peer and its priority.
import { createAttachments } from './attach.js';
import { avatar, h, idText } from './dom.js';
import { kindShort } from './format.js';
import { prioritySeg } from './prio.js';
import { anyGrokbot, buildSend, canSend, composePriority, counter, draftOf, eligible, errorText, GROK_QUOTA_DETAIL, grokQuotaNote, INITIAL_PRIORITY, keepKey, keyFor, matches, priorityLocked, sendRefusal } from './compose-model.js';

// The draft for the next mount only. The caller sets it and routes to #new in
// the same click, and that mount takes it, so a later New message (the nav
// link, a reload) starts empty.
let handoff = null;
export function prefill(draft) { handoff = draftOf(draft); }

function randKey() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  return 'ui-' + [...b].map(x => x.toString(16).padStart(2, '0')).join('');
}

export function mount(root, ctx) {
  const { api, store, me, nameOf, kindOf, peers, toast, nav } = ctx;
  const draft = handoff;
  handoff = null;
  const S = {
    recipients: draft ? draft.recipients : [],
    priority: draft ? draft.priority : INITIAL_PRIORITY,
    priorityManual: !!draft, // a prefilled draft is an explicit choice too
    pending: null,   // {key, sig}: the unsent body keeps its idempotency_key across retries
    sending: false,
    error: '',
    denied: '',      // recipient named by the last scope_denied
  };
  let gone = false;
  const att = createAttachments({ api, onChange: () => render(), now: () => store.hubNow() });

  const search = h('input', { type: 'search', id: 'c-search', 'aria-label': 'Search peers', placeholder: 'Search peers by name', autocomplete: 'off', spellcheck: 'false' });
  const picked = h('div', { class: 'picked', 'aria-live': 'polite' });
  const opts = h('div', { class: 'opts', role: 'group', 'aria-label': 'Peers' });
  const titleIn = h('input', { type: 'text', id: 'c-title', maxlength: '200', autocomplete: 'off', placeholder: 'Optional' });
  const prio = prioritySeg(p => { S.priority = p; S.priorityManual = true; render(); }), seg = prio.seg;
  const text = h('textarea', { id: 'c-text', rows: '6', placeholder: 'Write the first message' });
  const count = h('span', { class: 'bytes' });
  const history = h('select', { id: 'c-history' },
    h('option', { value: 'from_join', text: 'from_join: people added later see only what follows' }),
    h('option', { value: 'full', text: 'full: people added later see the whole thread' }));
  const locked = h('input', { type: 'checkbox', id: 'c-locked' });
  // The Grok Bot note: one line under the Priority row whose space is kept
  // while it is empty (.off), so picking or removing a Grok Bot moves
  // nothing. The timing and second-run detail are its title and a hidden
  // element the control's aria-describedby names.
  const quota = h('p', { class: 'qnote off', id: 'c-quota' });
  const quotaMore = h('span', { id: 'c-quota-more', hidden: true, text: GROK_QUOTA_DETAIL });
  const err = h('p', { class: 'err', role: 'alert' });
  const send = h('button', { type: 'submit', class: 'btn pri', text: 'Send' });
  const attachBtn = h('button', { type: 'button', class: 'btn', text: 'Attach', title: 'Attach files (or drop them on the form, or paste a screenshot into the message)' });
  const fields = h('fieldset', {},
    h('div', { class: 'field' },
      h('label', { for: 'c-search', text: 'To' }),
      h('div', { class: 'to' }, picked, search),
      opts),
    h('div', { class: 'row' },
      h('div', { class: 'field grow' }, h('label', { for: 'c-title', text: 'Title' }), titleIn),
      h('div', { class: 'field' }, h('span', { class: 'lbl', text: 'Priority' }), prio.el)),
    quota, quotaMore,
    h('div', { class: 'field' },
      h('label', { for: 'c-text', text: 'Message' }),
      h('div', { class: 'wrap' }, text, att.el, h('div', { class: 'bar' }, count, h('span', { class: 'hint', text: 'Drop or paste files · Ctrl+Enter to send' })))),
    h('details', { class: 'adv' },
      h('summary', { text: 'Advanced' }),
      h('div', { class: 'field' }, h('label', { for: 'c-history', text: 'History access' }), history),
      h('label', { class: 'check', for: 'c-locked' }, locked, h('span', { text: 'Lock participants: only you can add people later' }))),
    // Inside the fieldset, so Attach disables while a send is in flight.
    err,
    h('div', { class: 'actions' }, attachBtn, send));
  const form = h('form', { class: 'cform', novalidate: true, autocomplete: 'off' }, fields);

  root.replaceChildren(h('div', { class: 'compose' },
    h('h1', { text: 'New message' }),
    h('p', { class: 'lede', text: 'Start a thread with one or more peers. Pick them by name; the hub is sent their ids.' }),
    form));

  const formValue = () => ({
    recipients: S.recipients, title: titleIn.value, priority: S.priority, text: text.value,
    historyAccess: history.value, lockedParticipants: locked.checked,
    attachments: att.toAttachments(), uploadsReady: att.ready(),
  });

  function toggle(id) {
    S.recipients = S.recipients.includes(id) ? S.recipients.filter(r => r !== id) : [...S.recipients, id];
    render();
  }

  function renderPicked() {
    picked.replaceChildren(...S.recipients.map(id => h('span', { class: 'pick' + (id === S.denied ? ' denied' : ''), title: id },
      h('span', { text: nameOf(id) }),
      h('button', { type: 'button', 'aria-label': 'Remove ' + nameOf(id), text: '×', onclick: () => { toggle(id); search.focus(); } }))));
  }

  function renderOpts() {
    const all = eligible(peers.get(), me), shown = all.filter(p => matches(p, search.value));
    if (peers.error() && !peers.get()) {
      opts.replaceChildren(h('div', { class: 'none error', text: 'Directory unavailable: ' + peers.error().message }));
    } else if (!peers.get()) {
      opts.replaceChildren(h('div', { class: 'none', text: 'Loading peers…' }));
    } else if (!shown.length) {
      opts.replaceChildren(h('div', { class: 'none', text: all.length ? 'No peer matches that name.' : 'No peers can receive messages yet.' }));
    } else {
      opts.replaceChildren(...shown.map(p => {
        const on = S.recipients.includes(p.peer_id);
        return h('button', { type: 'button', class: 'opt' + (on ? ' on' : ''), 'aria-pressed': on ? 'true' : 'false', title: p.peer_id, onclick: () => toggle(p.peer_id) },
          avatar(p.peer_id, ctx),
          h('span', { class: 'nm', text: p.name }),
          h('span', { class: 'kd', text: kindShort(p.kind) }),
          idText(p.peer_id, 'id'));
      }));
    }
  }

  // Preselect wake_idle once any recipient is grokbot (Decisions §6). A manual
  // choice sticks. Lock only while a send is in flight or this draft would
  // still retry the captured body (PROTOCOL §§2.2, 5.2, 5.3). A changed draft
  // is chosen again before its key is minted.
  function syncPriority() {
    const f = formValue();
    S.priority = composePriority({
      manual: S.priorityManual,
      locked: priorityLocked({ sending: S.sending, pending: S.pending, form: f }),
      current: S.priority,
      recipients: S.recipients,
      peers: peers.get(),
    });
  }

  function render() {
    if (gone) return;
    syncPriority();
    // "Preselected" only while wake_idle is the automatic choice; after a
    // manual pick the note says only what holds for every priority.
    const note = grokQuotaNote({ addressed: anyGrokbot(S.recipients, peers.get()), manual: S.priorityManual, priority: S.priority });
    quota.classList.toggle('off', !note);
    quota.textContent = note;
    quota.title = note ? GROK_QUOTA_DETAIL : '';
    if (note) seg.setAttribute('aria-describedby', 'c-quota c-quota-more');
    else seg.removeAttribute('aria-describedby');
    renderPicked();
    renderOpts();
    for (const b of seg.querySelectorAll('button')) b.className = b.value === S.priority ? 'on' : '';
    const c = counter(text.value);
    count.textContent = c.label;
    count.className = 'bytes' + (c.tone ? ' ' + c.tone : '');
    const f = formValue();
    err.textContent = S.error || sendRefusal(f);
    fields.disabled = S.sending;
    send.disabled = S.sending || !canSend(f);
    send.textContent = S.sending ? 'Sending…' : 'Send';
  }

  async function submit() {
    if (S.sending) return;
    // Choose again before minting, so a draft edited after a 503 does not
    // keep the failed send's priority under a fresh key.
    syncPriority();
    const f = formValue();
    if (!canSend(f)) return;
    S.pending = keyFor(S.pending, f, randKey);
    const body = buildSend(f, S.pending.key);
    S.sending = true;
    S.error = '';
    S.denied = '';
    render();
    try {
      const r = await api.send(body);
      S.pending = null;
      att.clear();
      store.invalidate('threads');
      if (gone) { toast('Sent'); return; }
      nav('#thread/' + r.thread_id);
    } catch (e) {
      if (e.status === 401) return; // the shell signs out
      if (!keepKey(e)) S.pending = null;
      S.error = errorText(e, nameOf);
      if (e.code === 'scope_denied') S.denied = (e.details || {}).recipient || '';
      if (gone) toast(S.error, true);
    } finally {
      S.sending = false;
      render();
    }
  }

  form.addEventListener('submit', e => { e.preventDefault(); submit(); });
  text.addEventListener('keydown', e => {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !e.isComposing) { e.preventDefault(); submit(); }
  });
  // Enter in the search picks the first match that is not yet picked.
  search.addEventListener('keydown', e => {
    if (e.key !== 'Enter' || e.isComposing) return;
    e.preventDefault();
    const first = eligible(peers.get(), me).find(p => matches(p, search.value) && !S.recipients.includes(p.peer_id));
    if (first) { search.value = ''; toggle(first.peer_id); }
  });
  for (const el of [search, titleIn, text]) el.addEventListener('input', render);
  for (const el of [history, locked]) el.addEventListener('change', render);
  // The drop target is the form, outside the fieldset that disables while a
  // send is in flight, so drags are refused here (capture, before attach.js):
  // a file added mid-send would miss the body and be cleared on success.
  for (const type of ['dragenter', 'dragover', 'drop']) {
    form.addEventListener(type, e => {
      if (!S.sending) return;
      e.preventDefault();
      if (e.dataTransfer) e.dataTransfer.dropEffect = 'none';
      e.stopImmediatePropagation();
    }, true);
  }
  att.wire(form, text, attachBtn);

  render();
  // A draft has picked the recipient, so the message is next.
  (draft ? text : search).focus();
  const off = peers.subscribe(render);
  return () => {
    gone = true;
    off();
    att.destroy();
  };
}
