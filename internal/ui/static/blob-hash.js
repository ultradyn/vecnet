// SHA-256 of an attachment before a direct upload (R2 blobs plan, Task 7).
// The hub signs the digest into the provider PUT, so the bytes sent must be
// exactly the bytes hashed: hashFile returns both. Reading goes in bounded
// chunks with a yield between them, so a 25 MiB file never holds the page;
// the digest itself is Web Crypto's, which browsers run off the main thread.
// No MD5 and no hand-rolled hash: an unsupported browser fails closed.

export const CHUNK = 2 << 20; // 2 MiB a read

const abortError = () => new DOMException('The operation was aborted.', 'AbortError');
const notReadable = () => new DOMException('The file changed or could not be read.', 'NotReadableError');
// A macrotask, not a microtask: the page gets to paint and handle input.
const yieldToPage = () => new Promise(resolve => setTimeout(resolve, 0));

export function toHex(bytes) {
  let s = '';
  for (const b of bytes) s += b.toString(16).padStart(2, '0');
  return s;
}

export function toBase64(bytes) {
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s);
}

// hashFile reads file (a File or Blob) once into memory, chunk by chunk, and
// resolves { bytes, digest, hex, base64 }. onProgress(loaded, total) follows
// the read; signal aborts between chunks with an AbortError. A file that
// reads short or long (changed on disk since it was picked) is refused with a
// NotReadableError rather than hashed as something else.
export async function hashFile(file, { signal, onProgress, chunk = CHUNK, subtle = globalThis.crypto && globalThis.crypto.subtle } = {}) {
  const check = () => { if (signal && signal.aborted) throw abortError(); };
  check();
  if (!subtle || typeof subtle.digest !== 'function') throw new DOMException('Web Crypto is unavailable here; the page must be served over HTTPS or from localhost.', 'NotSupportedError');
  const size = file && file.size;
  if (!Number.isSafeInteger(size) || size < 0) throw notReadable();
  const step = Math.max(1, Math.min(chunk, CHUNK * 2));
  const bytes = new Uint8Array(size);
  for (let at = 0; at < size;) {
    const end = Math.min(size, at + step);
    let part;
    try { part = new Uint8Array(await file.slice(at, end).arrayBuffer()); } catch (e) { check(); throw notReadable(); }
    check();
    if (part.length !== end - at) throw notReadable();
    bytes.set(part, at);
    at = end;
    if (onProgress) onProgress(at, size);
    if (at < size) { await yieldToPage(); check(); }
  }
  const digest = new Uint8Array(await subtle.digest('SHA-256', bytes));
  check();
  return { bytes, digest, hex: toHex(digest), base64: toBase64(digest) };
}
