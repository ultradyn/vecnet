// Document-lived admission, instance-local payloads and physical DOM ownership.
import { h } from './dom.js';
import { bytes, clean } from './format.js';
import { createThumbCache, eligible, retryThumbCleanup } from './thumbs-model.js';
import { createThumbBudget } from './thumbs-budget.js';

const moduleBudget = createThumbBudget();
// Standalone capabilities: an unpublished cleanup cell must never retain a
// provider, mount adapter closure or DOM environment. Instrument before import.
const nativeURLs = Object.freeze({
  createObjectURL: globalThis.URL?.createObjectURL,
  revokeObjectURL: globalThis.URL?.revokeObjectURL,
});

// chip is today's attachment chip: cleaned name (bidi-isolated by base.css),
// size, and the hash on hover. Downloads stay in the thread's side panel.
function chip(att) {
  return h('span', { class: 'att', title: att.sha256 || '' },
    h('span', { class: 'nm', text: att.name || att.media_type || 'attachment' }),
    h('span', { class: 'sz', text: bytes(att.size) }));
}

function attempts(actions) {
  let failed = false, first;
  for (const action of actions) {
    try { action(); } catch (error) { if (!failed) { failed = true; first = error; } }
  }
  if (failed) throw first;
}

function metadata(att) {
  return { sha256: att.sha256, size: att.size, media_type: String(att.media_type || ''),
    name: clean(att.name || 'image'), blob_url: !!att.blob_url, origin_url: !!att.origin_url };
}

function visible(node) {
  if (!node?.isConnected) return false;
  for (let p = node; p; p = p.parentElement) {
    const style = getComputedStyle(p);
    if (p.hidden || p.inert || style.display === 'none' || style.visibility === 'hidden') return false;
  }
  return node.getClientRects().length > 0;
}

