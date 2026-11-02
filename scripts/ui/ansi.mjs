// Convert tmux SGR output to inert HTML. All peer-provided text is escaped;
// OSC/DCS, other terminal controls, C1 controls and bidi overrides are dropped.
const palette = ['#0b0f19', '#ef4444', '#22c55e', '#eab308', '#6366f1', '#a855f7', '#06b6d4', '#d1d5db', '#6b7280', '#f87171', '#4ade80', '#fde047', '#818cf8', '#c084fc', '#67e8f9', '#f3f4f6'];
const escape = text => text.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;');
function color(n) {
  if (n < 16) return palette[n];
  if (n >= 232) { const v = 8 + (n - 232) * 10; return `rgb(${v},${v},${v})`; }
  const v = n - 16, cube = [0, 95, 135, 175, 215, 255];
  return `rgb(${cube[Math.floor(v / 36)]},${cube[Math.floor(v / 6) % 6]},${cube[v % 6]})`;
}
// LINE is one terminal row in px: JetBrains Mono's ascent plus descent at
// 14px (1.32em), so box-drawing verticals meet the next row as they do in a
// terminal instead of leaving a gap. tmux.mjs sizes its viewport from it.
export const LINE = 18.5;

export function ansiHTML(input, { columns = 140, rows = 42 } = {}) {
  const state = {};
  let output = '', pos = 0;
  const text = input.replace(/\x1b\][\s\S]*?(?:\x07|\x1b\\|$)/g, '').replace(/\x1b[P^_][\s\S]*?(?:\x1b\\|$)/g, '');
  const tokens = /\x1b\[([0-9;:]*)m|\x1b\[[0-?]*[ -/]*[@-~]|\x1b./g;
  const append = raw => {
    const safe = raw.replace(/[\x00-\x08\x0b-\x1f\x7f-\x9f\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g, '');
    let fg = state.fg || '#f3f4f6', bg = state.bg || '#0b0f19';
    if (state.inverse) [fg, bg] = [bg, fg];
    output += `<span style="color:${fg};background:${bg};font-weight:${state.bold ? 700 : 400};opacity:${state.dim ? .6 : 1};text-decoration:${state.underline ? 'underline' : 'none'}">${escape(safe)}</span>`;
  };
  for (const match of text.matchAll(tokens)) {
    append(text.slice(pos, match.index)); pos = match.index + match[0].length;
    if (match[1] === undefined) continue;
    const codes = (match[1] || '0').replaceAll(':', ';').split(';').map(Number);
    for (let i = 0; i < codes.length; i++) {
      const c = codes[i];
      if (c === 0) { for (const k of Object.keys(state)) delete state[k]; }
      else if (c === 1) state.bold = true;
      else if (c === 2) state.dim = true;
      else if (c === 4) state.underline = true;
      else if (c === 7) state.inverse = true;
      else if (c === 22) state.bold = state.dim = false;
      else if (c === 24) state.underline = false;
      else if (c === 27) state.inverse = false;
      else if (c === 39) delete state.fg;
      else if (c === 49) delete state.bg;
      else if (c >= 30 && c <= 37) state.fg = color(c - 30);
      else if (c >= 40 && c <= 47) state.bg = color(c - 40);
      else if (c >= 90 && c <= 97) state.fg = color(c - 90 + 8);
      else if (c >= 100 && c <= 107) state.bg = color(c - 100 + 8);
      else if (c === 38 || c === 48) {
        const field = c === 38 ? 'fg' : 'bg', mode = codes[++i];
        if (mode === 5) { const n = codes[++i]; if (n >= 0 && n <= 255) state[field] = color(n); }
        else if (mode === 2) { const rgb = codes.slice(i + 1, i + 4); i += 3; if (rgb.length === 3 && rgb.every(v => v >= 0 && v <= 255)) state[field] = `rgb(${rgb.join(',')})`; }
      }
    }
  }
  append(text.slice(pos));
  return `<!doctype html><meta charset="utf-8"><style>html,body{margin:0;background:#0b0f19;color:#f3f4f6;color-scheme:dark}pre{margin:0;padding:16px;width:${columns}ch;height:${rows * LINE}px;font:14px/${LINE}px 'JetBrains Mono',ui-monospace,monospace;white-space:pre;overflow:hidden}</style><pre>${output}</pre>`;
}
export default ansiHTML;
