export interface RouteCheckDeps {
  fetch?: typeof fetch;
  readFile(path: string): Promise<string | Uint8Array>;
}

export async function checkRoute(
  route: { base_url: string; key_file?: string }, deps: RouteCheckDeps, signal?: AbortSignal,
): Promise<{ ok: boolean; models: string[]; detail?: string }> {
  let url: URL;
  try { url = new URL(route.base_url); } catch { return { ok: false, models: [], detail: 'not_loopback' }; }
  const host = url.hostname;
  const loopback = host === 'localhost' || host === '[::1]' || /^127(?:\.[0-9]{1,3}){3}$/.test(host);
  if (!loopback || !['http:', 'https:'].includes(url.protocol) || url.username || url.password) {
    return { ok: false, models: [], detail: 'not_loopback' };
  }
  try {
    const headers = new Headers();
    if (route.key_file) {
      const key = Buffer.from(await deps.readFile(route.key_file)).toString('utf8').trim();
      if (!key || /[\r\n]/.test(key)) return { ok: false, models: [], detail: 'invalid_key' };
      headers.set('authorization', `Bearer ${key}`);
    }
    url.pathname = `${url.pathname.replace(/\/+$/, '')}/models`;
    url.search = ''; url.hash = '';
    const response = await (deps.fetch ?? fetch)(url, { method: 'GET', headers, redirect: 'error', signal });
    if (!response.ok) return { ok: false, models: [], detail: 'route_unavailable' };
    const body: unknown = await response.json();
    if (!body || typeof body !== 'object' || !('data' in body) || !Array.isArray(body.data) || body.data.length === 0 ||
        !body.data.every(x => x && typeof x === 'object' && typeof x.id === 'string' && x.id.length > 0)) {
      return { ok: false, models: [], detail: 'invalid_models' };
    }
    // Model ids are display data too; never echo a key that a failing proxy reflects.
    const models = body.data.map((x: { id: string }) => x.id);
    const auth = headers.get('authorization')?.slice(7);
    if (auth && models.some(x => x.includes(auth))) return { ok: false, models: [], detail: 'invalid_models' };
    return { ok: true, models };
  } catch { return { ok: false, models: [], detail: signal?.aborted ? 'aborted' : 'route_unavailable' }; }
}
