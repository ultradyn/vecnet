> **Preserved copy (2026-11-06, Hub Preferences Task 6).** This is Max's selected prototype, copied from `.scratch/vecnet/prefs-proto/` so the visual inputs survive for implementation review (plan `docs/plans/2026-11-05-hub-preferences.md`, Design decision). Only the chosen List (A) and Policy (D) sources and their shots are kept; the eliminated variants B and C stay in scratch. The shipped page is `internal/ui/static/settings.js` and its screenshots are `docs/plans/shots/oui/prefs-*.png`.
>
> Superseded here, per the plan: archive mode does **not** retain acknowledged events (7-day acknowledged pruning continues; set `retention.ack_events=0` to keep them too); prices, plan size and growth rates are placeholders, never production estimates; the prototype's `saved` badge is the wire source `stored`; flag/env values are read-only; saving a built-in default shows `default`. The prototype's keys (`files.max_size_mb`, `peers.burst` and so on) are mock names; the real keys are the hub registry's (`internal/prefs/registry.go`).

# PROTOTYPE: vecnet operator Settings / Preferences page

This is a throwaway visual prototype, and it is not the live app. The backend plan is on branch `plan-prefs` (`docs/plans/2026-11-05-hub-preferences.md`). All data is mock data kept in memory, so a reload resets everything except the remembered view. Nothing here touches `internal/`.

View it at http://xsm:10435/vecnet-prefs-proto/. The server holds an rsync'd copy, not a symlink. After editing, run:

```
rsync -a --delete --exclude shots/ ./ ~/served-html/vecnet-prefs-proto/
```

## Decision (2026-11-05)

Max kept two variants, **A (Ledger)** and **D (Policy)**, and merged them into **one Settings page with a view toggle** in the page header:

- **List** (was A) is the default view. It is a modest, Cursor-like long page inside the real app shell:
  - label and help on the left, the control on the right;
  - search, plus an All / Customised / Flags filter;
  - an "On this page" rail and a source legend in the aside.
- **Policy** (was D) is the alternate view:
  - retention is drawn as draggable lanes on a log time axis (now, 1h, 1d, 1y, forever);
  - a 90-day storage forecast reacts to the settings;
  - everything else reads as plain sentences whose values are clickable tokens with inline editors.
- Both views share one store. Edits, the dirty state, save/undo and source badges carry over when you switch.
- The chosen view is remembered in `localStorage` under the key `vecnet.ui.settingsView`, wrapped in try/catch.

Eliminated variants:

- **B (Bento)**: packed tiles.
- **C (prefs.conf)**: a TUI with a `git diff` of pending changes.

Their code is in `eliminated/` and their shots are in `shots/early-variants/`. Both are kept as the primary source.

## URL parameters (dev aid)

- `?view=list|policy` forces a view without remembering it.
- `?state=initial|dirty|saving|saved|error|confirm-archive|confirm-trim|confirm-files` forces a demo state. The floating bar's "demo state" dropdown does the same. The bar is a dev aid, not part of the design.

## Shared behaviour (shared.js)

- **Source badge** on every setting:
  - `default`: built in.
  - `flag`: set on the hub command line, and read-only here.
  - `saved`: saved by an operator here.
  - `edited`: changed, not saved yet.
- **Archive mode** pauses message-history and acknowledged-event retention. Files keep their own expiry.
  - Turning archive mode off asks for confirmation. The default choice is "only items from now on", which deletes nothing.
  - The other choice, "also trim existing history", only works after you type `trim`.
- **File expiry off** asks for confirmation and shows a storage and cost estimate for the hosted hub. An inline warning stays while it is off.
- **Validation:**
  - every number has a range;
  - max file size is capped at 100 MB on hosted hubs;
  - burst cannot exceed the per-minute rate.
  - While anything is invalid, Save is disabled. "Show" jumps to the problem; in Policy it opens that token's editor.
- **Saving:** a sticky save/undo bar shows the change count, then a mock save takes about 1.1 s, then a toast. Saved rows flash briefly.
- **Appearance** settings are per operator. Compact density previews live.

## Open questions for the plan

1. Should flag-set values be editable here, overriding the flag, or stay read-only? The prototype shows them read-only.
2. Does archive mode cover acknowledged events as well as messages? The prototype says yes, and files are never covered.
3. When a value is saved back to its default, should its source revert to `default` (the stored row is deleted) or stay `saved`? The prototype reverts it.
4. Does trimming existing history run at the next sweep or immediately?
5. Hosted pricing ($0.15 / GB-month, 10 GB plan) and the growth rates are placeholders.

## Screenshots

From the repo root, run `node .scratch/vecnet/prefs-proto/shots.mjs [list,policy] [states]`. It uses `scripts/ui/lib.mjs`, needs no npm install, and writes `shots/<view>-<state>-<desk|phone>.png` at 1440x900 and 390x844 (set `SIZES=desk|phone` to pick one).
