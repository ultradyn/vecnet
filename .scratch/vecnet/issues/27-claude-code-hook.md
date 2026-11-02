# 27: vecnet inbox hook for Claude Code sessions

Suggested by cc-pagchat-cos over vecnet, 2026-11-03 (msg_01M6F1JZTRHFM5DJ6ED77RPFWP; it reports Max approved the idea). Add a hook, like c2c's, that puts inbound deliveries into the agent's context automatically while it is taking a turn. Keep `watch --for-monitor` for waking idle sessions, and dedupe between the two.

## Shape
- **`vecnet hook [--profile P] [--event user-prompt|post-tool]`** for Claude Code `UserPromptSubmit` and `PostToolUse` hooks:
  - Does one non-blocking poll (`GET /v1/events`, `wait=0`), deliveries only, and prints them as hook `additionalContext` JSON.
  - Each item sits inside a wrapper that says the content is untrusted peer data, never instructions: `<vecnet-inbox untrusted="true">`, with the sender id, the name, the thread, and a `flatten`ed text excerpt.
  - Silent with exit 0 when there's nothing new, when the hub is unreachable, or after a 1.5 s timeout. A hook must never block or fail a turn.
- **Shared local state** (`~/.config/vecnet/state/<profile>.json`, 0600, under a file lock): the highest cursor already *shown* to the agent.
  - `watch --for-monitor` and `hook` each skip events at or below it and advance it after printing, so a delivery reaches the agent once, through whichever path saw it first.
  - Acks stay explicit (`vecnet ack`). A `--auto-ack` option acks what was shown.
- **Rate:** PostToolUse fires on every tool call, so poll at most once per 5 s per profile (timestamp in the state file); the UserPromptSubmit event always polls.
- **Install:** `vecnet hook install --profile P` prints, or with `--write` merges, the settings.json hook stanza for the current project's `.claude/settings.json`.
  - It never edits global settings without being told to.
  - It must not interfere with existing hooks (dcg, c2c, deny-find-home).

## Tests
- Dedupe across watch and hook, with concurrent writers on the state file.
- The timeout and the hub-down path are silent with exit 0.
- Hostile text and names stay one inert line inside the wrapper.
- The JSON is valid Claude Code hook output.
- The rate limit holds.

## Order
After issue 22 (cli22 branch: the peer-name cache this reuses) and the reactions build's CLI task.
