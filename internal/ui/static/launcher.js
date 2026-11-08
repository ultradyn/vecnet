import { h, toast } from './dom.js';
import { buildLaunch, launcherChoices } from './launcher-model.js';

// Keep the draft mounted across presence refreshes. Read current discovery
// again at submit, so stale selections cannot introduce paths or new presets.
export function launcherSection(ctx) {
  let row = null, busy = false, gone = false, pending = null, choicesKey = '';
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
  const fields = h('fieldset');
  const field = (label, control) => h('div', { class: 'launch-field' }, h('label', { for: control.id || control.getAttribute('id'), text: label }), control);
  fields.append(
    h('div', { class: 'launch-grid' }, field('Project', project), field('Workspace', workspace), field('Backend', backend), field('Preset', preset), field('Permissions', mode)),
    field('Job title', title), field('Job prompt', prompt), field('Deadline (your local time)', deadline));
  const form = h('form', { class: 'launch-form' }, fields, error, send);
  const el = h('section', { class: 'dash-sec launcher', id: 'pv-launch', hidden: true }, h('h2', { text: 'Launch job' }),
    h('p', { class: 'note', text: 'Choose a project and workspace, or No project for a temporary directory. Choices come from the daemon’s configuration. Codex preset default inherits the host’s existing Codex route. The daemon enforces project, workspace and permission policies.' }), note, form);
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
    note.textContent = c.reason || 'Launcher ready. Submission opens the job’s thread; acceptance by the daemon is reported there.';
    fields.disabled = busy || !!pending || !!c.reason;
    send.disabled = busy || (!pending && !!c.reason);
    send.textContent = busy ? 'Launching…' : pending ? 'Retry launch' : 'Launch job';
  }
  form.addEventListener('submit', async e => {
    e.preventDefault();
    if (busy) return;
    error.textContent = '';
    let body;
    try {
      if (pending) body = pending; // uncertain sends retry the exact key, body and deadline
      else body = buildLaunch(row, { project: project.value, workspace: workspace.value, backend: backend.value, preset: preset.value, mode: mode.value,
        title: title.value, prompt: prompt.value, deadline: deadline.value }, 'launch-' + [...crypto.getRandomValues(new Uint8Array(16))].map(x => x.toString(16).padStart(2, '0')).join(''), ctx.store.hubNow());
    } catch (x) { error.textContent = x.message; return; }
    busy = true;
    pending = body;
    draw(row);
    try {
      const res = await ctx.api.send(body);
      pending = null;
      ctx.store.invalidate('threads');
      if (gone) toast('Launch requested.');
      else ctx.nav('#thread/' + res.thread_id);
    } catch (x) {
      const uncertain = !x.status || x.status >= 500 || ['network', 'aborted', 'hub_moved', 'idempotency_conflict'].includes(x.code);
      if (!uncertain) pending = null;
      const text = (x.message || String(x)) + (uncertain ? ' Retry sends the same request. Your draft is held until the result is known.' : '');
      if (gone) toast(text, true); else error.textContent = text;
    } finally {
      busy = false;
      if (!gone) { draw(row); fields.disabled = !!pending || !!launcherChoices(row).reason; }
    }
  });
  return { el, draw, unmount() { gone = true; } };
}
