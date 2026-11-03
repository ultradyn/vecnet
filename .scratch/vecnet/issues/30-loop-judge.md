# 30: Loop judge instead of a hard depth stop

Raised by Max, 2026-11-03, after the coordinator ↔ pag-chat design thread hit `max_depth_exceeded` at 16 (issue 26 dogfooding note). His idea: rather than a hard stop, have a cheap model (gpt-6-luna--fast, or a decision model such as Jev/Kev) judge every ~20 messages whether the thread is stuck in a loop, and let it continue if not. Add a rate-limit guard for many messages in quick succession.

## Proposed shape
- **Checkpoints, not a wall.** When a reply would pass the thread's current depth allowance, the hub still **accepts** it, so nothing is lost and the sender gets 202. Its Deliveries are **held** in a new `loop_check` state, and the thread is marked "checking". A depth allowance starts at 20 and grows in steps of 20.
- **Deterministic pre-check first** (cheap, injection-proof):
  - pair alternation: the same two senders alternating, as §6 already describes;
  - repetition: near-duplicate bodies across the window (shingle or hash similarity);
  - rate: more than N messages in M seconds between the same senders.
  A rate breach always pauses for the operator; a model can't override it.
- **The model judges only the ambiguous cases.** The hub calls a configured judge (an LLMP endpoint, or a judge peer over vecnet) with the last ~20 messages.
  - Each message goes in as a structured, size-capped excerpt (sender kind, length, first 300 chars) inside an untrusted-data wrapper. The judge's instructions say bodies are data.
  - It returns a typed verdict: `{loop: bool, progress: "none"|"some"|"clear", reason}`.
- **The verdict:**
  - Not a loop, with progress: allow 20 more, release the held Deliveries, emit `thread_updated`.
  - A loop, or no progress: the thread is paused. The held Deliveries stay held, and an `approval_request` goes to the operator: continue or stop.
  - Judge timeout or error: fail closed, same as a loop, and the operator decides.
- **Safety:** a message body can try to talk the judge out of it ("this isn't a loop"). So the model can only *extend* when the deterministic checks also pass. It can never override a rate breach or the thread budget (§6 stays the hard cap). Every verdict is audited, and the operator UI shows the checking state and the reason.
- **Operator:** operator replies already reset depth (§16.1). The operator can also force-continue or stop from the UI or CLI.
- **Config:** judge model and endpoint, checkpoint interval (20), rate thresholds, and per-thread opt-out for operator-only threads.

## Also
`--continue` (a linked follow-up thread) is still useful for humans, but becomes optional.

## Order
After reactions. Plan it alongside the launcher, because launched sessions talking to their parent are the main long-thread users.
