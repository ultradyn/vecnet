# Where the hub runs: Fly vs VPS vs other

Type: research
Status: resolved
Blocked by:

## Question

Max asked whether Fly is cost-effective vs a VPS. Context: single hub, SQLite + blob store on disk (~25 MiB attachments, retention), public HTTPS, low traffic, Max already has a Fly account (openclaw deploys there) and runs webhook2mcp on the NAS behind a Cloudflare Tunnel. Build an IGC matrix (goals: public HTTPS with no inbound port at home, persistent disk, <= ~USD 10/mo, backups possible, Max can operate it, survives a home power loss) over Fly machine+volume, Hetzner/other VPS, NAS + Cloudflare Tunnel. Deliverable: `docs/research/hosting.md` with the matrix and a recommendation; Max makes the final call.

## Answer

Full write-up: [`docs/research/hosting.md`](../../../docs/research/hosting.md). **The final pick is Max's.**

- **NAS + Cloudflare Tunnel is out.** It costs nothing extra but fails "survives a home power or ISP outage". No UPS is recorded, and even a UPS would not cover an ISP outage.
- **Fly with auto-stop is out.** Agents hold SSE streams open around the clock. Either the machine never stops, so nothing is saved, or the streams drop. `openclaw` already runs with auto-stop off for this reason.
- **Fly always on and Hetzner both pass every goal in the ticket.**
  - Fly is about USD 4.14/mo in `iad` for a `shared-cpu-1x` 512 MB machine and a 3 GB volume. Daily snapshots are included, and the `syd` price is unverified.
  - Hetzner CX23 with daily backups is EUR 7.19/mo before tax. On 2026-11-01 Hetzner's pages listed it as unavailable.
- **Recommendation: Fly, always on.** It wins only if Max agrees to a tie-breaker goal the ticket does not state: no new provider account and no host OS to patch.
- **Max confirmed Fly, always-on, by voice and text on 2026-11-01** ("sure let's do fly").
- **Required with Fly.** Snapshot retention of 14–30 days, a nightly off-host copy of the DB and blobs (a Fly volume lives on one host), and SSE heartbeats every 15–30 s.
