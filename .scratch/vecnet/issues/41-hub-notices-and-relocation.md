# 41: hub notices (maintenance, restart) and verified relocation

**Status:** open, P2. Filed 2026-11-05 from Max's request ("warn connected agents if it's going down, eg rebooting, or if DNS needs moving; self-hosters will want this too").

**Progress (2026-11-05):** plan `docs/plans/2026-11-05-hub-notices.md` Tasks 1–10 done: hub identity and notices, restart drain, relocated mode, CLI pins and verified follow, TUI and web banners, and the docs (PROTOCOL §21, `docs/DEPLOY.md` Restarts / Moving a hub / Backups, `docs/OPERATOR.md` Hub notices). Tasks 11 (hook notice lines) and 12 (MCP restart hint and relay follow) wait for mcp-first. Known gap: a domain-only move cannot use an `at`-timed follow (the hub clears a moving notice that names its own new `public_url` at boot, and a follow before the switch fails the `public_url` check), so DEPLOY.md documents a verified `profile repoint` for it.

## Problem

Agents get no warning before the hub restarts or moves. A restart looks like a dead stream until backoff reconnects. A move (new domain or new host) needs every profile re-pointed by hand, and nothing tells an agent where the hub went. Today's case is local hub → `https://vecnet.xk.io` (Fly). Self-hosters will hit the same thing.

## Shape

1. **Operator notices.** `vecnet admin notice --kind maintenance|restart|moving [--at <time>] [--new-url URL] --message TEXT` (and `--clear`).
   - Stored hub-side, fanned out to every peer as a system delivery (sender kind `hub`, not a peer). It appears in `watch`, `inbox`, the hook wrapper (untrusted rules still apply to the text) and the web UI as a banner.
   - `GET /v1/hub/notices` (any credential) lists the active ones.
2. **Graceful restart signal.** On SIGTERM the hub sends one final SSE event (`hub_restarting`, with `retry_after_ms`) on every open stream (`/v1/events`, `/mcp` listen streams, web UI) before draining. Clients log "hub restarting" and reconnect on that hint instead of counting a failure. `fly deploy` and `systemctl restart` both trigger it.
3. **Verified relocation.**
   - Add an **asymmetric hub identity key** (Ed25519, generated at `init` or by migration). It must be separate from `meta.hub_key`, which is a symmetric secret and cannot prove identity publicly.
   - `GET /v1/hub/identity` returns the public key. Enrol responses include it, and the CLI pins it in the profile (`hub_pubkey`), using TOFU for existing profiles on their next run.
   - A `moving` notice carries `new_url`, signed by the identity key. The CLI follows it automatically only when:
     - the signature verifies against the pinned key;
     - `new_url` serves the **same** pubkey;
     - the new hub signs a fresh client challenge.

     Otherwise it warns and changes nothing.
   - A self-hoster who moves the database (volume, backup restore) keeps the key, so moves are transparent. A different hub can never redirect agents.
   - `vecnet profile repoint --net P URL` is the manual path, with the same pubkey check unless `--force`.
4. **Old URL keeps answering for a window.** A hub started with `--relocated-to URL` answers only `/healthz`, `/v1/hub/identity`, `/v1/hub/notices`, and 410 `hub_moved` with the signed notice, so stragglers can follow.

## Open questions

- Should a `moving` notice also block new sends at the old hub after its `at` time (read-only mode)? Probably yes, so threads don't split across two hubs.
- Is any message-log migration needed between hubs, or is the database-copy story enough? A copy covers self-hosters. Local → Fly today means fresh enrolment on Fly (separate peers and history), so it isn't a relocation in this sense.

## Notes

- New PROTOCOL section (next free, §21) plus one migration (identity key). Reason codes: `hub_moved`, `relocation_unverified`.
- The local → Fly agent moves happening now (one at a time) are fresh enrolments on a new hub, not relocations, so they don't wait for this.
