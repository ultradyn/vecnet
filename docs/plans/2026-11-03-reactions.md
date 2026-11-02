# Message Reactions Implementation Plan

> **For agentic workers:** executed by `docs/plans/vecnet-core-build.workflow.mjs` with `docs/plans/reactions.workflow-args.json` (written by the coordinator from this plan). Steps use checkbox (`- [ ]`) syntax.

**Goal:** Any peer or the operator can react to a message with one of eight fixed emoji. Every reaction is an Event (history chips, sync across clients), and a reaction may also wake the message's sender with one Delivery of a new Message kind, `reaction`. Waking reactions are loop-safe, budget-free and coalesced. Surfaces: hub API, CLI, MCP, web operator UI, TUI.

**Architecture:**
- **Storage and API.** A new `reactions` table holds one row per (message, peer, emoji). `POST /v1/reactions` (`Hub.React`) changes a row and appends a `reaction` Event to every participant that can see the target, and to the reactor. It shares the per-peer send limit through an append-only `reaction_ops` table.
- **History.** Thread history aggregates the rows into `Message.reactions`.
- **Wake.** With `priority` `wake` or `wake_idle`, an `add` also writes one `kind: reaction` Message. It is addressed to the target's sender and fanned out by the existing `fanOut`, so push routes, Grok Bot, dots and wake_idle holds behave exactly as for a message.
- **Wake limits:**
  - **At most one wake per (reactor, target, emoji), ever**, recorded in `reaction_wakes`; the operator is exempt.
  - The Message never touches `message_count`, budget or `last_message_at`.
  - It is hidden from history and the matrix, and cannot be replied to or reacted to.
  - A newer waking reaction from the same reactor in the same thread supersedes a still-queued one (new internal delivery state `superseded`).
- **Client defaults.** Clients apply per-emoji default priorities from one shared table.

**Tech Stack:** as the operator UI plan: Go 1.26 (`CGO_ENABLED=0`), SQLite migrations, vanilla ES modules in `internal/ui/static/`, Bubble Tea v2 TUI, Playwright and private tmux screen scripts.

**Spec:** `.scratch/vecnet/issues/24-reactions.md` (decisions settled by Max). PROTOCOL §1.2, §2.2, §2.3, §2.8, §2.13, §4.3–4.4, §5.1–5.2, §6, §13.1, §13.7, §16.1–16.3. `docs/plans/2026-11-01-operator-ui.md` (its Global Constraints still apply except where this plan says otherwise). Reviewed against the code before the build: `.scratch/vecnet/build-reactions/plan-review.md` (all 24 findings folded in).

**Starts from:** `master` after the attachments branch, issue 21 (priority explainers) and issue 23 (markdown messages) merge.
- **Rebase points.** This plan touches files those builds also edit; the executor rebases onto them and never reverts them:
  - `internal/ui/static/thread.js`, `thread.css`, `api.js`, `index.html`: attachments, issue 23 (`thread.js`, `thread.css`) and issue 21 (`thread.js`, `format.js`).
  - `internal/opclient/client.go`, `client_test.go`, `internal/tui/tui.go`, `fake_test.go`, `screen_chat.go`, `screen_chat_test.go`, `internal/tui/testdata/*.golden`: attachments Task 5.
- **Issue 22** (`watch`/`inbox` readability) may land in parallel. Reaction lines only fill the existing `eventFields`, and their test asserts fields rather than the layout, so 22 can change the layout freely.

## Dependency map

```json
{"1":[],"2":[],"3":[1],"4":[3],"5":[4],"6":[4],"7":[3],"8":[3],"9":[2,4,5,6,7,8]}
```

**Build settings:**
- `taskPrefix: "rx"`, worktree `.worktrees/reactions`, branch `reactions` off `master`.
- Status files go in `.scratch/vecnet/build-reactions/`. **Housekeeping:** the coordinator adds `.scratch/vecnet/build-reactions/` to `.gitignore` before the run (today only build-connectors, -operator-ui, -attachments and -openclaw are listed), so status files and the plan review are never swept into a commit. No task owns this.
- Suggested routes: Go tasks 1, 3, 4, 5, 6 → Sol; 7 (TUI) and 8 (web) → Opus; 2 and 9 (docs, journey) → Opus.
- **Waits on Task 4:** Tasks 5 and 6 assert wake behaviour and `not_replyable`, which need Task 4's reaction Messages. Tasks 7 and 8 test only request bodies, chips and the add/remove round trip, so they need only Task 3.

## Global Constraints

- **The fixed set, in this order:** 👍 👎 ❤️ 😂 👀 ✅ ❓ ❗.
  - Code points: U+1F44D, U+1F44E, U+2764 U+FE0F, U+1F602, U+1F440, U+2705, U+2753, U+2757.
  - Names: `+1 -1 heart laugh eyes check q bang`.
  - The order is the display order and the order of `Message.reactions`.
  - A bare U+2764 (no FE0F) is normalised to ❤️. Nothing else is allowed: no skin tones, no ZWJ sequences.
- **Default priority per emoji (clients only):** ✅ ❓ ❗ → `wake_idle`; 👍 👎 ❤️ 😂 👀 → `hold`. The **hub API** defaults an omitted `priority` to `hold`. The CLI, MCP tool, TUI and web send the per-emoji default unless the user overrides it.
- **One code table, two languages:** Go `internal/envelope/reactions.go` and JS `internal/ui/static/react-model.js`. Both are checked against `internal/envelope/testdata/reactions.json`, with code points written as `\u` escapes.
  - The schema's `$defs/reactionEmoji.enum` is checked against the same fixture.
  - The MCP tool description and every client error that lists the set are built from the table at run time.
  - Prose (PROTOCOL §17, docs) may list the set; no other code may.
- **The `reaction` Event is never pushed:** not to push routes, Grok Bot or dots. `internal/events/worker.go` `pushable` and the `events/subscribe` `types` enum (`internal/mcpserver/events.go:51`) stay unchanged. Only the wake layer's Delivery travels through §5.2.
- **Wire shapes** come from `internal/envelope/schema.json`, which stays byte-identical to `docs/protocol/envelope.schema.json` (`just validate-protocol`). `SendRequest.kind` gets its own enum without `reaction`, so peers cannot author a reaction Message through `POST /v1/messages` or `vecnet_send`.
- **Response of `POST /v1/reactions`** (200, also on a no-op):

  ```json
  {"message_id": "msg_…", "thread_id": "thr_…", "emoji": "✅", "op": "add", "changed": true,
   "reactions": [{"emoji": "✅", "peers": ["peer_…"]}],
   "superseded": ["dly_…"],
   "wake": {"message_id": "msg_…", "delivery_id": "dly_…", "recipient": "peer_…", "state": "queued"}}
  ```

  - `reactions` and `superseded` are always arrays, never null.
  - `superseded` lists the reactor's queued reaction Deliveries this call superseded or withdrew, on `add` or `remove`.
  - `wake` is `null` unless this call created a Delivery.
- **Check order.** `Hub.React` performs steps 2–8 itself, so the HTTP and MCP paths behave the same:
  1. Body shape: 400 `invalid_body` (HTTP layer; MCP schema).
  2. `emoji` outside the set: 422 `emoji_not_allowed`. Bad `op` or `priority`: 422 `invalid_value` with `details.field`.
  3. Reactor (`as`): `as_required` / `forbidden`, as on send.
  4. Message unknown or not visible to the reactor: 404 `not_found`.
  5. Target is `kind: reaction`: 422 `not_replyable`.
  6. **No-op:** an `add` whose row exists, or a `remove` whose row is absent, returns 200 `changed: false`, `superseded: []`, `wake: null` at once, before any rate, loop or wake check. A retry after a lost response therefore always succeeds.
  7. Rate: 429 `rate_limited`.
  8. Wake checks (Task 4): eligibility, scopes, loop.
  9. Writes.

  A rate or loop refusal commits only its counter and audit row. `React` wraps `h.DB.Tx` exactly as `sendAs` does (send.go:131-143): the closure returns nil on `errors.As(e, &ce)` with `*cutError`, and `React` returns `ce.api` after the commit.
