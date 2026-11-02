// Operator presence: the nav dot with its one-tap toggle, the inbox aside, and
// the heartbeat that keeps a chosen state past the hub's 1 h TTL (PROTOCOL
// §2.6, §8). State shown is always the hub's: `me` from the inbox snapshot,
// redrawn only after a POST answers 204 and the snapshot reloads.
import { h } from './dom.js';
import { clean, when } from './format.js';
import { HEARTBEAT_MS, heartbeatAction, navLabel, toggleTarget, wireTime } from './presence-model.js';

// Toggles and heartbeats in this tab run one at a time, so a beat that read
// the old state can never land after a toggle and undo it.
let queue = Promise.resolve();
function serial(fn) {
  const run = queue.then(fn);
  queue = run.catch(() => {});
  return run;
}

const meOf = ctx => (ctx.inboxHead.get() || {}).me || null;

// renderNav returns an unsubscribe function, which app.js calls on sign-out.
export function renderNav(el, ctx) {
  let pending = '', live = true;
  const dot = h('span', { class: 'dot' });
  const label = h('span', { class: 'who' });
  const btn = h('button', { type: 'button', class: 'ptog', hidden: true });

  const render = () => {
    if (!live) return;
    const me = meOf(ctx);
    const state = me ? me.presence : '';
    dot.className = 'dot ' + (state || 'none');
    dot.title = state ? 'presence ' + state : '';
    label.textContent = clean(navLabel((me && me.name) || ctx.nameOf(ctx.me), state));
    label.title = me && me.presence_at ? 'since ' + me.presence_at : '';
    btn.hidden = !me;
    btn.disabled = !!pending;
    if (pending) btn.setAttribute('aria-busy', 'true');
    else btn.removeAttribute('aria-busy');
    const target = toggleTarget(state);
    // Short while busy: the sidebar has room for the label or a long button.
    btn.textContent = pending ? 'Setting…' : 'Set ' + target;
    btn.title = pending ? 'Setting ' + pending : 'Tell peers you are ' + target;
  };

  btn.addEventListener('click', () => {
    const me = meOf(ctx);
    if (pending || !me) return;
    pending = toggleTarget(me.presence);
    render();
    const target = pending;
    serial(async () => {
      try {
        await ctx.api.presence(target, wireTime(ctx.store.hubNow()));
      } catch (e) {
        // A rejected secret signs out on its own; nothing to report here.
        if (live && e.status !== 401) ctx.toast('Presence unchanged: ' + e.message, true);
        return;
      }
      await ctx.inboxHead.reload();
      if (live && ctx.inboxHead.error()) ctx.toast('Presence set to ' + target + ', but the inbox did not refresh', true);
    }).finally(() => { pending = ''; render(); });
  });

  el.replaceChildren(dot, label, btn);
  render();
  const offs = [ctx.inboxHead.subscribe(render), ctx.peers.subscribe(render)];
  return () => { live = false; for (const off of offs) off(); };
}

export function renderAside(el, me, ctx) {
  const state = (me && me.presence) || 'unknown';
  const at = me && me.presence_at;
  const time = at ? h('time', { datetime: at, title: at, text: when(at, ctx.store.hubNow()) }) : null;
  let line;
  if (state === 'unknown') {
    line = at ? ['unknown: no fresh report. Last set ', time, '.'] : ['unknown: nothing reported yet.'];
  } else {
    line = at ? [state + ' since ', time, '.'] : [state + '.'];
  }
  el.replaceChildren(
    h('h3', { text: 'Your presence' }),
    h('p', { class: 'pres' }, h('span', { class: 'dot ' + state, title: 'presence ' + state }), h('span', {}, ...line)),
    h('p', { class: 'pnote' }, 'Peers see this in the directory. Only ', h('code', { text: 'wake' }), ' messages fire your notifications.'));
}

// startHeartbeat re-posts the hub's own state every HEARTBEAT_MS while a tab
// is open. It returns the function that stops it.
export function startHeartbeat(ctx, every = HEARTBEAT_MS) {
  let live = true;
  const beat = () => serial(async () => {
    if (!live) return;
    await ctx.inboxHead.reload();
    // A failed reload leaves the previous snapshot in place, which may be a
    // state another client has since changed: skip this beat.
    if (!live || ctx.inboxHead.error()) return;
    const state = heartbeatAction(meOf(ctx));
    // Dated when the hub served the state it re-posts: a manual change in
    // another client after that is newer, and the hub keeps it (§2.6).
    const snap = ctx.inboxHead.get();
    const at = snap && typeof snap.now === 'string' ? snap.now : wireTime(ctx.store.hubNow());
    if (state) await ctx.api.presence(state, at);
  }).catch(() => {}); // the next beat tries again; a 401 has already signed out
  const timer = setInterval(beat, every);
  return () => { live = false; clearInterval(timer); };
}
