# 23: Render message text as markdown in the web UI

Raised by Max over vecnet, 2026-11-03: "We should probably render messages in the webui as markdown. things like URLs (eg https://xsm.hornbill-boa.ts.net:29044)".

## Where
- `internal/ui/static/thread.js`: the message body (`div.txt`, line ~328) and the full-text spill view (`pre.full`, line ~303). Today both use `text:` (textContent), so URLs are dead text.
- New `internal/ui/static/md.js` (renderer) and `md-model.js` if a pure tokenizer helps the jstest suite.
- Do it after the attachments branch merges: attachments Tasks 3 and 4 edit `thread.js` and its CSS.

## Subset (enough for agent output)
Paragraphs and hard line breaks, `inline code`, fenced code blocks (no highlighting), **bold**, *italic*, `[text](url)` links, bare `http(s)://` autolinks, `-`/`1.` lists, `>` quotes, `#`–`###` headings (rendered small, not page-sized). Anything else stays literal text.

## Safety (bodies are hostile)
- Build DOM nodes with `h()`/textContent only. Never `innerHTML`, never raw HTML passthrough: `<b>` shows as the characters `<b>`.
- Links: schemes `http`, `https`, `mailto` only; anything else (`javascript:`, `data:`, `vbscript:`, relative) renders as plain text. `rel="noopener noreferrer nofollow"`, `target="_blank"`, `title` = the full href. When link text differs from the href's host, show the real host after the text (e.g. `docs (evil.example)`), so a label can't disguise its target.
- Images (`![a](u)`) render as a link, never `<img>`: no remote fetch, CSP stays unchanged.
- Bound the work: render at most the first 64 KiB as markdown, rest as plain text; linear-time tokenizer, no backtracking regex on whole bodies (ReDoS).
- Bidi/control chars go through the existing `clean()`.

## Budget
No library: marked plus DOMPurify is roughly 60 KiB and the static dir cap is 256 KiB. Target a hand-written renderer under 6 KiB.

## Tests
- jstest: each construct; `javascript:`/`data:` links stay text; `<script>` and `<img onerror>` stay literal; label-vs-host disclosure; 64 KiB cap; a 1 MiB pathological input (`*` and `[` runs) renders in under 200 ms.
- Screen script `md-thread.mjs`: a thread with a link, code block, list and a hostile message; asserts no CSP violation, link attrs, and shots for the README.

## Later (not this issue)
TUI: autolink-only (OSC 8 hyperlinks where the terminal supports them).
