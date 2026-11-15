// The thread's read tracker (CoS unread ruling). A message is seen once
// SEEN_RATIO of it (or of the view, if taller) is visible for DWELL_MS, or
// when scrolled past going down, and only while active() (page visible,
// window focused). Scrolled past includes a tail that stays in the root:
// the top has left and what remains is under SEEN_RATIO, which happens when
// the thread ends before the message can clear the view. Each seen unread
// message gets a read receipt (PROTOCOL §2.5), batched over BATCH_MS; a
// failure is retried once after RETRY_MS, quietly, then left unread. Never
// a mark-all. One exception to the dwell: a thread that opens with its first
// message unread (an agent's new thread) has that message read at once, or
// as soon as the page is active (readOnOpen). No DOM: thread.js feeds it
// IntersectionObserver entries; tests drive it under mocked timers.

export const SEEN_RATIO = 0.6, DWELL_MS = 1000, BATCH_MS = 500, RETRY_MS = 2000;

// enough: ratio of the message is visible, shown px of it, in a view px tall.
export function enough(ratio, shown, view) {
  return ratio >= SEEN_RATIO || (view > 0 && shown >= SEEN_RATIO * view);
}

// createReader tracks one thread. onSeen/onLost(id) drop or restore the
// unread style. A pass counts only if the message was visible enough while
// tracked: a sliver swept off by a jump is not read.
export function createReader({ api, active, onSeen = () => {}, onLost = () => {} }) {
  // By message id: my delivery; intersecting; visible enough; ever so since
  // tracking (re)started; dwell timer; seen; receipt failed twice.
  let unread = new Map();
  const inter = new Set(), shown = new Set(), met = new Set(), dwell = new Map(), seen = new Set(), lost = new Set();
  const sent = new Map();    // delivery id -> message id
  const queue = new Map();   // delivery id -> attempt, awaiting the batch
  let batch = null, gone = false;
  let eager = null;          // readOnOpen's message, until read or dropped

  function stopDwell(id) {
    const t = dwell.get(id);
    if (t !== undefined) { clearTimeout(t); dwell.delete(id); }
  }
  function startDwell(id) {
    if (gone || dwell.has(id) || seen.has(id) || !unread.has(id) || !active()) return;
    dwell.set(id, setTimeout(() => {
      dwell.delete(id);
      if (shown.has(id)) see(id);
    }, DWELL_MS));
  }

  // see marks id seen and queues its receipt; now sends the batch at once.
  function see(id, now) {
    if (gone || seen.has(id) || !unread.has(id) || !active()) return;
    seen.add(id);
    stopDwell(id);
    const dly = unread.get(id);
    if (!sent.has(dly)) {
      sent.set(dly, id);
      enqueue(dly, 1);
      if (now) flush();
    }
    onSeen(id);
  }
  // readEager reads readOnOpen's message once the page is active. One that
  // stopped being unread (read elsewhere) is dropped.
  function readEager() {
    if (gone || !eager) return;
    if (seen.has(eager) || !unread.has(eager)) { eager = null; return; }
    if (!active()) return;
    const id = eager;
    eager = null;
    see(id, true);
  }

  function enqueue(dly, attempt) {
    queue.set(dly, attempt);
    if (batch === null) batch = setTimeout(flush, BATCH_MS);
  }
  function flush() {
    if (batch !== null) { clearTimeout(batch); batch = null; }
    const jobs = [...queue];
    queue.clear();
    for (const [dly, attempt] of jobs) {
      Promise.resolve().then(() => api.receipt(dly, 'read')).catch(() => {
        if (attempt < 2) { setTimeout(() => enqueue(dly, attempt + 1), RETRY_MS); return; }
        const id = sent.get(dly);
        lost.add(id);
        if (!gone) onLost(id);
      });
    }
  }
  // reset forgets every place; the view re-observes.
  function reset() {
    for (const id of [...dwell.keys()]) stopDwell(id);
    inter.clear();
    shown.clear();
    met.clear();
  }

  return {
    // setUnread takes thread-model unreadOf's list.
    setUnread(list) {
      unread = new Map((list || []).map(u => [u.message_id, u.delivery_id]));
      for (const id of [...dwell.keys()]) if (!unread.has(id)) stopDwell(id);
      for (const id of shown) startDwell(id);
      if (eager && !unread.has(eager)) eager = null;
    },
    // readOnOpen: the thread opened with id, its first message, unread. It
    // is read now, with no dwell and no batch wait, or the moment the page
    // is next active. Only that message: the rest follow the dwell rule.
    readOnOpen(id) {
      if (gone || !unread.has(id) || seen.has(id)) return;
      eager = id;
      readEager();
    },
    // view: one message's place. Leaving through the top is a pass going
    // down, and so is a tail (pastTop: its top is above the root) after it
    // was already visible enough. A sliver that never reached enough is not.
    view(id, { intersecting, enough: plenty, above, pastTop }) {
      const was = inter.has(id);
      if (intersecting) inter.add(id); else inter.delete(id);
      if (intersecting && plenty) {
        shown.add(id);
        met.add(id);
        startDwell(id);
        return;
      }
      shown.delete(id);
      stopDwell(id);
      if (met.has(id) && ((was && !intersecting && above) || (intersecting && pastTop))) see(id);
    },
    // activeChanged (visibility, focus, a modal): tracking starts over, so
    // nothing moved while away counts. A waiting readOnOpen is read now.
    activeChanged() { reset(); readEager(); },
    seen: id => seen.has(id) && !lost.has(id),
    // destroy stops tracking; earned receipts go out now, retries still run.
    destroy() {
      gone = true;
      eager = null;
      reset();
      if (queue.size) flush();
    },
  };
}