- **Visibility** = PROTOCOL §16.3:
  - The operator sees every message.
  - Anyone else must participate and, under `from_join`, have joined before the target: `EXISTS (SELECT 1 FROM thread_participants tp JOIN threads t ON t.thr_id = tp.thr_id WHERE tp.thr_id = ? AND tp.peer_id = ? AND (t.history_access = 'full' OR tp.joined_after_seq < ?))`, where the last parameter is the target's `seq`.
  - The same predicate picks who receives the `reaction` Event.
- **After commit**, `React` calls `h.Log.Notify` for every log it appended to: the audience, the `operator_view` peers and the wake recipient. `postReactions` calls `s.kick()` after a successful `React`, as `postMessages` does (messages.go:32). The MCP path kicks through `s.Kick()`.
- **Rendering and safety:**
  - Peer strings reach the DOM only through `h()`/`textContent` and `clean()`; TUI names go through `Clean`.
  - No HTML sinks, CSP unchanged, dark palette, en-AU times.
  - New web static files total ≤ 10 KiB. The cap is `uiMaxBytes` 256 KiB in `internal/httpapi/ui_test.go`; it is about 210 KiB after attachments and md.
- Builds and tests always run through `build2` with two threads: `build2 go test -p 2 …`, `build2 node --test --test-concurrency=2 …`.
- **Screen shots:**
  - Shots go to `docs/plans/shots/oui/`, named `rx<NN>-<screen>-<state>.png` (NN = task number); each UI task produces four dark PNGs, and every PNG is inspected with the Read tool.
  - Scripts build their fixtures at run time through `asPeer`; the demo seed does not change.
  - `just ui-shots` globs `scripts/ui/*.mjs`, so a new script needs no registration.
  - The shots README is updated once, in Task 9.
- Every merge runs the whole screen gate (`just ui-shots`), not only unit tests.
- A test marked **(guard)** passes before its task's change and must keep passing; every other listed test must fail first.

## Review Focus

1. **Bounded wakes.**
   - A non-operator reactor wakes a target's sender at most once per emoji, ever: re-adding after remove, retrying, and rotating ✅→❓→❗→✅ never wake beyond the three distinct emoji.
   - At most one live (not `superseded`) reaction Delivery exists per (reactor, recipient, thread).
   - `remove` never wakes and withdraws that emoji's still-queued wake.
   - A wake already accepted is never superseded.
   - The operator is exempt from the once-ever rule.
2. **No loops through reactions.**
   - A reaction Message cannot be replied to (`POST /v1/messages` with `in_reply_to`, `vecnet_reply`, `vecnet reply`) or reacted to.
   - It is never the implicit loop parent (`latestMessage`).
   - Its depth is target + 1 and its `sender_chain` is the target's chain + the reactor, so the §6 client breaker sees alternation.
   - It is refused past `max_depth` or expiry except for the operator (§16.1).
   - It costs no budget and shares the per-peer send limit.
   - A no-op retry never meets a rate or loop refusal.
3. **Visibility and leaks.**
   - A non-participant, or a `from_join` late joiner reacting to an earlier message, gets 404 and nothing is written.
   - Late joiners and outsiders receive no `reaction` Event for messages they cannot see.
   - A non-participant operator gets `operator_view {reason:"reaction"}` instead.
   - Superseded wakes never reach the operator's unread count or mark-read.
4. **Never pushed, but wakes like a message.**
   - After a reaction there is no POST to an http route, Grok Bot routine or dot.
   - A waking reaction's Delivery is pushed exactly as a Message of that priority would be, including a held `wake_idle` released on idle and the operator's notification webhooks for `wake`.
   - SSE and long-poll readers see the `reaction` Event at once (Notify after commit), not at the next heartbeat.
5. **Web picker under churn.**
   - The thread rebuilds every `.msg` node on each store update (`thread.js:349`). The picker stays open and keeps focus while a peer's reaction arrives.
   - Escape and an outside click close it and return focus to the react button.
   - Hostile peer names in chip titles are cleaned, and nothing shifts the layout.

---

### Task 1: Shared contract: schema, reaction table, migration, invalidation tags

**Depends:** none

**Files:**
- Create:
  - `internal/envelope/reactions.go`, `internal/envelope/reactions_test.go`, `internal/envelope/testdata/reactions.json`
  - `internal/store/migrations/0012_reactions.sql`
  - `docs/protocol/examples/09-reaction-delivery.json`, `docs/protocol/examples/10-reaction-event.json`
- Modify:
  - `internal/envelope/schema.json` and `docs/protocol/envelope.schema.json` (identical), `internal/envelope/types.go`, `internal/envelope/validate.go`, `internal/envelope/validate_test.go`
  - `scripts/validate_examples.py`
  - `internal/store/store_test.go`
  - `internal/opapi/tags.go`, `internal/opapi/tags_test.go`, `internal/opapi/testdata/tags.json`, `internal/opapi/types.go`
  - `internal/ui/static/store.js`, `internal/ui/jstest/store.test.mjs`

**Interfaces:**
- **`reactions.go`:**
  - `var ReactionEmoji = [8]string{…}` (fixed order) and `var ReactionNames = map[string]string{"+1": "👍", …}`.
  - `func CanonicalEmoji(s string) (string, bool)`: emoji of the set only, bare ❤ normalised. The hub uses it.
  - `func ParseReaction(s string) (string, bool)`: emoji or name, case-insensitive name. Clients use it.
  - `func ReactionName(emoji string) string`, `func ReactionIndex(emoji string) int` (−1 when absent), `func DefaultReactionPriority(emoji string) string`.
  - `func ReactionList() string`: "👍 +1, 👎 -1, …", used by every error message and description that lists the set.
  - Types, all with snake_case JSON:
    - `ReactionRef{Target, Emoji}`, the Message sub-object;
    - `ReactionGroup{Emoji string; Peers []string}`;
    - `ReactionEvent{MessageID, ThreadID, PeerID, Emoji, Op, At}`;
    - `ReactionRequest{MessageID, Emoji, Op, As, Priority string}`, with `as` and `priority` omitempty.
- **`types.go`:** `Message` gains two fields.
  - `Reaction *ReactionRef `json:"reaction,omitempty"``.
  - `Reactions []ReactionGroup `json:"reactions,omitempty"``. Only thread history sets it; it is never in a stored body or a Delivery, so Deliveries stay byte-identical. Absent means none.
- **`validate.go`:** `defs` adds `"reaction"`, `"reactionEvent"`, `"reactionRequest"`, and the `Validate` doc comment lists them.
- **`schema.json`:**
  - `$defs/kind` adds `reaction`. `sendRequest.kind` becomes its own enum: the old four, default `message`.
  - New defs:
    - `reactionEmoji`: the enum of 8.
    - `reaction`: `{target: messageId, emoji: reactionEmoji}`, strict.
    - `reactionGroup`: `{emoji, peers: [peerId] minItems 1}`.
    - `reactionRequest`: `message_id`; `emoji` as a string of at most 32 bytes, deliberately not the enum, so the hub can answer 422 `emoji_not_allowed`; `op` enum `add|remove`; optional `as` and `priority`.
    - `reactionEvent`: `{message_id, thread_id, peer_id, emoji, op, at}`, strict.
  - `message.properties` adds `reaction` and `reactions`; the latter's description reads "thread history only; never in a Delivery".
  - `message.allOf` adds two rules:
    - kind `reaction` requires `reaction` and forbids `job_request`, `job_status`, `approval_request`, `approval_response`;
    - `{"if": {"not": {"properties": {"kind": {"const": "reaction"}}}}, "then": {"not": {"required": ["reaction"]}}}`, so no other kind carries `reaction`.
  - `event.type` enum adds `reaction`, and `event.data.anyOf` adds `$ref reactionEvent`.
  - The free-form `event.data` description gains the `operator_view` reason `reaction`.
