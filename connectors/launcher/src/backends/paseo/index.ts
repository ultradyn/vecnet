import { mkdtemp, mkdir, writeFile, rm, realpath } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve, relative, isAbsolute } from 'node:path';
import { definePlugin, localPlatform } from '../../backend.ts';
import type { Backend, BackendCapabilities, BackendHealth, PluginContext, SessionRef, SessionHooks, SessionHandle, SessionState, TurnEvent, TurnInput, SendMode, LaunchSpec } from '../../backend.ts';
import { Bridge, fullAccessModes } from './bridge.ts';
import type { PaseoConfig, Agent, Preset } from './bridge.ts';
import { hash, parseCursor, finalBlock } from './transcript.ts';
export type { PaseoConfig } from './bridge.ts';

const units = ['paseo-bot.service', 'paseo-bridge-watch.service', 'paseo-cache-prune.service'];
const live = (a: Agent) => !a.archived && !a.archivedAt && !['archived', 'ended', 'settled', 'exited'].includes(a.status ?? '');
const owns = (name: string, tag: string) => tag.startsWith('vl-') && (name === tag || name.startsWith(`${tag} `));
const inside = (root: string, path: string) => { const rel = relative(root, path); return rel !== '' && rel !== '..' && !rel.startsWith('../') && !rel.startsWith('..\\') && !isAbsolute(rel); };

