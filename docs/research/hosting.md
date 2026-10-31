# Where the hub runs: Fly vs VPS vs NAS

Researched 2026-11-01 for ticket 05. Prices come from the providers' own pages, fetched that day. Max makes the final call.

## In one paragraph

Fly (always on) and a Hetzner VPS both pass every goal in the ticket; the NAS fails only one, but that one is decisive: it goes down with home power or the ISP. **Recommendation: Fly.io, one `shared-cpu-1x` 512 MB machine with a 3 GB volume, `auto_stop_machines = false`, about USD 4–5/month in the cheapest region.** The tie with Hetzner is broken by a goal Max likely holds but the ticket does not state: no new provider account and no host OS to patch. Fly volumes sit on one host, so add an off-host backup of the SQLite DB and blobs as well as Fly's daily snapshots.

## Context

- One hub process. SQLite plus a blob store on local disk. Attachments up to ~25 MiB, with retention. Expected disk 1–3 GB.
- Public HTTPS, low traffic. Agents hold long-lived SSE connections. Inbound webhooks arrive at any time.
- Max already deploys `openclaw` on Fly (`~/src/openclaw/fly.toml`: `shared-cpu-2x`/2048 MB, volume at `/data`, `auto_stop_machines = false # Keep running for persistent connections`, `min_machines_running = 1`).
- Max already runs webhook2mcp on the TrueNAS NAS behind a remote-managed Cloudflare Tunnel, with daily ZFS snapshots kept 14 days (`~/.llm-general/lan-swarm/operations/webhook2mcp-nas.md`). That doc records no UPS.
- Max is in Australia, so prices below are before any local GST.

## Facts

### Fly.io

Source: <https://docs.fly.io/about/pricing> unless noted.

| Item | Price |
|---|---|
| `shared-cpu-1x` 256 MB, 24/7 | USD 2.19/mo |
| `shared-cpu-1x` 512 MB, 24/7 | USD 3.69/mo |
| `shared-cpu-1x` 1 GB, 24/7 | USD 6.70/mo |
| Volume | USD 0.15/GB/mo provisioned (3 GB = USD 0.45) |
| Volume snapshots | USD 0.08/GB/mo stored, first 10 GB free each month |
| Shared IPv4 + IPv6 | free |
| Dedicated IPv4 | USD 2/mo (not needed) |
| TLS certs | first 10 single-hostname certs free |