- **`0012_reactions.sql`:**
  - `reactions(msg_id, peer_id, emoji, created_at, PRIMARY KEY (msg_id, peer_id, emoji)) STRICT`.
  - `reaction_ops(peer_id, at) STRICT` with index `(peer_id, at)`.
  - `reaction_wakes(msg_id, peer_id, emoji, created_at, PRIMARY KEY (msg_id, peer_id, emoji)) STRICT`: one row per wake ever created. It is never deleted by `remove`.
  - `messages` is recreated as `messages_0012` with the `kind` CHECK adding `'reaction'`: same columns, `UNIQUE (thr_id, seq)`, rows copied, old table dropped and the new one renamed. This is the `0007_grokbot_routes.sql` pattern; no table, trigger or view references `messages`.
- **Tags:**
  - `opapi.Tags` maps `reaction` → `["thread:<thread_id>"]`; a missing, empty or non-string thread id gives `[]`.
  - `store.js` `THREAD_TAGS` gains `reaction: []`, meaning the thread tag only.
- **`opapi/types.go`:**
  - `ReactionResult{MessageID, ThreadID, Emoji, Op string; Changed bool; Reactions []envelope.ReactionGroup; Superseded []string; Wake *ReactionWake}`.
  - `ReactionWake{MessageID, DeliveryID, Recipient, State string}`.
  - A `MarshalJSON` emits empty `reactions` and `superseded` as `[]`, like `History` (types.go:424-431).

- [ ] **Step 1: Write the failing tests**

```go
func TestReactionTableMatchesFixture(t *testing.T)     // ReactionEmoji, names, default priorities == testdata/reactions.json in order; schema $defs/reactionEmoji.enum == the same list
func TestCanonicalEmoji(t *testing.T)                  // all 8 ok; bare ❤ → ❤️; "👍🏽", "🙂", "check", "", "✅́́", "‮✅" → false
func TestParseReactionNames(t *testing.T)              // "+1","CHECK","q","bang" → emoji; "smile" → false
func TestReactionMessageSchema(t *testing.T)           // Validate("message"): kind reaction with reaction{} ok; without it fails; kind message, job_status or approval_request carrying reaction fails; kind reaction carrying job_status fails
func TestSendRequestRefusesReactionKind(t *testing.T)  // Validate("sendRequest") with kind "reaction" fails at /kind
func TestReactionEventAndRequestSchema(t *testing.T)   // Validate("reactionEvent"/"reactionRequest"/"reaction"): spec shapes pass, unknown fields fail
func TestMigration0012KeepsMessages(t *testing.T)      // store_test.go, as TestMigration0007KeepsRoutes: pre-0012 schema with a message row → row intact after 0012; a kind 'reaction' row inserts; a 'bogus' kind fails the CHECK
func TestReactionTablesPrimaryKeys(t *testing.T)       // second (msg, peer, emoji) insert into reactions, and into reaction_wakes, conflicts
func TestTagsFixture(t *testing.T)                     // existing; new row {"type":"reaction","data":{"thread_id":"thr_reaction",…}} → ["thread:thr_reaction"]; the bad-data loops (tags_test.go:35,42) include "reaction"
func TestWireSlicesNeverNull(t *testing.T)             // existing; values gain opapi.ReactionResult{}
```

JS (`store.test.mjs`):
- The fixture check covers the new row.
- The malformed-events test (store.test.mjs:51-69) gains `{type:'reaction'}`, `{type:'reaction', data:{}}`, `{type:'reaction', data:{thread_id:7}}` and `{type:'reaction', data:{thread_id:''}}`, each → `[]`.

`TestOpenMigratesAndIsIdempotent` and `TestExamplesValidate` gain 0012 and examples 09/10. `validate_examples.py` validates 09 as `delivery` and 10 as `event`.

- [ ] **Step 2:** These fail on the new tests:
  - `build2 go test -p 2 ./internal/envelope/ ./internal/store/ ./internal/opapi/ -run 'Reaction|Migration0012|Tags|Examples|OpenMigrates|WireSlices'`
  - `build2 node --test --test-concurrency=2 internal/ui/jstest/store.test.mjs`
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** These pass: `build2 go test -p 2 ./...`, `build2 node --test --test-concurrency=2 internal/ui/jstest/`, `just validate-protocol`.
- [ ] **Step 5: Commit** `feat: reaction wire shapes, fixed emoji table, reactions migration`

---

### Task 2: PROTOCOL §17 amendment, glossary, changelog

**Depends:** none (written from this plan; Task 9 re-checks it against the code)

**Files:** Modify `docs/PROTOCOL.md`, `GLOSSARY.md`. Create `CHANGELOG.md` (repo root, newest first; there is none yet).

**Content:**
- **New `## 17. Reactions (2026-11-03)` [issue 24],** citing the sections it extends:
  - **17.1 Event layer:**
    - the fixed set, its order and its canonical code points;
    - storage: one row per (message, peer, emoji);
    - the `reaction` Event `{message_id, thread_id, peer_id, emoji, op, at}`, appended to every participant that can see the target (§16.3) plus the reactor;
    - the Event is never pushed; unknown event types are already ignored (§2.13).
  - **17.2 `POST /v1/reactions`:**
    - request, response (top-level `superseded`) and check order as in Global Constraints;
    - idempotent: a repeated add or remove returns 200 `changed: false` before any rate, loop or wake check;
    - every state change counts against §16.2's sends-per-minute, which it shares with sends; a no-op does not count.
  - **17.3 Read side:**
    - `Message.reactions` in `GET /v1/threads/{id}/messages` and `vecnet_thread`, in fixed order, peers in reaction order; absent means none;
    - reaction Messages never appear in history;
    - invalidation: `reaction` → `thread:<thread_id>`;
    - `operator_view` reason `reaction`, sent to enabled operator peers that did not receive the Event.
  - **17.4 Wake layer:**
    - `priority` defaults to `hold`, which means the Event only.
    - With `wake`/`wake_idle`, an `add` that changed state creates one Message of kind `reaction`: `recipients: [target sender]`, `in_reply_to: target`, `reaction: {target, emoji}`, `content.text` = the emoji, `priority` as given, `loop.sender_chain` = the target's chain + the reactor, `depth` = target + 1. It has one Delivery under §5.2.
    - **At most one wake per (reactor, target message, emoji), ever:** a later add of the same emoji, after a remove, is Event-only. Peers of kind `operator` are exempt, as §16.1 exempts them from loop refusals. With the eight emoji, an agent can wake a given message's sender at most eight times, and three times with the default priorities. Real messages are budgeted and depth-bounded, so the thread budget stays the hard stop (§6).
    - No wake (Event only) when the target's sender is the reactor, a source or `hub`.
    - Scopes apply (`scope_denied`).
    - `max_depth` and `expires_at` apply (§6), and a refused wake refuses the whole reaction. The operator resets depth and TTL (§16.1).
    - Exempt from the thread budget; changes neither `message_count` nor `last_message_at`; never the implicit loop parent.
    - Replying to it or reacting to it is 422 `not_replyable`.
    - Connectors' breakers treat it like any message.
    - An operator that is not a participant may wake, and its reaction Message then has a sender outside `thread.participants`.
  - **17.5 Coalescing:**
    - A newer waking reaction by the same reactor in the same thread moves that reactor's still-`queued` reaction Deliveries to the recipient to `superseded`.
    - A `remove` withdraws the still-`queued` wake of that (target, emoji) the same way.
    - `superseded` is terminal and internal: never pushed, absent from the operator inbox, its unread count and mark-read; the hub sends no receipt when it supersedes.
  - **17.6 Client defaults:** the per-emoji priority table and the names, for CLI, MCP, TUI and web.
- **Edits elsewhere:**
  - §1.2: the Message `kind` list, the Event types list, and a sentence after "Receipts and presence are Event types…": a reaction is always an Event and, only when it wakes, also a `reaction` Message that cannot be replied to.
  - §2.1: endpoint index row.
  - §2.2: rejection table gains `not_replyable`.
  - §2.8: history `reactions`.
  - §2.13: invalidation table row `reaction`, and the `operator_view` reason list.
  - §4.3: tool table `vecnet_react`, and the tool count.
  - §4.4 Types: one clause that `reaction` Events are never pushed to a dot.
  - §5.1: `superseded` beside the diagram's terminal states.
  - §6 "What counts": reactions exempt from the budget, with the once-ever wake bound.
  - §13.1: drop `reaction` Events like receipts; a Delivery of kind `reaction` MAY wake.
  - §13.7: `react <message_id> <emoji|name> [--remove] [--wake|--wake-idle|--hold]`.
  - §15 reason codes: `emoji_not_allowed` (422), `not_replyable` (422).
