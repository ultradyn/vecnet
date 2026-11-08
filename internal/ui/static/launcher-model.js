// Launcher discovery is advisory. The daemon still enforces project and policy
// allowlists; requester input never contains a repository path.
const ALIAS = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const MODES = ['read-only', 'ask', 'auto', 'full-access'];
const WORKSPACES = ['worktree', 'project-root', 'temp'];
const strings = xs => Array.isArray(xs) ? xs.filter(x => typeof x === 'string') : [];
const unique = xs => [...new Set(xs)];

export const LAUNCHER_HOST_HELP = 'On the launcher host, run vecnet --net PROFILE launcher status and vecnet --net PROFILE launcher check. Native Codex requires a supported, running background app-server daemon, using the same configured CODEX_HOME as launcher setup. If it is stopped, explicitly run CODEX_HOME="${CODEX_HOME:-$HOME/.codex}" codex app-server daemon start, then run launcher check again. These are host operator actions; health checks do not start Codex or load existing threads.';

export function launcherHostRecovery(profile) {
  return 'CODEX_HOME="${CODEX_HOME:-$HOME/.codex}" codex app-server daemon start\n' +
    'vecnet --net ' + quote(profile) + ' launcher check\nvecnet --net ' + quote(profile) + ' launcher status';
}

export function launcherChoices(row) {
  const p = row?.peer || {}, caps = strings(p.capabilities);
  const tags = prefix => unique(caps.filter(x => x.startsWith(prefix)).map(x => x.slice(prefix.length)));
  const aliases = tags('project:').filter(x => ALIAS.test(x));
  const scratch = tags('scratch:').filter(x => aliases.includes(x));
  const scratchProject = scratch[0] || '';
  const projects = aliases.filter(x => !scratch.includes(x));
  const temporary = !!scratchProject;
  const modes = tags('mode:').filter(x => MODES.includes(x));
  // Only a legacy peer without any scoped tags uses the global union. A
  // backend missing modes on a new peer must not inherit another's modes.
  const hasBackendModes = tags('mode:').some(x => x.includes('/'));
  const hasWorkspaces = tags('workspace:').some(x => x.includes('/'));
  const backends = tags('launch:').filter(x => ALIAS.test(x) && x !== 'any').map(id => ({
    id, health: p.quota?.backends?.[id] || 'unknown',
    presets: tags('preset:' + id + '/').filter(Boolean),
    modes: hasBackendModes ? tags('mode:' + id + '/').filter(x => MODES.includes(x)) : modes,
    workspaces: (hasWorkspaces ? tags('workspace:' + id + '/').filter(x => WORKSPACES.includes(x)) : ['worktree'])
      .filter(x => x === 'temp' ? temporary : projects.length > 0),
  }));
  let reason = '';
  if (p.kind !== 'launcher') reason = 'This peer is not a launcher.';
  else if (row.disabled || row.archived_at) reason = 'Enable this launcher before launching a job.';
  else if (row.credential !== 'active') reason = 'Complete launcher installation and enrolment first.';
  else if (!['idle', 'working'].includes(p.presence)) reason = 'Launcher is ' + (p.presence || 'unknown') + '. ' + LAUNCHER_HOST_HELP;
  else if (!aliases.length) reason = 'No project aliases advertised. Configure projects on the launcher host and restart the daemon.';
  else if (!backends.some(b => b.health === 'ok' && b.presets.length)) reason = 'No healthy backend with an advertised preset is available. ' + LAUNCHER_HOST_HELP;
  else if (!backends.some(b => b.health === 'ok' && b.presets.length && b.modes.length)) reason = 'No permission modes advertised for a healthy backend.';
  else if (!backends.some(b => b.health === 'ok' && b.presets.length && b.modes.length && b.workspaces.length)) reason = 'No supported workspace is configured for a healthy backend.';
  return { projects, scratchProject, temporary, backends, modes, reason };
}

