# Codex hooks, shared repositories, and session ownership

Research date: 2026-11-13. Scope: inspect XPP's hooks, trace both vecnet
implementations, check the installed runtime, and identify existing work. No
configuration, binding, service, trust, or delivery-state changes were made.

## Findings

XPP's `.codex/hooks.json` is a legacy configuration: both `PostToolUse` and
`UserPromptSubmit` hard-code `xpp-native-icant-ui-proto`. Every Codex instance
loading that project layer invokes the same profile. Codex also combines matching
hooks across config layers and starts matching command hooks concurrently; a
project file does not replace user-level hooks. [S1, S2]

The original vecnet implementation already limits a profile's hook to one
session. It does not make one profile into several independent agents: shown
deliveries and polling state remain shared per profile/`--as`. `VECNET_PROFILE`
filters other profiles' hooks; session binding admits one owner. Codex lacks the
Claude-specific process/session environment used for automatic takeover and
direct rebind, so the old Codex rebind opens a ten-minute next-hook window. The
old guide explicitly documents these limits. [S3, S4]

There is a deeper problem than multiple root sessions: the official hook contract
says subagent hooks use the parent's `session_id`, and `SessionStart` also runs
when subagents start. The old implementation skips a present `agent_id`, but it
cannot use a parent session ID alone to prove which conversation will receive
hook context. Async hooks also do not start an idle turn. A cwd or environment
profile selector does not solve either problem. [S1, S3]

