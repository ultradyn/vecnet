import type { Log } from '../../hub-client/src/ports.ts';

// Pattern data from t3-bridge/src/redaction.mjs (scrubForDisplay), extended
// with vecnet auth.NewSecret and webhook secrets. No bridge code is imported.
export function redactText(s: string): { text: string; changed: boolean } {
  const text = s.replace(/Bearer\s+\S+/gi, 'Bearer [redacted]')
    .replace(/(?:crsr_|whsec_|vn_[A-Za-z0-9]+_)[A-Za-z0-9_-]+/g, '[redacted]')
    .replace(/https?:\/\/[^\s)]+/gi, url => {
      try { return `${new URL(url).origin}/[redacted]`; } catch { return '[redacted-url]'; }
    });
  return { text, changed: text !== s };
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
