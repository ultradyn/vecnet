// A small markdown subset for message bodies (issue 23). parse() returns plain
// data; md.js turns it into DOM nodes with h(), so nothing here ever becomes
// markup. Bodies are hostile: every scan is bounded so a crafted input stays
// linear, and links survive only with an http, https or mailto scheme.
//
// Blocks: paragraph (lines kept as hard breaks), heading (# to ###), fenced
// code, bullet and numbered lists (one level), quote. Inline: `code`,
// **bold**, *italic* / _italic_, [text](url), ![alt](url) as a link, bare
// http(s) URLs and <url> autolinks, and backslash escapes. Anything else stays literal text.

export const MD_LIMIT = 64 * 1024; // chars rendered as markdown; the rest is plain text
const SPAN = 2000; // longest emphasis or code span, in chars
const LABEL = 300; // longest link label
const HREF = 2048; // longest link target
const DEPTH = 3; // emphasis nesting
const SCHEMES = new Set(['http:', 'https:', 'mailto:']);

// safeHref returns {href, host} for an allowed absolute URL, else null.
// host is the punycode host (or the mailto address), so a lookalike domain
// shows its real form.
export function safeHref(raw) {
  const s = String(raw || '').trim();
  if (!s || s.length > HREF || /[\s\u0000-\u001f\u007f]/.test(s)) return null;
  let u;
  try { u = new URL(s); } catch { return null; }
  if (!SCHEMES.has(u.protocol)) return null;
  if (u.protocol === 'mailto:') {
    const addr = decodeURIComponent(u.pathname || '');
    if (!/^[^@\s]+@[^@\s]+$/.test(addr)) return null;
    return { href: u.href, host: addr };
  }
  if (!u.hostname) return null;
  return { href: u.href, host: u.host };
}

// parse turns text into blocks. Text past MD_LIMIT comes back as one
// {t: 'rest'} block, cut at a line break when one is near.
export function parse(text) {
  let s = String(text == null ? '' : text).replace(/\r\n?/g, '\n');
  let rest = '';
  if (s.length > MD_LIMIT) {
    let cut = s.lastIndexOf('\n', MD_LIMIT);
    if (cut < MD_LIMIT - 4096) cut = MD_LIMIT;
    rest = s.slice(cut);
    s = s.slice(0, cut);
  }
  const blocks = parseBlocks(s.split('\n'), true);
  if (rest) blocks.push({ t: 'rest', v: rest });
  return blocks;
}

const FENCE = /^ {0,3}(`{3,}|~{3,})/;
const HEAD = /^ {0,3}(#{1,3})[ \t]+(.*?)[ \t#]*$/;
const BULLET = /^ {0,3}[-*+][ \t]+(.*)$/;
const NUM = /^ {0,3}(\d{1,9})[.)][ \t]+(.*)$/;
const QUOTE = /^ {0,3}>[ ]?(.*)$/;
const RULE = /^ {0,3}([-*_])([ \t]*\1){2,}[ \t]*$/;

// closesFence: a line of only the fence character, at least as long as the opener.
function closesFence(line, mark) {
  const m = /^ {0,3}(`{3,}|~{3,})[ \t]*$/.exec(line);
  return !!m && m[1][0] === mark[0] && m[1].length >= mark.length;
}

