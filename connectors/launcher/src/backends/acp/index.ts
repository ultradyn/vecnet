import { createHash, randomUUID } from 'node:crypto';
import { access, constants } from 'node:fs/promises';
import { isAbsolute, basename } from 'node:path';
import { definePlugin, MAX_TURN_INPUT_BYTES } from '../../backend.ts';
import type { ActivityReport, Backend, BackendCapabilities, LaunchSpec, PluginContext, SessionHandle, SessionHooks,
  SessionRef, SessionState, TurnCursor, TurnEvent, TurnInput, SendMode, SendResult, SettleMode, SettleResult } from '../../backend.ts';
import { scrubEnv } from '../../redact.ts';
import { AcpRpc, object } from './rpc.ts';
import type { RpcId } from './rpc.ts';
import { CANCEL_OPTION, permissionRequest } from './permission.ts';
import { allocate, workspaceModes, workspaceRoot } from './workspace.ts';
import type { AcpLaunchSpec, Workspace } from './workspace.ts';

export interface AcpPreset {
  /** Operator-pinned ACP executable and arguments, never a shell command. */
  argv: string[];
  provider: string;
  /** Agent's advertised mode id implementing ask; e.g. Claude's default mode.
   * No inference from display labels, and no silent fallback to bypass mode. */
  mode_id: string;
}
export interface AcpConfig { presets: Record<string, AcpPreset>; workspace_root?: string; request_timeout_ms?: number }
interface Owned {
  tag: string; host: string; owner: string; jobId: string; preset: string;
  phase: 'intent' | 'live' | 'lost' | 'settled'; id?: string; workspace?: Workspace; exitConfirmed?: boolean; fingerprint: string;
}
interface Active { id: string; consumed: string[]; text: string; messageId?: string; afterTool: boolean; cancelSent: boolean; done: Promise<void> }
const configSchema = {
  type: 'object', additionalProperties: false, required: ['presets'], properties: {
    workspace_root: { type: 'string', minLength: 1 }, request_timeout_ms: { type: 'integer', minimum: 100, maximum: 60000 },
    presets: { type: 'object', minProperties: 1, additionalProperties: {
      type: 'object', additionalProperties: false, required: ['argv', 'provider', 'mode_id'], properties: {
        argv: { type: 'array', minItems: 1, items: { type: 'string', minLength: 1 } },
        provider: { type: 'string', minLength: 1 }, mode_id: { type: 'string', minLength: 1 },
      },
    } },
  },
};
const cancelled = () => ({ outcome: { outcome: 'cancelled' } });
const hash = (value: unknown) => createHash('sha256').update(JSON.stringify(value)).digest('hex');

export default definePlugin<AcpConfig>({
  id: 'acp', configSchema,
  async create(config, ctx) { return new AcpBackend(config, ctx); },
});

