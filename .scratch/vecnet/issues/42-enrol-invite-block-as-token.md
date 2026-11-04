# Issue 42: `vecnet enrol --token -` given the whole invite block

**Reported:** 2026-11-05, during the Fly move. cc-pagchat-cos stored the invite as a file and piped the whole file into `vecnet enrol --token -`. The hub answered `unauthenticated: missing, unknown or revoked credential`. The agent took that to mean the invite had expired, and we spent a re-issue on it. The token itself was fine.

## Problem

- The invite we hand agents is a paste block: an intro line, a `printf %s '<token>' | vecnet enrol --hub … --token - --profile …` line, and a guide line.
- Storing the block in a 0600 file and piping that file in is the obvious move. `--token -` then reads all of it as the token, and the hub refuses it.
- The error doesn't say why. `unauthenticated` reads as expired or revoked, which sends the agent looking in the wrong place.

## Fix (CLI only; no hub or PROTOCOL change)

1. **Reject block-shaped input before calling the hub.** If the token read from stdin, the env var or `--token` has whitespace inside it, more than one line, or the text `vecnet enrol`, exit 2 without contacting the hub. Message: `vecnet: that looks like the whole invite block, not the token; run the block's printf line instead (or pass only the quoted token)`.
2. **No guessing.** The CLI never pulls a token out of the block. If it did, an attacker-supplied block could choose the `--hub`. The agent copies the printf line on purpose.
3. **Clearer refusal.** When enrol gets 401, add one line: `the token may be mistyped, already used, re-issued or expired; ask the operator for a fresh invite (vecnet peer invite <peer>)`.

## Tests

- `TestEnrolRejectsInviteBlock`: the full block on stdin, the block in `VECNET_ENROL_TOKEN`, and a token with an embedded newline all exit 2 with the message. The fake hub sees no request.
- `TestEnrolTokenTrailingNewline`: a token followed by a single trailing `\n` (as `echo` produces) still works.
- `TestEnrolUnauthenticatedHint`: a 401 prints the hint line.
