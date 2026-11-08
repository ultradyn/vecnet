import type { Log } from "../../hub-client/src/ports.ts";

// The contract every plugin implements.

export type BackendId = "t3" | "paseo" | "codex-app-server" | "acp" | "sandcastle" | (string & {});

/** Normalised permission vocabulary; each plugin maps it to its backend's modes. */
export type PermissionMode =
  | "read-only"    // no writes, no commands with side effects
  | "ask"          // per-action approvals surfaced through onPermissionRequest
  | "auto"         // backend-mediated: sandboxed workspace writes, backend's own auto-review
  | "full-access"; // permissions bypassed (paseo presets today, codex full-access, claude bypassPermissions)

export type WorkspaceMode = 'worktree' | 'project-root' | 'temp';

export interface BackendCapabilities {
  maxInputBytes?: number;
  steer: boolean;                              // can change an in-flight turn
  queue: "native" | "launcher";                // who holds input sent mid-turn
  interrupt: boolean;
  approvals: "typed" | "ui-only" | "none";     // typed = surfaced to onPermissionRequest
  questions: "typed" | "text-only";            // typed = surfaced to onQuestion; text-only = a turn ending in a question
  resume: "live" | "transcript" | "none";      // after the agent process stops
  backgroundTasks: "observable" | "opaque" | "none";
  attachments: boolean;                        // can pass files into a turn
  workspace: "backend-worktree" | "launcher-dir";
  workspaceModes?: readonly WorkspaceMode[]; // absent = legacy isolated worktree only
  branchName: "settable" | "backend-chosen";
  permissionModes: readonly PermissionMode[];
  presets: readonly PresetInfo[];
}

export interface PresetInfo { id: string; provider: string; model: string; effort?: string; note?: string }

export interface LaunchSpec {
  jobId: string;                 // job_… ; also the idempotency key for launch
  tag: string;                   // "vl-<job_id>": stamped into title/metadata so sweep and reattach can find it
  title: string;
  prompt: string;                // job_request.instructions, plus the launcher's footer
  project: ResolvedProject;      // launcher-side alias resolved to repo root + base ref
  workspaceMode?: WorkspaceMode;
  branch: string;                // "launch/<job_id>" when branchName is settable
  preset: string;
  permissionMode: PermissionMode;
  attachments: LocalFile[];      // blobs fetched by the launcher, never URLs with tokens
  env: Readonly<Record<string, string>>; // already scrubbed; never contains vecnet credentials
}

export interface ResolvedProject { alias: string; repoRoot: string; baseRef: string; scratch?: boolean }
export interface LocalFile { path: string; name: string; mediaType: string; sha256: string }

/** Opaque, JSON-serialisable; persisted in the ledger and given back to reattach(). */
export interface SessionRef { backend: BackendId; host: string; id: string; extra?: Record<string, unknown> }

export type SessionState =
  | { kind: "starting" }
  | { kind: "turn_running"; turnId: string; since: string }
  | { kind: "idle"; since: string }                         // turn finished, process alive (resumable now)
  | { kind: "parked"; since: string }                       // process stopped, transcript resumable
  | { kind: "waiting_approval"; requestIds: string[] }
  | { kind: "ended"; reason: "settled" | "exited" | "lost"; resumable: boolean };

export interface TurnInput {
  items: Array<{ deliveryId: string; messageId: string; from: string; text: string; attachments: LocalFile[] }>;
}

export type SendMode = "start" | "steer" | "queue";
export interface SendResult { mode: SendMode; backendInputId?: string; turnId?: string }

export type TurnEvent =
  | { type: "turn_started"; turnId: string; at: string; consumed: string[] }            // delivery_ids
  | { type: "progress"; turnId: string; summary: string }                               // tool use, plan step; not posted by default
  | { type: "turn_finished"; turnId: string; at: string;
      outcome: "completed" | "interrupted" | "failed" | "refused" | "max_tokens";
      finalText: string; consumed: string[]; usage?: Record<string, number> }
  | { type: "state"; state: SessionState }
  | { type: "process_exited"; at: string; code: number | null; resumable: boolean };

export interface TurnCursor { value: string }   // plugin-defined; persisted per session