class AcpBackend implements Backend {
  readonly id = 'acp';
  readonly config: AcpConfig;
  readonly ctx: PluginContext;
  private readonly owner = randomUUID();
  private readonly root: string;
  private readonly handles = new Map<string, AcpSession>();
  private readonly launching = new Map<string, Promise<SessionHandle>>();
  constructor(config: AcpConfig, ctx: PluginContext) {
    this.config = config; this.ctx = ctx; this.root = workspaceRoot(config.workspace_root);
    if (!Object.keys(config.presets).length) throw new Error('acp_presets_required');
    for (const preset of Object.values(config.presets)) {
      if (!preset.argv.length || !isAbsolute(preset.argv[0]) || preset.argv.some(v => !v || v.includes('\0')) ||
        !preset.provider || !preset.mode_id || /codex/i.test(preset.provider + ' ' + basename(preset.argv[0])) ||
        /bypass|full.access|accept.?edits|auto/i.test(preset.mode_id)) throw new Error('acp_invalid_preset');
    }
  }
  private key(tag: string) { return 'session:' + tag; }
  save(owned: Owned): void { this.ctx.store.set(this.key(owned.tag), JSON.stringify(owned)); }
  private saved(tag: string): Owned | undefined {
    const raw = this.ctx.store.get(this.key(tag)); return raw ? JSON.parse(raw) as Owned : undefined;
  }
  private records(): Owned[] { return this.ctx.store.keys('session:').map(k => JSON.parse(this.ctx.store.get(k)!) as Owned); }
  async capabilities(): Promise<BackendCapabilities & { workspaceModes: typeof workspaceModes }> {
    return { steer: false, queue: 'launcher', interrupt: true, approvals: 'typed', questions: 'text-only', resume: 'none',
      backgroundTasks: 'opaque', attachments: false, workspace: 'launcher-dir', workspaceModes, branchName: 'settable',
      maxInputBytes: MAX_TURN_INPUT_BYTES, permissionModes: ['ask'],
      presets: Object.entries(this.config.presets).map(([id, p]) => ({ id, provider: p.provider, model: 'agent-default', note: 'Agent default model; no model override or authentication performed by ACP backend' })),
    };
  }
  async health(signal?: AbortSignal) {
    if (signal?.aborted) throw new Error('acp_health_aborted');
    const degraded: string[] = [];
    for (const [id, p] of Object.entries(this.config.presets)) {
      try { await access(p.argv[0], constants.X_OK); }
      catch { degraded.push(this.ctx.redact(`ACP executable unavailable for preset ${id}`)); }
    }
    return { ok: degraded.length === 0, version: 'acp-v1', degraded,
      detail: { probe: 'executable-access-only', authentication: 'operator-preconfigured; not probed', restart: 'no transcript or live process recovery after daemon restart' } };
  }
  launch(spec: LaunchSpec, hooks: SessionHooks, signal?: AbortSignal): Promise<SessionHandle> {
    if (!/^job_[A-Za-z0-9_]+$/.test(spec.jobId) || spec.tag !== `vl-${spec.jobId}` || spec.permissionMode !== 'ask' ||
      !Object.hasOwn(this.config.presets, spec.preset) || spec.attachments.length || !spec.prompt ||
      Buffer.byteLength(spec.prompt) > MAX_TURN_INPUT_BYTES) return Promise.reject(new Error('acp_launch_spec_unsupported'));
    const pending = this.launching.get(spec.tag);
    if (pending) {
      if (this.saved(spec.tag)?.fingerprint !== hash(spec)) return Promise.reject(new Error('acp_launch_identity_conflict'));
      return pending;
    } // Reserve synchronously before any workspace/process work.
    const previous = this.saved(spec.tag);
    if (previous) {
      if (previous.fingerprint !== hash(spec)) return Promise.reject(new Error('acp_launch_identity_conflict'));
      const h = this.handles.get(spec.tag);
      if (h?.live && previous.owner === this.owner && previous.phase === 'live') { h.hooks = hooks; return Promise.resolve(h); }
      return Promise.reject(new Error('acp_launch_outcome_unknown: persisted intent forbids duplicate spawn'));
    }
    const work = this.start(spec as AcpLaunchSpec, hooks, signal).finally(() => this.launching.delete(spec.tag));
    this.launching.set(spec.tag, work);
    return work;
  }
  private async start(spec: AcpLaunchSpec, hooks: SessionHooks, signal?: AbortSignal): Promise<SessionHandle> {
    if (signal?.aborted) throw new Error('acp_launch_aborted');
    const owned: Owned = { tag: spec.tag, jobId: spec.jobId, preset: spec.preset, host: this.ctx.host, owner: this.owner, phase: 'intent', fingerprint: hash(spec) };
    this.save(owned); // Crash fence is durable BEFORE allocation, spawn or session/new.
    let rpc: AcpRpc | undefined;
    try {
      owned.workspace = await allocate(spec, this.root, this.ctx, signal); this.save(owned);
      const preset = this.config.presets[spec.preset];
      if (signal?.aborted) throw new Error('acp_launch_aborted');
      rpc = new AcpRpc(preset.argv, owned.workspace.path, scrubEnv({ ...process.env, ...spec.env }, []), this.ctx);
      const h = new AcpSession(owned, rpc, hooks, this);
      this.handles.set(spec.tag, h);
      const timeout = this.config.request_timeout_ms ?? 15000;
      const init = object(await rpc.call('initialize', { protocolVersion: 1, clientCapabilities: {}, clientInfo: { name: 'vecnet-launcher', version: '1' } }, timeout, signal));
      if (init.protocolVersion !== 1 || /codex/i.test(String(object(init.agentInfo).name ?? ''))) throw new Error('acp_protocol_or_agent_unsupported');
      // Authentication is provisioned by the operator. Never read auth files,
      // run authenticate, or change account routes from this plugin.
      const session = object(await rpc.call('session/new', { cwd: owned.workspace.path, mcpServers: [] }, timeout, signal));
      if (typeof session.sessionId !== 'string' || !session.sessionId || session.sessionId.length > 512) throw new Error('acp_invalid_session_id');
      owned.id = session.sessionId; this.save(owned);
      const modes = object(session.modes);
      if (!Array.isArray(modes.availableModes) || !modes.availableModes.some(m => object(m).id === preset.mode_id)) throw new Error('acp_ask_mode_unavailable');
      await rpc.call('session/set_mode', { sessionId: owned.id, modeId: preset.mode_id }, timeout, signal);
      h.ready(); owned.phase = 'live'; this.save(owned);
      h.initial(spec.prompt); // session/prompt returns at TURN END, not acceptance.
      return h;
    } catch {
      owned.phase = 'lost';
      // Teardown is mandatory even when a ledger write fails.
      try { this.save(owned); } finally { if (rpc) await rpc.stop(); }
      throw new Error('acp_launch_failed: workspace retained; launch fence forbids respawn');
    }
  }
  async reattach(ref: SessionRef, hooks: SessionHooks) {
    if (ref.backend !== 'acp' || ref.host !== this.ctx.host || typeof ref.extra?.tag !== 'string') return { found: 'none' } as const;
    const owned = this.saved(ref.extra.tag);
    if (!owned || owned.host !== this.ctx.host || owned.id !== ref.id || owned.phase === 'settled') return { found: 'none' } as const;
    const h = this.handles.get(owned.tag);
    if (owned.owner === this.owner && h?.live && owned.phase === 'live') { h.hooks = hooks; return { found: 'live', handle: h } as const; }
    // A PID or saved session id cannot establish surviving stdio ownership.
    // Throw means unreachable/unknown, not an explicit miss or a new session.
    throw new Error('acp_reconnect_outcome_unknown: stdio owner unavailable');
  }
  async findByTag(tag: string): Promise<SessionRef | null> {
    const o = this.saved(tag);
    if (!o || o.host !== this.ctx.host || o.phase === 'settled') return null;
    const h = this.handles.get(tag);
    if (o.owner === this.owner && h?.live && o.phase === 'live') return h.ref;
    throw new Error('acp_launch_outcome_unknown: cannot discover a prior stdio process');
  }
  async listOwned(): Promise<SessionRef[]> {
    const live: SessionRef[] = [];
    for (const o of this.records()) {
      if (o.host !== this.ctx.host || o.phase === 'settled' || o.phase === 'lost' && o.exitConfirmed) continue;
      const h = this.handles.get(o.tag);
      if (o.owner !== this.owner || !h?.live || o.phase !== 'live') throw new Error('acp_ownership_unknown');
      live.push(h.ref);
    }
    return live;
  }
}

