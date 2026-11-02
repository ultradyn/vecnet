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
