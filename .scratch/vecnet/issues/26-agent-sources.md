# 26: Agent sources (spawning agents through vecnet)

Raised by Max, 2026-11-03: vecnet lets agents talk; it should also let them be **spawned**. That means starting a coding-agent session on a host with a prompt, which then joins vecnet as a peer.

## Candidate backends (Max)
- His Grok Bot bridges: `~/src/t3-bridge`, `~/src/paseo-bridge`.
- Servers that already spawn and manage sessions: T3 Code, Paseo, Orca.
- The Codex app-server daemon on a host, "or similar kinds of servers for other things".
- Coding CLIs in ACP mode, run as supervised processes on a box. This overlaps with what pag-chat's `pagd` does.

## Direction
Leverage existing spawners, and integrate with vecnet and pag-chat in a way that works long term. Talk to pag-chat (overlap with pagd supervision) before designing.

## Name
Working name **launcher** (peer kind `launcher`, a `launch` job). "Source" is already a vecnet peer kind (§10, inbound webhooks). Max to settle.

## pag-chat's input (2026-11-03, thr_01M6EFWMX794R8DPT3W1XSGJWT)
- Supervision goes in a separate small launcher daemon per host and backend, not pagd. pagd runs one-shot `codex exec` batch jobs over SSH, and only on xsm. Reuse its patterns: SSH exec with a kill-and-reap sweep, reattach by a pid/socket file, an outcome fence (pag-chat packages/machines: ssh.ts, reattach.ts, agent-sweep.ts).
- pag bots may later use a launcher as an interactive dispatch backend. Not soon: pag-chat has no vecnet receiver yet.
- Launcher sessions need their own workspace dirs and branch prefix, so neither side's orphan sweep touches the other's.
- Spawned peers are untrusted by default. The directory marks them (e.g. `spawned_by`: launcher plus requester).
- A launched session's permission prompts become approvals for whoever requested the launch, so there is one approval surface.

## Status
- Research: `docs/research/2026-11-03-agent-sources.md` (in progress).
- pag-chat asked over vecnet, thread `thr_01M6EFWMX794R8DPT3W1XSGJWT`.
- Then a design note (the PROTOCOL shape, probably a spawner peer kind using §5.6 jobs) for Max to approve, then a plan.

## pag-chat review of the research (2026-11-03, msg_01M6ER5S4BTZD1MN8ME06ZX5FV)
- Approvals: agreed. The operator by default; a requester approves only when a launcher policy names it explicitly.
- pagd is a client at most, never a backend, and only for launches Max asked for in pag-chat. Not near-term.
- Share code, not runtime state: copy or extract @pag/machines patterns (systemd-run units, reattach, sweep). The launcher uses its own uid range (`vecnet-*`) and its own nftables chain on x-tall. A future shared supervision library would be a package, not a shared daemon.
- Credentials: the launcher brings its own model auth. pag-minted per-job tokens are only for pag-requested launches, and they need a secret channel (never a vecnet message body). Until that exists, pag-requested launches wait.
- When pag launches through vecnet, the vecnet thread is the work thread; pag-chat stores only a pointer, receipts and the outcome summary.
- Launcher-spawned peers are always untrusted: requester taint never raises them, and user-remote is only for sessions Max enrolled himself.

## Max's decisions (2026-11-03)
- a. Name: **launcher**.
- b. Who may launch: **any agent** at this stage. Per-launcher concurrency caps stay as a safety bound.
- c. Approvals: the rec (to Max by default). Max asked what they're for; explained, see the design note.
- d. Hosts: xsm first, **and set up whatever we use on the other hosts too** (build boxes).
- e. Language: Node, **written in TypeScript**. Look at Matt Pocock's sandcastle and other libraries.
- f. **Don't edit t3-bridge.** Make the launcher extensible instead: backends as plugins (t3-bridge, paseo-bridge, Codex app-server, ACP, …) that use each bridge's existing API.
- g. A new `launcher` peer kind now; `launched` session peers later. Expect special behaviour and handling.
- h. 2 h idle wrap-up for now, **to revisit and make more rigorous**: coding agents don't come back after they stop unless they have background tasks.
- i. A launched agent's main comms are its parent (the requester), in its own thread.
- j. Approval relays: pagd only.
- k. Launcher users on build boxes get their own outbound firewall rule.
