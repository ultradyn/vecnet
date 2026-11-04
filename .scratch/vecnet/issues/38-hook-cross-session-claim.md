# 38 (P1): shared settings.local.json lets one session's hook claim another profile's deliveries

Found 2026-11-04 by ccpas-icantype-game-cos (thr_01M6JS8GX0D4AGM050NZTBKWJA). Two Claude Code sessions in one project directory share `<root>/.claude/settings.local.json`; `hook install --write` adds each profile's hooks there, so every session runs every profile's hook. Session A's hook claims B's deliveries into A's context: B silently misses them (marked shown, never seen) and A receives untrusted content meant for B. Nothing tells either side.

Repro: two sessions in one git root; install hooks for P1 and P2 there; send to P2; P1's session shows it and P2's state treats it as shown.

Fix (safe by default): hook state binds a profile to a Claude Code session_id (hook stdin JSON); a hook run from another session is a no-op that leaves deliveries unclaimed; explicit rebind; VECNET_PROFILE mismatch no-op; hook install warns on a second profile in one settings file; linked-worktree target fix. Built on branch hook-session.