export interface ActivityReport {
  turnRunning: boolean;
  openApprovals: number;
  queuedInputs: number;                        // native backend queue, if visible
  backgroundTasks: { known: number; observable: boolean; detail?: string[] };
  lastActivityAt: string;
}

export type SettleMode = "wrapup" | "requested" | "cancel" | "lost";
export interface SettleResult {
  branch?: string; headSha?: string; commits?: string[];
  workspaceKept: boolean; transcriptRef?: string; summary?: string;
}

export interface PermissionRequest {
  requestId: string;                           // backend's id
  summary: string;                             // one line, <= 512 chars: becomes approval_request.question
  permission: PermissionDraft;                // machine-readable; section 3.5. The plugin fills it, the daemon validates and redacts it
  options: Array<{ id: string; label: string; grants: Grant }>; // backend options; the daemon may append "escalate"
  defaultOnExpiry: string;                     // option id: the deny/cancel option
}
export type Grant = "once" | "session" | "always" | "deny" | "deny_always" | "cancel";
export interface PermissionDecision { optionId: string; reason?: string; decidedBy: string /* peer_ id, or "expired" */ }

export interface AgentQuestion {
  requestId: string;
  blocking: boolean;                           // app-server isBlocking; ACP elicitation is always blocking
  questions: Array<{ id: string; header?: string; text: string;
                     options?: Array<{ label: string; description?: string }>;
                     allowOther: boolean; secret: boolean }>; // secret questions are never relayed (section 3.4)
}
export interface QuestionAnswer {
  answers: Record<string, string>;             // question id -> chosen label or free text
  answeredBy: string;                          // peer_ id, or "timeout"
}

export interface SessionHooks {
  onPermissionRequest(req: PermissionRequest, signal: AbortSignal): Promise<PermissionDecision>;
  onQuestion(q: AgentQuestion, signal: AbortSignal): Promise<QuestionAnswer>;
  onEvent(ev: TurnEvent): void;                // the daemon persists and acts; must not throw
}

export interface BackendHealth {
  ok: boolean; degraded?: string[];            // e.g. ["paseo-bridge-watch.service failed"]
  version: string; detail?: Record<string, string>;
}

export interface SessionHandle {
  readonly ref: SessionRef;
  state(): SessionState;
  send(input: TurnInput, mode: SendMode): Promise<SendResult>;   // idempotent on deliveryId
  turns(after?: TurnCursor, signal?: AbortSignal): AsyncIterable<{ cursor: TurnCursor; event: TurnEvent }>;
  interrupt(reason: string): Promise<void>;
  activity(): Promise<ActivityReport>;
  park?(): Promise<void>;                      // release the process, keep the transcript
  settle(mode: SettleMode): Promise<SettleResult>;
}

export type ReattachResult =
  | { found: "live"; handle: SessionHandle }
  | { found: "parked"; handle: SessionHandle }
  | { found: "none" };                         // the launcher settles the job as outcome_unknown

export interface Backend {
  cleanupWorkspace?(ref: SessionRef): Promise<{ removed: boolean; reason?: string }>;
  /** Failed-launch cleanup only: prove exact owned settlement without obtaining
   * an input-capable attachment. Reject uncertain ownership or cleanup; persist
   * settlement before resolving. Backends without this retain reattach+settle. */
  settleOwned?(ref: SessionRef, mode: SettleMode): Promise<void>;
  readonly id: BackendId;
  capabilities(): Promise<BackendCapabilities>;
  health(signal?: AbortSignal): Promise<BackendHealth>;
  launch(spec: LaunchSpec, hooks: SessionHooks, signal?: AbortSignal): Promise<SessionHandle>;
  reattach(ref: SessionRef, hooks: SessionHooks): Promise<ReattachResult>;
  findByTag(tag: string): Promise<SessionRef | null>; // closes the gap between launch intent and recorded ref
  listOwned(): Promise<SessionRef[]>;                 // sessions carrying this launcher's tag prefix; for the sweep
}