export function createThumbs({ api, budget = moduleBudget }) {
  let dead = false, frozen = false, epoch = 0n, serial = 0n, frame = null;
  let needsDemand = false;
  let activeWrites = 0, releaseDebt = 0n, releaseDepth = 0, modelDepth = 0, draining = false, wave = null;
  let registrations = new Map(), staging = null, viewer = null, closing = null;
  let viewport = null, composer = null, fallback = null, io = null, ro = null;
  const owners = new Map(), consumers = new Set(), retiring = new Set(), listeners = [];
  const supported = typeof api?.thumbnailBlob === 'function' &&
    typeof nativeURLs.createObjectURL === 'function' && typeof nativeURLs.revokeObjectURL === 'function' &&
    typeof IntersectionObserver === 'function' && typeof ResizeObserver === 'function' &&
    typeof requestAnimationFrame === 'function';
  const cache = supported ? createThumbCache({ api, URL: nativeURLs, budget,
    beforeRevoke, onChange: changed }) : null;
  api = null;

  function listen(target, type, fn, options) {
    target.addEventListener(type, fn, options);
    listeners.push(() => target.removeEventListener(type, fn, options));
  }

  function releaseError(error) {
    if (wave && !wave.failed) { wave.failed = true; wave.error = error; }
  }
  function settleRelease() {
    if (!wave || activeWrites || releaseDebt || releaseDepth || modelDepth || draining) return;
    for (const [key, owner] of owners) if (![...consumers].some(c => c.owner === owner)) owners.delete(key);
    const { observer, failed, error } = wave;
    wave = null;
    observer?.({ failed, error });
  }
  function releaseModel() {
    modelDepth++;
    try { cache?.release(); }
    catch (error) { releaseError(error); throw error; }
    finally { modelDepth--; }
  }
  function drainRelease() {
    if (activeWrites || draining) return;
    draining = true;
    let failed = false, first;
    try {
      while (releaseDebt) {
        releaseDebt--;
        try { releaseModel(); } catch (error) { if (!failed) { failed = true; first = error; } }
      }
    } finally { draining = false; }
    if (failed) throw first;
  }

  function schedule() {
    needsDemand = true;
    queueFrame();
  }

  function changed({ sha, reason }) {
    if (dead) return;
    // Flags live only on the bounded owner ledger, never in a history of
    // notifications. Callback errors must escape our frame, not be swallowed
    // by the cache's observer exception boundary.
    for (const owner of owners.values()) if (sha === null || owner.sha === sha) owner.dirty = true;
    if (reason === 'capacity') needsDemand = true;
    queueFrame();
  }

  function queueFrame() {
    if (dead || frozen || frame !== null || !supported) return;
    const stamp = epoch;
    const scheduled = requestAnimationFrame(() => {
      if (dead || frozen || stamp !== epoch || frame !== scheduled) return;
      frame = null;
      if (needsDemand) { needsDemand = false; refresh(); }
      else patchChanged();
    });
    frame = scheduled;
  }

  // Keep the record until real removal returns. A pending outer removal is
  // not detachment; synchronous reentry may safely complete that same removal.
  function detach(consumer) {
    if (!consumers.has(consumer)) return;
    const writing = consumer.phase === 'writing';
    consumer.retireRequested = true;
    if (!writing) consumer.phase = 'retiring';
    consumer.img.removeEventListener('error', consumer.error);
    consumer.img.removeAttribute('src');
    if (writing) {
      if (!dead) throw new Error('Thumbnail assignment is unresolved');
      return;
    }
    consumers.delete(consumer);
    consumer.phase = 'retired';
    if (consumer.slot.consumer === consumer) consumer.slot.consumer = null;
  }

  function detachWhere(predicate) {
    attempts([...consumers].filter(predicate).map(c => () => detach(c)));
  }

  function beforeRevoke(sha, generation) {
    // Model mutation guard is active here: physical cleanup only.
    detachWhere(c => c.sha === sha && c.generation === generation);
  }

  function ownerSnapshot(owner) {
    const key = owner.key, lease = owner.lease, generation = lease?.generation, incarnation = owner.incarnation;
    return () => owners.get(key) === owner && owner.key === key && owner.lease === lease &&
      lease?.generation === generation && owner.incarnation === incarnation;
  }
  function drop(owner, guard = () => true) {
    if (!owner) return false;
    const key = owner.key, current = ownerSnapshot(owner);
    if (!current() || !guard()) return false;
    detachWhere(c => c.owner === owner);
    if (!current() || !guard()) return false;
    cache.drop(key);
    if (current()) owners.delete(key);
    return owners.get(key) !== owner;
  }

  function acquire(key, att, hosted, kind, occurrence) {
    if (dead || !cache) return null;
    const old = owners.get(key);
    if (old) return old;
    const owner = { key, sha: att.sha256, lease: null, kind, occurrence, incarnation: 0n };
    // Acquisition can synchronously dispatch and transport can reenter us.
    owners.set(key, owner);
    let lease;
    try { lease = cache.acquire(key, att, hosted, { priority: kind === 'inline' ? 'automatic' : 'explicit' }); }
    catch (error) { if (owners.get(key) === owner) owners.delete(key); throw error; }
    if (owners.get(key) !== owner || dead) return null;
    if (!lease || lease.state === 'dormant') { owners.delete(key); return null; }
    owner.lease = lease;
    return owner;
  }

  function move(owner, key, kind, occurrence, guard = () => true) {
    if (!owner) return null;
    const from = owner.key, current = ownerSnapshot(owner);
    if (!current() || owners.has(key) || !guard()) return null;
    detachWhere(c => c.owner === owner);
    if (dead || !current() || owners.has(key) || !guard()) return null;
    const previousKind = owner.kind, previousOccurrence = owner.occurrence, incarnation = ++owner.incarnation;
    owners.delete(from); owners.set(key, owner);
    owner.key = key; owner.kind = kind; owner.occurrence = occurrence;
    let lease;
    try { lease = cache.moveOwner(from, key, { priority: kind === 'inline' ? 'automatic' : 'explicit' }); }
    catch (error) {
      if (owners.get(key) === owner && owner.incarnation === incarnation) {
        owners.delete(key); owners.set(from, owner);
        owner.key = from; owner.kind = previousKind; owner.occurrence = previousOccurrence;
      }
      throw error;
    }
    if (!lease && owners.get(key) === owner && owner.incarnation === incarnation) {
      owners.delete(key); owners.set(from, owner);
      owner.key = from; owner.kind = previousKind; owner.occurrence = previousOccurrence;
      return null;
    }
    // A synchronous dispatch may close/transfer an owner before its original
    // acquire returned its lease. The successful move supplies that same view.
    if (lease && !owner.lease) owner.lease = lease;
    if (dead || owners.get(key) !== owner || owner.incarnation !== incarnation || lease !== owner.lease || !guard()) return null;
    return owner;
  }

  const inlineKey = key => 'inline/' + key;
  function liveRegistration(r) {
    return !dead && !frozen && registrations.get(r.key) === r && r.button.isConnected;
  }
  function liveViewer(v) { return !dead && viewer === v && v.active && v.dlg.isConnected; }

  function attach(slot, owner, container, isCurrent) {
    const lease = owner?.lease;
    if (!lease || lease.state !== 'ready' || !isCurrent()) return;
    const existing = slot.consumer;
    if (existing && existing.owner === owner && existing.generation === lease.generation) return;
    if (existing) detach(existing);
    if (dead || owners.get(owner.key) !== owner || lease.state !== 'ready' || !isCurrent()) return;
    const generation = lease.generation, url = lease.url;
    const current = ownerSnapshot(owner);
    const img = h('img', { alt: slot.att.name, decoding: 'async', draggable: 'false' });
    if (!isCurrent() || slot.consumer || dead || !current() || lease.state !== 'ready') return;
    const consumer = { img, slot, owner, ownerKey: owner.key, lease, incarnation: owner.incarnation,
      sha: owner.sha, generation, error: null, phase: 'writing', retireRequested: false };
    consumer.error = () => {
      if (!isCurrent() || slot.consumer !== consumer || !consumers.has(consumer) ||
          owners.get(owner.key) !== owner || lease.generation !== generation) return;
      for (const r of registrations.values()) if (r.att.sha256 === owner.sha) r.failed = true;
      beforeRevoke(owner.sha, generation);
      cache.broken(owner.sha, generation);
      patchAll();
    };
    slot.consumer = consumer; consumers.add(consumer);
    let failed = false, first;
    activeWrites++;
    try {
      img.addEventListener('error', consumer.error);
      if (!isCurrent() || slot.consumer !== consumer || consumer.retireRequested ||
          dead || !current() || lease.state !== 'ready') consumer.retireRequested = true;
      else img.setAttribute('src', url);
      if (consumer.retireRequested || dead || owner.key !== consumer.ownerKey || owner.incarnation !== consumer.incarnation ||
          lease.generation !== generation || owners.get(consumer.ownerKey) !== owner || !isCurrent() || slot.consumer !== consumer) {
        consumer.retireRequested = true;
      } else container.replaceChildren(img);
    } catch (error) {
      failed = true; first = error; consumer.retireRequested = true; releaseError(error);
    } finally {
      consumer.phase = 'live';
      try { if (failed || consumer.retireRequested || dead) detach(consumer); }
      catch (error) { releaseError(error); if (!failed) { failed = true; first = error; } }
      finally { activeWrites--; }
      try { drainRelease(); }
      catch (error) { if (!failed) { failed = true; first = error; } }
      try { settleRelease(); }
      catch (error) { if (!failed) { failed = true; first = error; } }
    }
    if (failed) throw first;
  }

  function stateText(owner, failed = false) {
    if (!supported) return 'Image preview unavailable';
    const state = owner?.lease?.state;
    if (state === 'loading') return 'Loading image…';
    if (state === 'queued' || state === 'waiting') return 'Waiting for earlier image requests…';
    if (state === 'error' || failed) return 'Image unavailable. Retry in viewer or use Files.';
    return 'Open image';
  }

  function patchRegistration(r) {
    if (!liveRegistration(r)) return;
    const owner = owners.get(inlineKey(r.key));
    if (owner?.lease?.state === 'error') r.failed = true;
    if (owner?.lease?.state === 'ready') {
      r.failed = false;
      attach(r, owner, r.button, () => liveRegistration(r));
      r.button.className = 'thumb';
      return;
    }
    if (r.consumer) detach(r.consumer);
    const text = stateText(owner, r.failed), waiting = owner?.lease?.state === 'loading';
    r.button.className = 'thumb' + (waiting ? ' wait' : ' deferred');
    if (r.button.textContent !== text) r.button.replaceChildren(h('span', { text }));
  }

  function patchViewer(v) {
    if (!liveViewer(v)) return;
    const owner = owners.get(v.ownerKey);
    const text = !owner && supported ? 'Image preview unavailable. Retry or use Files.' : stateText(owner, v.failed || v.cleanupFailed);
    v.status.textContent = owner?.lease?.state === 'ready' ? '' : text;
    if (owner?.lease?.state === 'ready') attach(v, owner, v.picture, () => liveViewer(v));
    else {
      if (v.consumer) detach(v.consumer);
      v.picture.replaceChildren();
    }
  }

  function patchAll() {
    if (dead || frozen) return;
    attempts([...registrations.values()].map(r => () => patchRegistration(r))
      .concat(viewer ? [() => patchViewer(viewer)] : []));
  }

  function reconcile(owner) {
    if (owner.lease?.state !== 'dormant') return;
    detachWhere(c => c.owner === owner);
    if (owners.get(owner.key) === owner) owners.delete(owner.key);
    const r = registrations.get(owner.occurrence);
    if (r) r.suppressed = true;
  }

  function patchChanged() {
    if (dead || frozen) return;
    attempts([...owners.values()].filter(o => o.dirty).map(owner => () => {
      owner.dirty = false;
      reconcile(owner);
      const r = registrations.get(owner.occurrence);
      if (r) patchRegistration(r);
    }).concat(viewer ? [() => patchViewer(viewer)] : []));
  }

  function demand(r) {
    if (!r.button.isConnected || document.visibilityState !== 'visible' || !visible(r.button)) return null;
    const rect = r.button.getBoundingClientRect();
    let left = 0, top = 0, right = innerWidth, bottom = innerHeight;
    for (let p = r.button.parentElement; p; p = p.parentElement) {
      const style = getComputedStyle(p);
      if (p === viewport || /auto|scroll|hidden|clip/.test(style.overflow + style.overflowY + style.overflowX)) {
        const box = p.getBoundingClientRect();
        left = Math.max(left, box.left); right = Math.min(right, box.right);
        top = Math.max(top, box.top); bottom = Math.min(bottom, box.bottom);
      }
    }
    if (composer?.isConnected && visible(composer)) bottom = Math.min(bottom, composer.getBoundingClientRect().top);
    if (rect.right <= left || rect.left >= right || rect.bottom <= top || rect.top >= bottom || bottom <= top || right <= left) return null;
    return Math.abs((rect.top + rect.bottom - top - bottom) / 2);
  }

  function refresh() {
    if (dead || frozen) return;
    for (const owner of [...owners.values()]) reconcile(owner);
    if (viewer) explicitCapacity(viewer, owners.get(viewer.ownerKey));
    const candidates = [];
    for (const r of registrations.values()) {
      const distance = demand(r), demanded = distance !== null;
      if (!demanded && r.demanded) r.suppressed = false;
      r.demanded = demanded;
      const owner = owners.get(inlineKey(r.key));
      if (owner?.lease?.state === 'error') r.failed = true;
      if (!demanded) { if (owner) drop(owner); continue; }
      if (!owner && !r.failed && !r.suppressed) candidates.push({ r, distance });
    }
    candidates.sort((a, b) => Number(b.r.button === document.activeElement) - Number(a.r.button === document.activeElement) ||
      a.distance - b.distance || (a.r.key < b.r.key ? -1 : a.r.key > b.r.key ? 1 : 0));
    for (const { r } of candidates) {
      if (dead || frozen || !liveRegistration(r) || !r.demanded) continue;
      // A transferred opener stays metadata-only while V owns its permit.
      if (viewer?.active && viewer.key === r.key && viewer.transferred === owners.get(viewer.ownerKey) && viewer.transferred) continue;
      const owner = acquire(inlineKey(r.key), r.att, r.hosted, 'inline', r.key);
      if (!owner) r.suppressed = true;
    }
    patchAll();
  }

  function beginRender() {
    if (dead) return;
    frozen = true; epoch++;
    if (frame !== null) { cancelAnimationFrame(frame); frame = null; }
    io?.disconnect();
    staging = new Map();
    detachWhere(c => c.owner.kind === 'inline');
  }

  function render(att, hosted, occurrenceKey) {
    if (!eligible(att, hosted)) return chip(att);
    if (dead) return chip(att);
    if (!frozen || !staging || typeof occurrenceKey !== 'string' || !occurrenceKey) throw new TypeError('Thumbnail render requires an occurrence transaction');
    const key = occurrenceKey, old = registrations.get(key), data = metadata(att);
    const r = { key, att: data, hosted: typeof hosted === 'boolean' ? hosted : undefined, button: null, consumer: null,
      failed: old?.failed || false, suppressed: old?.suppressed || false, demanded: old?.demanded || false };
    r.button = h('button', { type: 'button', class: 'thumb deferred', 'data-sha': data.sha256,
      'data-occurrence': key, title: data.name + ' · ' + bytes(data.size), 'aria-label': 'Open ' + data.name,
      onclick: () => { if (liveRegistration(r)) open(r.att, r.button, r.hosted, r.key); } }, h('span', { text: 'Open image' }));
    staging.set(key, r);
    return r.button;
  }

  function endRender(options) {
    if (dead) return;
    try {
      const next = new Map();
      for (const [key, r] of staging || []) if (r.button.isConnected) next.set(key, r);
      for (const [key, r] of registrations) if (!next.has(key) && r.button.isConnected) next.set(key, r);
      registrations = next;
      const newViewport = options.viewport;
      if (viewport !== newViewport) {
        viewport?.removeEventListener('scroll', schedule);
        viewport = newViewport;
        viewport?.addEventListener('scroll', schedule, { passive: true });
      }
      composer = options.composer;
      const firstButton = next.values().next().value?.button;
      fallback = firstButton?.closest('.msgs') || firstButton?.parentElement ||
        (fallback?.isConnected ? fallback : null) || viewport?.querySelector('.msgs') || viewport;
      attempts([...owners.values()].filter(o => o.kind === 'inline' && !next.has(o.occurrence)).map(o => () => drop(o)));
    } finally { staging = null; frozen = false; }
    if (!supported) { patchAll(); return; }
    const stamp = epoch;
    io?.disconnect(); ro?.disconnect();
    io = new IntersectionObserver(() => { if (!dead && stamp === epoch) schedule(); }, { root: viewport, threshold: [0, 1] });
    ro = new ResizeObserver(() => { if (!dead && stamp === epoch) schedule(); });
    if (viewport) ro.observe(viewport);
    if (composer) ro.observe(composer);
    for (const r of registrations.values()) io.observe(r.button);
    needsDemand = false;
    refresh();
  }

  function focusNode(node) {
    if (!visible(node)) return false;
    if (!node.hasAttribute('tabindex')) node.setAttribute('tabindex', '-1');
    node.focus({ preventScroll: true });
    return document.activeElement === node;
  }

  function focusFiles() {
    if (dead || viewer) return;
    const aside = document.getElementById('aside');
    if (!aside?.isConnected) return;
    let body = aside.querySelector('.aside-body');
    const toggle = aside.querySelector('.aside-toggle');
    if (!visible(body) && visible(toggle) && toggle.getAttribute('aria-expanded') === 'false') toggle.click();
    if (dead || viewer) return;
    body = aside.querySelector('.aside-body');
    const files = aside.querySelector('.files');
    const target = visible(files) ? files : visible(body) ? body : aside;
    if (focusNode(target)) target.scrollIntoView({ block: 'nearest' });
  }

  function restoreFocus(v) {
    if (dead || viewer || v.focus === 'none') return;
    if (v.focus === 'files') { focusFiles(); return; }
    const r = registrations.get(v.key);
    if (r && visible(r.button)) r.button.focus({ preventScroll: true });
    else focusNode(fallback);
  }

  function finishDialog(v) {
    v.dlg.removeEventListener('close', v.onClose);
    v.dlg.removeEventListener('cancel', v.onCancel);
    if (v.dlg.open) v.dlg.close();
    v.dlg.remove();
    retiring.delete(v);
    if (closing === v) closing = null;
  }

  function retire(v) {
    detachWhere(c => c.slot === v);
    let owner = owners.get(v.ownerKey);
    if (owner) {
      const r = registrations.get(v.key), key = inlineKey(v.key);
      if (!dead && !owners.has(key) && r && eligible(r.att, r.hosted) && demand(r) !== null) {
        move(owner, key, 'inline', v.key, () => retiring.has(v) && !v.active && registrations.get(v.key) === r && demand(r) !== null);
      }
      owner = owners.get(v.ownerKey);
      if (owner) drop(owner, () => retiring.has(v) && !v.active);
    }
    if (owners.has(v.ownerKey)) return;
    v.dlg.removeEventListener('cancel', v.onCancel);
    if (v.dlg.open && !dead) {
      if (closing && closing !== v) finishDialog(closing);
      closing = v; retiring.delete(v);
      // Keep just this closed dialog until its native close event reaches the
      // thread's existing capture listener and resumes ordinary dwell rules.
      v.dlg.close();
    } else finishDialog(v);
  }

  function close(v, focus = 'opener') {
    if (dead || viewer !== v || !v.active) return;
    v.active = false; v.focus = focus; viewer = null; retiring.add(v);
    retire(v);
    if (dead || viewer) return;
    patchAll(); restoreFocus(v); schedule();
  }

  function explicitCapacity(v, owner) {
    if (!owner || !liveViewer(v)) return null;
    // A source permit alone cannot make a different resident payload fit.
    // Reclaim our lower-priority ready consumers, never another cache's work
    // or a same-payload opener. Running cancellation gives no instant credit.
    for (const other of [...owners.values()].reverse()) {
      if (!liveViewer(v) || owners.get(v.ownerKey) !== owner) return null;
      if (!['queued', 'waiting'].includes(owner.lease?.state)) break;
      if (!budget.hasConcurrencySlot()) break;
      if (other.kind !== 'inline' || other.sha === owner.sha || other.lease?.state !== 'ready') continue;
      const r = registrations.get(other.occurrence);
      const protectedOwner = () => r?.button === document.activeElement ||
        [...owners.values()].some(o => o.sha === other.sha && o.kind === 'viewer');
      if (protectedOwner()) continue;
      const current = ownerSnapshot(owner);
      const reclaimed = drop(other, () => liveViewer(v) && current() && owners.get(v.ownerKey) === owner &&
        !protectedOwner() && ['queued', 'waiting'].includes(owner.lease?.state) && budget.hasConcurrencySlot());
      if (!reclaimed) {
        if (r && registrations.get(r.key) === r && demand(r) !== null) patchRegistration(r);
        break;
      }
      if (r && registrations.get(other.occurrence) === r) r.suppressed = true;
    }
    return liveViewer(v) && owners.get(v.ownerKey) === owner ? owner : null;
  }

  function viewerOwner(v) {
    if (!liveViewer(v) || !cache) return null;
    let owner = owners.get(v.ownerKey);
    if (owner?.lease?.state === 'dormant') { drop(owner); owner = null; }
    if (owner) return explicitCapacity(v, owner);
    // Reentrant Open completes retiring V before evicting a live automatic K.
    for (const old of [...retiring]) retire(old);
    if (!liveViewer(v)) return null;
    owner = acquire(v.ownerKey, v.att, v.hosted, 'viewer', v.key);
    if (owner || !liveViewer(v)) return explicitCapacity(v, owner);
    for (const other of [...owners.values()].reverse()) {
      if (other.kind !== 'inline' || other.occurrence === v.key) continue;
      const r = registrations.get(other.occurrence);
      if (r) r.suppressed = true;
      drop(other);
      if (!liveViewer(v)) return null;
      owner = acquire(v.ownerKey, v.att, v.hosted, 'viewer', v.key);
      if (owner) return explicitCapacity(v, owner);
    }
    const opener = owners.get(inlineKey(v.key));
    owner = opener ? move(opener, v.ownerKey, 'viewer', v.key, () => liveViewer(v)) : null;
    if (owner && liveViewer(v)) v.transferred = owner;
    return explicitCapacity(v, owner);
  }

  function retry(v) {
    if (!liveViewer(v)) return;
    let cleanupFailed = false;
    try { retryThumbCleanup(budget); } catch { cleanupFailed = true; }
    if (!liveViewer(v) || !eligible(v.att, v.hosted)) return;
    v.cleanupFailed = cleanupFailed;
    let owner = owners.get(v.ownerKey);
    if (!owner || owner.lease?.state === 'dormant') owner = viewerOwner(v);
    if (!liveViewer(v)) return;
    if (owner && (owner.lease.state === 'waiting' || owner.lease.state === 'error')) cache.retry(owner.key);
    if (!liveViewer(v)) return;
    patchAll(); schedule();
  }

  function open(att, opener, hosted, occurrenceKey) {
    if (dead || !eligible(att, hosted)) return;
    const key = occurrenceKey || opener?.dataset.occurrence;
    const r = registrations.get(key);
    if (!r || !liveRegistration(r) || r.button !== opener || att.sha256 !== r.att.sha256 || !eligible(r.att, r.hosted)) return;
    if (viewer?.active && viewer.key === key) return;
    if (viewer) close(viewer, 'none');
    if (dead || viewer || !liveRegistration(r)) return;
    for (const old of [...retiring]) retire(old);
    if (closing) finishDialog(closing);
    if (dead || viewer || !liveRegistration(r)) return;
    const v = { key, att: metadata(r.att), hosted: r.hosted, ownerKey: 'viewer/' + ++serial,
      active: true, consumer: null, failed: false, cleanupFailed: false, focus: 'opener' };
    v.picture = h('div', { class: 'viewer-picture' });
    v.status = h('p', { class: 'viewer-status', role: 'status', 'aria-live': 'polite' });
    const cancel = h('button', { type: 'button', class: 'btn', text: 'Cancel', onclick: () => close(v) });
    const again = h('button', { type: 'button', class: 'btn', text: 'Retry', onclick: () => retry(v) });
    const files = h('button', { type: 'button', class: 'btn', text: 'Files', onclick: () => close(v, 'files') });
    v.dlg = h('dialog', { class: 'viewer', 'aria-label': v.att.name },
      h('figure', {}, v.picture, v.status,
        h('figcaption', {}, h('span', { class: 'nm', text: v.att.name }), h('span', { class: 'sz', text: bytes(v.att.size) })),
        h('div', { class: 'viewer-actions' }, again, cancel, files)),
      h('button', { type: 'button', class: 'btn vx', 'aria-label': 'Close', text: '×', onclick: () => close(v) }));
    v.onCancel = e => { if (liveViewer(v)) { e.preventDefault(); close(v); } };
    v.onClose = () => {
      if (closing === v) finishDialog(v);
      else if (liveViewer(v)) close(v);
    };
    v.dlg.addEventListener('cancel', v.onCancel);
    v.dlg.addEventListener('close', v.onClose);
    v.dlg.addEventListener('click', e => {
      if (e.target === v.dlg || e.target === v.dlg.firstElementChild) close(v);
    });
    viewer = v;
    document.body.append(v.dlg);
    v.dlg.showModal();
    viewerOwner(v);
    if (liveViewer(v)) { patchAll(); schedule(); }
  }

  function onVisibility() {
    if (dead) return;
    if (document.visibilityState !== 'visible') {
      for (const r of registrations.values()) { r.demanded = false; r.suppressed = false; }
      attempts([...owners.values()].filter(o => o.kind === 'inline').map(o => () => {
        const r = registrations.get(o.occurrence);
        if (r && o.lease?.state === 'error') r.failed = true;
        drop(o);
      }));
      patchAll();
    } else schedule();
  }

  listen(window, 'scroll', schedule, { passive: true });
  listen(window, 'resize', schedule, { passive: true });
  listen(document, 'visibilitychange', onVisibility);

  function release(onSettled) {
    wave ||= { observer: null, failed: false, error: undefined };
    releaseDepth++;
    const pending = activeWrites > 0;
    if (pending) releaseDebt++;
    dead = true; frozen = false; epoch++;
    if (viewer) { viewer.active = false; retiring.add(viewer); viewer = null; }
    if (closing) { retiring.add(closing); closing = null; }
    const actions = [
      () => {
        if (onSettled === undefined) return;
        if (typeof onSettled !== 'function' || (wave.observer && wave.observer !== onSettled)) throw new TypeError('Conflicting teardown observer');
        wave.observer = onSettled;
      },
      () => { if (frame !== null) { cancelAnimationFrame(frame); frame = null; } },
      () => { io?.disconnect(); io = null; }, () => { ro?.disconnect(); ro = null; },
      () => { viewport?.removeEventListener('scroll', schedule); viewport = null; },
      ...listeners.map(off => () => { off(); const index = listeners.indexOf(off); if (index !== -1) listeners.splice(index, 1); }),
      () => detachWhere(() => true),
      ...[...retiring].map(v => () => finishDialog(v)),
      // Always reach the model, including repeated and failed retirement.
      () => { if (!pending) releaseModel(); },
      () => {
        registrations.clear(); staging = null;
        composer = fallback = null;
        for (const [key, owner] of owners) {
          if (![...consumers].some(c => c.owner === owner)) owners.delete(key);
        }
      },
    ];
    let failed = false, first;
    try {
      for (const action of actions) {
        try { action(); } catch (error) { releaseError(error); if (!failed) { failed = true; first = error; } }
      }
    } finally { releaseDepth--; }
    try { settleRelease(); } catch (error) { if (!failed) { failed = true; first = error; } }
    if (failed) throw first;
  }

  return { beginRender, render, endRender, open, release,
    get stats() { return cache?.stats || Object.freeze({ owners: 0, entries: 0, queued: 0, loading: 0,
      residentEntries: 0, residentBytes: 0, fetches: 0, created: 0, revoked: 0 }); } };
}
