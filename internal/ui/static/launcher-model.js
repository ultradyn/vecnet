// Launcher discovery is advisory. The daemon still enforces project and policy
// allowlists; requester input never contains a repository path.
const ALIAS = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;
const MODES = ['read-only', 'ask', 'auto', 'full-access'];
const strings = xs => Array.isArray(xs) ? xs.filter(x => typeof x === 'string') : [];
const unique = xs => [...new Set(xs)];

export function launcherChoices(row) {
  const p = row?.peer || {}, caps = strings(p.capabilities);
  const tags = prefix => unique(caps.filter(x => x.startsWith(prefix)).map(x => x.slice(prefix.length)));
  const projects = tags('project:').filter(x => ALIAS.test(x));
  const modes = tags('mode:').filter(x => MODES.includes(x));
  // Only a legacy peer without any scoped tags uses the global union. A
  // backend missing modes on a new peer must not inherit another's modes.
  const hasBackendModes = tags('mode:').some(x => x.includes('/'));
  const backends = tags('launch:').filter(x => ALIAS.test(x) && x !== 'any').map(id => ({
    id, health: p.quota?.backends?.[id] || 'unknown',
    presets: tags('preset:' + id + '/').filter(Boolean),
    modes: hasBackendModes ? tags('mode:' + id + '/').filter(x => MODES.includes(x)) : modes,
  }));
  let reason = '';
  if (p.kind !== 'launcher') reason = 'This peer is not a launcher.';
  else if (row.disabled || row.archived_at) reason = 'Enable this launcher before launching a job.';
  else if (row.credential !== 'active') reason = 'Complete launcher installation and enrolment first.';
  else if (!['idle', 'working'].includes(p.presence)) reason = 'Launcher is ' + (p.presence || 'unknown') + '. Wait for a healthy presence report.';
  else if (!projects.length) reason = 'No project aliases advertised. Configure projects on the launcher host and restart the daemon.';
  else if (!backends.some(b => b.health === 'ok' && b.presets.length)) reason = 'No healthy backend with an advertised preset is available.';
  else if (!backends.some(b => b.health === 'ok' && b.presets.length && b.modes.length)) reason = 'No permission modes advertised for a healthy backend.';
  return { projects, backends, modes, reason };
}

export function buildLaunch(row, values, key, now) {
  const choices = launcherChoices(row);
  if (choices.reason) throw new Error(choices.reason);
  const backend = choices.backends.find(b => b.id === values.backend && b.health === 'ok');
  if (!backend || !backend.presets.includes(values.preset)) throw new Error('Choose an advertised preset on a healthy backend.');
  if (!choices.projects.includes(values.project)) throw new Error('Choose a project alias advertised by this launcher.');
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
      input: { backend: values.backend, project: values.project, preset: values.preset, permission_mode: values.mode, ...(title ? { title } : {}) } },
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
