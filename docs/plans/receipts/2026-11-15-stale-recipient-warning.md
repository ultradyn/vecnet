# Stale recipient send warnings

Sends and replies now warn when a resolved agent recipient was last seen more
than an hour ago. The warning reports elapsed minutes and preserves delivery
success. Open monitor/SSE streams count as ongoing activity. Group and team
fanout produces one warning per inactive agent, and idempotent retries keep the
original receipt. Operator and source recipients are excluded.

Configure the live operator preference `presence.send_warning_after` (seconds)
or bootstrap it using `vecnet-hub --send-warning-after=30m` /
`VECNET_SEND_WARNING_AFTER=30m`. Default: `1h`. Zero disables the warning;
otherwise the accepted range is 60 seconds to 365 days. Stored preferences
win over startup settings. HTTP, MCP, CLI JSON and operator response metadata
preserve the warning; CLI human output prints sanitized warnings to stderr.

A never-connected peer uses enrollment as its age origin. When no valid age
evidence exists, the send proceeds without age-based advice. Independent review
found the normal unarchive path clears enrollment for never-connected peers;
regression tests now cover direct and group delivery, replay, and missing or
malformed timestamps.

Validation covers threshold boundaries, recipient activity, monitor lifetime,
group deduplication, replies, restart behavior, live configuration/disable,
idempotency, bootstrap validation, response projection and CLI/MCP output.
Independent whole-change review and scoped re-review completed with no remaining
blockers. The unarchive fix was demonstrated failing before implementation and
passing afterward. The complete `go test -p 2 ./...` suite passed with the
fixture environment (umask 022 and inherited Codex detection variables unset).
Detailed logs and review reports are preserved in the ignored task workspace.