function parseBlocks(lines, allowQuote) {
  const out = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) { i++; continue; }
    let m;
    if ((m = FENCE.exec(line))) {
      const mark = m[1], body = [];
      i++;
      while (i < lines.length && !closesFence(lines[i], mark)) body.push(lines[i++]);
      i++; // closing fence, or past the end
      out.push({ t: 'code', v: body.join('\n') });
      continue;
    }
    if ((m = HEAD.exec(line))) { out.push({ t: 'h', level: m[1].length, kids: inline(m[2]) }); i++; continue; }
    if (RULE.test(line)) { out.push({ t: 'hr' }); i++; continue; }
    if (allowQuote && QUOTE.test(line)) {
      const body = [];
      while (i < lines.length && (m = QUOTE.exec(lines[i]))) { body.push(m[1]); i++; }
      out.push({ t: 'quote', kids: parseBlocks(body, false) });
      continue;
    }
    if (BULLET.test(line) || NUM.test(line)) {
      const ordered = !BULLET.test(line);
      const re = ordered ? NUM : BULLET;
      const start = ordered ? Number(NUM.exec(line)[1]) : 1;
      const items = [];
      while (i < lines.length && lines[i].trim()) {
        if ((m = re.exec(lines[i]))) items.push([ordered ? m[2] : m[1]]);
        else if (/^[ \t]/.test(lines[i]) && items.length) items[items.length - 1].push(lines[i].trim());
        else break;
        i++;
      }
      out.push({ t: ordered ? 'ol' : 'ul', start, items: items.map(ls => inlineLines(ls)) });
      continue;
    }
    const para = [];
    while (i < lines.length && lines[i].trim() && !FENCE.test(lines[i]) && !HEAD.test(lines[i]) && !(allowQuote && QUOTE.test(lines[i])) && !(para.length && (BULLET.test(lines[i]) || NUM.test(lines[i])))) para.push(lines[i++]);
    out.push({ t: 'p', kids: inlineLines(para) });
  }
  return out;
}

// inlineLines keeps each line break as a hard {t: 'br'}: agents lay out
// their messages line by line.
function inlineLines(lines) {
  const kids = [];
  lines.forEach((l, n) => { if (n) kids.push({ t: 'br' }); kids.push(...inline(l)); });
  return kids;
}

// Next-occurrence cache: for each delimiter, the next index at or after a
// position. Every opener asks it instead of rescanning, so a line of a
// thousand unmatched asterisks costs one scan, not a thousand.
// ok, when given, is a test on the match position alone (never on the
// opener), so a cached answer stays right for every later opener.
function finder(s) {
  const memo = new Map();
  return (needle, from, ok, key = needle) => {
    const c = memo.get(key);
    if (c !== undefined && (c === -1 || c >= from)) return c;
    let j = s.indexOf(needle, from);
    while (ok && j !== -1 && !ok(j)) j = s.indexOf(needle, j + 1);
    memo.set(key, j);
    return j;
  };
}

