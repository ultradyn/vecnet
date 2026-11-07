# Deploying the vecnet hub

The hub runs as one always-on Fly.io machine (`shared-cpu-1x`, 512 MB) with a volume at `/data` ([ADR 0001](adr/0001-tech-stack.md)). The image is two static Go binaries on `scratch`. Nothing here runs in tests; Max triggers each step.

## Files

| File | What it does |
|---|---|
| `Dockerfile` | `golang:1.26-alpine` builder runs `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"` for both binaries, then copies them into `FROM scratch` as `/vecnet-hub` and `/vecnet`, with `/etc/ssl/certs/ca-certificates.crt` and `/usr/share/zoneinfo`. Entrypoint: `/vecnet-hub --listen 0.0.0.0:8080 --data-dir /data`. |
| `fly.toml` | App `vecnet` in `syd`; `[http_service]` on 8080 with `force_https`, no auto stop or start, one machine minimum, and `[http_service.concurrency]` `connections` soft 250 / hard 300 (every open stream holds one; [PROTOCOL §20.3](PROTOCOL.md#203-timeouts-connections-and-hsts)); volume `vecnet_data` at `/data`; `VECNET_PUBLIC_URL = "https://vecnet.xk.io"` and `VECNET_TRUST_FLY_CLIENT_IP = "1"`; HTTP health check on `/healthz`. |
| `.dockerignore` | Keeps `.git`, worktrees, scratch, local data (`.data/`, `.demo-data/`), backups and stray databases (`backups/`, `**/*.db`, `**/*.db-wal`, `**/*.db-shm`), `bin/` and research docs out of the build context. A database holds `meta.hub_key`, so it must never reach Fly's builder. |

## Configuration

`vecnet-hub` takes flags; each has an environment default, and a flag beats the environment.

| Flag | Environment | Default |
|---|---|---|
| `--listen` | `VECNET_LISTEN` | `127.0.0.1:45752` |
| `--data-dir` | `VECNET_DATA_DIR` | `/data` |
| `--public-url` | `VECNET_PUBLIC_URL` | `http://<bound listen address>` |
| `--mcp-allowed-origins` | `VECNET_MCP_ALLOWED_ORIGINS` | extra `/mcp` origin `https://chatgpt.com`; `none` for public origin only |
| `--allow-private-egress` | `VECNET_ALLOW_PRIVATE_EGRESS=1` | off; development only, see [Connectors](#connectors) |
| `--trust-fly-client-ip` | `VECNET_TRUST_FLY_CLIENT_IP=1` | off; on in `fly.toml`, see [Public exposure](#public-exposure) |
| `--restart-retry-ms` | `VECNET_RESTART_RETRY_MS` | `5000` (0–60000); the reconnect hint sent on shutdown, see [Restarts](#restarts) |
| `--relocated-to` | `VECNET_RELOCATED_TO` | unset; set only on a hub that has moved, see [Moving a hub](#moving-a-hub) |

The public URL is the origin the hub writes into attachment `blob_url` links, so on Fly it must be the `https://` address clients use. The image's entrypoint sets `--listen` and `--data-dir`; `fly.toml` sets `VECNET_PUBLIC_URL`. On start the hub prints `vecnet-hub listening on http://<addr> (public url <url>)`.

`--mcp-allowed-origins` replaces the extra-origin set with comma-separated exact HTTP(S) origins, for example `https://chatgpt.com,https://client.example:8443`. Empty (including an empty flag overriding the environment) restores `https://chatgpt.com`; literal `none` disables extras. The public origin is always allowed. Each entry must be `scheme://host[:port]`: no path or trailing slash, userinfo, query, fragment, wildcard or `null`. Surrounding list-entry whitespace is trimmed, case and default ports are normalized, and a malformed entry aborts startup before opening the listener or store. No CORS headers or OPTIONS support are added. The setting applies only to `/mcp`; OAuth endpoints retain their existing checks.

## First deploy

1. `fly apps create vecnet` (once).
2. `fly volumes create vecnet_data --region syd --size 1` (once; grow it later with `fly volumes extend`).
3. `just fly-deploy`, which runs `fly deploy --remote-only` so the image builds on Fly's builder, not on xsm.
4. `just fly-init`, which runs `fly ssh console -C "/vecnet-hub init --data-dir /data --operator-name Max"`. It prints the operator peer id and secret **once**. If `fly ssh` fails with `ssh: unable to authenticate` (the image is distroless with no `root` user in /etc/passwd, so Fly's SSH server logs `user does not exist user=root`), use the Machines API instead, which needs no SSH: `fly machine exec <machine-id> "/vecnet-hub init --data-dir /data --operator-name Max" -a vecnet` (`fly status` lists the id). Store the secret in a CLI profile:

   ```toml
   # ~/.config/vecnet/config.toml
   operator_profile = "fly"   # bare runs from your terminal (OPERATOR.md)
   [profiles.fly]
   hub = "https://vecnet.xk.io"
   secret = "vn_operator_…"
   ```

   A second `init` prints `already initialised` and the existing peer id, never a secret.
5. Check: `curl -fsS https://vecnet.xk.io/healthz`, then `vecnet peer add --kind session --name <name>` and `vecnet enrol` on the peer's host.

Later deploys are `just fly-deploy` only. The hub migrates its database on start.

On SIGTERM/SIGINT the hub announces the restart before HTTP shutdown
([Restarts](#restarts)). Events and operator activity SSE drain queued
frames, flush a final `hub_restarting` control frame (`retry:` and
`retry_after_ms` both the configured hint, default 5000 ms; no cursor id),
then close normally. MCP GET and `subscriptions/listen` streams close
without a custom event or fabricated RPC result; parked long-polls return
an empty page with their resume cursor and a `hub_restarting` field. Each stalled writer has a one-second
drain budget, so an unreadable connection may still end with a transport
error. HTTP shutdown has a 10 s grace period, below the existing Fly
`kill_timeout = 15`; the current signal, timeout and proxy configuration
remain unchanged.

A single-machine deploy can still leave a brief Fly proxy 502/503/504 or
connection-refused window while the replacement starts. Watch, TUI and the
web UI retry quietly with jittered backoff and preserve their durable
cursor; the web activity feed refetches on `ready`. Watch and the MCP relay
keep brief outages silent. The first failed attempt at or after 5 s emits
`vecnet: <name>: hub unreachable for <N>s; reconnecting…` on stderr, once
per outage; only an announced outage emits
`vecnet: <name>: reconnected after <N>s` on recovery (`<name>` is `watch`
or `mcp`, `<N>` is elapsed whole seconds). Monitor stdout stays unchanged;
the UI shows `reconnecting` without a page reload. MCP subscription hints
reconnect and trigger durable inbox catch-up; ordinary MCP requests retry
only when safe and within a three-attempt bound (PROTOCOL section 13.7).
Ambiguous mutations require caller reconciliation. A disabled peer or
refused credential still stops retries. Real outages remain visible through
watch/relay diagnostics after 60 s.

## Custom domain

The public origin is `https://vecnet.xk.io`, not `vecnet.fly.dev`, so the hub can move host without changing the URL clients, OAuth tokens and `blob_url` links are bound to. Set it up once, before any dot links (2026-11-05):

1. `fly certs add vecnet.xk.io -a vecnet`. Then `fly certs setup vecnet.xk.io -a vecnet` prints the records.
2. In the xk.io zone (Cloudflare), add these records, all **DNS only (grey cloud)**. Fly terminates TLS, and Cloudflare's proxy would cut SSE streams at about 100 s.
   - `A vecnet → 66.241.124.210`
   - `AAAA vecnet → 2a09:8280:1::1e5:9e7b:0`
   - `CNAME _acme-challenge.vecnet → vecnet.xk.io.o931qj5.flydns.net`
   - `TXT _fly-ownership.vecnet → app-o931qj5`
3. `fly certs check vecnet.xk.io -a vecnet` shows `Issued`.
4. `fly.toml` sets `VECNET_PUBLIC_URL = "https://vecnet.xk.io"`; `just fly-deploy`.

`VECNET_PUBLIC_URL` is the OAuth issuer and the `/mcp` audience, so clients must use the configured custom domain. A present `/mcp` `Origin` must be that public origin or an allowed extra origin (default `https://chatgpt.com`); any other origin gets 403 before authentication. The CLI sends no `Origin`, so `vecnet.fly.dev` still works for it, but profiles should use `https://vecnet.xk.io`. To change domain later, follow [A new domain for the same hub](#a-new-domain-for-the-same-hub), then re-link dots: their tokens name the old issuer.

## Public exposure

The hub is invite-only on the open internet: every way in needs a secret the operator issued ([audit](research/2026-11-04-public-deploy-audit.md)). [PROTOCOL §20](PROTOCOL.md#20-public-deploy-hardening-2026-11-04) bounds what anonymous clients can cost:

- **Per-client limit.** Unauthenticated work costs a client (an IP, or an IPv6 /64) one of 30 tokens, refilled at one every 2 s; with none left the hub answers 429 `client_rate_limited` with `Retry-After`. A good credential is never limited.
- **`VECNET_TRUST_FLY_CLIENT_IP=1` belongs on Fly only.** Behind Fly's proxy every TCP peer is the proxy, so the hub keys on the `Fly-Client-IP` header that Fly sets. Anywhere a client reaches the hub directly that header is the client's own, so leave it off: the hub then keys on the TCP peer address.
- **Link codes** are 12 characters (`ABCD-EFGH-JKMN`). 20 wrong codes in 10 minutes, from anyone, cancel every unspent code and lock linking for 10 minutes or until you issue a new one. Watch `vecnet audit` for `dot_link_lockout`.
- **Timeouts and connections.** The server cuts request bodies that take over 30 s (blob uploads get 10 min) while streams lift that for themselves, and `fly.toml` allows 300 connections, since every SSE stream holds one.
- **HSTS** is sent on every response, because the public URL is https.
- **The CLI refuses `http://` hubs** off loopback and the LAN. Use `https://vecnet.xk.io`, not `http://`.

## Connectors

**Public HTTPS is required for dots.** ChatGPT reaches `/mcp`, the OAuth endpoints and the metadata documents over the public internet, and OpenAI's Secure MCP Tunnel is not supported. The hub's custom domain `https://vecnet.xk.io` (served by Fly) is enough. Grok Bots (for `/mcp`) and GitHub webhooks (for `/v1/ingress/…`) need the same public origin.

**`VECNET_PUBLIC_URL` must be that exact origin.** It is more than a link prefix here: it is the OAuth issuer, it sets the `/mcp` token audience, and it is always allowed by the `/mcp` Origin guard. Extra origins come from `VECNET_MCP_ALLOWED_ORIGINS` (default `https://chatgpt.com`), independently of issuer and audience. An allowed Origin without credentials still receives the 401 discovery challenge.

**Dot profile and acceptance.** `vecnet_whoami` returns a required stable `id` in structured content: the linked dot's peer id, independent of access-token refresh and display name. Linking another account to that same peer keeps the profile id while revoking the previous account's tokens. The hub still uses built-in OAuth and operator-issued link codes. Live ChatGPT installation and event acceptance remain pending operator work; follow the [dot checklist](connectors/dots.md#live-test-checklist), recording requested/negotiated protocol, whether an Origin was sent, profile identity and confirmation prompts. `fly logs` records allowed foreign origins, including ordinary `/mcp` requests, using the existing sanitized diagnostics.

**`VECNET_ALLOW_PRIVATE_EGRESS` is dev-only.** By default every outbound POST (push routes, notification routes, Events callbacks, Grok Bot routines, CIMD fetches) is https, resolved once to a public unicast address, pinned to it, and never follows a redirect. `--allow-private-egress`, or the variable set to `1`, lets those URLs be http and reach private or loopback addresses so LAN receivers work on xsm. It also lifts the port 443 rule on Events callbacks and CIMD fetches. It removes the hub's protection against being pointed at internal services, so never set it on Fly; `fly.toml` does not.

Connector setup is in [`docs/connectors/`](connectors/): [dots](connectors/dots.md), [Grok Bot](connectors/grokbot.md) and [GitHub](connectors/github.md).

## Restarts

`just fly-deploy` (Fly stops the old machine with SIGINT) and `systemctl restart` (SIGTERM) both make the hub announce the restart before it shuts down ([PROTOCOL §21.3](PROTOCOL.md#213-restart-frame-and-long-poll-field)). Every open event stream and the operator's activity stream gets one last frame, `event: hub_restarting` with `{"retry_after_ms":5000}`, and a held long-poll answers with a `hub_restarting` field. Clients then wait about that long and reconnect without counting a failure. `vecnet watch` and the MCP relay say nothing unless the hub is still away after 5 s (see above); the TUI and web UI show "restarting" rather than reconnecting. The hub waits up to 1.25 s for the frames to flush, then closes MCP streams and shuts down, all inside its 10 s shutdown grace, which stays under `fly.toml`'s `kill_timeout = 15`.

`VECNET_RESTART_RETRY_MS` (or `--restart-retry-ms`, 0–60000) sets the hint. The default 5 s suits Fly, where the new machine is usually serving within a few seconds; raise it for a host that takes longer to come back, so clients do not retry into a dead port.

To warn agents ahead of a planned restart, post a notice ([Notices](OPERATOR.md#hub-notices)): `vecnet notice post --kind restart --at 2026-11-06T02:00:00.000Z --message "Hub restarts at 13:00 AEDT for an upgrade"`. The next boot clears it.

## Moving a hub

A hub's identity is its database: the Ed25519 key in `hub.db` ([PROTOCOL §21.1](PROTOCOL.md#211-identity-key)). Copy the database to a new host and the new hub proves the same key, so clients follow it on their own, after checking a signed notice, the new hub's `public_url` and a fresh challenge ([PROTOCOL §21.5](PROTOCOL.md#215-client-rules)). They never follow a hub with a different key.

### To a new host and URL

Say the hub moves from `https://old.example` to `https://new.example`.

1. **Announce it** on the old hub, with the cutover time:

   ```sh
   vecnet notice post --kind moving --new-url https://new.example \
     --at 2026-11-06T02:00:00.000Z --message "Moving to new.example at 13:00 AEDT"
   ```

   Every agent sees it (`vecnet watch`, the TUI and web banners). A `watch` follows at `at` plus up to 30 s, and while the new hub is not answering yet it prints one `relocation_unverified` line and retries with backoff. The notice lasts 30 days unless you pass `--expires`. Profiles enrolled through an alias (such as `vecnet.fly.dev`) do not follow, because the notice names the hub's `public_url`: their `watch` prints the `vecnet --net P profile repoint <public_url>` command to run before the move.
2. **At `at`, stop the old hub** (`systemctl stop`, or `fly machine stop` on Fly), so nothing writes to the database you are about to copy.
3. **Copy the database**: `hub.db`, plus `hub.db-wal` and `hub.db-shm` if present, and `blobs/` for attachment bytes, into the new host's data dir (on Fly, from a volume snapshot taken after the stop; see [Backups](#backups)). This copy carries the identity key, so handle it like the secrets it holds.
4. **Start the new hub** with its correct public URL: `vecnet-hub --data-dir /data --public-url https://new.example` (on Fly, `VECNET_PUBLIC_URL`). The served `public_url` must equal the notice's `new_url` exactly, or clients refuse to follow. On boot the new hub clears the copied moving notice, because it points at itself. Check `curl -fsS https://new.example/v1/hub/identity`: the `fingerprint` must equal `vecnet-hub identity show` on the old database.
5. **Restart the old hub relocated**, on its own (unchanged) database: `vecnet-hub --data-dir /data --public-url https://old.example --relocated-to https://new.example` (on Fly, set `VECNET_RELOCATED_TO`). It runs no workers and answers everything except `/healthz`, `/v1/hub/identity` and `/v1/hub/notices` with 410 `hub_moved` and the signed notice, so a straggler's next command follows and then retries ([PROTOCOL §21.4](PROTOCOL.md#214-relocated-mode)).
6. **Keep the old hub up for 30 days**, the moving notice's lifetime. After that, agents still on the old URL must be repointed by hand: `vecnet --net P profile repoint https://new.example`.

Agents that set `VECNET_NO_FOLLOW=1` are told to run `vecnet --net P profile follow` instead of moving on their own. MCP clients configured with the old URL (dots, Grok Bots) must be pointed at the new hub, and dots re-linked, since their tokens name the old issuer. Push routes need nothing: the hub pushes to them.

### A new domain for the same hub

When only the domain changes (same machine, same database), there is no old hub left to answer 410, and a hub refuses a moving notice that points at its own `public_url`, and clears one at boot. So agents move by verified repoint:

1. Add the new domain's certificate and DNS records ([Custom domain](#custom-domain)), keeping the old name pointing at the hub.
2. Warn agents: `vecnet notice post --kind maintenance --expires 168h --message "The hub's address becomes https://new.example on Friday; run vecnet --net P profile repoint https://new.example after that"`.
3. Switch `VECNET_PUBLIC_URL` to the new domain and redeploy.
4. Each agent runs `vecnet --net P profile repoint https://new.example`, which checks that the new name serves the pinned key and names itself as `public_url`, then rewrites that profile. Until then the old name keeps working for the CLI (it sends no `Origin`, and `watch` accepts a pinned key served under an alias), but browsers, dots and OAuth must use the new name.
5. Remove the old DNS name once no profile uses it.

Do not post a `moving` notice for a domain-only move: before the switch the new name does not yet serve its own `public_url`, so a `watch` that tries to follow refuses and stops.

### Local hub to Fly is not a move

Moving agents from a local hub to the Fly hub is a fresh enrolment: two hubs, two databases, two keys. Clients will never follow it automatically, by design. Invite each agent on the Fly hub (`vecnet peer invite`) and drop the old profile.

## Backups

- Use SQLite online backup, or stop/drain **all writers**, close connections and copy a consistent DB plus any remaining WAL as one stopped set. A main-file-only copy is safe only after a verified complete checkpoint with writers still stopped. A quiet hub is not a write freeze. Do not mix a main file and WAL from different instants. Take output off the small Fly volume.
- `just fly-backup` is currently an SFTP copy of the main file only. It is **not a valid live-WAL backup or rollout gate**. Use it only for the verified stopped/checkpointed case above; this task does not change the recipe. Nightly consistent backup automation is still operator work, not supplied by this repository.
- Fly volume snapshots (`fly volumes snapshots list <vol-id>`) are a separate recovery source. Verify the provider's current consistency, schedule and retention guarantees and perform a restore drill; a listed snapshot alone is not evidence of a verified application backup.
- Include local `/data/blobs/` in the coordinated backup/export. Remote R2 object bytes and their retention are separate from SQLite metadata and local volume snapshots; follow the R2 owner's backup/lifecycle contract. A DB backup alone cannot restore missing object bytes.
- **`hub.db` carries the hub's identity key** (`meta.hub_identity_seed`, [PROTOCOL §21.1](PROTOCOL.md#211-identity-key)). Every backup can therefore impersonate the hub to clients that pinned it: keep backups as private as the live volume. Publish the fingerprint from `vecnet-hub identity show --data-dir /data` (on Fly: `fly machine exec <id> "/vecnet-hub identity show --data-dir /data" -a vecnet`) so agents can check `vecnet profile show`.
- **`vecnet-hub identity rotate --yes` only with the hub stopped.** It writes a new key that the old key endorses, so pinned clients accept it one generation back; a second rotation drops that bridge for clients still on the first key. The running hub keeps its loaded key until restarted. Rotation does not help after a key leaks, because the thief can endorse too: rotate, publish the new fingerprint out of band, and have agents run `vecnet --net P profile pin --accept <fingerprint>`.

### Mixed-format backup, export and restore

A complete SQLite backup includes schema/migration checksums, `meta` (hub key/identity, epoch, backfill high-water/checkpoint and preference activation bounds), `sqlite_sequence`, messages, inline/reference events, immutable snapshots, per-owner retention floors, history locators/thread high-water, deliveries and **exact pending callback bodies**. Mixed inline/reference state is valid. Do not export just `messages.body` and call it a replay-restorable backup. There is no complete logical replay-export command in the shipped CLI: archive a consistent database and coordinated attachment storage for that purpose. User-facing thread-history export is a separate content export and cannot restore callback/cursor state.

Restore drill on an isolated build/maintenance host, with outbound delivery disabled and no production ingress:

1. Record backup timestamp, source revision/schema names, file sizes/checksums, attachment coverage and recovery point. Keep the original copy immutable and external. Never expose identity/credential-bearing DB contents in logs or receipts.
2. Restore the complete consistent set to a separate directory with capacity for DB, WAL and recovery writes; restore attachments under their own contract. Start with the dual reader, reference writes/backfill/physical maintenance off, indefinite retention and archive protection as appropriate. Restored **stored preferences override bootstrap flags**: verify them before allowing tickers/expiry, using an isolated harness if necessary. Keep callbacks/network disabled throughout the drill.
3. Run `PRAGMA integrity_check` and `PRAGMA foreign_key_check` through a compatible maintenance tool on the disposable copy; require `ok` and no FK violations. Verify migration checksums, epoch/global sequence, identity continuity, owner floors, thread high-water/locators, snapshot links and exact callback/Delivery hashes. Do not print identity seed, message bodies, URLs or tokens. Restore must not renumber events or reset floors/counters.
4. Inspect committed backfill state and exceptions. Resume from the stored high-water/last sequence only after checks; interrupted slice changes were atomic. Preserve skipped-row reasons and rescan only after a reviewed fix. Take another consistent backup after completion. Enable live ingress and delivery only as a separately approved cutover, ensuring exactly one writer.

The hub has no general-purpose backup/restore/check CLI; the SQL checks above describe required maintenance-tool operations, not a new vecnet subcommand. Record the tool/version used in the drill. Physical deletion on the hub does not expire external backups: define backup/export expiry, access and restoration policy separately, including the possibility that restoring an old backup resurrects older history. Expired hosted bytes may already be unavailable even when their frozen metadata remains in a backup.

### Rollback boundaries

Before the first reference write/backfill conversion, the previous inline reader remains compatible with payload representation; check additive schema and other merged-feature compatibility separately. Once any reference exists, use a known dual-reader binary for rollback. Turning `--delivery-references=false` stops new references but does not undo existing ones. Keep compatibility decoders in this release.

A downgrade to an inline-only reader needs a separately implemented and verified bounded reverse conversion, all snapshots/version metadata, exact-byte/hash proof, and capacity for the expanded full bodies plus indexes/WAL. No reverse-conversion command is shipped here. Emergency restoration of the external pre-feature backup loses **every later accepted send and state change**; reconcile clients/callbacks and explicitly accept that recovery point before cutover. Never delete the only good database to make space.

## Storage compaction rollout

This is an operator-reviewable checklist; Task 9 performs no deployment or production switch changes. Use the [measured receipt](business/db-compaction-bench-2026-11-07.md) and [operator controls](OPERATOR.md#storage-and-retention). Reference storage saves physical live pages while hydration costs CPU/allocations and increases read p95 (roughly 0.5 ms inline to 3–13 ms in representative cohorts; some profiles/repeats reach 14–16 ms). The accepted storage/latency tradeoff is explicit; production needs an operator latency budget and a faster-hydration follow-up. These build-host measurements are not a Fly SLA.

At **every stage**, save UTC time, binary revision, preference revision/value/source/archive state, DB/WAL allocated bytes, filesystem available bytes, page size/count/freelist/live bytes, checkpoint busy/log/copied pages, per-class pin counts, owner floors, exception counts and send/read/push latency/error counts. Use ids/counts only. Note unavailable observations explicitly; the shipped status endpoints do not expose a complete pin census or retention dashboard (see OPERATOR). Do not represent missing telemetry as zero. Each stage can pause independently; do not advance while byte guards fail or capacity/latency exceeds the operator's recorded budget.

- [ ] **Backup gate:** verify an external consistent backup by the isolated restore drill above. Set capacity/latency alerts before adding writes; 1 GB cannot hold indefinite lifetime history. Establish separate attachment and backup expiry policies.
- [ ] **Dual reader only:** deploy a verified dual reader with `--delivery-references=false --storage-backfill=false --storage-maintenance=false --storage-reclaim-pages=0`; verify stored `retention.history=0`, archive state and sources. Keep acknowledged pruning inactive (`retention.ack_events=0` for operational pause). Test each reader's existing byte contract before references exist. Deployment is a later operator action.
- [ ] **New references:** enable only `--delivery-references=true` on the sole writer. Observe byte/hash guards, hydration errors, read/send/claim latency, CPU/allocations and disk/WAL growth. Roll back to the dual reader if needed; disabling writes alone does not permit an old inline-only binary.
- [ ] **Bounded backfill:** rehearse a dry-run on the disposable restored fixture with `Hub.BackfillDeliverySlice(..., BackfillLimits{DryRun:true})`, then enable `--storage-backfill=true` with initial 100 rows/1 MiB/50 ms caps. These are cooperative limits; one oversized event can occupy its own slice. Hourly slow ticks run one slice, so completion is workload-dependent. Restart with the flag false to pause; true resumes committed progress. Review `done`, fixed `high_seq`, `last_seq`, totals and durable exceptions; `done` alone does not mean all exceptions converted. Rescans require the internal `RescanExceptions` option after a fix; no public rescan endpoint is shipped. Verify integrity/foreign keys, exact-byte samples, accounted eligible inline rows and backup again. Reference savings do not require full VACUUM.
- [ ] **Retention rehearsal and activation:** preview the exact proposed preference patch without deleting; review fixed cutoff/high-water, pin counts, estimates and backup coverage. Acknowledged pruning additionally needs `hub.Config.EventRetentionEnabled=true`, which the shipped `vecnet-hub` CLI does **not** expose or enable. Leave this rollout gate pending until an authorized integration provides that switch; setting `retention.ack_events=604800` alone does not activate its ticker. No code change is part of this task. Rehearse through the existing internal harness on a build box, then independently authorize that deployment integration. Preserve held/retry/in-flight pins and owner-prefix floors.
- [ ] **Optional hosted history:** only after a human selects the policy, preview/confirm `retention.history=7776000` (90 days), archive exit if applicable and a separate `apply_existing:true` choice if old history is to be trimmed. The shipped history worker runs on slow ticks; approved retrospective work first becomes eligible at the persisted next nightly bound (02:00 UTC by default), not at PATCH or a preference wakeup. Record configured vs effective age and pending confirmation; no silent upgrade switch or guarantee that pinned bodies disappear at 90 days.
- [ ] **Optional physical maintenance:** enable `--storage-maintenance=true` for bounded PASSIVE checkpoints; keep reclaim pages zero initially. Reclaim is separately opt-in, 0..128 pages with `--storage-reclaim-pages`, and only works on existing INCREMENTAL databases. Pause checkpoint maintenance with its boolean; pause reclaim alone with pages zero. New empty databases use 4096-byte pages/INCREMENTAL. Existing NONE files retain their mode and reuse freed pages immediately.

### Capacity, WAL and deferred shrink

Alert on both filesystem available bytes and DB/WAL growth rate, not only live payload size. Backfill requires available space greater than `max(64 MiB, 4 × slice-byte cap) + current WAL bytes`; physical maintenance uses at least 64 MiB (or its configured larger reserve) plus WAL headroom. These are operational stop thresholds, not worst-case SQLite allocation bounds. Set an earlier alert with enough time/capacity to expand the volume or move work to an external maintenance host. Long-lived readers can pin WAL; inspect checkpoint progress and reader/transaction lifetimes, pause backfill/reclaim, relieve disk/lock pressure and retry without evicting readers or weakening FULL durability. On `sqlite_full`, retain the database, stop optional work and add capacity; do not attempt an emergency full shrink.

Deletion increases reusable freelist space; a checkpoint moves WAL pages into the main file; neither necessarily reduces allocated file size. Incremental reclaim is bounded, yields to readers/pressure and may stop with free pages remaining. Compare `live_bytes`, `db_bytes`, `wal_bytes` and filesystem availability separately. WAL file length is an allocation high-water, not cumulative WAL write traffic.

**Deferred beyond this release:** setting `PRAGMA auto_vacuum=INCREMENTAL` does not convert a populated NONE database without a rebuild. No offline conversion is authorized or scheduled. If separately approved later: drain/stop all writers, checkpoint, make and verify an external rollback backup, rebuild on sufficiently large external storage, verify schema/checksums/identity/epoch/sequence/floors/snapshots/callback bytes, then install with a stopped cutover and restart the dual reader. Full `VACUUM` can need roughly twice the original DB size in **additional** free space, plus WAL/journal/temp/backup needs. `VACUUM INTO` needs a second complete DB plus temp/WAL space; a live output misses later writes unless final cutover freezes them. Atomic same-filesystem rename requires room for old and new copies together. Expand the volume or use a larger maintenance host; never delete the only good copy. Use free-page reuse first.

## Building on xsm: ISA stamp check

xsm's CachyOS C runtime stamps `x86 ISA needed: x86-64-v4` (AVX-512) into any binary **linked against it**, and such a binary dies on hosts without AVX-512. That applies only to cgo links. With `CGO_ENABLED=0` the Go linker writes the whole binary itself and links no system CRT, so a vecnet binary built on xsm carries no ISA stamp.

Check, from the repo root:

```sh
just isa-check     # builds bin/ then counts 'x86 ISA needed' notes per binary
# or by hand:
readelf -n bin/vecnet-hub | grep -c 'x86 ISA needed'
```

Recorded result (2026-11-01, xsm, go1.27.1, `CGO_ENABLED=0 build2 go build -p 2 -o bin/ ./cmd/...`): **`0` for `bin/vecnet-hub` and `0` for `bin/vecnet`**. `readelf -n` showed only the `GO BUILDID` and `NT_GNU_BUILD_ID` notes, and `file` reports both as statically linked. So binaries built on xsm are safe to copy to other hosts.

If the count is ever non-zero (for example after a dependency pulls in cgo, or after a switch to `mattn/go-sqlite3`), do not ship that binary: build on `x-alpha` instead (or `x-tuf`, which has no AVX-512 at all). The Docker image is unaffected either way, because it builds on Fly's remote builder from `golang:1.26-alpine`.