class PaseoBackend implements Backend {
  readonly id = 'paseo';
  readonly bridge: Bridge;
  readonly ctx: PluginContext;
  readonly config: PaseoConfig;
  constructor(config: PaseoConfig, ctx: PluginContext) { this.config = config; this.ctx = ctx; this.bridge = new Bridge(config, ctx); }
  save(ref: SessionRef) {
    this.ctx.store.set(`ref:${ref.extra?.tag}`, JSON.stringify(ref));
    this.ctx.store.set(`id:${ref.id}`, JSON.stringify(ref));
  }
  stored(ref: SessionRef): SessionRef {
    const byTag = typeof ref.extra?.tag === 'string' ? this.ctx.store.get(`ref:${ref.extra.tag}`) : undefined;
    return JSON.parse(byTag ?? this.ctx.store.get(`id:${ref.id}`) ?? JSON.stringify(ref));
  }
  agentRef(agent: Agent): SessionRef {
    const tag = agent.name.split(' ')[0];
    const saved = this.ctx.store.get(`ref:${tag}`);
    const ref: SessionRef = saved ? JSON.parse(saved) : { backend: 'paseo', host: this.ctx.host, id: agent.id, extra: { tag, workspace: agent.cwd, branch: `launch/${tag.slice(3)}` } };
    ref.id = agent.id;
    return ref;
  }
  async capabilities(): Promise<BackendCapabilities> {
    const available = await this.bridge.json<Preset[]>(['preset', 'list']);
    if (!Array.isArray(available)) throw new Error('invalid_paseo_presets');
    const presets = [];
    for (const [id, config] of Object.entries(this.config.presets)) {
      const preset = available.find(p => p.name === config.preset && p.mode !== null && fullAccessModes.has(p.mode));
      if (!preset || (config.route && !(await this.ctx.checkRoute(config.route)).ok)) continue;
      const slash = preset.provider_model.indexOf('/');
      presets.push({ id, provider: slash < 0 ? 'paseo' : preset.provider_model.slice(0, slash), model: slash < 0 ? preset.provider_model : preset.provider_model.slice(slash + 1), effort: preset.thinking, note: preset.description });
    }
    return { steer: false, queue: 'launcher', interrupt: true, approvals: 'none', questions: 'text-only', resume: 'transcript', backgroundTasks: 'opaque', attachments: false, workspace: 'launcher-dir', branchName: 'settable', permissionModes: presets.length ? ['full-access'] : [], presets };
  }
  async health(signal?: AbortSignal): Promise<BackendHealth> {
    const degraded: string[] = [];
    let version = 'paseo-bridge';
    try {
      await mkdir(this.config.workspace_root, { recursive: true });
      const result = await this.bridge.raw(['doctor'], { cwd: this.config.workspace_root, signal });
      const doctor = JSON.parse(result.stdout) as { ok: boolean; pin?: { version?: string }; checks?: Array<{ name: string; ok: boolean }> };
      if (doctor.pin?.version) version = this.ctx.redact(doctor.pin.version);
      for (const check of doctor.checks ?? []) if (!check.ok) degraded.push(this.ctx.redact(`doctor: ${check.name}`));
      if (!doctor.ok || result.code !== 0) degraded.push('doctor failed');
    } catch { if (signal?.aborted) throw signal.reason; degraded.push('doctor unavailable'); }
    if (process.platform === 'linux') try {
      const failed = await this.ctx.exec(['systemctl', '--user', 'is-failed', ...units], { timeoutMs: 90_000, signal });
      const states = failed.stdout.trim().split(/\r?\n/);
      if (states.length !== units.length || ![0, 1].includes(failed.code)) degraded.push('systemd unit status unavailable');
      units.forEach((unit, i) => { if (states[i] === 'failed') degraded.push(`${unit} failed`); });
      const bot = await this.ctx.exec(['systemctl', '--user', 'is-active', 'paseo-bot.service'], { timeoutMs: 90_000, signal });
      if (bot.code !== 0 || bot.stdout.trim() !== 'active') degraded.push('paseo-bot.service inactive');
    } catch { if (signal?.aborted) throw signal.reason; degraded.push('systemd unavailable'); }
    for (const preset of Object.values(this.config.presets)) if (preset.route && !(await this.ctx.checkRoute(preset.route, signal)).ok) degraded.push('preset route unavailable');
    return { ok: degraded.length === 0, degraded, version };
  }
  async launch(spec: LaunchSpec, hooks: SessionHooks, signal?: AbortSignal): Promise<SessionHandle> {
    const preset = this.config.presets[spec.preset];
    if (!preset || spec.permissionMode !== 'full-access' || !(await this.capabilities()).presets.some(p => p.id === spec.preset)) throw new Error('preset_not_allowed');
    if (!/^[A-Za-z0-9][A-Za-z0-9._-]*$/.test(spec.jobId) || spec.tag !== `vl-${spec.jobId}`) throw new Error('invalid_paseo_job');
    const workspace = resolve(this.config.workspace_root, spec.jobId), branch = `launch/${spec.jobId}`;
    await mkdir(this.config.workspace_root, { recursive: true });
    const base = await this.bridge.git(['-C', spec.project.repoRoot, 'rev-parse', '--verify', `${spec.project.baseRef}^{commit}`], signal);
    await this.bridge.git(['-C', spec.project.repoRoot, 'worktree', 'add', '-b', branch, workspace, spec.project.baseRef], signal);
    const ref: SessionRef = { backend: 'paseo', host: this.ctx.host, id: '', extra: { tag: spec.tag, workspace, branch, base, repoRoot: spec.project.repoRoot } };
    this.save(ref); // Keep workspace provenance even if launch returns an ambiguous result.
    const dir = await mkdtemp(join(tmpdir(), 'vl-paseo-'));
    const prompt = join(dir, 'prompt');
    let settled = false;
    try {
      if (process.platform === 'win32') localPlatform('write-private', prompt, { input: spec.prompt });
      else await writeFile(prompt, spec.prompt, { mode: 0o600 });
      const result = await this.bridge.json<{ agent_id: string | null; preset: string; mode: string | null }>([
        'launch', '--preset', preset.preset, ...(preset.service_tier ? ['--service-tier', preset.service_tier] : []),
        '--title', `${spec.tag} ${this.ctx.redact(spec.title)}`, '--message-file', prompt, '--cwd', workspace, '--no-worktree',
      ], { env: { ...spec.env }, signal });
      if (!result.agent_id) throw new Error('launch_failed');
      ref.id = result.agent_id; ref.extra = { ...ref.extra, preset: result.preset, mode: result.mode }; this.save(ref);
      if (result.preset !== preset.preset || result.mode === null || !fullAccessModes.has(result.mode)) {
        await new PaseoSession(this, ref, hooks).settle('lost');
        settled = true;
        throw new Error('preset_changed');
      }
      return new PaseoSession(this, ref, hooks);
    } catch (error) {
      if (!settled) {
        // A failed save must not strand an agent whose ID we already know.
        // The local ref retains workspace provenance even if the store is full.
        const found = ref.id ? ref : await this.findByTag(spec.tag);
        if (found) await new PaseoSession(this, found, hooks).settle('lost');
      }
      throw error;
    } finally { await rm(dir, { recursive: true, force: true }); }
  }
  async reattach(ref: SessionRef, hooks: SessionHooks) {
    ref = this.stored(ref);
    const agent = (await this.bridge.agents(true)).find(a => a.id === ref.id);
    if (!agent || !live(agent)) return { found: 'none' } as const;
    return { found: 'live', handle: new PaseoSession(this, ref, hooks, agent.status) } as const;
  }
  async listOwned() { return (await this.bridge.agents()).filter(a => live(a) && a.name.startsWith('vl-')).map(a => this.agentRef(a)); }
  async findByTag(tag: string) {
    const refs = await this.listOwned();
    // A handoff already saved to the ledger wins over an older directory row.
    const saved = this.ctx.store.get(`ref:${tag}`);
    const preferred = saved ? (JSON.parse(saved) as SessionRef).id : undefined;
    return refs.find(r => r.extra?.tag === tag && r.id === preferred) ?? refs.find(r => owns(String(r.extra?.tag), tag)) ?? null;
  }
  async cleanupWorkspace(ref: SessionRef): Promise<{ removed: boolean; reason?: string }> {
    // A launch that failed before any agent existed leaves only the tag in the
    // ledger job; the worktree's provenance was saved here when it was created.
    // Only that exact tag's record counts (never the id index: a failed launch has no id).
    if (typeof ref.extra?.workspace !== 'string' && typeof ref.extra?.tag === 'string' && ref.extra.tag.startsWith('vl-')) {
      const saved = this.ctx.store.get(`ref:${ref.extra.tag}`);
      if (saved) ref = JSON.parse(saved) as SessionRef;
    }
    const workspace = ref.extra?.workspace, branch = ref.extra?.branch;
    if (typeof workspace !== 'string' || typeof branch !== 'string' || !branch.startsWith('launch/')) return { removed: false, reason: 'missing_workspace_metadata' };
    try {
      const root = await realpath(this.config.workspace_root), ws = await realpath(workspace);
      if (!inside(root, ws) || resolve(workspace) !== ws) return { removed: false, reason: 'outside_workspace_root' };
      const git = (args: string[]) => this.bridge.git(['-C', ws, ...args]);
      if (await git(['status', '--porcelain'])) return { removed: false, reason: 'dirty_workspace' };
      await git(['show-ref', '--verify', `refs/heads/${branch}`]);
      if (await git(['symbolic-ref', '--short', 'HEAD']) !== branch) return { removed: false, reason: 'branch_mismatch' };
      const records = (await git(['worktree', 'list', '--porcelain'])).split('\n\n');
      for (const record of records) {
        const lines = record.split('\n');
        if (lines.includes(`branch refs/heads/${branch}`) && resolve(lines[0].slice('worktree '.length)) !== ws) return { removed: false, reason: 'branch_checked_out_elsewhere' };
      }
      const repo = typeof ref.extra?.repoRoot === 'string' ? ref.extra.repoRoot : ws;
      await this.bridge.git(['-C', repo, 'worktree', 'remove', ws]);
      return { removed: true };
    } catch { return { removed: false, reason: 'workspace_unverifiable' }; }
  }
}

