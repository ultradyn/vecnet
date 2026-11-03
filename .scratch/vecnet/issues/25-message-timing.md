# 25: Message delay and relative time in the web thread

Raised by Max over vecnet, 2026-11-03: "showing a msg delta (delay) in the webui would be great, too. Also, maybe on the right hand side of each message near the reply link button thing, we could have like a humanized time ago, like 8 seconds ago or like 15 minutes ago … putting it over on the right will just keep it out of the way."

## What
- **Delta:** each message after the first shows how long after the previous message in the thread it was sent, e.g. `+3.8s`, `+4m 12s`, `+2h 5m`, `+3d`. Use one decimal under 10 s, whole seconds under a minute. The title reads "3.8 s after the previous message". Muted, beside the existing timestamp. It is computed from hub `created_at`, so it measures hub-to-hub time (send to reply), not network delay.
- **Relative time:** on the right of the message header, next to the reply button: `8s ago`, `15m ago`, `3h ago`, `2d ago`, from the existing `ago()` in `format.js`. The title is the full local date and time. It refreshes on the thread's existing one-second countdown tick (thread.js, the "expires in" timer), only while the thread is visible, and updates only the text of nodes whose label changed.

## Where
`internal/ui/static/thread.js` (message header), `thread-model.js` (delta: a pure `deltaLabel(prevIso, iso)` in `format.js` with jstest), `thread.css`. Do it after the attachments merge, in the same change as issues 21 and 23 (same files).

## Tests
- jstest: `deltaLabel` boundaries (0, 9.94 s, 10 s, 59 s, 60 s, 1 h, 1 d), a negative or out-of-order pair shows nothing, and a bad timestamp shows nothing.
- Screen script: a thread with known timestamps; asserts the delta and ago text and their titles; shots of the header at desktop and narrow widths (the right-side label must not wrap the reply button).

## Done (2026-11-03, branch `md`)
Each message header shows `deltaLabel(prev.created_at, created_at)` (`span.dt`, title from the new `deltaTitle`, e.g. "42 s after the previous message") beside its time, where prev is the previous message in the thread, and `time.ago` beside reply with the full local date and time (new `fullWhen`) as its title. The thread's one-second tick refreshes the ago labels from the hub clock only while the page is visible and writes only labels whose text changed; returning to the tab refreshes at once. `.acts` is `nowrap`/`flex:none`, so ago and reply never split at narrow widths. jstests cover `deltaTitle` and `fullWhen`; `scripts/ui/md-thread.mjs` checks the labels, titles, the untouched-label rule and the 390 px header.