- `GLOSSARY.md`: a **Reaction** entry.
- `CHANGELOG.md`: `# Changelog` plus one line: `- 2026-11-03: reactions: POST /v1/reactions, reaction Event, Message kind reaction, operator_view reason reaction (PROTOCOL §17).`

- [ ] **Step 1:** Write it. Every rule cites §17 or the section it amends.
- [ ] **Step 2:** `just validate-protocol` passes, and `grep -n 'approval_expired' docs/PROTOCOL.md` shows no Event-type list missing `reaction`.
- [ ] **Step 3: Commit** `docs: PROTOCOL §17 reactions`

---

### Task 3: Hub event layer: `POST /v1/reactions`, storage, fan-out, history, rate share

**Depends:** 1

**Files:**
- Create: `internal/hub/reactions.go`, `internal/hub/reactions_test.go`, `internal/httpapi/reactions.go`, `internal/httpapi/reactions_test.go`.
- Modify:
  - `internal/httpapi/server.go`: one route.
  - `internal/hub/history.go`: `ThreadMessages` aggregation.
  - `internal/hub/ratelimit.go`: `rateVerdict` sends query.
  - `internal/hub/tickers.go`: prune `reaction_ops` on the slow tick.
  - `internal/hub/operator_view.go`: an `except` set.

**Interfaces:**
- `func (h *Hub) React(ctx context.Context, c auth.Caller, req envelope.ReactionRequest, ingress string) (*ReactResponse, error)`. `ReactResponse` matches Global Constraints (`Superseded` always non-nil; `Wake *ReactWake` nil until Task 4).
- **Before the Tx:**
  - `CanonicalEmoji`, else 422 `emoji_not_allowed` with `details.emoji`.
  - `op` ∈ {add, remove}, else 422 `invalid_value` with `details.field` `op`.
  - `priority` ∈ {"", wake, wake_idle, hold}, else 422 `invalid_value` with `details.field` `priority`; "" means `hold`.
- **Then one `h.DB.Tx`** (BEGIN IMMEDIATE), wrapped as Global Constraints says for `cutError`:
  1. `resolveSender(ctx, tx, c, req.As)`.
  2. `loadTarget(ctx, tx, msgID) (msg *envelope.Message, thr string, seq int, found bool, err error)`, via `SELECT thr_id, seq, body FROM messages WHERE msg_id = ?`. `loadMessage` is untouched.
  3. Visibility through `messageVisible(ctx, tx, snd sender, thr string, seq int) (bool, error)`, using the Global Constraints predicate. When not found or not visible: 404 `not_found` with `details.message_id`.
  4. `msg.Kind == "reaction"` → 422 `not_replyable`. This check is cheap; Task 4 adds its test.
  5. No-op check with `SELECT 1 FROM reactions WHERE msg_id=? AND peer_id=? AND emoji=?`. Return 200 `changed: false` and the current aggregate when nothing would change.
  6. `checkRate(ctx, tx, snd.peer, false, now)`; on 429, `countRateCut` and return `&cutError{api}`.
  7. *(Task 4 inserts the wake checks here.)*
  8. Writes:
     - `add` uses `INSERT`; `remove` uses `DELETE`;
     - insert one `reaction_ops(peer, now)` row;
     - append the `reaction` Event to `reactionAudience(ctx, tx, thr, seq, reactor) ([]string, error)`: visible participants (same join) plus the reactor, deduped, in `peer_id` order;
     - `noteOperatorView(ctx, tx, "reaction", thr, except)` with the audience as `except`, so operator peers already in it get no `operator_view`. The existing callers pass nil;
     - `audit.Record(…, "react", [msg, thr], {emoji, op, priority})`;
     - collect every owner appended to.
  9. Return the message's aggregate from `reactionGroups(ctx, tx, []string{msgID})`.
- **After commit:** `h.Log.Notify(owners...)`.
- **Helpers and hub changes:**
  - `func reactionGroups(ctx, tx, msgIDs []string) (map[string][]envelope.ReactionGroup, error)`: fixed-set order, peers by `created_at, peer_id`. `ThreadMessages` calls it once per page and sets `Message.Reactions`.
  - `rateVerdict`'s sends query counts `messages WHERE sender = ?1 AND kind != 'reaction' AND created_at > ?2` `UNION ALL` `reaction_ops WHERE peer_id = ?1 AND at > ?2`, ordered by time. `windowRetry` keeps its call; its trailing bare `?` becomes `?3`, so sources are unchanged.
  - `Tick(slow)`: `DELETE FROM reaction_ops WHERE at < now − 1 h`.
  - Until Task 4 lands, `wake` and `wake_idle` are accepted and behave as `hold`. No Task 3 test pins that.
- **HTTP handler** `httpapi/reactions.go` `postReactions`:
  - parses with `decodeObject(raw, {message_id, emoji, op, as, priority}, {message_id, emoji, op})` and `stringField`;
  - ingress is `operator_ui` when an operator sends `X-Vecnet-Ingress: operator_ui`, else `http_send` (as `messages.go:19`);
  - after a successful `React`, calls `s.kick()`, then writes 200 JSON;
  - a 429 gets `Retry-After` from the existing `writeError`.
  - Route: `mux.HandleFunc("POST /v1/reactions", s.postReactions)`.

- [ ] **Step 1: Write the failing tests** (`hub/reactions_test.go`, package `hub_test`, reusing `mustSend`, `count`, `apiError`, `mustReject` from `send_test.go`/`loop_test.go`):

```go
func TestReactAddRemoveIdempotent(t *testing.T)        // add → changed true; add again → changed false, one row, one event; remove → changed true; remove again → false; superseded is [] throughout
func TestReactVisibility(t *testing.T)                 // outsider → 404 not_found; from_join late joiner on an earlier message → 404; same joiner on a later message → 200; history_access full → 200; operator non-participant → 200
func TestReactEmojiNotAllowed(t *testing.T)            // "🙂", "👍🏽", "check", "✅́", "‮✅" → 422 emoji_not_allowed; bare ❤ stored and echoed as ❤️
func TestReactBadOpAndPriority(t *testing.T)           // op "toggle" → 422 invalid_value field op; priority "now" → field priority (Hub.React directly and over HTTP)
func TestReactAsPrincipal(t *testing.T)                // principal without as → 422 as_required; as outside principal → 403 forbidden
func TestReactSharesSendLimit(t *testing.T)            // SetLimits sends_per_min 3: 2 sends + 1 react ok; next react → 429 rate_limited details.limit sends_per_min; next send → 429; at the limit a no-op add → 200 changed:false and records no reaction_ops row
func TestReactionEventFanOut(t *testing.T)             // participants that can see it and the reactor get one `reaction` event; Validate("reactionEvent") passes on its data; a late joiner and an outsider get none
func TestReactionWakesLongPoll(t *testing.T)           // a parked GET /v1/events?wait=25 of a participant returns the reaction event in well under a second (Notify after commit)
func TestReactionOperatorView(t *testing.T)            // a non-participant operator gets operator_view {reason:"reaction", thread_id}; a participant operator gets the reaction event and no operator_view
func TestReactionEventNeverPushed(t *testing.T)        // peer with a verified http route (testhub.NewCallback): a reaction adds no POST; a dot subscription with default types receives nothing
func TestHistoryAggregatesReactions(t *testing.T)      // GET /v1/threads/{id}/messages: reactions in fixed-set order, peers in reaction order, field absent on unreacted messages; delivery bodies never carry `reactions`
func TestReactionOpsPruned(t *testing.T)               // FakeClock + Tick(slow): rows older than 1 h are gone
func TestSendRefusesReactionKind(t *testing.T)         // (guard after Task 1) POST /v1/messages with kind "reaction" → 400 invalid_body, details.path /kind
```

`httpapi/reactions_test.go`:
- `TestPostReactionsShape`: an unknown field, a missing `op`, or a non-string `emoji` → 400 `invalid_body`. A 200 body decodes into `opapi.ReactionResult` with `superseded` `[]`.
- `TestPostReactionsRetryAfter`: a 429 carries `Retry-After`.

