# Manual R2 provider spike

This stdlib-only Go tool is gated by `//go:build spike`, so ordinary `just build`
and `go test ./...` do not ship or execute it. It is a provider acceptance probe,
not the production signer. Task 1 can reuse its literal canonical vector and the
independently computed Python HMAC signature in `sign_test.go`.

Run only against the assigned `vecnet-blobs-test` bucket on x-boss:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 env \
  VECNET_R2_SPIKE_CREDENTIALS_FILE=/home/xertrov/bb-src/vecnet-r2-cred/r2-test.env \
  go run -tags spike ./internal/tools/r2spike
```

The `env` command sets the file path inside the remote command because the lane
wrapper refuses to forward environment names containing `CRED`. The file stays
at its original protected location on x-boss. Never copy or shell-source it.
Only its five assigned `R2_*` keys are accepted; optional matching quotes are
stripped without shell evaluation. Mode/owner/type/inode and no-follow checks
happen before any provider call. The endpoint must match the supplied account's
exact HTTPS R2 S3 hostname and the bucket must be the assigned test bucket.

The run takes about eleven minutes: it observes a real 600-second GET expiry
and re-mints a capability for the same object. It writes a sanitized checkpoint
receipt to `docs/plans/receipts/2026-11-05-r2-spike.data.json`; `just bb` pulls it
back, including when the tool exits 1 for a failed gate. Raw URLs, request headers, credentials, XML error messages and transport
errors are never formatted or persisted. Provider codes use a fixed allowlist;
unrecognized codes become `OtherProviderError`. Panic output is suppressed.

The tool allocates only exact per-run keys under
`attachments/v1/exp/1h/spike-<runid>/` and
`attachments/v1/keep/spike-<runid>/`. It never lists the bucket or mutates bucket
configuration. It deletes and HEAD-confirms all its allocated keys after every
PUT capability's 300-second TTL plus 60 seconds, except the two lifecycle
fixtures named in the receipt. Those require later physical-deletion and keep
survival observations. An interrupted run can leave keys; its checkpoint
identifies the run namespace, and cleanup must remain confined to that run.

To explicitly replace a completed run's two retained fixtures during a rerun,
add `--replace-previous-fixtures`. This validates the existing completed receipt
and exact expected fixture names, deletes them, and confirms absence before
starting the new run. Archive the earlier receipt first if it is needed. A normal
run preserves prior fixtures. No production resources or credentials are used.

Offline checks (no provider credentials or outbound network):

```sh
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 \
  go test -tags spike ./internal/tools/r2spike
BB_HOST=x-boss BB_JOBS=2 just bb GOMAXPROCS=2 \
  go vet -tags spike ./internal/tools/r2spike
```

The acceptance receipt distinguishes raw OPTIONS evidence from outstanding
browser Blob/automatic Content-Length/CORS tests, configuration verified by the
coordinator from object-token read attempts, and logical expiry from eventual
physical lifecycle deletion. Selecting a checksum profile does not override a
failed binding or CORS gate. A valid exact-size chunked request may be normalized
by R2; the security probe gives a wrong-size chunked body a matching checksum
while preserving the original signed length, so checksum rejection alone cannot
hide a length bypass.
