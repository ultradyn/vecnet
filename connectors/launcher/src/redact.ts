import type { Log } from '../../hub-client/src/ports.ts';

// Pattern data from t3-bridge/src/redaction.mjs (scrubForDisplay), extended
// with the hub's own secret formats: vn_<kind>_ (auth.NewSecret), whsec_
// (webhook.NewSecret), vat_/vrt_/vac_ (OAuth tokens and codes) and rt_ (blob
// read tokens). No bridge code is imported.
/** Credentials only: bearer values and the token formats above. URLs are kept. */
export function redactSecrets(s: string): string {
  return s.replace(/Bearer\s+\S+/gi, 'Bearer [redacted]')
    // whsec_ keys are standard Base64 (+ / =); the hub's other secrets are base64url.
    .replace(/whsec_[A-Za-z0-9+/=_-]+/g, '[redacted]')
    .replace(/(?:crsr_|vn_[A-Za-z0-9]+_|\bv(?:at|rt|ac)_)[A-Za-z0-9_-]+/g, '[redacted]')
    // Blob read tokens: base64url of a payload far longer than any identifier.
    .replace(/\brt_[A-Za-z0-9_-]{32,}/g, '[redacted]');
}
export function redactText(s: string): { text: string; changed: boolean } {
  const text = redactSecrets(s).replace(/https?:\/\/[^\s)]+/gi, url => {
    try { return `${new URL(url).origin}/[redacted]`; } catch { return '[redacted-url]'; }
  });
  return { text, changed: text !== s };
}

/** Every string (and key) in a JSON-like value with credentials redacted, structurally. */
export function redactSecretsIn<T>(value: T): T {
  if (typeof value === 'string') return redactSecrets(value) as T;
  if (Array.isArray(value)) return value.map(v => redactSecretsIn(v)) as T;
  if (value && typeof value === 'object') return Object.fromEntries(Object.entries(value).map(([k, v]) => [redactSecrets(k), redactSecretsIn(v)])) as T;
  return value;
}

export function scrubEnv(env: Readonly<Record<string, string | undefined>>, allow: string[]): Record<string, string> {
  return Object.fromEntries(Object.entries(env).filter(([key, value]) => value !== undefined &&
    (allow.includes(key) || !/^(?:VECNET_.*|.*_(?:API_KEY|AUTH_TOKEN|BASE_URL|SECRET|TOKEN|PASSWORD))$/i.test(key)))) as Record<string, string>;
}

export function redactingLog(inner: Log): Log {
  const forbidden = /^(text|body|content|prompt|credential|secret|token)$/i;
  function scrub(value: unknown, seen = new Set<object>()): unknown {
    if (typeof value === 'string') return redactText(value).text;
    if (value === null || typeof value !== 'object') return value;
    if (seen.has(value)) return '[circular]';
    seen.add(value);
    const result: unknown = Array.isArray(value) ? value.map(v => scrub(v, seen)) :
      Object.fromEntries(Object.entries(value).filter(([k]) => !forbidden.test(k)).map(([k,v]) => [redactText(k).text, scrub(v, seen)]));
    seen.delete(value);
    return result;
  }
  const write = (level: keyof Log) => (msg: string, meta?: Record<string, unknown>) =>
    inner[level](redactText(msg).text, meta === undefined ? undefined : scrub(meta) as Record<string, unknown>);
  return { info: write('info'), warn: write('warn'), error: write('error') };
}