- [ ] **Step 2:** `build2 go test -p 2 ./internal/hub/ ./internal/httpapi/ -run 'React|Reaction|HistoryAggregates|PostReactions|SendRefusesReactionKind'` fails (except the guard).
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `build2 go test -p 2 ./...` passes; the rate-limit and history suites are unchanged.
- [ ] **Step 5: Commit** `feat: reactions as events: POST /v1/reactions, history chips, shared rate limit`

---

### Task 4: Hub wake layer, loop rules and coalescing

**Depends:** 3

**Files:** Modify:
- `internal/hub/reactions.go`, `internal/hub/reactions_test.go`
- `internal/hub/send.go`: the `in_reply_to` check.
- `internal/hub/threads.go`: `ReplyThread`, `latestMessage`.
- `internal/hub/history.go`: hide reaction Messages.
- `internal/hub/deliveries.go`: `DeliveryPushGate`.
- `internal/opview/inbox.go`, `internal/opview/threads.go`, `internal/opview/inbox_test.go`, `internal/opview/threads_test.go`
- `internal/hub/loop_test.go`

**Interfaces:**
- **Wake checks** (step 7 of Task 3's Tx), for a changing `add` with `priority` `wake`/`wake_idle`. They all run before any write:
  - **Eligibility:** wake only when the target's sender is a `peer_` id other than the reactor and, unless `snd.kind == "operator"`, no `reaction_wakes` row exists for (target, reactor, emoji). An ineligible add proceeds Event-only with `wake: null` and still returns 200.
  - **Scopes:** `targets, err := h.resolveTargets(ctx, tx, snd, []string{target.Sender})` gives the send's scope and unknown-recipient errors. A disabled recipient is not refused, as on send.
  - **Loop:** `loop, apiErr := h.buildLoop(target, snd.peer, snd.kind, &envelope.SendRequest{}, now)` gives depth target + 1, `sender_chain` = the target's chain + the reactor, and the operator reset (§16.1). On a cut: `countCut(ctx, tx, CounterLoopCut, snd.peer, []string{thr}, {"reason": code, "via": "reaction"})`, then `&cutError{apiErr}`. There is no budget check.
- **Writes**, after the row insert, for a wake:
  1. **Coalesce:** `UPDATE deliveries SET state='superseded', held=0, next_attempt_at=NULL, updated_at=? WHERE recipient=? AND thr_id=? AND state='queued' AND msg_id IN (SELECT msg_id FROM messages WHERE kind='reaction' AND sender=? AND thr_id=?) RETURNING dly_id`. The ids go into `superseded`.
  2. Build the Message: `kind: "reaction"`, `in_reply_to: target`, `recipients: [target.Sender]`, `priority`, `content.text` = the emoji, `reaction: {target, emoji}`, `loop`, and empty attachments and metadata.
  3. `insertReactionMessage(ctx, tx, thr, &msg, msgJSON)`: the next `seq` and the row only. `message_count`, `budget_remaining` and `last_message_at` are untouched.
  4. `INSERT INTO reaction_wakes …`, skipped for the operator.
  5. `h.fanOut(ctx, tx, thr, &msg, msgJSON, targets, nil, ingress, msg.Loop.ExpiresAt, now)`.
  6. Add the recipient to the owners to notify.
  7. After commit: `h.notifyOperatorDeliveries(ctx, msgID)`, which fires the operator notification webhooks for `wake` (§5.2).
- **`remove` that changed:** the same UPDATE, narrowed by `json_extract(m.body,'$.reaction.target') = ? AND json_extract(m.body,'$.reaction.emoji') = ?`. The ids go into `superseded`. It never creates a Delivery and never deletes a `reaction_wakes` row.
- **Reaction Messages stay out of every list and loop parent:**
  - `sendTx`: a parent loaded from `in_reply_to` of kind `reaction` → 422 `not_replyable`, `details.in_reply_to`.
  - `ReplyThread`: the same check, after visibility.
  - `latestMessage`: `AND kind != 'reaction'`.
  - `ThreadMessages`: `AND kind != 'reaction'` in the page query, so pages stay full.
  - `opview` `Matrix`:
    - the row query (threads.go:264) adds `AND kind != 'reaction'`;
    - the recipient-column query (threads.go:246-247) counts only deliveries of non-reaction messages: `AND d.msg_id IN (SELECT msg_id FROM messages WHERE thr_id = p.thr_id AND kind != 'reaction')`.
  - `opview` inbox:
    - the shared `visible` predicate (inbox.go:44) adds `AND d.state != 'superseded'`, which covers both the page and the unread count;
    - `MarkThreadRead`'s query (inbox.go:205) adds `AND state != 'superseded'`, so no read receipt goes out for a withdrawn wake.
- **Push gating:** `DeliveryPushGate` returns `GateNever` for `superseded`. `ClaimDue`, `ExpireDeliveries`, ack and the processed/failed receipt `from` lists already ignore it (guards below).

- [ ] **Step 1: Write the failing tests**

```go
func TestReactionHoldCreatesNoDelivery(t *testing.T)          // hold (and omitted priority): no kind reaction row, no deliveries row, no reaction_wakes row
func TestReactionWakeOneDeliveryToSender(t *testing.T)        // 3-peer thread, B reacts ✅ wake to A's message: one delivery to A; C and B get none; Validate("delivery") passes; kind reaction, in_reply_to target, reaction{target,emoji}, recipients [A]; response wake set
func TestReactionWakePushedPerRoute(t *testing.T)             // A has a verified http route: one signed POST whose body is that Delivery
func TestReactionWakeIdleHeldThenReleased(t *testing.T)       // A presence working: delivery held; A posts idle → pushed once
func TestReactionLoopFields(t *testing.T)                     // storedLoop(reaction msg): Depth == target.Depth+1, SenderChain == target chain + B
func TestReactionRefusedPastMaxDepth(t *testing.T)            // target at depth == max_depth: 422 max_depth_exceeded, no reactions row, no event, loop_cut counter +1 and audit row committed
func TestReactionRefusedAfterExpiry(t *testing.T)             // FakeClock past target expires_at: wake → 422 expired; hold on the same message → 200
func TestNoopRetryAfterExpiryIs200(t *testing.T)              // add ✅ wake succeeds; clock passes expiry; the same add → 200 changed:false, no new delivery
func TestOperatorReactionNeverCut(t *testing.T)               // operator wake on an expired, max-depth message → 200, one delivery, depth 0
func TestReactionNoBudgetCost(t *testing.T)                   // budget_remaining, message_count, last_message_at unchanged after a waking reaction; works when budget is 0
func TestReplyToReactionRefused(t *testing.T)                 // POST /v1/messages in_reply_to the reaction msg → 422 not_replyable; ReplyThread → 422 not_replyable
func TestReactToReactionRefused(t *testing.T)                 // POST /v1/reactions on the reaction msg (A can see it) → 422 not_replyable
func TestRepeatedAddDoesNotWakeTwice(t *testing.T)            // add ✅ wake twice → one delivery
func TestReaddAfterRemoveDoesNotWakeAgain(t *testing.T)       // B: add ✅ wake (A accepts it), remove, add ✅ wake → changed true, reaction event sent, wake null, still one delivery
func TestEmojiRotationWakesAtMostThree(t *testing.T)          // B rotates ✅→❓→❗→✅→❓→❗ (remove between, all wake, A accepts each) on one target → exactly 3 deliveries to A
func TestOperatorReaddDoesWake(t *testing.T)                  // operator: add ✅ wake, remove, add ✅ wake → 2 deliveries; no reaction_wakes rows for the operator
func TestRemoveNeverWakes(t *testing.T)                       // remove with priority wake → no delivery
func TestRemoveWithdrawsQueuedWake(t *testing.T)              // A working, B adds ✅ wake_idle (held), removes → response superseded = [that dly]; state superseded; release on idle pushes nothing
func TestCoalesceSupersedesQueuedWake(t *testing.T)           // A working: B ✅ then ❗ (wake_idle) on two A messages in one thread → first superseded (listed in superseded), second queued; idle → exactly one push
func TestCoalesceLeavesAcceptedWake(t *testing.T)             // first delivery acked (accepted) → second creates a delivery, superseded []
func TestCoalesceIsPerThreadAndReactor(t *testing.T)          // B's wake in thread X does not supersede B's in thread Y, nor C's in X
func TestReactionWakeSkipsSelfAndSources(t *testing.T)        // react wake to own message, and to a source-sent message → 200, wake null, no delivery
func TestReactionWakeRespectsScopes(t *testing.T)             // B's scopes deny A → 403 scope_denied, nothing written
func TestHistoryHidesReactionMessages(t *testing.T)           // history lists target, not the reaction msg; next_after paging unaffected
func TestPlainSendAfterReactionContinuesLoop(t *testing.T)    // loop_test.go: send without in_reply_to after a reaction → depth = latest non-reaction + 1
func TestSupersededNeverPushed(t *testing.T)                  // DeliveryPushGate(superseded) == GateNever (fails first); guards: ClaimDue skips it, ack and ExpireDeliveries leave it superseded, the hub appends no receipt to the reactor
func TestReactionWakeNotifiesOperator(t *testing.T)           // agent reacts ✅ wake to an operator message: operator notification webhook fires once (notify_test.go pattern)
```

`opview`:
- `TestInboxHidesSuperseded`: a superseded delivery to the operator is absent from `Deliveries`, is not counted in `Unread`, and `MarkThreadRead` posts no receipt for it.
- `TestMatrixHidesReactionMessages`: no row for the reaction message, and no column for a peer whose only delivery is a reaction wake.

- [ ] **Step 2:** `build2 go test -p 2 ./internal/hub/ ./internal/opview/ -run 'Reaction|React|Readd|Rotation|Noop|Coalesce|Remove|Superseded|PlainSendAfterReaction|InboxHidesSuperseded|MatrixHidesReaction'` fails.
- [ ] **Step 3:** Implement.
- [ ] **Step 4:** `build2 go test -p 2 ./...` passes.
- [ ] **Step 5: Commit** `feat: waking reactions: one Delivery to the sender, once per emoji, loop-safe, coalesced`

---

### Task 5: CLI `vecnet react`, reaction lines in watch, inbox and thread

**Depends:** 4

**Files:** Modify `internal/cli/commands.go`, `internal/cli/watch.go`, `internal/cli/cli_test.go`.

**Interfaces:**
- `usageText`, messaging section, gains: `react <message_id> <emoji|name> [--remove] [--wake|--wake-idle|--hold]`, with the note that the default priority depends on the emoji (✅ ❓ ❗ wake_idle, others hold). Usage does not list the set; a refusal prints `envelope.ReactionList()`. `(*app).dispatch` gains `case "react"`.
- `func (a *app) cmdReact(args []string) error`:
  - parses with `parseFlags("react", args, mergeSpec(priorityFlags, {"remove": boolFlag}))`;
  - the emoji goes through `envelope.ParseReaction(pos[1])`, else a usage error listing `ReactionList()`;
  - priority comes from `reactPriority(o, emoji) (string, error)`: at most one priority flag, else `envelope.DefaultReactionPriority`. It does not use `priority()`, which demands exactly one flag;
  - `--remove` with a priority flag is a usage error;
  - `a.as` goes into the body, then `a.call(POST, "/v1/reactions", body, &out)`;
  - human output is `reacted ✅ msg_…`, `removed ✅ msg_…` or `no change ✅ msg_…`, plus ` (wakes peer_…, dly_…)` when `wake` is set and ` superseded dly_…[,dly_…]` when `superseded` is non-empty;
  - `--json` prints the response.
- `eventFieldsOf` gains `case "reaction"`: thread = `thread_id`, sender = `peer_id`, msg = `message_id`, text = `"+✅"` for add or `"-✅"` for remove.
  - This is the only `watch.go` change. `monitorLine`, `inboxLine` and `--deliveries-only` stay as they are, so issue 22's layout applies unchanged.
  - A delivery of kind `reaction` already prints as a delivery whose text is the emoji.
- `thread` output (`commands.go:909`): a message with `reactions` gets one indented line after it, `  reactions: 👍 2  ✅ 1`.

- [ ] **Step 1: Failing tests** (against `testhub.Start(t)`):
  - `TestReactCommand`: add, repeat (no change), `--remove`; the hub row matches.
  - `TestReactNamesAndDefaults`: `check` wakes the target's sender with a `wake_idle` reaction delivery; `+1` creates no delivery; `--hold` on `check` creates none.
  - `TestReactRefusals`: `smile` exits 1 listing names; two priority flags exit 1; `--remove --wake` exits 1; a hub 422 `not_replyable` (reacting to the reaction message just created) exits 2 with the code.
  - `TestReactionEventFields`: a unit test of `eventFieldsOf` on a `reaction` event gives thread, sender, msg and `+✅`.
  - `TestWatchPrintsReaction`: a loose `--for-monitor` match `reaction.*\+✅`, so issue 22's layout does not break it.
  - `TestThreadShowsReactions`.
- [ ] **Step 2–4:** red, implement, then `build2 go test -p 2 ./internal/cli/` passes.
- [ ] **Step 5: Commit** `feat: vecnet react and reaction lines in watch and thread`

---

### Task 6: MCP `vecnet_react`

**Depends:** 4

**Files:** Modify `internal/mcpserver/tools.go`, `internal/mcpserver/tools_test.go`, `internal/mcpserver/resources_test.go`.

**Interfaces:**
- `toolSpecs` adds `vecnet_react`:
  - input: `object({message_id: ref("messageId"), emoji: {type: string, description: "one of " + envelope.ReactionList()}, op: {enum: [add, remove], default: add}, priority: ref("priority"), as: ref("peerId")}, "message_id", "emoji")`;
  - annotations: `writeAnnotations(false)`;
  - the description says it writes to the operator's bus (the phrase the existing test demands), states the per-emoji default priority and the once-per-emoji wake rule, and says reactions cannot be replied to.
- `callReact(ctx, s, c, raw)`:
  - decode the arguments;
  - an empty `req.Op` becomes `"add"`, since a schema `default` is not injected;
  - `envelope.ParseReaction`; an unknown value passes through so the hub answers `emoji_not_allowed`;
  - an omitted priority becomes `DefaultReactionPriority` when `op` is `add`;
  - `s.Hub.React(ctx, c, req, mcpIngress)`, then `s.Kick()`;
  - return the response.
- Update the comment `toolSpecs returns the nine tools` (tools.go:528) to ten.
- `events.go` does not change, because dots never receive `reaction`. The inbox resource does not change either: it already returns the raw event tail, so `reaction` Events appear in it.

- [ ] **Step 1: Failing tests:**
  - `TestToolsListNamesAndHints`: the exact sorted list (tools_test.go:244-247) gains `vecnet_react`, not read-only.
  - `TestReactTool`:
    - `check` by name gives a `wake_idle` delivery to the sender;
    - `+1` creates no delivery;
    - an omitted `op` adds;
    - `op: remove` works;
    - `not_replyable` comes back as a tool error with the code.
  - `TestInboxResourceListsReactions` (`resources_test.go`): after a reaction, `resources/read` of the reactor's inbox contains a `reaction` event.
  - `TestEventsSubscribeRefusesReactionType` **(guard)**: `types: ["reaction"]` → `-32602`.
  - `TestSendToolRefusesReactionKind` **(guard after Task 1)**: `vecnet_send` with `kind: "reaction"` is a tool error at `/kind`.
- [ ] **Step 2–4:** red, implement, then `build2 go test -p 2 ./internal/mcpserver/` passes.
- [ ] **Step 5: Commit** `feat: vecnet_react MCP tool`

---

### Task 7: TUI chips and `:react`, `opclient.React`

**Depends:** 3

**Files:** Modify `internal/opclient/client.go`, `internal/opclient/client_test.go`, `internal/tui/tui.go` (`API`), `internal/tui/fake_test.go` (`fakeAPI.React`), `internal/tui/screen_chat.go`, `internal/tui/screen_chat_test.go`. Create `internal/tui/testdata/chat-reactions-80x24.golden` and `scripts/ui/rx-tui.mjs`.

**Interfaces:**
- **`opclient.React`:** `func (c *Client) React(ctx context.Context, req envelope.ReactionRequest) (opapi.ReactionResult, error)`, implemented as `c.do(POST, "/v1/reactions", req, &out)`.
  - `request()` adds `X-Vecnet-Ingress: operator_ui` for this path as it does for `/v1/messages` (`client.go:66`).
  - `tui.API` gains `React` with the same signature, and `var _ API = (*opclient.Client)(nil)` pins it.
- **Chips row:** `chatLog.item` (`screen_chat.go:1202`) emits a reactions row before the `receipts` row when `msg.Reactions` is non-empty, indented by `chatIndent`.
  - It is ASCII so cell widths stay exact, since emoji widths vary between terminals (❤️ most of all): `[+1 2] [check 1*]`, where `*` marks a reaction by the operator's own peer.
  - On the selected message the row shows names instead: `[check: codex, you] [+1: sleuth]`, with names through `l.names.short` and `Clean`.
  - It truncates with `+N more`, like receipts.
  - Reactions arrive through `opapi.Tags` (`reaction` → `thread:<id>`, Task 1), so `onTailBatch` reloads the thread with no TUI event code.
- **`:react` command:** `chatScreen.register` adds `m.Commands["react"]`. Help: `[n] <emoji|name> [--wake|--wake-idle|--hold]  toggle a reaction; n counts back from the newest message (1 = newest), default the selected one`.
  - **Parsing:** the first of two positional arguments is `n` only when it matches `^[1-9][0-9]*$`. With one positional argument, it is always the emoji, so `:react +1` is 👍 and `:react -1` is 👎. `n` counts only peer messages: hub system lines (`sender == "hub"`) are skipped.
  - **Toggle:** when the operator's peer already has that reaction, `:react` removes it; otherwise it adds it with the per-emoji default or the override flag.
  - **Refusals** toast and return `chatRefusedMsg`, so the line stays: no open thread, no message, `n` out of range, an unknown emoji or name (the toast lists `ReactionList()`), two priority flags.
  - **Hub errors** toast with the code; `rate_limited` shows the retry time, as `chatSendWhy` formats it.
  - **Success** toasts `reacted ✅` or `removed ✅` and reloads the thread.
  - The chat's `Hints()` gains `:react`.

- [ ] **Step 1: Failing tests:**
  - `TestClientReact` (`opclient`, testhub): add, then remove. It decodes `opapi.ReactionResult` with `superseded` `[]`, and the ingress header is sent.
  - Chat tests:
    - `TestReactionChipsRow`: counts and the `*` marker;
    - `TestReactionChipsNamesWhenSelected`;
    - `TestReactCommandToggles`: the fake sees add, then remove;
    - `TestReactCommandDefaults`: `check` sends `wake_idle`, `eyes` sends `hold`, `--wake` overrides;
    - `TestReactCommandByNumber`: `:react 2 +1` targets the second-newest peer message, skipping a hub system line; `:react +1` and `:react -1` target the selected message with 👍 and 👎;
    - `TestReactCommandRefusals`: no thread, `n` out of range, `smile`, two flags; the fake sees no call;
    - `TestReactHubErrorToast`: 422 `not_replyable`, and 429 with the retry time;
    - `TestChatReactionsGolden`: 80×24;
    - `TestChatHostile`: extended with a hostile-named reactor on the selected message.
- [ ] **Step 2–4:** red, implement, then `build2 go test -p 2 ./internal/tui/ ./internal/opclient/` passes. Then run `build2 node scripts/ui/rx-tui.mjs`. The script makes two agents react ✅ and 👍 to an operator message through `asPeer` before opening the thread.
- **Shots:**
  - `rx07-tui-initial`: chips under a message.
  - `rx07-tui-progress`: the selected message's row shows names, with `:react check` typed.
  - `rx07-tui-success`: after Enter, `[check 2*]`.
  - `rx07-tui-error`: `:react 1 smile`, the toast lists the names, and the line is kept.
- [ ] **Step 5: Commit** `feat: reaction chips and :react in the TUI chat`

---

### Task 8: Web chips, react button and picker

**Depends:** 3

**Files:**
- Create: `internal/ui/static/react-model.js`, `react.js`, `react.css`, `internal/ui/jstest/react.test.mjs`, `scripts/ui/rx-thread.mjs`
- Modify: `internal/ui/static/api.js` (one method), `index.html` (one stylesheet link), `thread.js` (wire-up only)

**Interfaces:**
- **`react-model.js`** is pure, and the only part node tests touch:
  - `REACTIONS`: `[{emoji, name, prio}]` in fixed order.
  - `defaultPriority(emoji)`.
  - `chips(reactions, me, nameOf)` → `[{emoji, count, mine, title}]`. `title` is the names joined with `, ` after `clean()`; the operator's own peer shows as `you`.
  - `nextOp(reactions, me, emoji)` → `'add'|'remove'`.
  - `reactBody({messageId, emoji, op, override})` → the request body; `priority` is set only on `add` (the override, else the default).
  - `errorText(err, nowMs)`:
    - `not_replyable` → "Reactions can't be reacted to.";
    - `rate_limited` → the en-AU time from `details.retry_after_seconds`;
    - `emoji_not_allowed` and network errors → fixed text;
    - anything else → the hub's message.
- **`api.react(body)`** → `post('/v1/reactions', {body, headers: {'X-Vecnet-Ingress': 'operator_ui'}})`.
- **`react.js`:** `createReactions({ api, me, nameOf, onDone, toast })` → `{ chipsRow(message) → Node|null, button(message) → Node, picker → Node, close(), destroy() }`.
  - **Chips:** `button.rx-chip[aria-pressed]` showing the emoji and count, with `title` = the names and `.mine` highlighted. A click sends `nextOp` with the default priority.
  - **React button:** `button.rx-btn[type=button][aria-haspopup=dialog][aria-expanded]`, id `rx-btn-<message_id>`, in `span.acts` beside `reply`, visible on `.msg:hover` and `:focus-within`.
  - **Picker:** one `div.rx-pick[role=dialog][aria-label="React"]`, built once per thread mount and appended outside the message list, so the `.msg` rebuild at `thread.js:349` never destroys it.
    - The open state (`messageId`) lives in the reactions object. The picker is positioned beside the button through `style.setProperty('--rx-top', …)`.
    - It holds 8 emoji buttons (`aria-label` such as "check, wakes if idle") and a `.seg` row `wake | wake_idle | hold`. With no segment selected, each emoji uses its own default, shown as a small marker on the emoji. A selected segment overrides for one reaction and resets on close.
    - Escape and an outside click close it. Focus goes to the first emoji on open and returns to `#rx-btn-<id>` on close (looked up after re-render).
    - Its buttons are disabled while a request is in flight.
  - **Results:** success calls `onDone()` (`store.invalidate('thread:'+id)`). An error calls `toast(errorText(e), true)`, except a 401, which stays silent as `sendError` does.
- **`thread.js`:** `message(it)` adds `R.button(m)` to `span.acts` and `R.chipsRow(m)` after `div.atts`, before `div.rc`. Mount creates `R`; unmount calls `R.destroy()`. Nothing else in `thread.js` changes.
- **`react.css`** (linked from `index.html`):
  - chips use the existing `--panel2`/`--line` tokens, with `.mine` given the accent gradient border;
  - the picker is a dark panel;
  - the chips row has a reserved height, so chips never shift the bottom-pinned list.

- [ ] **Step 1: Write the failing tests** (`react.test.mjs`)

```js
test('REACTIONS matches internal/envelope/testdata/reactions.json in order, names and default priority')
test('chips: counts, mine for the operator peer, titles cleaned (bidi override and controls stripped), own name shown as you')
test('nextOp: remove when mine, else add')
test('reactBody: check → wake_idle, +1 → hold, override wins, remove carries no priority')
test('errorText: rate_limited shows en-AU time from retry_after_seconds; not_replyable and emoji_not_allowed have fixed text')
```

- [ ] **Step 2–4:** red, implement, then green: `build2 node --test --test-concurrency=2 internal/ui/jstest/` passes, as do the `ui_test.go` size-cap, no-sinks and linked-stylesheet tests. Then run `build2 node scripts/ui/rx-thread.mjs`, which:
  1. Seeds reactions through `asPeer`: two agents put ✅ on one message, one agent puts 👍.
  2. Opens the thread and asserts the chips and their titles.
  3. Opens the picker, then makes a peer react through `asPeer`. The picker stays open with focus kept, and the new chip appears.
  4. Picks ❓ with `hold` selected. The intercepted request body has `priority: "hold"`.
  5. Clicks its own chip, which removes it.
  6. Presses Escape. Focus is back on the react button.
  7. Checks there are no console errors and no CSP violations.
- **Shots:**
  - `rx08-thread-initial`: chips, own 👀 highlighted.
  - `rx08-thread-progress`: the picker open with default markers.
  - `rx08-thread-success`: the new ❓ chip highlighted after the pick.
  - `rx08-thread-error`: a faked `page.route('**/v1/reactions', …)` 429 `rate_limited` with `details.retry_after_seconds`; the toast shows the time.
- [ ] **Step 5: Commit** `feat: reaction chips and picker in the web thread`

---

### Task 9: Journey, docs, gate

**Depends:** 2, 4, 5, 6, 7, 8

**Files:** Create `scripts/ui/rx-journey.mjs`. Modify:
- `docs/OPERATOR.md`: a Reactions section covering:
  - the set and names;
  - the per-emoji defaults and why;
  - the override;
  - the once-per-emoji wake rule for agents;
  - chips and toggling;
  - `:react` and CLI `react`;
  - coalescing, in one sentence;
  - that reactions can't be replied to.
- `docs/plans/shots/oui/README.md`: one line per new PNG.
- `README.md`: one line.
- `.scratch/vecnet/issues/24-reactions.md`: link the plan and mark it done.
- `docs/PROTOCOL.md`: only to fix any drift from Tasks 3–4 that this task finds.

- [ ] **Step 1: Journey** (no shots): web and TUI on one demo hub (`withTui({ env: ctx })`, as `journey.mjs` does).
  1. In the web, the operator picks ✅ (default `wake_idle`) on an agent's message.
     - The TUI shows the chip without a reload.
     - The agent's `asPeer` `GET /v1/events` has one `delivery` with `message.kind` `reaction` and `in_reply_to` = the target.
  2. The operator picks ✅ again from the chip, which removes it. No second delivery appears.
  3. In the TUI, `:react 1 eyes`. The web chip appears without a reload, and the agent gets no delivery (`hold`).
  4. An agent reacts ✅ (wake) to an operator message through `asPeer`, removes it, then adds it again. Both clients show the chip, and the operator inbox gains exactly one reaction delivery.
  5. An agent tries `POST /v1/messages` with `in_reply_to` set to the reaction message: 422 `not_replyable`.
- [ ] **Step 2: Docs check:** §17 matches the code: check order, response fields (top-level `superseded`), the once-ever wake rule and its operator exemption, `superseded`, the operator_view reason, the tool count.
- [ ] **Step 3: Gate:** run
  - `build2 go test -p 2 ./...`
  - `build2 node --test --test-concurrency=2 internal/ui/jstest/`
  - `build2 go vet ./...`
  - `gofmt -l .`
  - `CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...`
  - `just isa-check`
  - `just validate-protocol`
  - `SHOTS_DIR=.scratch/vecnet/build-reactions/gate-shots just ui-shots` (this runs the journey too)

  Expected: all pass. Every PNG in the README exists, and every PNG in the directory is listed.
- [ ] **Step 4: Commit** `docs: reactions guide and journey`

After the merge, the coordinator (not a task) tells pag-chat over vecnet that §17 adds a `reaction` Event type and a `reaction` Message kind, both safe to ignore (spec "Tell").

---

--- SUMMARY ---

- **What:** reactions (tapbacks) with a fixed set of 8 emoji (👍 👎 ❤️ 😂 👀 ✅ ❓ ❗, names `+1 -1 heart laugh eyes check q bang`) on every surface: `POST /v1/reactions`, `vecnet react`, MCP `vecnet_react`, web chips and picker, TUI chips and `:react`. PROTOCOL gains §17, and the new root `CHANGELOG.md` gets its first line.
- **Two layers:**
  - **Event:** every reaction is a `reaction` Event: a row per (message, peer, emoji), fanned out to participants who can see the message and to the reactor (Notify after commit), aggregated into `Message.reactions` in history, never pushed, and costing no budget.
  - **Wake:** with `wake`/`wake_idle`, an `add` also makes one `kind: reaction` Message to the target's sender through the existing `fanOut`, so push routes, Grok Bot, dots and held `wake_idle` work unchanged.
- **Waste control:**
  - The hub defaults to `hold`. Clients use per-emoji defaults: ✅ ❓ ❗ `wake_idle`, the rest `hold`, with one-off overrides.
  - A no-op retry returns 200 before any check.
  - A non-operator wakes a given message's sender at most once per emoji, ever (`reaction_wakes`).
  - `remove` never wakes and withdraws a still-queued wake.
  - A newer waking reaction supersedes the same reactor's still-queued one in that thread. `superseded` is internal: the hub sends no receipt and it is hidden from the inbox. The ids are reported in a top-level `superseded` array.
- **Loop safety:**
  - Reaction Messages take depth target + 1, with `sender_chain` = the target's chain + the reactor, and obey `max_depth` and expiry, except for the operator (§16.1).
  - They can't be replied to or reacted to (`not_replyable`), are never the implicit loop parent, are hidden from history and the matrix, and cost no budget.
  - They share the per-peer send limit. The once-ever rule bounds wakes per message to 8 per reactor, so the thread budget on real messages stays the hard stop.
- **Contract first:**
  - Task 1 fixes the schema (the `reaction` kind; `SendRequest.kind` without it; new defs compiled by `Validate`).
  - It sets up one emoji table checked by Go, JS and the schema against one fixture.
  - Migration 0012 adds `reactions`, `reaction_ops` and `reaction_wakes`, and rebuilds `messages` for the kind CHECK. Task 1 also adds the invalidation tags and the null-safe `opapi.ReactionResult`.
  - Task 2 writes §17 in parallel.
- **Shape:** nine tasks.
  - 1 and 2 start at once.
  - 3 (hub event layer) follows 1.
  - 4 (wake layer), 7 (TUI) and 8 (web) follow 3. 7 and 8 need only the endpoint, and until 4 lands a wake behaves as hold.
  - 5 (CLI) and 6 (MCP) follow 4, because they assert wake deliveries and `not_replyable`.
  - 9 (journey, docs, gate) is last.
- **Rebase points:** `thread.js`, `thread.css`, `api.js`, `index.html`, the opclient files and the TUI chat files are shared with the attachments build and issues 21 and 23. Reaction lines in `watch` only fill `eventFields`, and their tests assert fields, so they stay compatible with issue 22.
- **Decisions (reversible, recorded for Max):**
  1. **Once-ever wake:** at most one wake per (reactor, target, emoji). A same-emoji re-add after a remove is Event-only. The operator is exempt. *(Coordinator decision.)*
  2. **Event audience:** late joiners under `from_join` get no reaction Events for messages they can't see.
  3. **Operator reactions** reset depth and TTL (§16.1).
  4. **Refused wake:** a waking reaction refused for loop reasons refuses the whole call. `hold` reactions are never loop-checked.
  5. **Scopes** apply to the wake. A disabled recipient is not refused, as on send.
  6. **Skipped wakes:** a wake is skipped (Event only) for the reactor's own messages, sources and `hub`.
  7. **`remove`** withdraws a still-queued wake.
  8. **TUI chips** are ASCII. `n` counts back from the newest peer message, and one argument is always the emoji.
  9. **The hub API** takes the emoji only; names are client sugar.
  10. **MCP:** the inbox resource shows `reaction` Events as part of its raw event tail.
  11. **Reaction deliveries** to the operator show as-is in the inbox (kind `reaction`, excerpt = the emoji).
  12. **No `CHANGELOG.md`** existed, so Task 2 creates one.
- **Open:** none blocking.
