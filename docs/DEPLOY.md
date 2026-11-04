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

On SIGTERM/SIGINT the hub signals its stream stop channel before HTTP
shutdown. Events and operator activity SSE drain queued frames, flush a
final `hub_restarting` control frame with `retry: 1000` and no cursor id,
then close normally. MCP GET and `subscriptions/listen` streams close
without a custom event or fabricated RPC result; parked long-polls return
an empty page with their resume cursor. Each stalled writer has a one-second
drain budget, so an unreadable connection may still end with a transport
error. HTTP shutdown has a 10 s grace period, below the existing Fly
`kill_timeout = 15`; the current signal, timeout and proxy configuration
remain unchanged.

A single-machine deploy can still leave a brief Fly proxy 502/503/504 or
connection-refused window while the replacement starts. Watch, TUI and the
web UI retry quietly with jittered backoff and preserve their durable
cursor; the web activity feed refetches on `ready`. Watch emits one outage
line and one recovery line on stderr, keeping monitor stdout unchanged;
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

`VECNET_PUBLIC_URL` is the OAuth issuer and the `/mcp` audience, so clients must use the configured custom domain. A present `/mcp` `Origin` must be that public origin or an allowed extra origin (default `https://chatgpt.com`); any other origin gets 403 before authentication. The CLI sends no `Origin`, so `vecnet.fly.dev` still works for it, but profiles should use `https://vecnet.xk.io`. To change domain later, add the new cert and records, switch `VECNET_PUBLIC_URL`, redeploy, and re-link dots: their tokens name the old issuer. [Issue 41](../.scratch/vecnet/issues/41-hub-notices-and-relocation.md) plans notices and verified relocation for this.

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

## Backups

- **Fly volume snapshots** are the consistent copy: Fly snapshots volumes daily and keeps them for a few days (`fly volumes snapshots list <vol-id>`). Restore by creating a volume from a snapshot.
- **`just fly-backup`** copies `/data/hub.db` to `backups/hub-<UTC stamp>.db` with `fly ssh sftp get`. It is a manual, best-effort copy: the database runs in WAL mode, so commits still in `hub.db-wal` are not in the copy, and a copy taken during a write can be inconsistent. Prefer it on a quiet hub. `backups/` is gitignored and dockerignored, so a deploy never uploads a copy.
- **Nightly copy:** run `just fly-backup` once a day from an always-on machine (a systemd user timer on xsm is enough) and prune old files by hand. It is documented here, not automated in this repo.
- Blobs live under `/data/blobs/` and are covered by volume snapshots only.

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
