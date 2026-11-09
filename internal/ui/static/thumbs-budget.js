// Document-lifetime admission only. Payloads, transport cleanup and physical
// src detachment belong to callers; cancellation is never proof of settlement.
const DEFAULTS = Object.freeze({ B: 33554432, C: 2, E: 16, N: 16, Q: 16, M: 10485760 });

function object(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new TypeError('Expected an options object');
  return value;
}
function readLimits(options) {
  const input = object(options).limits;
  const source = input === undefined ? {} : object(input), result = {};
  for (const [name, fallback] of Object.entries(DEFAULTS)) {
    const configured = source[name], value = configured === undefined ? fallback : configured;
    if (!Number.isSafeInteger(value) || value <= 0) throw new RangeError(`Invalid thumbnail limit ${name}`);
    result[name] = value;
  }
  if (result.M > DEFAULTS.M || result.B < result.M) throw new RangeError('Invalid thumbnail byte limits');
  return result;
}
function validateKey(key) {
  if (typeof key !== 'string' || !key.length) throw new TypeError('Expected a nonempty owner key');
}
function validatePriority(priority) {
  if (priority !== 'automatic' && priority !== 'explicit') throw new TypeError('Invalid thumbnail priority');
}
function sourceCallback(options) {
  const callback = object(options).onCapacity;
  if (callback !== undefined && typeof callback !== 'function') throw new TypeError('Invalid capacity callback');
  return callback;
}
function invoke(callback, ...args) {
  try { if (callback) callback(...args); } catch { /* Owner exceptions cannot change admission accounting. */ }
}

