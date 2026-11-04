// Deployed-hub checks. Keep diagnostics static: responses and fetch errors may
// contain credentials, so neither is ever written to stdout or stderr.
const current = '2026-07-28';
const token = process.env.VECNET_MCP_TOKEN;
let origin;
try {
  const url = new URL(process.argv[2]);
  if (process.argv.length !== 3 || !['https:', 'http:'].includes(url.protocol)
      || url.username || url.password || url.pathname !== '/' || url.search || url.hash || !token) {
    throw new Error('invalid input');
  }
  origin = url.origin;
} catch {
  console.log('FAIL input (origin and VECNET_MCP_TOKEN required)');
  process.exit(1);
}

let failed = false;
let authenticationFailed = false;
let id = 0;
function requireCheck(condition) {
  if (!condition) throw new Error('check failed');
}
function object(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}
async function check(name, fn) {
  try {
    await fn();
    console.log(`PASS ${name}`);
  } catch {
    failed = true;
    console.log(`FAIL ${name}`);
  }
}
async function request(path, { authenticated = false, ...options } = {}) {
  // After the first failed bearer, skip subsequent authenticated checks. This
  // bounds unauthenticated /mcp failures to two even for an invalid token.
  if (authenticated && authenticationFailed) throw new Error('authentication failed');
  const response = await fetch(origin + path, {
    ...options,
    headers: { ...(authenticated ? { Authorization: `Bearer ${token}` } : {}), ...options.headers },
    redirect: 'error',
    signal: AbortSignal.timeout(5000),
  });
  if (authenticated && response.status === 401) authenticationFailed = true;
  const body = await response.text();
  return { response, body };
}
async function metadata(path) {
  const { response, body } = await request(path);
  requireCheck(response.status === 200);
  return JSON.parse(body);
}
async function rpc(method, params = {}, { legacy = false, errorExpected = false } = {}) {
  const requestID = ++id;
  const { response, body } = await request('/mcp', {
    authenticated: true,
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Accept: 'application/json, text/event-stream',
      ...(!legacy ? { 'MCP-Protocol-Version': current } : {}),
    },
    body: JSON.stringify({
      jsonrpc: '2.0', id: requestID, method,
      params: legacy ? params : { ...params, _meta: { 'io.modelcontextprotocol/protocolVersion': current } },
    }),
  });
  requireCheck(response.status === 200);
  const message = JSON.parse(body);
  requireCheck(message.jsonrpc === '2.0' && message.id === requestID);
  if (errorExpected) {
    requireCheck(object(message.error) && message.result === undefined);
    return message.error;
  }
  requireCheck(message.error === undefined && object(message.result));
  return message.result;
}

await check('unauthenticated challenge', async () => {
  const { response } = await request('/mcp', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: 0, method: 'server/discover' }),
  });
  requireCheck(response.status === 401);
  requireCheck(response.headers.get('www-authenticate')?.includes(
    `resource_metadata="${origin}/.well-known/oauth-protected-resource"`));
});
await check('protected-resource metadata', async () => {
  const metadataResponse = await metadata('/.well-known/oauth-protected-resource');
  requireCheck(metadataResponse.resource === `${origin}/mcp`);
});
await check('authorization-server metadata', async () => {
  const metadataResponse = await metadata('/.well-known/oauth-authorization-server');
  requireCheck(Array.isArray(metadataResponse.code_challenge_methods_supported)
    && metadataResponse.code_challenge_methods_supported.includes('S256'));
  requireCheck(typeof metadataResponse.registration_endpoint === 'string' && metadataResponse.registration_endpoint.length > 0);
  requireCheck(metadataResponse.client_id_metadata_document_supported === true);
  requireCheck(metadataResponse.authorization_response_iss_parameter_supported === true);
});
await check('server/discover', async () => {
  const result = await rpc('server/discover');
  requireCheck(object(result.capabilities?.events) && result.resultType === 'complete');
});
await check('initialize older revision', async () => {
  // PROTOCOL §4.1 requires rejection carrying the newest supported version.
  // Task 9 instead asks for legacy negotiation; leave that conflict visible
  // until the coordinator resolves the protocol contract.
  const error = await rpc('initialize', {
    protocolVersion: '2025-03-26', capabilities: {},
    clientInfo: { name: 'vecnet-mcp-conformance', version: '1' },
  }, { legacy: true, errorExpected: true });
  requireCheck(Array.isArray(error.data?.supportedVersions) && error.data.supportedVersions.includes(current));
});
await check('tools/list', async () => {
  const result = await rpc('tools/list');
  requireCheck(Array.isArray(result.tools) && result.tools.length > 0);
  requireCheck(result.tools.every(tool => Array.isArray(tool.securitySchemes) && tool.securitySchemes.length > 0));
  const whoami = result.tools.find(tool => tool.name === 'vecnet_whoami');
  requireCheck(whoami?._meta?.['openai/profile'] === true);
});
await check('events/list', async () => {
  const result = await rpc('events/list');
  requireCheck(Array.isArray(result.events));
  const event = result.events.find(event => event.name === 'vecnet.message');
  requireCheck(Array.isArray(event?.delivery) && event.delivery.length === 1 && event.delivery[0] === 'webhook');
});
await check('invalid Origin', async () => {
  // Skip after a rejected bearer so a broken Origin guard cannot spend a
  // third unauthenticated request from the client's rate-limit budget.
  const { response } = await request('/mcp', {
    method: 'POST',
    authenticated: true,
    headers: { Origin: 'https://evil.example', 'Content-Type': 'application/json' },
    body: JSON.stringify({ jsonrpc: '2.0', id: ++id, method: 'server/discover' }),
  });
  requireCheck(response.status === 403);
});
await check('GET without a session', async () => {
  const { response } = await request('/mcp', { authenticated: true, headers: { Accept: 'text/event-stream' } });
  requireCheck(response.status === 405);
});
process.exitCode = failed ? 1 : 0;