- Machine prices are for `iad`/`ewr`, "our lowest-priced regions"; "Machine prices vary by region". Examples on the page: 1 GB is USD 7.73 in `fra` and USD 8.50 in `sin`. The `syd` row is rendered by JavaScript and could not be read, so the Sydney price is unverified.
- There is no free tier and no monthly platform fee. A card on file is required.
- **Snapshots** (<https://docs.fly.io/volumes/snapshots>): "We automatically take daily snapshots of all Fly Volumes", kept "5 days by default", configurable "from 1 to 60 days" via `--snapshot-retention` or `snapshot_retention` under `[mounts]`.
- **Volume durability** (same page): "If you only have a single copy of your data on a single volume, and the host fails, then any data stored between the time when the snapshot was taken and the time when the failure occurred will be lost." Fly advises other backup or replication for frequently changing data.
- **Auto-stop** (<https://docs.fly.io/reference/fly-proxy-autostop-autostart>): every few minutes the proxy stops a lone machine if it "has no traffic (a load of 0)". Auto-start wakes a stopped machine on the next request; "A cold start takes about 2 seconds or more". The docs do not say whether an open SSE connection counts as load.
- No Fly page read states an HTTP idle-connection timeout. Treat it as unknown and send SSE heartbeats (see ticket 06).

**Monthly total (iad):** 512 MB machine + 3 GB volume + snapshots within free 10 GB = **about USD 4.14**. With 1 GB RAM it is about USD 7.15. A Sydney deployment costs more by an unverified amount.

**Auto-stop gives no saving here.** Agents hold SSE open around the clock. If an open connection counts as load, the machine never stops. If it does not, the proxy stops the machine and drops every agent's stream, and webhooks pay a ~2 s cold start. Either way, run it always on, as `openclaw` already does.

### Hetzner Cloud

Sources: <https://www.hetzner.com/cloud/cost-optimized/>, <https://www.hetzner.com/cloud/regular-performance/>, <https://docs.hetzner.com/cloud/servers/backups-snapshots/overview/>. Prices are EUR excluding VAT, as shown, "Price incl. IPv4".

| Plan | vCPU / RAM / disk | EUR/mo | Locations |
|---|---|---|---|
| CX23 (cost-optimized) | 2 / 4 GB / 40 GB | 5.99 | NBG1, HEL1 |
| CAX11 (Arm) | 2 / 4 GB / 40 GB | 6.49 | NBG1, HEL1 |
| CPX02 (new) | 1 / 1 GB / 20 GB | 6.49 | FSN1, NBG1, HEL1; SIN1 at 7.99 |
| CPX11 | 2 / 2 GB / 40 GB | 17.99 | HIL1, ASH1 (US) |

- The CX22 and the old CPX11 EU pricing no longer appear. The CPX11 now lists only US locations at EUR 17.99.
- **Backups**: daily, "a total of seven Backup slots", priced relative to the server, "starting at EUR 1.1980" (20% of the EUR 5.99 plan). Backups are deleted with the server.
- **Snapshots**: EUR 0.0143/GB/mo, never auto-deleted.
- **Traffic**: at least 20 TB/mo included in the EU.
- **Availability caveat**: on 2026-11-01 the pages marked every shared plan "not available" / "currently unavailable". This may be a temporary stock state. Check before ordering.

**Monthly total:** CX23 + backups = **EUR 7.19 excl. VAT**. At roughly 1.15 USD/EUR (an assumed rate, not looked up) that is about USD 8.30. It stays under USD 10 only if local tax is 10% or less.

Running a VPS also means Max patches the OS, runs the reverse proxy and TLS (or cloudflared on the VPS), and sets the firewall.

### NAS + Cloudflare Tunnel

Sources: <https://developers.cloudflare.com/tunnel/>, <https://developers.cloudflare.com/fundamentals/reference/connection-limits/>, <https://developers.cloudflare.com/cache/concepts/default-cache-behavior/>, <https://www.cloudflare.com/service-specific-terms-application-services/>.

- Tunnel is "Available on all plans" and is an "outbound-only" connection with "no inbound ports or firewall changes required". The page does not state a price. Max runs two tunnels today on what appears to be the Free plan.
- Free plan max upload size is 100 MB, so 25 MiB attachments fit.
- Proxy read timeout between Cloudflare and origin is 125 s; idle timeout 900 s. The docs do not say whether periodic SSE data resets the read timeout. webhook2mcp already streams through this tunnel (Caddy `flush_interval -1`), so it works in practice.
- The CDN terms forbid serving "a disproportionate percentage of pictures, audio files, or other large files" without paid products. At low volume this is unlikely to bite, but it is a risk for a blob store.
- Marginal cost: USD 0. Backups: existing daily ZFS snapshots, 14 days.
- **Weakness**: the hub is down whenever home power, the NAS or the ISP is down. No UPS is recorded.

## IGC matrix

| Idea | All | G1 | G2 | G3 | G4 | G5 | G6 | G7 | G8* |
|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| A. Fly `shared-cpu-1x` 512 MB + 3 GB volume, always on | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ |
| B. Fly, same, with auto-stop | ✘ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✘ | ✔ |
| C. Hetzner CX23 + daily backups | ✘ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✔ | ✘ |
| D. NAS + Cloudflare Tunnel | ✘ | ✔ | ✔ | ✔ | ✔ | ✔ | ✘ | ✔ | ✔ |

- **G1** public HTTPS with no inbound port open at home
- **G2** persistent disk for SQLite + blobs (1–3 GB)
- **G3** at most about USD 10/month
- **G4** backups are possible
- **G5** Max can operate it
- **G6** survives a home power or ISP outage
- **G7** held-open SSE streams are not dropped by the platform. This is implied by the ticket context (agents hold SSE), not listed in it.
- **G8\*** no new provider account and no host OS to patch. *Not in the ticket.* Proposed as the tie-breaker between A and C. It counts only if Max actually holds it; without it, A and C are both All-✔.

**Why the ✘s.**
- **D fails G6.** The NAS shares home power and the ISP link. A blackout or ISP outage takes the hub, its webhook ingress and every agent stream down together. Cloudflare cannot queue inbound webhooks for an unreachable origin. A UPS would cover short blackouts but not ISP outages or long blackouts.
- **B fails G7.** Either an open SSE connection keeps the machine up, so auto-stop never saves money, or the proxy stops it and drops every agent stream. The docs do not say which. Max's `openclaw` config already turns auto-stop off for this reason.
- **C fails G8.** It needs a new Hetzner account and billing, and the server OS must be patched and secured by Max. Fly runs the host and Max's account already exists.

**Contested ✔s and open items.**
- **A on G3** holds in `iad` (about USD 4.14). In `syd` the price is unverified. 512 MB should still fit; a 1 GB machine might approach the limit.
- **A on G4** passes, but snapshots are daily and the volume sits on one host. Add a nightly off-host copy: an SQLite `.backup` plus a blob sync to the NAS or to object storage, or Litestream for the DB.
- **C on G3** is tight. EUR 7.19 plus FX and any GST lands about USD 8.30–9.20.
- **C's availability** is unknown. The pages showed all shared plans as unavailable on 2026-11-01.
- **G5** passes for all. Max already runs Docker Compose on the NAS and `fly deploy` for `openclaw`.

## Recommendation

**Idea A: Fly, always on.**

- One `shared-cpu-1x` machine, 512 MB, raised to 1 GB only if the chosen stack (ticket 07) needs it.
- A 3 GB volume mounted at `/data` for SQLite and blobs. Set snapshot retention to 14–30 days.
- `auto_stop_machines = false`, `min_machines_running = 1`, as in `openclaw`.
- Region: `syd` for latency from Australia, or `iad` for the lowest price. Check the `syd` price in the Fly dashboard.
- Off-host backup: nightly SQLite `.backup` and blob sync to the NAS or object storage, because a Fly volume lives on one host.
- SSE heartbeats every 15–30 s, under both Fly's unknown idle timeout and Cloudflare's 125 s read timeout if a tunnel is ever placed in front.

## What Max must decide

1. **Accept G8 as a real goal?** If yes, Fly wins outright. If no, Fly and Hetzner tie and either is fine. Hetzner gives 4 GB RAM and 40 GB disk for about twice the price.
2. **Region:** `syd` (latency) or `iad` (cheapest). Check the `syd` price first.
3. **Off-host backup target:** the NAS (free, already snapshotted) or object storage.
4. **Is NAS-only acceptable after all?** Only if an outage of hours is acceptable for agent comms. The NAS stays useful as the backup target either way.

--- SUMMARY ---

- Fly always-on and Hetzner both pass the ticket's six goals. The NAS fails "survives a home power or ISP outage", and Fly with auto-stop fails because SSE streams would drop or nothing is saved.
- Fly costs about USD 4.14/mo in `iad` for 512 MB plus a 3 GB volume. Hetzner CX23 plus backups costs EUR 7.19 before tax. The NAS costs nothing extra.
- The proposed tie-breaker is "no new account, no host OS to patch". Fly passes it and Hetzner fails it.
- Recommendation: Fly always on, with longer snapshot retention, a nightly off-host backup and SSE heartbeats.
- Open items: the Sydney price, Hetzner stock availability, and whether Max holds the tie-breaker goal.
