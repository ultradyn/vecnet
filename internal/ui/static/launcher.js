import { h } from './dom.js';
import { buildLaunch, createLaunchRecovery, launcherChoices } from './launcher-model.js';

// Keep the draft mounted across presence refreshes. Read current discovery
// again at submit, so stale selections cannot introduce paths or new presets.
export function launcherSection(ctx) {
  let row = null, busy = false, gone = false, pending = null, choicesKey = '';
  let recovery = null, identityReady = false, identityError = '', offRecovery = () => {};
  const win = ctx.recoveryWindow || globalThis.window;
  let storage = ctx.recoveryStorage;
  if (storage === undefined) { try { storage = globalThis.sessionStorage; } catch { storage = null; } }
  const recoveryState = () => recovery?.snapshot() || {};
  const leaveMessage = () => {
    const state = recoveryState();
    return (state.invalid || (state.value && !state.value.result)) && !state.persisted
      ? 'A launch may already be running. This browser cannot save its recovery request. Leaving or reloading may lose the original retry key; inspect the launcher’s threads before submitting another job.' : '';
  };
  const onBefore = e => { if (leaveMessage()) { e.preventDefault(); e.returnValue = ''; } };
  win?.addEventListener('beforeunload', onBefore);
  ctx.setLeaveGuard?.(leaveMessage);
  const select = id => h('select', { id });
  const project = select('launch-project'), workspace = select('launch-workspace'), backend = select('launch-backend'), preset = select('launch-preset'), mode = select('launch-mode');
  const title = h('input', { id: 'launch-title', maxlength: '200', placeholder: 'Optional job title' });
  const prompt = h('textarea', { id: 'launch-prompt', rows: '6', maxlength: '16384', required: true, placeholder: 'Describe the work to run in the selected project…' });
  const deadline = h('input', { id: 'launch-deadline', type: 'datetime-local', required: true });
  // Match the normal hub job lifetime and leave room for the daemon's
  // opaque-backend settle window (2 h) plus its admission margin (10 min).
  const later = new Date(ctx.store.hubNow() + 24 * 60 * 60 * 1000);
  deadline.value = new Date(later.getTime() - later.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
  const note = h('p', { class: 'note', role: 'status' }), error = h('p', { class: 'err', role: 'alert' });
  const send = h('button', { type: 'submit', class: 'btn pri', text: 'Launch job' });
  const recoveryNote = h('p', { id: 'launch-recovery', class: 'note', role: 'status' });
  const clear = h('button', { type: 'button', class: 'btn', text: 'Discard recovery', hidden: true, onclick: () => {
    if (busy) return;
    const state = recoveryState();
    if (!state.value?.result && !win?.confirm('Discard this unresolved launch recovery? A job may already be running. This does not cancel it. Inspect the launcher’s threads before starting another job, which could duplicate the work.')) return;
    recovery.discard();
    choicesKey = ''; prompt.value = ''; title.value = ''; error.textContent = ''; draw(row);
  } });
  const fields = h('fieldset');
  const field = (label, control) => h('div', { class: 'launch-field' }, h('label', { for: control.id || control.getAttribute('id'), text: label }), control);
  fields.append(
    h('div', { class: 'launch-grid' }, field('Project', project), field('Workspace', workspace), field('Backend', backend), field('Preset', preset), field('Permissions', mode)),
    field('Job title', title), field('Job prompt', prompt), field('Deadline (your local time)', deadline));
  const form = h('form', { class: 'launch-form' }, fields, recoveryNote, error, clear, send);
  const el = h('section', { class: 'dash-sec launcher', id: 'pv-launch', hidden: true }, h('h2', { text: 'Launch job' }),
    h('p', { class: 'note', text: 'Choose a project and workspace, or No project for a temporary directory. Choices come from the daemon’s configuration. Codex preset default inherits the host’s existing Codex route. The daemon enforces project, workspace and permission policies.' }), note, h('p', { class: 'note', text: 'Submitted launch requests, including the prompt, are kept in this tab for recovery across navigation and reload until you clear recovery or close the tab. Credentials and unsent drafts are not saved.' }), form);
  function options(control, values, preferred) {
    const keep = control.value;
    const labels = control === workspace ? { worktree: 'New worktree', 'project-root': 'Existing project root', temp: 'Temporary directory' } : control === project ? { '': 'No project' } : {};
    control.replaceChildren(...values.map(v => h('option', { value: v, text: labels[v] || v })));
    control.value = values.includes(keep) ? keep : values.includes(preferred) ? preferred : values[0] || '';
  }
  function backendChoices() {
    const c = launcherChoices(row), b = c.backends.find(b => b.id === backend.value);
    options(preset, b?.presets || [], 'default');
    options(mode, b?.modes || [], 'ask');
    options(workspace, b?.workspaces || [], 'worktree');
    projectChoices();
  }
  function projectChoices() {
    const c = launcherChoices(row), b = c.backends.find(b => b.id === backend.value);
    const available = [...c.projects, ...(b?.workspaces.includes('temp') ? [''] : [])];
    options(project, available, c.projects[0]);
    if (workspace.value === 'temp') project.value = '';
    else if (!project.value) project.value = c.projects[0] || '';
    project.disabled = workspace.value === 'temp';
  }
  workspace.addEventListener('change', projectChoices);
  project.addEventListener('change', () => {
    if (!project.value) { workspace.value = 'temp'; projectChoices(); }
  });
  backend.addEventListener('change', backendChoices);
  function draw(next) {
    row = next;
    el.hidden = row?.peer?.kind !== 'launcher';
    if (el.hidden) return;
    const c = launcherChoices(row), key = JSON.stringify(c);
    if (key !== choicesKey) {
      choicesKey = key;
      options(backend, c.backends.filter(b => b.health === 'ok' && b.presets.length && b.modes.length && b.workspaces.length).map(b => b.id), 'codex-app-server');
      backendChoices();
    }
    const state = recoveryState(), saved = state.value;
    pending = saved && !saved.result ? saved.body : null;
    if (saved) {
      const input = saved.body.job_request.input;
      options(backend, [input.backend]); options(preset, [input.preset]); options(mode, [input.permission_mode]);
      options(workspace, [input.workspace_mode]); options(project, [input.workspace_mode === 'temp' ? '' : input.project]);
      title.value = saved.body.thread_title || ''; prompt.value = saved.body.job_request.instructions;
      const at = new Date(saved.body.expires_at);
      deadline.value = new Date(at.getTime() - at.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
    }
    recoveryNote.textContent = identityError || (state.invalid ? 'Recovery data could not be read. An existing job may still be running. Inspect its threads before explicitly discarding recovery.' : saved
      ? (saved.result ? 'Launch accepted. Open the existing job thread, or explicitly start another job.' : 'Unresolved launch ' + saved.body.idempotency_key + ' · original deadline ' + saved.body.expires_at + '. Retry reconciles the same request. ' + (state.persisted ? 'Recovery is saved in this tab.' : 'Recovery is in memory only; reload is guarded because tab storage is unavailable.'))
      : identityReady ? '' : 'Checking the hub identity for launch recovery…');
    clear.hidden = !saved && !state.invalid;
    clear.disabled = busy;
    clear.textContent = saved?.result ? 'Start another job' : 'Discard recovery (job may still be running)';
    note.textContent = c.reason || 'Launcher ready. Submission opens the job’s thread; acceptance by the daemon is reported there.';
    fields.disabled = busy || !!saved || !!c.reason || !identityReady || !!identityError || !!state.invalid;
    send.disabled = busy || !identityReady || !!identityError || !!state.invalid || (!saved && !!c.reason);
    send.textContent = busy ? 'Launching…' : saved?.result ? 'Open job thread' : pending ? 'Retry launch' : 'Launch job';
  }
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (busy || !identityReady || identityError || recoveryState().invalid) return;
    if (recoveryState().value?.result) { ctx.nav('#thread/' + recoveryState().value.result.thread_id); return; }
    error.textContent = '';
    let body;
    const retrying = !!pending;
    try {
      if (pending && ctx.store.hubNow() - recoveryState().value.at >= 24 * 60 * 60 * 1000) throw new Error('The safe retry window has passed. Inspect launcher threads for job key ' + pending.job_request.job_key + ' before discarding recovery. The original job may still be running.');
      if (pending) body = pending; // uncertain sends retry the exact key, body and deadline
      else body = buildLaunch(row, { project: project.value, workspace: workspace.value, backend: backend.value, preset: preset.value, mode: mode.value,
        title: title.value, prompt: prompt.value, deadline: deadline.value }, 'launch-' + [...crypto.getRandomValues(new Uint8Array(16))].map(x => x.toString(16).padStart(2, '0')).join(''), ctx.store.hubNow());
      if (!retrying) recovery.begin(body, ctx.store.hubNow());
    } catch (x) { error.textContent = x.message; return; }
    busy = true;
    pending = body;
    draw(row);
    try {
      const res = await ctx.api.send(body);
      if (!res?.thread_id || !res?.job_id || !res?.message_id) throw new Error('Unreadable launch result; retain the original recovery request.');
      recovery.resolve(body, res);
      pending = null;
      ctx.store.invalidate('threads');
      if (!gone) ctx.nav('#thread/' + res.thread_id);
    } catch (x) {
      const uncertain = !x.status || x.status >= 500 || ['network', 'aborted', 'hub_moved', 'idempotency_conflict'].includes(x.code);
      if (!uncertain && !retrying) recovery.discard(body);
      const text = (x.message || String(x)) + (uncertain || retrying ? ' The exact launch request is retained for recovery; a job may already be running.' : '');
      if (!gone) error.textContent = text;
    } finally {
      busy = false;
      if (!gone) draw(row);
    }
  });
  const ready = (async () => {
    try {
      const identity = await ctx.api?.hubIdentity();
      recovery = createLaunchRecovery({ storage, origin: ctx.recoveryOrigin || globalThis.location?.origin,
        pubkey: identity?.pubkey, caller: ctx.me, launcher: ctx.peerId });
      identityReady = true;
      if (!gone) offRecovery = recovery.subscribe(() => draw(row));
    } catch (x) { identityError = x.message; }
    if (!gone) draw(row);
  })();
  return { el, ready, draw, unmount() {
    gone = true; offRecovery(); win?.removeEventListener('beforeunload', onBefore); ctx.setLeaveGuard?.(null);
  } };

}
