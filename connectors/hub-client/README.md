# Shared hub client

Dependency-free TypeScript wire types, ports and retry helpers for vecnet connectors. Relative imports use `.ts` so Node 22.18 or newer can run the erasable TypeScript directly.

- `src/wire.ts`: the PROTOCOL wire shapes a connector sends and receives (messages, Deliveries, Events, `job_request`/`job_status`, presence, receipts, directory entries).
- `src/ports.ts`: `HubApi` (send, ack, receipt, presence, blobs, directory), `EventStream`, `CursorStore`, and the `Clock` and `Log` ports. Every timer goes through `Clock`, so tests use a fake clock.
- `src/client.ts`: `createHubClient`, the HTTP `HubApi`. Disk blob fetches stay on the hub's origin, follow no redirects and send no bearer. R2 `putBlob` grants a direct upload, PUTs the bytes to the allowed object store with the required signed headers, then completes; it returns the reference and verified integrity, never a stored URL. `fetchBlob` accepts that reference, or a same-origin hub URL that names a blob id, exchanges it for a fresh grant and reads the object without hub credentials. URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired. `origin_url` is never fetched.
- `src/stream.ts` and `src/sse.ts`: a resumable SSE event stream that saves its cursor and stops with a reason (`auth`, `disabled`, `aborted`) rather than retrying a refused credential.
- `src/retry.ts`, `src/errors.ts`, `src/ids.ts`: bounded retries that honour `Retry-After`, `HubError` with the hub's error code, and id patterns.
- `testing/`: an HTTP fake hub with SSE replay and long-poll, a harness that starts a real `vecnet-hub`, fixtures, a deterministic clock and an in-memory log.

The [launcher](../launcher/README.md) is its first user and type-checks this package together with its own source (`just ln-typecheck`); `just ln-test` runs both packages' tests. The package has no `package.json` dependencies at all.

R2 discovery refreshes per connection/config generation; an old hub's 404 selects legacy disk, while an R2 error never falls back to a byte upload through Fly. Direct transfers follow no redirects and omit hub bearer/cookies. `putBlob` returns completed immutable reference/checksum evidence, and `fetchBlob` obtains a fresh grant on every explicit call; URLs are never persisted. GET lasts at most 600s, PUT 300s; default object expiry is 24 h and off records keep with null deadline. Shared Settings changes affect new grants only. The shared mint budget is 30 per peer per rolling 60s across HTTP/MCP/CLI: honor `blob_url_rate_limited` retry detail, and preserve distinct `blob_expired`/`blob_purged` errors. Redact capabilities in logs, protect MCP transcripts, and whitelist only the private S3 origin. Storage quota defaults to 5 GiB; hosted off needs shared cost confirmation. Durable spills/source raw JSON and avatars stay in SQLite. No Worker/custom-domain/public bucket or file migration is required; [operator setup](../../docs/DEPLOY.md#r2-operator-setup-max-only) retains the outstanding provider gates.

### Windows process fixtures

`src/platform.ts` supplies HOME/USERPROFILE selection, case-insensitive environment
lookup, safe executable suffix/npm-shim argv resolution, and owned-tree termination
for launcher/test subprocesses. Native executables do not run through a shell.
`testing/real-hub.ts` selects `vecnet-hub.exe` on Windows and starts hidden children.
Run the shared tests with Node's built-in runner; the launcher's
`check-windows.ps1` also typechecks and runs hub-client tests.