export interface PluginContext {
  redact(s: string): string;
  checkRoute(route: { base_url: string; key_file?: string }, signal?: AbortSignal): Promise<{ ok: boolean; models: string[]; detail?: string }>;
  host: string;
  log: Logger;                                 // redacting logger; bodies and secrets never logged (§12)
  store: KeyValue;                             // namespaced to the plugin, in the ledger
  exec: (argv: string[], opts: { env?: Record<string, string>; stdin?: string; timeoutMs: number; signal?: AbortSignal })
          => Promise<{ code: number; stdout: string; stderr: string }>;
  readSecretFile: (path: string) => Promise<string>;
  now: () => Date;
  /** The daemon's Clock: plugins never create their own timers. Rejects when signal aborts. */
  sleep: (ms: number, signal?: AbortSignal) => Promise<void>;
}

export interface Plugin<C = unknown> {
  id: BackendId;
  configSchema: unknown;                       // JSON Schema; config is validated before create()
  create(config: C, ctx: PluginContext): Promise<Backend>;
}
export function definePlugin<C>(p: Plugin<C>): Plugin<C> { return p; }

export interface LaunchPermission {
  v: 1;
  kind: "exec" | "edit" | "network" | "mcp_tool" | "other";
  machine_id: string;              // launcher-computed: the launcher's host name ("xsm", "x-tall"), as pagd's MachineGrant.machine_id names machines
  job_id: string;                  // the launch job this session belongs to
  permission_mode: PermissionMode; // the session's mode, set at spawn time
  preset: string;                  // the session's preset (backend/model)
  backend: BackendId;
  backend_method: string;          // e.g. "item/commandExecution/requestApproval", "session/request_permission"
  tool?: string;                   // agent tool name: "Bash", "shell", "apply_patch", "mcp:<server>/<tool>"
  command?: {
    argv?: string[];               // only when the backend supplies argv; never synthesised
    line?: string;                 // the command string as the backend gave it
    cwd?: string;
    shell_operators: boolean;      // launcher-computed: line contains ; | & && || $( ` > < etc., so it is not one argv
  };
  paths?: Array<{
    path: string;                  // absolute, after realpath where it exists
    access: "read" | "write" | "delete" | "move";
    in_workspace: boolean;         // launcher-computed against the session's workspace root
  }>;
  host?: { name: string; protocol?: string; port?: number };
  scope: "once" | "session" | "always"; // the broadest grant any offered allow-option would give
  options: Array<{ id: string; label: string; grants: Grant }>; // mirrors approval_request.options, with meaning
  workspace_root: string;
  outside_workspace: true | false | "unknown"; // launcher-computed. false only when there is no command, every path has
                                   // fidelity "enforced", and every path is in_workspace; true when any known path or cwd is outside;
                                   // otherwise "unknown" (e.g. a command line, which can touch anything)
  reason?: string;                 // the agent's own stated reason: untrusted text
  backend_raw: unknown;            // the backend's original request params, redacted, <= 4 KiB serialised
  backend_raw_truncated: boolean;
  backend_raw_sha256?: string;     // the full raw request, attached when truncated
  redacted: boolean;               // true if redaction changed command, paths or backend_raw
  fidelity: {
    kind: "enforced" | "reported";             // enforced: the backend's request type fixes it; reported: the agent's adapter chose it
    command: "argv" | "line" | "none";
    paths: "enforced" | "reported" | "none";   // enforced: the approval grants exactly these paths (app-server fileChange item,
                                               // permissions entries); reported: the agent or adapter described them (ACP
                                               // locations, codex commandActions); none: no paths known
    host: "enforced" | "reported" | "none";
  };
}

export type Logger = Log;
export interface KeyValue {
  get(k: string): string | undefined;
  set(k: string, v: string): void;
  delete(k: string): void;
  keys(prefix?: string): string[];
}
export type PermissionDraft = Omit<LaunchPermission,
  "v" | "machine_id" | "job_id" | "permission_mode" | "preset" | "backend" |
  "workspace_root" | "outside_workspace" | "redacted" | "backend_raw_truncated" |
  "backend_raw_sha256" | "paths" | "command"
> & {
  paths?: Array<Omit<NonNullable<LaunchPermission["paths"]>[number], "in_workspace">>;
  command?: Omit<NonNullable<LaunchPermission["command"]>, "shell_operators">;
};
/** Every backend-returned ref carries extra.tag, plus branch/workspace/link where known.
 * listOwned excludes settled, archived and ended sessions. Throws from reattach or
 * findByTag mean unreachable, never gone: only an explicit miss establishes loss.
 */
export const MAX_TURN_INPUT_BYTES = 96 * 1024;
