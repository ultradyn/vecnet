// Hub update indicator, pure (GET /v1/admin/update). Nothing here acts on a
// field the hub sends except to show it: the update button posts no version.
const SEMVER = /^\d{1,9}\.\d{1,9}\.\d{1,9}(?:-[0-9A-Za-z.-]{1,40})?$/;
const RELEASE = /^https:\/\/www\.npmjs\.com\/package\/vecnet\/v\/[0-9A-Za-z.+-]{1,64}$/;
const COMMAND = /^npm install -g vecnet@[0-9A-Za-z.-]{1,64}$/;
const REASONS = {
  unsupported_os: 'One-click update runs only on Linux.',
  container: 'This hub runs in a container; redeploy it with the new version.',
  not_npm_global: 'This hub was not started from a global npm install.',
  version_mismatch: 'The installed npm package does not match the running hub.',
  npm_not_found: 'npm was not found on the hub host.',
  npm_prefix_mismatch: 'npm would install somewhere other than this hub.',
  not_writable: 'The hub cannot write its npm install directory.',
  detect_failed: 'The hub could not inspect its installation.',
};
const STATES = {
  checking: 'Checking npm…', installing: 'Installing ', verifying: 'Verifying ',
  restarting: 'Restarting into ', installed: 'Installed ',
};

const str = v => typeof v === 'string' ? v : '';

// updateView: what the sidebar shows for one status, or null to show nothing
// (up to date, check disabled, never succeeded, or a reply it cannot trust).
// busy: a job is running or the hub is restarting (no button).
export function updateView(st) {
  if (!st || typeof st !== 'object' || st.enabled !== true) return null;
  const latest = str(st.latest), current = str(st.current);
  if (st.update_available !== true || !SEMVER.test(latest) || !SEMVER.test(current)) return null;
  const job = st.job && typeof st.job === 'object' ? st.job : null;
  const state = job ? str(job.state) : '';
  const v = {
    latest, current,
    title: 'Update available: ' + latest,
    sub: "You're on " + current,
    release: RELEASE.test(str(st.release_url)) ? st.release_url : '',
    oneClick: st.one_click === true,
    restart: st.restart === true,
    manual: COMMAND.test(str(st.manual_command)) ? st.manual_command : '',
    reason: REASONS[str(st.manual_reason)] || '',
    busy: false, progress: '', error: '', note: '',
  };
  if (state in STATES) {
    const ver = SEMVER.test(str(job.version)) ? job.version : latest;
    if (state === 'installed') {
      v.note = 'Installed ' + ver + '. Restart the hub to run it.';
    } else {
      v.busy = true;
      v.progress = state === 'checking' ? STATES.checking : STATES[state] + ver + '…';
    }
  } else if (state === 'failed') {
    v.error = str(job.error).slice(0, 300) || 'The update failed; the hub keeps running ' + current + '.';
  }
  return v;
}

// buttonLabel: the one-click button's text.
export const buttonLabel = v => v.restart ? 'Update & restart' : 'Install update';

// pollDelay: how soon to re-read the status. A running job polls quickly so
// progress shows; otherwise the sidebar re-reads every 10 minutes (the hub
// checks npm itself, hours apart; reading is free).
export function pollDelay(v) {
  return v && v.busy ? 1500 : 600000;
}
