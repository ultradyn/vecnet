# Operator web inbox, chat UI and dashboard

Type: prototype
Status: resolved
Blocked by: 09, 14

## Question

Prototype the web inbox (first), then the chat UI, and the read-only dashboard (peers, threads, dead letters, audit log). Rough HTML to react to, dark mode. Decide what v1 ships.

## Answer

Prototype by agent `proto-18-ui`, 2026-11-01. Awaiting Max's pick of a variant; the recommendation below assumes no pick yet.

**Prototype:** `docs/prototypes/operator-ui/index.html` (one throwaway file, mock data from `docs/protocol/examples/`, no persistence). Variants via `?variant=a|b|c`, tabs via `?tab=inbox|chat|dash`, floating bottom bar to switch, `+15 min` to fast-forward the mock clock. How to open: `docs/prototypes/operator-ui/README.md` (`python3 -m http.server 48213 --directory docs/prototypes/operator-ui`).

- **A, modest docs** (Cursor-like nav + reading column + context aside)
- **B, packed tiles** (bento grid: approval tiles with countdown rings, delivery matrix, one tile per peer)
- **C, terminal** (tmux-like panes, keyboard-first, command line, raw event log)

**Screenshots** (1440x900, `docs/prototypes/operator-ui/shots/`): `a-inbox.png`, `a-chat.png`, `a-dash.png`, `b-inbox.png`, `b-chat.png`, `b-dash.png`, `c-inbox.png`, `c-chat.png`, `c-dash.png`. The README has one line on what to look at in each.

**What the prototype settles**

- The envelope and directory shapes are enough to render all three surfaces; no field was missing.
- **Presence vs reachability** renders cleanly as two separate signals (filled dot vs ring). Dots and Grok Bots show `reachable` with `unknown` presence and never read as offline.
- **Inbox sections** fall out of the data: approvals (with expiry), dead letters (retry/discard), messages that **woke** the operator, and messages **held** (`hold`, or `wake_idle` while the operator is `working`). Showing *why* a message was held needs the operator's presence beside the inbox.
- **Receipts** work as quiet per-recipient markers under each message; the delivery matrix in B makes fan-out progress legible at a glance.
- **Approvals** need a visible countdown; the hub-posted `expired` response reads naturally as a system line in the thread.
- **Auto-spill** reads well as a preview plus a dashed `message.md` chip; **reply links** as a quoted one-line parent that jumps on click.
- **Loop control** is visible as a 24 h cut counter by reason plus a per-thread `cut` state; a send into a cut thread is rejected with 422.
- Operator-facing hub reads the UI needs: the operator's own deliveries including held ones, dead-letter list with retry/discard, directory, thread list, thread history, audit tail. All exist in tickets 06, 10, 11 and 14; SPEC.md should list them as the UI's API.

**Left to Max**

1. Which variant, or which mix ("B's inbox with A's chat").
2. Whether inbox and chat merge into one view, or stay as a cross-thread triage queue plus a per-thread chat (prototype keeps them separate and links every inbox item to its thread).
3. Whether `read` is a receipt state the operator needs. Ticket 10 has `accepted`/`processed` only; the prototype shows `read` as a marker to provoke the question.
4. Whether the operator sets their own presence in the UI (B has a one-tap idle/working toggle), since it decides whether `wake_idle` interrupts them.
5. Whether the dashboard stays strictly read-only, with dead-letter retry/discard living in the inbox (as prototyped), or gains actions such as raising a thread budget.

**Recommendation:** ship variant **A** as the v1 shell for inbox and chat, because it is the most legible for long threads and the cheapest to build, and use **B's tiles for the dashboard**, where packed heterogeneous status is the point. Borrow two pieces: B's approval countdown and C's keyboard layer (`j`/`k`, `a` then a digit, `r`/`x`) as shortcuts over A. Keep inbox and chat separate but linked. Defer to after v1: enrolment and scope editing in the UI (the CLI covers them), budget raising, escalation (phase 2 per ticket 15), and C's full command line.

**Found while building:** example `04-approval-request.json` addresses the operator as `peer_…8C`. In examples 01, 02 and 06, `…8C` is the codex session and `…8B` is the operator. The prototype uses `…8B`. Example 04 should be corrected.

### Max's verdict (voice, 2026-11-01)

- **Variant A is the base**: A's inbox and A's chat ship. B's inbox is "a bit of a mess"; nothing from B is better as a whole.
- **The TUI (variant C) is wanted as its own thing too**, alongside the web UI, not instead of it.
- **Dashboard**: B's packed tiles take too much space; Max wants a cleaner dashboard and will redo it later. Treat the dashboard as provisional, read-only.
- **Chat**: start from A's vertical chat but borrow B's better use of horizontal space in the right-hand side panel. The delivery matrix is useful but as an **extra view** one opens, not always-on. The side panel for a thread should also show that thread's files/attachments alongside participants.
- Inbox and chat stay separate but linked (Q2 accepted).
- Max also accepted, in the same round: the history_access reversal (from_join default), wake_idle re-push on idle, loop accounting and per-peer rate limits, the one-tap presence toggle, read-only dashboard, and moving to build planning.

Live copy for review: `http://xsm:10435/vecnet-proto/operator-ui/index.html?variant=a` (synced from `docs/prototypes/operator-ui/`).

### Build follow-through (2026-11-02)

The [operator UI plan](../../../docs/plans/2026-11-01-operator-ui.md) implements the chosen web shell and standalone TUI. The [operator guide](../../../docs/OPERATOR.md) explains the actual clients; [screenshots](../../../docs/plans/shots/oui/README.md) index the state checks. The prototype decision remains resolved. Task 24 adds durable operator-view invalidations; Task 23's shared-hub journey verifies approval answers, reads, wake delivery, presence and both heartbeats, expiry history, and browser credential rejection across the built web and terminal clients. The final screenshot gate is still pending on the outdated section-heading assertion in Task 8's inbox screen script; Phase 3 completion is not yet claimed.
