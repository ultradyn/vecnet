const UNITS = [
  ['year', 31536000], ['month', 2592000], ['day', 86400],
  ['hour', 3600], ['minute', 60], ['second', 1],
];

// timeAgo is pure so boundary behaviour stays testable without a browser.
export function timeAgo(value, now = Date.now()) {
  const at = new Date(value).getTime();
  if (!Number.isFinite(at)) return 'unknown';
  const elapsed = Math.max(0, Math.floor((now - at) / 1000));
  if (elapsed < 5) return 'just now';
  for (const [name, seconds] of UNITS) {
    if (elapsed >= seconds) {
      const count = Math.floor(elapsed / seconds);
      return `${count} ${name}${count === 1 ? '' : 's'} ago`;
    }
  }
  return 'just now';
}

export function buildInfoLine(info, now = Date.now()) {
  const version = info?.version || 'dev';
  const commit = info?.commit || 'unknown';
  const rawDate = info?.commit_date || 'unknown';
  const parsed = new Date(rawDate);
  const date = Number.isFinite(parsed.getTime()) ? parsed.toISOString().slice(0, 10) : 'unknown';
  return `vecnet ${version} · ${commit} · ${date} · ${timeAgo(rawDate, now)}`;
}

export function showBuildInfo(element) {
  fetch('/healthz').then(response => response.ok ? response.json() : Promise.reject())
    .then(info => {
      const render = () => { element.textContent = buildInfoLine(info); };
      render();
      setInterval(render, 60000);
    })
    .catch(() => { element.textContent = buildInfoLine(null); });
}