export function buildLaunch(row, values, key, now) {
  const choices = launcherChoices(row);
  if (choices.reason) throw new Error(choices.reason);
  const backend = choices.backends.find(b => b.id === values.backend && b.health === 'ok');
  if (!backend || !backend.presets.includes(values.preset)) throw new Error('Choose an advertised preset on a healthy backend.');
  const workspace = values.workspace || 'worktree';
  if (!backend.workspaces.includes(workspace)) throw new Error('Choose a workspace mode advertised for the selected backend.');
  if (workspace !== 'temp' && !choices.projects.includes(values.project)) throw new Error('Choose a project alias advertised by this launcher.');
  if (!backend.modes.includes(values.mode)) throw new Error('Choose a permission mode advertised for the selected backend.');
  const prompt = String(values.prompt || '').trim(), title = String(values.title || '').trim();
  if (!prompt || prompt.length > 16384 || new TextEncoder().encode(prompt).length > 16384) throw new Error('Enter a job prompt of at most 16 KiB.');
  if (title.length > 200) throw new Error('Title must be at most 200 characters.');
  const deadline = Date.parse(values.deadline);
  if (!Number.isFinite(deadline) || deadline <= now) throw new Error('Choose a future deadline.');
  if (!key || !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(key)) throw new Error('Invalid launch request key.');
  return {
    idempotency_key: key, recipients: [row.peer.peer_id], kind: 'message', priority: 'wake',
    content: { text: prompt }, ...(title ? { thread_title: title } : {}),
    expires_at: new Date(deadline).toISOString(),
    job_request: { version: 1, job_key: key, operation: 'launch', instructions: prompt,
      input: { backend: values.backend, project: workspace === 'temp' ? choices.scratchProject : values.project, workspace_mode: workspace,
        preset: values.preset, permission_mode: values.mode, ...(title ? { title } : {}) } },
  };
}