export function createThumbBudget(options = {}) {
  const { B, C, E, N, Q, M } = readLimits(options);
  options = undefined; // Do not retain the caller's options/limits through owner closures.
  const tickets = new Map(), sources = new Map();
  // queue preserves original automatic acceptance order; explicit preserves
  // explicit acceptance/upgrade order without an ever-growing sequence counter.
  const queue = [], explicit = [];
  let running = 0, residentEntries = 0, residentBytes = 0;
  let pumping = false, capacityPending = false;

  function capacity() {
    if (capacityPending || !sources.size) return;
    capacityPending = true;
    queueMicrotask(() => {
      capacityPending = false;
      // A bounded delivery snapshot prevents reentrant claims extending this
      // pass forever. Fetch the current callback only when its turn arrives.
      for (const source of [...sources.values()]) {
        if (source.live) invoke(source.callback);
      }
    });
  }
  function canStart() {
    return running < C && running + residentEntries < E && M <= B - residentBytes - M * running;
  }
  function hasConcurrencySlot() {
    return running < C;
  }
  function unqueue(record) {
    queue.splice(queue.indexOf(record), 1);
    const index = explicit.indexOf(record);
    if (index !== -1) explicit.splice(index, 1);
  }
  function forget(record) {
    tickets.delete(record.key);
    record.key = null;
    record.state = 'dead';
    record.onStart = record.onCancel = null;
  }
  function removeQueued(record) {
    const callback = record.onCancel;
    unqueue(record);
    forget(record);
    return callback;
  }
  function start(record) {
    record.state = 'running';
    running++;
    const callback = record.onStart;
    record.onStart = null;
    invoke(callback, record.ticket);
  }
  function pump(first) {
    if (pumping) return;
    pumping = true;
    try {
      if (first) start(first);
      while (queue.length && canStart()) {
        const record = explicit[0] || queue[0];
        unqueue(record);
        // Previously waiting work releases a Q slot. Immediate admission below
        // never takes a Q slot and therefore emits no initial capacity hint.
        capacity();
        start(record);
      }
    } finally { pumping = false; }
  }

  // Factories have no enqueue/claim options in scope: retired handles cannot
  // accidentally retain the original callback-bearing argument objects.
  function makeTicket(record) {
    return Object.freeze({
      setPriority(priority) {
        validatePriority(priority);
        if (record.state !== 'queued' || record.priority === priority) return;
        if (record.priority === 'explicit') explicit.splice(explicit.indexOf(record), 1);
        record.priority = priority;
        if (priority === 'explicit') explicit.push(record);
        pump();
      },
      cancel() {
        if (record.state === 'queued') {
          const callback = removeQueued(record);
          capacity();
          invoke(callback, 'cancelled');
          pump();
        } else if (record.state === 'running' && !record.cancelled) {
          record.cancelled = true;
          const callback = record.onCancel;
          record.onStart = record.onCancel = null;
          invoke(callback, 'cancelled');
          pump();
        }
      },
      settle(result) {
        const value = object(result);
        if (!Object.prototype.hasOwnProperty.call(value, 'residentBytes')) throw new TypeError('Missing residentBytes');
        const bytes = value.residentBytes;
        if (bytes !== null && (!Number.isSafeInteger(bytes) || bytes < 0 || bytes > M)) throw new RangeError('Invalid residentBytes');
        if (record.state === 'queued') throw new Error('Cannot settle queued thumbnail work');
        if (record.state !== 'running') return;
        running--;
        record.onStart = record.onCancel = null;
        if (!record.cancelled && bytes !== null) {
          record.state = 'resident';
          record.bytes = bytes;
          residentEntries++;
          residentBytes += bytes;
        } else {
          forget(record);
        }
        capacity();
        pump();
      },
      dropPayload() {
        if (record.state !== 'resident') return;
        residentEntries--;
        residentBytes -= record.bytes;
        record.bytes = 0;
        forget(record);
        capacity();
        pump();
      },
    });
  }
  function enqueue(input) {
    const { key, priority, onStart, onCancel } = object(input);
    validateKey(key);
    validatePriority(priority);
    if (typeof onStart !== 'function' || typeof onCancel !== 'function') throw new TypeError('Invalid task callbacks');
    if (tickets.has(key)) return null;
    let victim;
    if (queue.length >= Q) {
      if (priority === 'explicit') {
        for (let index = queue.length - 1; index >= 0; index--) {
          if (queue[index].priority === 'automatic') { victim = queue[index]; break; }
        }
      }
      if (!victim) return null;
    }
    const record = { key, priority, onStart, onCancel, state: 'queued', cancelled: false, bytes: 0, ticket: null };
    record.ticket = makeTicket(record);
    // Complete the replacement before victim notification can reenter admission.
    const displaced = victim ? removeQueued(victim) : null;
    tickets.set(key, record);
    if (!victim && !queue.length && !pumping && canStart()) {
      pump(record);
    } else {
      queue.push(record);
      if (priority === 'explicit') explicit.push(record);
      invoke(displaced, 'displaced');
      pump();
    }
    return record.ticket;
  }

  function makePermit(source) {
    return Object.freeze({
      drop() {
        if (!source.live) return;
        sources.delete(source.key);
        source.live = false;
        source.key = source.callback = null;
        capacity();
      },
      move(nextOwnerKey, options = {}) {
        validateKey(nextOwnerKey);
        const callback = sourceCallback(options);
        if (!source.live) return;
        if (nextOwnerKey !== source.key && sources.has(nextOwnerKey)) throw new RangeError('Source owner already admitted');
        sources.delete(source.key);
        source.key = nextOwnerKey;
        source.callback = callback;
        sources.set(nextOwnerKey, source);
      },
    });
  }
  function claimSource(ownerKey, options = {}) {
    validateKey(ownerKey);
    const callback = sourceCallback(options);
    if (sources.has(ownerKey) || sources.size >= N) return null;
    const source = { key: ownerKey, callback, live: true };
    const permit = makePermit(source);
    sources.set(ownerKey, source);
    return permit;
  }
  return Object.freeze({
    enqueue,
    claimSource,
    hasConcurrencySlot,
    get stats() {
      return Object.freeze({ running, residentEntries, residentBytes, reservedBytes: M * running, queued: queue.length, sources: sources.size });
    },
  });
}