class PaseoSession implements SessionHandle {
  readonly backend: PaseoBackend;
  readonly ref: SessionRef;
  readonly hooks: SessionHooks;
  current: SessionState;
  lastActivity: string;
  constructor(backend: PaseoBackend, ref: SessionRef, hooks: SessionHooks, status = 'running') {
    this.backend = backend; this.ref = ref; this.hooks = hooks; this.lastActivity = backend.ctx.now().toISOString();
    this.current = status === 'idle' ? { kind: 'idle', since: this.lastActivity } : { kind: 'turn_running', turnId: ref.id, since: this.lastActivity };
  }
  state() { return this.current; }
  adopt(id?: string) {
    if (!id || id === this.ref.id) return false;
    const previous = this.ref.id; this.ref.id = id;
    this.backend.save(this.ref);
    this.backend.ctx.store.set(`id:${previous}`, JSON.stringify(this.ref));
    return true;
  }
  async send(input: TurnInput, mode: SendMode) {
    if (mode !== 'start') throw new Error('paseo_send_mode_unsupported');
    if (!input.items.length) throw new Error('paseo_empty_input');
    const tag = String(this.ref.extra?.tag ?? this.ref.id);
    const deliveryKey = (id: string) => `delivery:${tag}:${id}`;
    const seen = new Set<string>();
    const items = input.items.filter(i => {
      if (seen.has(i.deliveryId) || this.backend.ctx.store.get(deliveryKey(i.deliveryId))) return false;
      seen.add(i.deliveryId); return true;
    });
    if (!items.length) return { mode: 'start' } as const;
    const ids = items.map(i => i.deliveryId);
    const batchId = hash(JSON.stringify(ids));
    const key = `batch:${tag}:${batchId}`;
    if (this.backend.ctx.store.get(key)) return { mode: 'start' } as const;
    this.backend.ctx.store.set(key, 'attempted'); // Do not retry even if exec fails after dispatch.
    for (const id of ids) this.backend.ctx.store.set(deliveryKey(id), batchId);
    await this.backend.bridge.json(['send', this.ref.id, '--text', items.map(i => i.text).join('\n\n')]);
    // Count accepted sends. A failure reported again with no send in between is
    // the same failure (wait keeps reporting an inactive error); after new input
    // an identical error text is a new failed turn and must be emitted.
    const genKey = `send-gen:${tag}`;
    this.backend.ctx.store.set(genKey, String(Number(this.backend.ctx.store.get(genKey) ?? '0') + 1));
    this.lastActivity = this.backend.ctx.now().toISOString(); this.current = { kind: 'turn_running', turnId: batchId, since: this.lastActivity };
    return { mode: 'start' } as const;
  }
  async *turns(after?: { value: string }, signal?: AbortSignal) {
    let cursor = parseCursor(after?.value);
    const resetKey = `cursor-reset:${this.ref.extra?.tag ?? this.ref.id}`;
    // Persist the invalidated cursor before changing the agent id. The daemon
    // may crash after the ref is saved but before it persists the progress row.
    const resetRaw = this.backend.ctx.store.get(resetKey);
    const reset = resetRaw ? JSON.parse(resetRaw) as { target: string; cursor: string } : undefined;
    if (reset?.target === this.ref.id && reset.cursor === JSON.stringify(cursor)) cursor = { line: 0, last: '' };
    let emittedCursor = { ...cursor };
    let permissionNoted = false;
    while (!signal?.aborted && this.current.kind !== 'ended') {
      const result = await this.backend.bridge.wait(this.ref.id, signal);
      if (signal?.aborted) return;
      if (result.agentId && result.agentId !== this.ref.id) {
        this.backend.ctx.store.set(resetKey, JSON.stringify({ target: result.agentId, cursor: JSON.stringify(emittedCursor) }));
        this.adopt(result.agentId);
        cursor = { line: 0, last: '' };
        emittedCursor = { ...cursor };
        yield { cursor: { value: JSON.stringify(cursor) }, event: { type: 'progress', turnId: this.ref.id, summary: 'Paseo capacity handoff adopted' } as TurnEvent };
      }
      if (result.status === 'timeout') continue;
      if (result.status === 'permission') {
        this.current = { kind: 'waiting_approval', requestIds: [] };
        if (!permissionNoted) {
          permissionNoted = true; this.backend.ctx.log.warn('waiting for permission in Paseo');
          emittedCursor = { ...cursor };
          yield { cursor: { value: JSON.stringify(cursor) }, event: { type: 'progress', turnId: this.ref.id, summary: 'waiting for permission in Paseo' } as TurnEvent };
        }
        continue;
      }
      permissionNoted = false;
      let text: string;
      const previousCursor = { ...emittedCursor };
      if (result.status === 'error') text = result.message || 'Paseo turn failed';
      else {
        const read = await this.backend.bridge.json<{ lines: string[] }>(['read', this.ref.id, '--after', String(cursor.line)], { signal });
        if (!Array.isArray(read.lines) || read.lines.some(s => typeof s !== 'string')) throw new Error('invalid_paseo_transcript');
        // wait reports idle on every poll, not just on a transition. An empty
        // reread cannot establish another completion after one was captured.
        if (cursor.last && this.current.kind === 'idle' &&
            read.lines.every(line => !line.trim() || line.trim() === 'No activity to display.')) {
          cursor.line += read.lines.length; continue;
        }
        text = finalBlock(read.lines, result.message); cursor.line += read.lines.length;
      }
      // Bind the hash to the agent generation: identical text and line counts
      // from both sides of a handoff must remain distinguishable on restart.
      // Hash the raw block so redaction cannot merge distinct completions.
      // An error with identical text is a different outcome and must finish
      // its own turn. The durable send generation separates turns across new
      // input: repeated idle polls of one turn dedupe, while a second turn
      // after a new send that ends with the same text is its own completion.
      const sendGen = this.backend.ctx.store.get(`send-gen:${this.ref.extra?.tag ?? this.ref.id}`) ?? '0';
      const last = hash(JSON.stringify(result.status === 'error' ? [this.ref.id, text, 'failed', sendGen] : [this.ref.id, text, sendGen]));
      const duplicate = last === cursor.last;
      text = this.backend.ctx.redact(text);
      cursor.last = last;
      if (!duplicate || this.current.kind !== 'idle') this.lastActivity = this.backend.ctx.now().toISOString();
      this.current = { kind: 'idle', since: this.lastActivity };
      if (!duplicate) {
        emittedCursor = { ...cursor };
        yield { cursor: { value: JSON.stringify(cursor) }, event: {
          type: 'turn_finished', turnId: `${this.ref.id}:${hash(JSON.stringify([previousCursor, last]))}`, at: this.lastActivity,
          outcome: result.status === 'error' ? 'failed' : 'completed', finalText: text, consumed: [], usage: { best_effort: 1 },
        } as TurnEvent };
      }
      if (result.status === 'error') return;
    }
  }
  async interrupt(_reason: string) { await this.backend.bridge.json(['stop', this.ref.id]); }
  async activity() {
    const row = (await this.backend.bridge.agents(true)).find(a => a.id === this.ref.id);
    return { turnRunning: row ? !['idle', 'error', 'archived', 'ended', 'settled', 'exited'].includes(row.status ?? '') : true,
      openApprovals: this.current.kind === 'waiting_approval' ? 1 : 0, queuedInputs: 0,
      backgroundTasks: { known: 0, observable: false }, lastActivityAt: this.lastActivity };
  }
  async settle(_mode: 'wrapup' | 'requested' | 'cancel' | 'lost') {
    const row = (await this.backend.bridge.agents(true)).find(a => a.id === this.ref.id);
    if (!row) throw new Error('paseo_session_missing');
    // Paseo stop is a no-op for inactive errors, and wait keeps reporting
    // their failure. Directory evidence is sufficient to try non-force settle;
    // the bridge still guards against a turn starting before archival.
    if (live(row) && row.status !== 'error') {
      if (row.status !== 'idle') await this.interrupt('settle');
      const waited = await this.backend.bridge.wait(this.ref.id, undefined, 30);
      this.adopt(waited.agentId);
      if (waited.status !== 'idle') {
        const failed = waited.status === 'error'
          ? (await this.backend.bridge.agents(true)).find(a => a.id === this.ref.id) : undefined;
        if (failed?.status !== 'error') throw new Error('paseo_settle_not_idle');
      }
    }
    const extra = this.ref.extra ?? {};
    if (typeof extra.workspace !== 'string' || typeof extra.branch !== 'string' || typeof extra.base !== 'string') throw new Error('paseo_workspace_metadata_missing');
    const git = (args: string[]) => this.backend.bridge.git(['-C', extra.workspace as string, ...args]);
    const headSha = await git(['rev-parse', extra.branch]);
    const dirty = await git(['status', '--porcelain']);
    const commits = (await git(['log', '--format=%H', `${extra.base}..${extra.branch}`])).split('\n').filter(Boolean);
    await this.backend.bridge.json(['settle', this.ref.id]);
    this.current = { kind: 'ended', reason: 'settled', resumable: true };
    return { branch: extra.branch, headSha, commits, workspaceKept: true,
      summary: dirty ? this.backend.ctx.redact(`uncommitted changes left in ${extra.workspace}`) : undefined };
  }
}

export const paseoPlugin = definePlugin<PaseoConfig>({
  id: 'paseo',
  configSchema: {
    type: 'object', additionalProperties: false, required: ['bridge_bin', 'workspace_root', 'presets'],
    properties: { bridge_bin: { type: 'string', minLength: 1 }, workspace_root: { type: 'string', minLength: 1 },
      wait_s: { type: 'number', exclusiveMinimum: 0 }, presets: { type: 'object', additionalProperties: {
        type: 'object', additionalProperties: false, required: ['preset'], properties: {
          preset: { type: 'string', minLength: 1 }, service_tier: { type: 'string' }, route: {
            type: 'object', additionalProperties: false, required: ['base_url'], properties: { base_url: { type: 'string' }, key_file: { type: 'string' } },
          },
        },
      } },
    },
  },
  async create(config, ctx) { return new PaseoBackend(config, ctx); },
});
export default paseoPlugin;
