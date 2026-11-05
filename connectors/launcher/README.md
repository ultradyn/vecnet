# vecnet launcher

A daemon that runs coding-agent sessions for `launch` jobs addressed to a vecnet peer of kind `launcher` ([PROTOCOL §13.8](../../docs/PROTOCOL.md#138-launcher-issue-26), §18). It relays the job thread into the agent, posts each finished turn back, and routes the agent's questions and permission prompts to the requester. **Operator and requester guide: [`docs/connectors/launcher.md`](../../docs/connectors/launcher.md)** (install, configuration, T3 prerequisites, P2 rollout, smoke test).

## Layout

- `src/main.ts`: `run --config PATH` (the daemon) and `check --config PATH` (backend health and live presets as JSON).
- `src/daemon.ts` wires the pieces: `ingress.ts` (event stream into the ledger), `dispatch.ts` (routes each Delivery), `policy.ts` (the launch gate and approval routing), `launch.ts` and `reattach.ts` (one session per job, across restarts), `session.ts` (the per-job lane: relay, priority mapping, turn replies, controls), `parent-bridge.ts` (questions and permission prompts), `idle.ts` (park, settle, deadline), `presence.ts`, `sweep.ts` (only `vl-` tagged sessions), `ledger.ts` and `outbox.ts` (SQLite, exactly-once posts).
- `src/backend.ts`: the plugin contract. Plugins live in `src/backends/<id>/` (`t3` through `t3-bridge`, `paseo` through `paseo-bridge`) and may import only `../../backend.ts`, their own files and `node:*`.
- `schema/`: the closed JSON Schemas of the `launch.*` objects. `config.example.json`: an xsm config to copy. `systemd/vecnet-launcher.service`: the user unit.

## Develop

Shared wire types, ports and fakes come from [`../hub-client`](../hub-client/README.md). Run `just ln-setup` once per worktree, then `just ln-check` (strict type check and unit tests, through `build2` with at most two workers) and `just ln-e2e` (against a real hub with a fake backend). On the build boxes: `just bb bash -c 'just ln-setup && just ln-check'`. Node 22.18 or newer runs the TypeScript as written; there are no runtime dependencies.

`just ln-run check --config …` and `just ln-run run --config …` run it from the checkout. `just ln-install` (coordinator or Max only) copies the checkout to `~/.local/share/vecnet-launcher/<sha>` and installs the unit without enabling it.