const quote = s => "'" + String(s).replace(/'/g, "'\\''") + "'";
export function launcherSetupIssue(v) {
  if (!ALIAS.test(v.project || '')) return 'Project alias must contain letters, digits, dots, underscores or hyphens, starting with a letter or digit.';
  if (!v.repo?.startsWith('/') || /[\x00-\x1f\x7f]/.test(v.repo)) return 'Enter an absolute repository directory on the launcher host.';
  if (!v.source?.startsWith('/') || /[\x00-\x1f\x7f]/.test(v.source)) return 'Enter the absolute vecnet source checkout directory on the launcher host.';
  if (!v.baseRef?.trim() || /[\x00-\x1f\x7f]/.test(v.baseRef)) return 'Enter the host project base ref.';
  return '';
}

export function launcherSetupCommands(output, v) {
  const issue = launcherSetupIssue(v);
  if (issue) throw new Error(issue);
  const enrol = output.match(/^\s*(printf %s .* \| vecnet enrol .*--profile ([A-Za-z0-9._-]+).*)$/m);
  if (!enrol) throw new Error('The hub did not return a launcher enrolment command.');
  const prefix = 'vecnet --net ' + quote(enrol[2]) + ' launcher ';
  return [enrol[1], prefix + 'setup --project ' + quote(v.project) + ' --repo ' + quote(v.repo) + ' --base-ref ' + quote(v.baseRef) + ' --write',
    prefix + 'install --source ' + quote(v.source) + ' --write --enable --start'].join('\n');
}

// Only submitted requests, public identity pins and safe result ids live in
// tab storage. No API client, bearer, enrolment token or unsent draft is saved.
const recoveries = new Map(), pins = new Map();
const RECOVERY_PREFIX = 'vecnet.ui.launcher.recovery.v1:';
const clone = value => JSON.parse(JSON.stringify(value));
const only = (value, keys) => value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(k => keys.includes(k));
function recoveryBody(body, launcher) {
  const j = body?.job_request, i = j?.input;
  return only(body, ['idempotency_key', 'recipients', 'kind', 'priority', 'content', 'thread_title', 'expires_at', 'job_request']) &&
    /^launch-[a-z0-9-]+$/.test(body.idempotency_key) && body.recipients?.length === 1 && body.recipients[0] === launcher &&
    body.kind === 'message' && body.priority === 'wake' && only(body.content, ['text']) && typeof body.content.text === 'string' &&
    Number.isFinite(Date.parse(body.expires_at)) && only(j, ['version', 'job_key', 'operation', 'instructions', 'input']) &&
    j.version === 1 && j.operation === 'launch' && j.job_key === body.idempotency_key && j.instructions === body.content.text &&
    only(i, ['backend', 'project', 'workspace_mode', 'preset', 'permission_mode', 'title']) && ALIAS.test(i.backend) && ALIAS.test(i.project) &&
    typeof i.preset === 'string' && MODES.includes(i.permission_mode) && WORKSPACES.includes(i.workspace_mode);
}

export function createLaunchRecovery({ storage, origin, pubkey, caller, launcher }) {
  if (!origin || !pubkey || !caller || !launcher) throw new Error('Authenticated launcher recovery identity is unavailable. Reconnect before launching.');
  const pinKey = 'vecnet.ui.launcher.pin.v1:' + origin;
  let pinned = pins.get(origin);
  try { pinned = storage?.getItem(pinKey) || pinned; } catch { /* memory pin */ }
  if (pinned && pinned !== pubkey) throw new Error('The hub identity changed. Verify the hub before continuing; the original launch recovery is retained in this tab.');
  pins.set(origin, pubkey);
  try { storage?.setItem(pinKey, pubkey); } catch { /* memory pin */ }
  const key = RECOVERY_PREFIX + JSON.stringify([origin, pubkey, caller, launcher]);
  let entry = recoveries.get(key);
  if (!entry) {
    entry = { value: null, persisted: false, invalid: false, listeners: new Set() };
    let raw;
    try { raw = storage?.getItem(key); } catch { /* memory-only recovery */ }
    try {
      if (raw) {
        const value = JSON.parse(raw);
        if (!only(value, ['v', 'at', 'body', 'result']) || value.v !== 1 || !Number.isFinite(value.at) || !recoveryBody(value.body, launcher) ||
          (value.result && (!only(value.result, ['thread_id', 'message_id', 'job_id']) || !['thread_id', 'message_id', 'job_id'].every(k => typeof value.result[k] === 'string')))) throw new Error('invalid recovery');
        entry.value = value; entry.persisted = true;
      }
    } catch { entry.invalid = true; }
    recoveries.set(key, entry);
  }
  function save() {
    entry.persisted = false;
    try {
      if (storage) {
        if (entry.value) storage.setItem(key, JSON.stringify(entry.value)); else storage.removeItem(key);
        entry.persisted = true;
      }
    } catch { /* retain the exact operation in memory and guard leaving */ }
    for (const notify of entry.listeners) notify();
  }
  return {
    snapshot: () => ({ value: entry.value && clone(entry.value), persisted: entry.persisted, invalid: entry.invalid }),
    subscribe(fn) { entry.listeners.add(fn); return () => entry.listeners.delete(fn); },
    begin(body, at) {
      if (entry.invalid || entry.value) throw new Error('Resolve the previous launch before starting another job.');
      if (!recoveryBody(body, launcher)) throw new Error('Invalid launch recovery request.');
      entry.value = { v: 1, at, body: clone(body) }; save();
    },
    resolve(body, result) {
      if (entry.value?.body.idempotency_key !== body.idempotency_key) return;
      entry.value.result = { thread_id: result.thread_id, message_id: result.message_id, job_id: result.job_id }; save();
    },
    discard(expected) {
      if (expected && entry.value?.body.idempotency_key !== expected.idempotency_key) return;
      entry.value = null; entry.invalid = false; save();
    },
  };
}