class AcpSession implements SessionHandle {
  hooks: SessionHooks;
  private readonly owned: Owned;
  private readonly rpc: AcpRpc;
  private readonly backend: AcpBackend;
  private current: SessionState = { kind: 'starting' };
  private active?: Active;
  private closing = false;
  private lastActivity: string;
  private sequence = 0;
  private readonly approvals = new Map<string, { controller: AbortController; cancel(): void }>();
  private readonly wakes = new Set<() => void>();
  private settling?: Promise<SettleResult>;
  constructor(owned: Owned, rpc: AcpRpc, hooks: SessionHooks, backend: AcpBackend) {
    this.owned = owned; this.rpc = rpc; this.hooks = hooks; this.backend = backend; this.lastActivity = this.now();
    rpc.onNotification = (method, params) => this.notice(method, params);
    rpc.onRequest = (method, params, id) => this.request(method, params, id);
    rpc.onLost = () => {
      this.cancelApprovals();
      if (!this.closing) { this.owned.phase = 'lost'; this.backend.save(this.owned); this.stateTo({ kind: 'ended', reason: 'lost', resumable: false }); }
      this.wake();
    };
    rpc.onExit = code => { this.owned.exitConfirmed = true; this.backend.save(this.owned); this.emit({ type: 'process_exited', at: this.now(), code, resumable: false }); this.wake(); };
  }
  get live() { return this.rpc.alive && !this.closing; }
  get ref(): SessionRef {
    if (!this.owned.id || !this.owned.workspace) throw new Error('acp_session_not_ready');
    return { backend: 'acp', host: this.backend.ctx.host, id: this.owned.id,
      extra: { tag: this.owned.tag, workspace: this.owned.workspace.path, workspace_mode: this.owned.workspace.mode,
        ...(this.owned.workspace.branch ? { branch: this.owned.workspace.branch } : {}), permission_mode: 'ask', owner: this.owned.owner } };
  }
  private now() { return this.backend.ctx.now().toISOString(); }
  private prefix() { return 'event:' + this.owned.tag + ':'; }
  private wake() { for (const wake of this.wakes) wake(); this.wakes.clear(); }
  private emit(event: TurnEvent) {
    this.lastActivity = this.now();
    const n = ++this.sequence;
    this.backend.ctx.store.set(this.prefix() + n, JSON.stringify(event));
    this.wake();
  }
  private stateTo(state: SessionState) { this.current = state; this.emit({ type: 'state', state }); }
  ready() { this.stateTo({ kind: 'idle', since: this.now() }); }
  state(): SessionState { return this.current; }
  initial(text: string) { this.start(text, [], 'initial', hash(text)); }
  private start(text: string, consumed: string[], inputId: string, payloadHash: string): SendResult {
    if (!this.live || this.current.kind !== 'idle') throw new Error('acp_session_busy_or_ended');
    const id = this.owned.tag + '-turn-' + randomUUID(), result: SendResult = { mode: 'start', backendInputId: inputId, turnId: id };
    // A receipt is persisted before writing the non-idempotent ACP prompt.
    // It means submitted/possibly submitted, never proof of model consumption.
    const receipt = JSON.stringify({ payloadHash, consumed, result });
    this.backend.ctx.store.set('sent:' + this.owned.tag + ':' + inputId, receipt);
    for (const delivery of consumed) this.backend.ctx.store.set('delivery:' + this.owned.tag + ':' + delivery, receipt);
    const active: Active = { id, consumed, text: '', afterTool: false, cancelSent: false, done: Promise.resolve() };
    this.active = active;
    try {
      this.stateTo({ kind: 'turn_running', turnId: id, since: this.now() });
      this.emit({ type: 'turn_started', turnId: id, at: this.now(), consumed });
    } catch {
      this.owned.phase = 'lost';
      void this.rpc.stop().catch(() => {});
      throw new Error('acp_prompt_journal_failed');
    }
    active.done = this.rpc.call('session/prompt', { sessionId: this.owned.id, prompt: [{ type: 'text', text }] }).then(
      response => this.finished(active, object(response).stopReason),
      () => this.failed(active),
    ).catch(() => {
      this.owned.phase = 'lost';
      try { this.backend.save(this.owned); } finally { void this.rpc.stop().catch(() => {}); }
    }).catch(() => {});
    return result;
  }
  async send(input: TurnInput, mode: SendMode): Promise<SendResult> {
    if (mode !== 'start') throw new Error('acp_no_steer_or_native_queue');
    if (!this.live || !input.items.length || input.items.some(i => !i.deliveryId || i.attachments.length)) throw new Error('acp_input_unsupported');
    const ids = input.items.map(i => i.deliveryId);
    if (new Set(ids).size !== ids.length) throw new Error('acp_duplicate_delivery');
    const text = input.items.map(i => i.text).join('\n\n');
    if (Buffer.byteLength(text) > MAX_TURN_INPUT_BYTES) throw new Error('input_too_large');
    const payloadHash = hash(input.items), inputId = hash(ids), key = 'sent:' + this.owned.tag + ':' + inputId;
    const receipts = [this.backend.ctx.store.get(key), ...ids.map(id => this.backend.ctx.store.get('delivery:' + this.owned.tag + ':' + id))].filter((x): x is string => x !== undefined);
    if (receipts.length) {
      const receipt = JSON.parse(receipts[0]) as { payloadHash: string; consumed: string[]; result: SendResult };
      if (receipt.payloadHash !== payloadHash || JSON.stringify(receipt.consumed) !== JSON.stringify(ids) || receipts.some(r => r !== receipts[0])) throw new Error('acp_delivery_receipt_conflict');
      return receipt.result;
    }
    return this.start(text, ids, inputId, payloadHash);
  }
  private finished(active: Active, reason: unknown) {
    if (this.active !== active) return;
    const outcome = reason === 'end_turn' ? 'completed' : reason === 'cancelled' ? 'interrupted' : reason === 'refusal' ? 'refused' :
      reason === 'max_tokens' ? 'max_tokens' : 'failed';
    if (!['end_turn', 'cancelled', 'refusal', 'max_tokens', 'max_turn_requests'].includes(String(reason))) { this.failed(active); return; }
    this.cancelApprovals(); this.active = undefined;
    this.emit({ type: 'turn_finished', turnId: active.id, at: this.now(), outcome, finalText: active.text, consumed: active.consumed });
    if (!this.closing && this.rpc.alive) this.stateTo({ kind: 'idle', since: this.now() });
  }
  private failed(active: Active) {
    if (this.active !== active) return;
    this.cancelApprovals(); this.active = undefined;
    // JSON-RPC rejection/loss is not an ACP stop reason, and does not prove
    // consumption or a finished turn. End as lost, preserving uncertain input.
    this.owned.phase = 'lost'; this.backend.save(this.owned);
    this.stateTo({ kind: 'ended', reason: 'lost', resumable: false });
    void this.rpc.stop().catch(() => {});
  }
  private notice(method: string, params: unknown) {
    if (method !== 'session/update') return;
    const p = object(params);
    if (p.sessionId !== this.owned.id) return;
    const u = object(p.update), active = this.active;
    this.lastActivity = this.now();
    if (u.sessionUpdate === 'current_mode_update' && this.owned.phase === 'live' &&
      u.currentModeId !== this.backend.config.presets[this.owned.preset].mode_id) throw new Error('acp_permission_mode_changed');
    if (!active) return;
    if (u.sessionUpdate === 'agent_message_chunk') {
      const content = object(u.content);
      if (content.type !== 'text' || typeof content.text !== 'string') return;
      const messageId = typeof u.messageId === 'string' ? u.messageId : undefined;
      if (active.afterTool || messageId && active.messageId && active.messageId !== messageId) active.text = '';
      active.afterTool = false; active.messageId = messageId;
      if (Buffer.byteLength(active.text) + Buffer.byteLength(content.text) > 4 * 1024 * 1024) throw new Error('acp_output_limit');
      active.text += content.text;
      // Progress contains the actual streamed text; thoughts and raw tool
      // arguments are not copied into finalText or logs.
      this.emit({ type: 'progress', turnId: active.id, summary: content.text });
    } else if (u.sessionUpdate === 'tool_call' || u.sessionUpdate === 'tool_call_update' || u.sessionUpdate === 'plan') {
      active.afterTool = true;
      this.emit({ type: 'progress', turnId: active.id, summary: u.sessionUpdate === 'plan' ? 'ACP plan updated' : 'ACP tool ' + (typeof u.status === 'string' ? u.status : 'pending') });
    }
  }
  private async request(method: string, params: unknown, id: RpcId): Promise<unknown> {
    const p = object(params);
    if (method !== 'session/request_permission') throw new Error('acp_client_method_unsupported');
    if (!this.active || this.active.cancelSent || this.closing || p.sessionId !== this.owned.id) return cancelled();
    const requestId = this.owned.tag + ':' + (typeof id) + ':' + String(id), controller = new AbortController();
    let cancel!: () => void;
    const stopped = new Promise<undefined>(resolve => { cancel = () => { controller.abort(); resolve(undefined); }; });
    this.approvals.set(requestId, { controller, cancel });
    this.stateTo({ kind: 'waiting_approval', requestIds: [...this.approvals.keys()] });
    try {
      const req = permissionRequest(params, requestId);
      const decision = await Promise.race([this.hooks.onPermissionRequest(req, controller.signal), stopped]);
      if (!decision || controller.signal.aborted || decision.optionId === CANCEL_OPTION || !req.options.some(o => o.id === decision.optionId)) return cancelled();
      return { outcome: { outcome: 'selected', optionId: decision.optionId } };
    } catch { return cancelled(); }
    finally {
      this.approvals.delete(requestId);
      if (this.active && !this.closing && this.rpc.alive) this.stateTo(this.approvals.size ? { kind: 'waiting_approval', requestIds: [...this.approvals.keys()] } :
        { kind: 'turn_running', turnId: this.active.id, since: this.lastActivity });
    }
  }
  private cancelApprovals() { for (const a of this.approvals.values()) a.cancel(); this.approvals.clear(); }
  async interrupt(_reason: string): Promise<void> {
    const active = this.active;
    if (!active) return;
    if (!this.rpc.alive) throw new Error('acp_cancel_outcome_unknown');
    active.cancelSent = true; this.cancelApprovals();
    this.rpc.notify('session/cancel', { sessionId: this.owned.id });
    // State remains busy until the original prompt returns cancelled (or
    // another real stop reason). Sending a notification is not an ack.
  }
  async activity(): Promise<ActivityReport> {
    return { turnRunning: !!this.active, openApprovals: this.approvals.size, queuedInputs: 0,
      backgroundTasks: { known: 0, observable: false }, lastActivityAt: this.lastActivity };
  }
  async *turns(after?: TurnCursor, signal?: AbortSignal): AsyncIterable<{ cursor: TurnCursor; event: TurnEvent }> {
    let position = 0;
    if (after) {
      let cursor: { tag?: unknown; n?: unknown };
      try { cursor = object(JSON.parse(after.value)); } catch { throw new Error('acp_cursor_invalid'); }
      if (cursor.tag !== this.owned.tag || typeof cursor.n !== 'number' || !Number.isSafeInteger(cursor.n) || cursor.n < 0 || cursor.n > this.sequence) throw new Error('acp_cursor_invalid');
      position = cursor.n;
    }
    while (!signal?.aborted) {
      while (position < this.sequence && !signal?.aborted) {
        const raw = this.backend.ctx.store.get(this.prefix() + (++position));
        if (!raw) throw new Error('acp_event_history_gap');
        yield { cursor: { value: JSON.stringify({ tag: this.owned.tag, n: position }) }, event: JSON.parse(raw) as TurnEvent };
      }
      if (this.current.kind === 'ended' || signal?.aborted) return;
      await new Promise<void>(resolve => {
        const done = () => { this.wakes.delete(done); signal?.removeEventListener('abort', done); resolve(); };
        this.wakes.add(done); signal?.addEventListener('abort', done, { once: true });
        if (signal?.aborted || position < this.sequence || this.current.kind === 'ended') done();
      });
    }
  }
  settle(mode: SettleMode): Promise<SettleResult> { return this.settling ??= this.settleOnce(mode).catch(error => { this.settling = undefined; throw error; }); }
  private async settleOnce(mode: SettleMode): Promise<SettleResult> {
    const active = this.active;
    if (active && this.rpc.alive) {
      await this.interrupt(mode);
      // Give pending permission cancelled outcomes and the original prompt's
      // cancellation response time to drain before terminating the process.
      const deadline = new AbortController();
      try { await Promise.race([active.done, this.backend.ctx.sleep(1000, deadline.signal)]); }
      finally { deadline.abort(); }
    }
    this.closing = true; this.cancelApprovals();
    await this.rpc.stop();
    this.owned.phase = 'settled'; this.backend.save(this.owned);
    this.stateTo({ kind: 'ended', reason: 'settled', resumable: false });
    return { ...(this.owned.workspace?.branch ? { branch: this.owned.workspace.branch } : {}), workspaceKept: true };
  }
}
