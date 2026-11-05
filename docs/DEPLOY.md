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
| `--allow-private-egress` | `VECNET_ALLOW_PRIVATE_EGRESS=1` | off; development only, see [Connectors](#connectors) |
| `--trust-fly-client-ip` | `VECNET_TRUST_FLY_CLIENT_IP=1` | off; on in `fly.toml`, see [Public exposure](#public-exposure) |
| `--restart-retry-ms` | `VECNET_RESTART_RETRY_MS` | `5000` (0–60000); the reconnect hint sent on shutdown, see [Restarts](#restarts) |
| `--relocated-to` | `VECNET_RELOCATED_TO` | unset; set only on a hub that has moved, see [Moving a hub](#moving-a-hub) |

The public URL is the origin the hub writes into attachment `blob_url` links, so on Fly it must be the `https://` address clients use. The image's entrypoint sets `--listen` and `--data-dir`; `fly.toml` sets `VECNET_PUBLIC_URL`. On start the hub prints `vecnet-hub listening on http://<addr> (public url <url>)`.

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

`VECNET_PUBLIC_URL` is the OAuth issuer and the `/mcp` audience, and a request with any other `Origin` gets 403. So browsers and dots must use the custom domain. The CLI sends no `Origin`, so `vecnet.fly.dev` still works for it, but profiles should use `https://vecnet.xk.io`. To change domain later, follow [A new domain for the same hub](#a-new-domain-for-the-same-hub), then re-link dots: their tokens name the old issuer.

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

**`VECNET_PUBLIC_URL` must be that exact origin.** It is more than a link prefix here: it is the OAuth issuer, it sets the `/mcp` token audience, and a request carrying any other `Origin` header gets 403.

**`VECNET_ALLOW_PRIVATE_EGRESS` is dev-only.** By default every outbound POST (push routes, notification routes, Events callbacks, Grok Bot routines, CIMD fetches) is https, resolved once to a public unicast address, pinned to it, and never follows a redirect. `--allow-private-egress`, or the variable set to `1`, lets those URLs be http and reach private or loopback addresses so LAN receivers work on xsm. It also lifts the port 443 rule on Events callbacks and CIMD fetches. It removes the hub's protection against being pointed at internal services, so never set it on Fly; `fly.toml` does not.

Connector setup is in [`docs/connectors/`](connectors/): [dots](connectors/dots.md), [Grok Bot](connectors/grokbot.md) and [GitHub](connectors/github.md).

## Restarts

`just fly-deploy` (Fly stops the old machine with SIGINT) and `systemctl restart` (SIGTERM) both make the hub announce the restart before it shuts down ([PROTOCOL §21.3](PROTOCOL.md#213-restart-frame-and-long-poll-field)). Every open event stream and the operator's activity stream gets one last frame, `event: hub_restarting` with `{"retry_after_ms":5000}`, and a held long-poll answers with a `hub_restarting` field. Clients then wait about that long and reconnect without counting a failure: `vecnet watch` prints `hub_restarting … reconnecting in ~5s`, and the TUI and web UI show "restarting" rather than offline. The hub waits up to 1.25 s for the frames to flush, inside its 10 s shutdown grace, which stays under `fly.toml`'s `kill_timeout = 15`.

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

   Every agent sees it (`vecnet watch`, the TUI and web banners). A `watch` follows at `at` plus up to 30 s, and while the new hub is not answering yet it prints one `relocation_unverified` line and retries with backoff. The notice lasts 30 days unless you pass `--expires`.
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

- **Fly volume snapshots** are the consistent copy: Fly snapshots volumes daily and keeps them for a few days (`fly volumes snapshots list <vol-id>`). Restore by creating a volume from a snapshot.
- **`just fly-backup`** copies `/data/hub.db` to `backups/hub-<UTC stamp>.db` with `fly ssh sftp get`. It is a manual, best-effort copy: the database runs in WAL mode, so commits still in `hub.db-wal` are not in the copy, and a copy taken during a write can be inconsistent. Prefer it on a quiet hub. `backups/` is gitignored and dockerignored, so a deploy never uploads a copy.
- **Nightly copy:** run `just fly-backup` once a day from an always-on machine (a systemd user timer on xsm is enough) and prune old files by hand. It is documented here, not automated in this repo.
- Blobs live under `/data/blobs/` and are covered by volume snapshots only.
- **`hub.db` carries the hub's identity key** (`meta.hub_identity_seed`, [PROTOCOL §21.1](PROTOCOL.md#211-identity-key)). Every backup can therefore impersonate the hub to clients that pinned it: keep backups as private as the live volume. Publish the fingerprint from `vecnet-hub identity show --data-dir /data` (on Fly: `fly machine exec <id> "/vecnet-hub identity show --data-dir /data" -a vecnet`) so agents can check `vecnet profile show`.
- **`vecnet-hub identity rotate --yes` only with the hub stopped.** It writes a new key that the old key endorses, so pinned clients accept it one generation back; a second rotation drops that bridge for clients still on the first key. The running hub keeps its loaded key until restarted. Rotation does not help after a key leaks, because the thief can endorse too: rotate, publish the new fingerprint out of band, and have agents run `vecnet --net P profile pin --accept <fingerprint>`.

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
