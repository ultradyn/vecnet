import { isAbsolute } from 'node:path';
import type { Grant, PermissionDraft, PermissionRequest } from '../../backend.ts';
import { object } from './rpc.ts';

export const CANCEL_OPTION = 'vl-acp-cancel';
const grants: Record<string, Grant> = { allow_once: 'once', allow_always: 'always', reject_once: 'deny', reject_always: 'deny_always' };

/** ACP kind, command and locations are adapter-reported metadata, never a
 * sandbox boundary. Preserve raw params for the daemon's redaction/cap pass. */
export function permissionRequest(params: unknown, requestId: string): PermissionRequest {
  const p = object(params), tool = object(p.toolCall), raw = object(tool.rawInput);
  if (!Array.isArray(p.options) || p.options.length === 0 || p.options.length > 6) throw new Error('acp_permission_options');
  const seen = new Set<string>([CANCEL_OPTION]);
  const options = p.options.map(value => {
    const o = object(value);
    if (typeof o.optionId !== 'string' || !o.optionId || o.optionId.length > 128 || seen.has(o.optionId) ||
      typeof o.name !== 'string' || !o.name || o.name.length > 64 || typeof o.kind !== 'string' || !Object.hasOwn(grants, o.kind)) throw new Error('acp_permission_options');
    seen.add(o.optionId);
    return { id: o.optionId, label: o.name, grants: grants[o.kind] };
  });
  options.push({ id: CANCEL_OPTION, label: 'Cancel request', grants: 'cancel' });
  const name = typeof tool.name === 'string' ? tool.name : typeof raw.tool === 'string' ? raw.tool : undefined;
  const mcp = name?.startsWith('mcp__') || typeof tool.title === 'string' && tool.title.startsWith('mcp__');
  const kind: PermissionDraft['kind'] = mcp ? 'mcp_tool' : tool.kind === 'execute' ? 'exec' :
    ['edit', 'delete', 'move'].includes(String(tool.kind)) ? 'edit' : tool.kind === 'fetch' ? 'network' : 'other';
  const command = typeof raw.command === 'string' ? { line: raw.command } :
    Array.isArray(raw.argv) && raw.argv.length > 0 && raw.argv.every(v => typeof v === 'string') ? { argv: raw.argv as string[] } : undefined;
  const paths: NonNullable<PermissionDraft['paths']> = [];
  if (Array.isArray(tool.locations)) for (const value of tool.locations) {
    const location = object(value);
    if (typeof location.path !== 'string' || !isAbsolute(location.path)) throw new Error('acp_permission_location');
    paths.push({ path: location.path, access: tool.kind === 'delete' ? 'delete' : tool.kind === 'move' ? 'move' : tool.kind === 'edit' ? 'write' : 'read' });
  }
  let host: PermissionDraft['host'];
  if (tool.kind === 'fetch' && typeof raw.url === 'string') {
    try { const url = new URL(raw.url); host = { name: url.hostname, protocol: url.protocol.slice(0, -1), ...(url.port ? { port: Number(url.port) } : {}) }; }
    catch { /* Unknown destination remains unknown. */ }
  }
  const permission: PermissionDraft = {
    kind, backend_method: 'session/request_permission', ...(name ? { tool: name } : {}),
    ...(command ? { command: { ...command, ...(typeof raw.cwd === 'string' ? { cwd: raw.cwd } : {}) } } : {}),
    ...(paths.length ? { paths } : {}), ...(host ? { host } : {}),
    scope: options.some(o => o.grants === 'always') ? 'always' : 'once', options,
    backend_raw: params, ...(typeof tool.title === 'string' ? { reason: tool.title } : {}),
    fidelity: { kind: 'reported', command: command && 'argv' in command ? 'argv' : command ? 'line' : 'none', paths: paths.length ? 'reported' : 'none', host: host ? 'reported' : 'none' },
  };
  return { requestId, summary: 'ACP permission request', permission, options, defaultOnExpiry: CANCEL_OPTION };
}