The replacement design has shipped: [PR #69](https://github.com/ultradyn/vecnet/pull/69)
merged on 2026-11-12 and is included in
[v0.2.2](https://github.com/ultradyn/vecnet/releases/tag/v0.2.2). Issue
[#68](https://github.com/ultradyn/vecnet/issues/68) remains open for population
rollout and unsupported hosts. The local worktree
`.worktrees/codex-delivery-isolation`, at
`1bef686931b57e0bb4e039e552ec007633cdb98e`, contains an earlier implementation
snapshot. The merged PR includes additional state-path, readiness, and recovery
fixes absent from that snapshot. This checkout's `master` is still
`b067fc9d7acd4e66cd7cb24e3193ba52946eb5d4` (2026-11-10), with the old hook
implementation and guide. Reading only this checkout would therefore give an
outdated answer about the installed software. [S5, S6, S10]

| Version/evidence | State on inspection |
|---|---|
| Checked-out local master | v0.2.0-era source, `b067fc9d` |
| Local isolation worktree | Earlier isolation snapshot, `1bef6869` |
| PR #69 | Merged at `8a6f35feeaad24f339b2afda7d7b3b1c2eea9f3a` |
| Released v0.2.2 / queried remote master | `0353b4abb6212a1c946c4058c91b9b0ad639a00c` |
| Issue #68 | Open; implementation delivered, population migration incomplete |

No fetch, checkout, or branch changes were used to establish remote status. [S10]

## The replacement ownership model

| Concern | Legacy approach | Replacement |
|---|---|---|
| Shared project hooks | A stanza per profile | Three generic, profile-free handlers |
| Peer content delivery | Hook stdout / `additionalContext` | App-server delivery to an exact root thread |
| Identity | Profile name and hook session | Authenticated hub recipient plus canonical config, Codex home, thread, generation, expiry |
| Concurrency | One session binding per profile | One automatic owner per actual recipient, including across profile aliases/config directories |
| Handoff | Next-hook window without Claude session environment | Explicit target thread and compare-and-swap against the current generation |
| Availability | Hook execution at the next prompt/tool point | Fresh connector status for the exact live owner |
| New conversation/fork | Risk of inherited shared hook identity | No inherited ownership; enroll separately or explicitly hand off |

The per-user authority is `$HOME/.local/state/vecnet/codex/bindings.json`.
Aliases of the same peer do not create separate inboxes. Use distinct authenticated
recipients for concurrent independent agents. Reusing a durable role in a new
conversation is an explicit handoff. Resume and compaction of the same root retain
its binding, subject to TTL and availability. This authority excludes competing
local owners for one OS user; it is not a distributed lease across machines or
users. [S5, S7]

Codex lifecycle hooks deliberately claim nothing, emit no peer text, and do not
advance or acknowledge delivery state. The explicit `--client codex` dispatcher
returns without delivering. Legacy commands also return early when the process
has a recognized Codex environment, such as `CODEX_THREAD_ID`. Registered
recipients have additional ownership protection against unmarked legacy hooks.
Unregistered custom hooks without a client/environment signal remain a migration
inspection concern. [S5, S8]

The app-server connector can start an idle root for `wake`/`wake_idle`, steer a
busy root for `wake`, and wait for idle for `wake_idle`. `hold` remains in the inbox;
there is no lifecycle-hook content fallback. Claims, accepted sends, processing
receipts, and acknowledgements are separate events. [S7]

## What is installed here

Read-only checks and previews on 2026-11-13 established:

- `/home/xertrov/.local/bin/vecnet` exposes generic hook migration and
  `codex setup`/`status`. Its SHA-256 is
  `74a007d0976a430864503bf912c42b1081c3f01db5bf8b18f0a699c3e6fe623f`, exactly
  matching the earlier isolation snapshot's recorded Linux installation receipt.
  The published v0.2.2 Linux x64 executable instead hashes to
  `789d12964de0a7923db2bdf92e520e9747986b65f2281778b05d48a0a9244c55`.
  The installed executable is therefore not byte-identical to the release asset;
  the exact installed source/recovery fixes remain unproven. [S6, S10, S11]
- Vecnet's own project hook file already has `hook --client codex --event ...`
  handlers, without profiles. XPP's file still has the two legacy pinned handlers.
- The standalone `vecnet-codex-delivery.service` reports `active/running` through
  the local user systemd bus. `vecnet-launcher.service` is inactive. An active
  service does not establish a conversation binding.
- The new canonical bindings file is absent. The legacy
  `~/.config/vecnet/codex/bindings.json` exists as version 1 with **zero rows**.
- `vecnet --net xpp-native-icant-ui-proto codex status --json`, for the invoking
  `CODEX_THREAD_ID`, reports `enrolled:false`, `migration:"legacy_unresolved"`,
  and `delivery:"unavailable"`. Here `enrolled:false` refers to the selected
  conversation's ownership binding, not absence of the stored CLI profile.
- Both an explicit generic hook and XPP's exact legacy `user-prompt` command
  returned exit 0 and zero stdout/stderr bytes in this Codex environment.
  Before/after hashes of the vecnet state JSON files were unchanged. This verifies
  the current recognized-Codex no-content behavior; it is not an end-to-end
  delivery test or a guarantee about hosts that strip Codex markers.

XPP's hook file remained byte-identical, SHA-256
`6de2483d6df48859330d80126ae421ab9cd01350a3a8be6a0c9e9969fa7529a3`.
No real binding was created, renewed, or transferred; no peer was messaged.
The release archive was downloaded only to `/tmp` for checksum and build-metadata
inspection; no downloaded executable was run or installed. [S11]

## Will setting up a new session fix the file?

Opening a session, enrolling a CLI profile, and completing automatic-delivery
setup are distinct operations. The replacement setup path is, from the intended
conversation, after selecting its existing enrolled profile:

```sh
vecnet --net P codex setup --dry-run
vecnet --net P codex setup
vecnet --net P codex status
```

These are documented future setup steps, not commands executed as part of this
research, except for the read-only dry-run/status probes above. Setup uses the
invoking `CODEX_THREAD_ID`, or an explicitly supplied `--thread`; it never chooses
a recent conversation or infers identity from the repository. It binds the
recipient and migrates the target project's recognized old hook handlers.
Custom handlers survive. Writes produce a backup; rollback refuses subsequent
file drift. Trust remains a separate Codex `/hooks` review. [S5, S9]

The installed binary's preview against XPP successfully replaces both pinned
commands with generic `--client codex` handlers, adds `SessionStart`, and sets
`additionalContextLimit:18000`. Thus the existing migration recognizes this exact
file. **That preview does not mean setup is currently unblocked.** [S9]

The legacy-source check treats the old bindings file's *presence* as unresolved,
even when its row list is empty. A real `codex setup` refuses before binding or
rewriting hooks, while `--dry-run` warns and still prints the proposed changes.
Reviewing/retiring the old connector source remains necessary; no file was removed
during this research. This guard is explicit implementation policy, not evidence
that an old connector is currently active. [S9]

Even after migration, readiness requires a reachable supported app-server and a
live matching connector/owner generation. Daemon-backed TUI roots are supported;
desktop, embedded/`--no-daemon`, `codex exec`, and independent stdio hosts do not
have a supported bridge in this connector. Lifecycle hooks do not provide a
fallback. Today's read-only checks did not establish end-to-end reachability of
the invoking conversation. [S7]

## Assessment and potential follow-up

Keep the replacement's separation of recipient identity, exact conversation
ownership, and delivery transport. Merely changing the shared file to read
`VECNET_PROFILE`, or naming a new profile alias, would leave root/subagent
ambiguity and shared-recipient ownership unresolved. Existing code already
addresses the architectural problem; avoid opening a duplicate implementation
issue before reviewing #68 and its associated PR.

The concrete local gap is migration/adoption: infrastructure is installed, but
XPP still looks configured through a legacy file while automatic delivery is
unavailable. CLI/connector alignment with the final release also needs checking
before rollout. The released connector adds shared launcher/standalone recovery
and guarded one-time import, so switching modes is not just replacing an
executable. Useful remaining work to assess under #68, or in a narrower linked
issue, is: [S10]

1. An actionable migration path for a verified retired legacy source, including
   the empty-file case, preserving backups and recovery state.
2. A clear distinction in onboarding between CLI profile enrollment, hook
   migration, ownership binding, and tested delivery readiness.
3. Dry-run output that makes blocking legacy migration conditions unmistakable,
   even when it can show a valid hook diff.
4. Root/subagent and same-checkout/worktree acceptance evidence for each supported
   host. Existing Linux daemon evidence does not establish desktop/embedded
   support or interactive TUI rendering. [S6, S7]

The generic handlers currently return without delivery. A possible later
simplification is to justify their ongoing role as bounded setup diagnostics or
remove unnecessary per-tool process launches once migration no longer needs them.
That is an evaluation proposal, not a discovered correctness defect or an agreed
implementation change. [S8]

## Follow-up requirement: automatic enrollment and upgrade repair

Operator direction on 2026-11-13: automatically set up a newly enrolled session,
and automatically keep out-of-date vecnet-managed local files current when the
software updates. This section records the desired behavior for subsequent
design/implementation; it is not implemented by this research.

The released v0.2.2 `cmdEnrol` exchanges the invitation, saves the profile and
returns. It does not invoke Codex setup, migrate hooks, provision a connector, or
verify a session binding. Its agent guide instructs separate manual setup and
service activation. Thus current enrollment does not meet that requirement.
[S12]

Package/binary updates also do not reconcile those installations. The npm package
defines no install/postinstall lifecycle, its wrapper only launches the bundled
binary, and its package builder does not include the standalone connector sources
or installer. The standalone installer stages a content-addressed private copy
of the CLI and connector, pins its systemd unit to those assets, and prints service
activation instructions. Updating the public CLI therefore leaves the connector's
private binary/assets unchanged. Even rerunning that installer does not restart
the running service. No managed hook-path registry or cross-project repair loop
was found in the inspected lifecycle paths. [S13]

There are narrower automatic migrations: the hub applies database migrations,
and an upgraded launcher can import its original recovery namespace into the
shared ledger on startup. The connector reloads existing authorized bindings.
These do not migrate project hooks, update connector software or enroll a new
conversation. [S13]

The proposed user contract is one enrollment action in the intended supported
root conversation. Internal setup should then identify that exact root, prepare
the matching CLI/connector installation, migrate recognized owned hooks, bind the
new recipient, activate delivery and verify readiness. The ordinary success path
should not require copying commands out of `guide` or separately invoking
`codex setup`.

Use one idempotent reconciliation implementation for enrollment, upgrades, and
repair on subsequent use. A local installation manifest should identify managed
files, their format/version and installed content, so an update can repair known
project and user paths without relying on whichever repository is currently open.
Enrollment registers the current project's managed paths. Shared project hooks
stay generic; recipient ownership stays in the private exact-thread authority.

### Local project and directory registry

Additional operator requirement on 2026-11-13: retain the projects/directories
where vecnet has been run, so a new installed version can identify all local
upgrade targets. Record ordinary project-scoped CLI use as well as enrollment and
setup; do not depend on users manually registering each repository.

The proposed per-user local registry records:

- Canonical directory and project root, with Git repository/worktree identity
  where available. Normalize path aliases while retaining separate worktrees
  when their managed files differ. Support directories outside Git too.
- First/last use and the most recently observed CLI version.
- Exact vecnet-managed hook/config/asset paths, their installed format/schema
  and version, and the content hashes needed for safe migration and rollback.
- The last completed migration and current upgrade result or specific failure.

Keep observed CLI version separate from successfully applied configuration and
connector versions: seeing a new executable must not mark old files upgraded.
Directory use is inventory; only actual managed artifacts are upgrade targets.
Merely running vecnet in a directory must not install hooks there or assign a
session/peer owner. The registry contains no credentials or message bodies and
does not replace the private delivery-ownership authority.

After installing a new version, reconcile all registered managed paths, including
projects other than the current working directory. Repeated/concurrent runs
should converge without duplicate records or handlers. Preserve custom content
and backups; retain missing or inaccessible projects as deferred entries, rather
than recreating deleted directories or claiming their migrations succeeded.
First use of a previously unrecorded legacy project should register and reconcile
its recognized vecnet files too. Include a way to inspect and forget entries.

This registry is required future behavior, not an existing v0.2.2 capability.
It should be part of the same enrollment/upgrade reconciliation work, rather
than a second manual setup process.

An upgrade should update the managed software/assets/configuration and activate
a compatible connector while retaining ownership and recovery state. It must not
infer a new recipient owner or renew an expired binding simply because software
changed. A newly invited peer can be enrolled automatically; an already owned
peer needs the existing addressed handoff rather than assignment to the next
conversation. Same-root retries should resume setup without redeeming the
one-use invitation again.

Migration should automatically resolve verified retired legacy sources, including
the observed empty-file case, with backups and preserved recovery records. An
unverifiable active legacy owner remains a specific migration conflict. Custom
hooks survive. Any host-required trust interaction should be surfaced precisely;
the implementation should avoid adding redundant no-op handlers that introduce
an unnecessary trust step.

Completion must report actual state: ready, credentials saved with setup pending,
or an explicit unsupported/conflicting condition. A saved credential or a running
service alone must not produce a connected result. Remaining design/acceptance
questions include simultaneous enrollment in one checkout, registered stale files
in another checkout, repeated upgrades, interruption after token redemption, and
the required bridges for currently unsupported hosts. [S7, S9, S12]

## Sources

- **S1:** [Official OpenAI Codex hooks documentation](https://developers.openai.com/codex/hooks/), fetched 2026-11-13: “How hooks run,” config layers, common `session_id`, SessionStart, and background-hook limitations.
- **S2:** `../xpp/.codex/hooks.json`, lines 9 and 20, inspected without modification.
- **S3:** [Legacy hook admission](../../internal/cli/hook.go#L362), [session admission](../../internal/cli/hooksession.go#L143), [shared state](../../internal/cli/hookstate.go#L101), at master `b067fc9d`.
- **S4:** [Legacy Codex guide](../connectors/codex.md#limits), [rebind implementation](../../internal/cli/hookrebind.go#L50), at master `b067fc9d`.
- **S5:** [Replacement Codex contract](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/docs/connectors/codex.md), [implementation plan](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/docs/plans/2026-11-12-codex-delivery-isolation.md).
- **S6:** [Recorded runtime and installation evidence](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/docs/research/2026-11-12-codex-isolation-runtime.md). Earlier recorded tests/live canaries were inspected, not rerun here.
- **S7:** [Connector capability/lifecycle matrix](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/docs/connectors/codex-appserver.md), [owner validation](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/internal/cli/codexowner.go#L29).
- **S8:** [Replacement lifecycle admission](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/internal/cli/hook.go#L345), [Codex environment detection](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/internal/cli/codexbind.go#L471).
- **S9:** [Setup and legacy-source checks](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/internal/cli/codexsetup.go#L16), [hook migration](https://github.com/ultradyn/vecnet/blob/1bef686931b57e0bb4e039e552ec007633cdb98e/internal/cli/hookinstall.go#L883). Installed dry-run/status behavior was also checked directly on 2026-11-13.
- **S10:** Live read-only GitHub issue/PR/release queries on 2026-11-13: [issue #68](https://github.com/ultradyn/vecnet/issues/68), [merged PR #69](https://github.com/ultradyn/vecnet/pull/69), [v0.2.2](https://github.com/ultradyn/vecnet/releases/tag/v0.2.2). [Merged connector contract](https://github.com/ultradyn/vecnet/blob/8a6f35feeaad24f339b2afda7d7b3b1c2eea9f3a/docs/connectors/codex-appserver.md#L105), [released connector contract](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/docs/connectors/codex-appserver.md#L105). Earlier open draft PRs #54/#55 are separate candidates, not the final isolation contract.
- **S11:** [v0.2.2 archive](https://github.com/ultradyn/vecnet/releases/download/v0.2.2/vecnet-0.2.2.tgz) verified against [published SHA256SUMS](https://github.com/ultradyn/vecnet/releases/download/v0.2.2/SHA256SUMS): archive SHA-256 `544246f439e3f8f81e3a1cacd1424855f523437b4eb2cc1abeb1899a6fdcfe64`. Inspection of `package/BUILD_INFO.json` records version 0.2.2, revision `0353b4abb6212a1c946c4058c91b9b0ad639a00c`, and the executable hash above; the hash was also computed directly from the archive member.
- **S12:** [Released enrollment implementation](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/internal/cli/commands.go#L610), [released manual setup instructions](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/internal/cli/guide.md#L20), [separate setup implementation](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/internal/cli/codexsetup.go#L16), fetched read-only on 2026-11-13.
- **S13:** Released [npm manifest](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/npm/package.json), [wrapper](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/npm/bin/run.cjs#L7), [package builder](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/scripts/npm/build.mjs#L14), [standalone installer](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/scripts/install-codex-delivery.py#L36), [explicit launcher upgrade instructions](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/docs/connectors/launcher.md#L54), [binding/recovery reconciliation](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/connectors/launcher/src/codex/connector.ts#L73), [hub database migration](https://github.com/ultradyn/vecnet/blob/0353b4abb6212a1c946c4058c91b9b0ad639a00c/internal/store/store.go#L57), inspected read-only on 2026-11-13.