export function inline(s, depth = 0) {
  const out = [];
  const find = finder(s);
  let buf = '';
  const flush = () => { if (buf) { autolink(buf, out); buf = ''; } };
  let i = 0;
  while (i < s.length) {
    const c = s[i];
    if (c === '\\' && i + 1 < s.length && /[!-\/:-@\[-`{-~]/.test(s[i + 1])) { buf += s[i + 1]; i += 2; continue; }
    if (c === '`') {
      let n = 1;
      while (s[i + n] === '`') n++;
      const tick = '`'.repeat(n);
      const j = find(tick, i + n, k => s[k + n] !== '`' && s[k - 1] !== '`', 'close' + tick);
      if (j !== -1 && j - i <= SPAN) {
        flush();
        let v = s.slice(i + n, j);
        if (v.length > 1 && v[0] === ' ' && v[v.length - 1] === ' ') v = v.slice(1, -1);
        out.push({ t: 'code', v });
        i = j + n;
        continue;
      }
      buf += tick; i += n; continue;
    }
    if (c === '<') {
      // <https://…> and <mailto:…> autolinks; anything else stays literal text.
      const j = find('>', i + 1);
      const ok = j !== -1 && j - i <= HREF + 1 ? safeHref(s.slice(i + 1, j)) : null;
      if (ok) { flush(); out.push({ t: 'a', href: ok.href, host: ok.host, auto: true, kids: [{ t: 'text', v: s.slice(i + 1, j) }] }); i = j + 1; continue; }
    }
    if (c === '!' && s[i + 1] === '[') {
      const l = link(s, i + 1, find);
      if (l) { flush(); out.push({ ...l.node, image: true }); i = l.end; continue; }
    }
    if (c === '[') {
      const l = link(s, i, find);
      if (l) { flush(); out.push(l.node); i = l.end; continue; }
    }
    if ((c === '*' || c === '_') && depth < DEPTH) {
      const dbl = s[i + 1] === c;
      const mark = dbl ? c + c : c;
      const after = s[i + mark.length];
      const before = i > 0 ? s[i - 1] : ' ';
      // Opens only before a non-space; _ never opens inside a word (snake_case).
      if (after && after !== ' ' && after !== c && !(c === '_' && /\w/.test(before))) {
        const j = find(mark, i + mark.length, k => s[k - 1] !== ' ' && (dbl || s[k + 1] !== c) && !(c === '_' && /\w/.test(s[k + mark.length] || '')), 'close' + mark);
        if (j !== -1 && j - i <= SPAN && j > i + mark.length) {
          flush();
          out.push({ t: dbl ? 'b' : 'i', kids: inline(s.slice(i + mark.length, j), depth + 1) });
          i = j + mark.length;
          continue;
        }
      }
      buf += mark; i += mark.length; continue;
    }
    buf += c; i++;
  }
  flush();
  return out;
}

// link parses [label](target) at s[i] === '['. A refused scheme leaves the
// whole thing as literal text, so `[x](javascript:…)` reads as typed.
function link(s, i, find) {
  const close = find('](', i + 1);
  if (close === -1 || close - i > LABEL) return null;
  const label = s.slice(i + 1, close);
  if (label.includes('[')) return null;
  const end = find(')', close + 2);
  if (end === -1 || end - close > HREF + 2) return null;
  let target = s.slice(close + 2, end).trim();
  if (target.startsWith('<') && target.endsWith('>')) target = target.slice(1, -1);
  const ok = safeHref(target);
  if (!ok) return null;
  return { end: end + 1, node: { t: 'a', href: ok.href, host: ok.host, kids: label ? inline(label, DEPTH) : [{ t: 'text', v: ok.href }] } };
}

const TRAIL = /[.,;:!?'"*_~]+$/;

// autolink splits plain text around bare http(s) URLs.
function autolink(s, out) {
  let i = 0;
  while (i < s.length) {
    const j = s.indexOf('http', i);
    if (j === -1) break;
    const m = /^https?:\/\/[^\s<>"'`]+/.exec(s.slice(j, j + HREF));
    if (!m || (j > 0 && /[\w/]/.test(s[j - 1]))) { pushText(out, s.slice(i, j + 4)); i = j + 4; continue; }
    let url = m[0].replace(TRAIL, '');
    // A closing paren belongs to the URL only when it opened one inside it.
    while (url.endsWith(')') && (url.split('(').length < url.split(')').length)) url = url.slice(0, -1).replace(TRAIL, '');
    const ok = safeHref(url);
    if (!ok) { pushText(out, s.slice(i, j + url.length)); i = j + url.length; continue; }
    pushText(out, s.slice(i, j));
    out.push({ t: 'a', href: ok.href, host: ok.host, auto: true, kids: [{ t: 'text', v: url }] });
    i = j + url.length;
  }
  pushText(out, s.slice(i));
}

function pushText(out, v) {
  if (!v) return;
  const last = out[out.length - 1];
  if (last && last.t === 'text') last.v += v;
  else out.push({ t: 'text', v });
}

// plainText flattens inline nodes, for the label-versus-host check.
export function plainText(kids) {
  return kids.map(k => k.t === 'text' || k.t === 'code' ? k.v : k.kids ? plainText(k.kids) : '').join('');
}

// needsHost says whether a link must show its real host beside its label,
// so a label cannot disguise where the link goes. Only a label that is the
// same URL, or names exactly the same host, goes without it.
const DOMAINISH = /^(?:www\.)?([a-z0-9-]+(?:\.[a-z0-9-]+)+)(?::\d+)?(?:[/?#]\S*)?$/i;

export function needsHost(node) {
  if (node.auto) return false;
  const label = plainText(node.kids).trim();
  if (!label) return false;
  const host = String(node.host).toLowerCase();
  if (label.toLowerCase() === host) return false;
  const asUrl = safeHref(label);
  if (asUrl) return asUrl.host.toLowerCase() !== host;
  const m = DOMAINISH.exec(label);
  if (m) {
    let named;
    try { named = new URL('http://' + label).host.toLowerCase(); } catch { return true; }
    return named !== host && named !== 'www.' + host && 'www.' + named !== host;
  }
  return true;
}
