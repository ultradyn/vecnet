# Shared hub client

Dependency-free TypeScript wire types, ports and retry helpers for vecnet connectors. Relative imports use `.ts` so Node 22.18 or newer can run the erasable TypeScript directly.

- `src/wire.ts`: the PROTOCOL wire shapes a connector sends and receives (messages, Deliveries, Events, `job_request`/`job_status`, presence, receipts, directory entries).
- `src/ports.ts`: `HubApi` (send, ack, receipt, presence, blobs, directory), `EventStream`, `CursorStore`, and the `Clock` and `Log` ports. Every timer goes through `Clock`, so tests use a fake clock.
- `src/client.ts`: `createHubClient`, the HTTP `HubApi`. Disk blob fetches stay on the hub's origin, follow no redirects and send no bearer. R2 `putBlob` grants a direct upload, PUTs the bytes to the allowed object store with the required signed headers, then completes; it returns the reference and verified integrity, never a stored URL. `fetchBlob` accepts that reference, or a same-origin hub URL that names a blob id, exchanges it for a fresh grant and reads the object without hub credentials. URLs are short-lived; call get again whenever you need the file, as long as it hasn't expired. `origin_url` is never fetched.
- `src/stream.ts` and `src/sse.ts`: a resumable SSE event stream that saves its cursor and stops with a reason (`auth`, `disabled`, `aborted`) rather than retrying a refused credential.
- `src/retry.ts`, `src/errors.ts`, `src/ids.ts`: bounded retries that honour `Retry-After`, `HubError` with the hub's error code, and id patterns.
- `testing/`: an HTTP fake hub with SSE replay and long-poll, a harness that starts a real `vecnet-hub`, fixtures, a deterministic clock and an in-memory log.

The [launcher](../launcher/README.md) is its first user and type-checks this package together with its own source (`just ln-typecheck`); `just ln-test` runs both packages' tests. The package has no `package.json` dependencies at all.
