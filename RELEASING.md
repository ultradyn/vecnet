# npm releases

The npm package is `vecnet`, its homepage is <https://vecnet.ai>, and its version lives in `npm/package.json`. Each release bundles the CLI and hub for Linux and Windows x64 and ARM64.

## One-time npm setup

Configure the trusted publisher at <https://www.npmjs.com/package/vecnet/access>:

| Field | Value |
| --- | --- |
| Provider | GitHub Actions |
| Organization/user | `ultradyn` |
| Repository | `vecnet` |
| Workflow filename | `release.yml` |
| Environment | Leave blank |

The workflow uses GitHub OIDC with `id-token: write` only in the publish job. It does not need an npm token secret. npm provenance is enabled when the source repository is public; the current private repository uses OIDC with provenance disabled, as required by [npm's trusted publishing documentation](https://docs.npmjs.com/trusted-publishers).

## Cut a release

From a clean `master` checkout:

```sh
just release 0.1.2
```

This updates only `npm/package.json`, commits, creates `v0.1.2`, and atomically pushes the commit and tag to the `gh` remote (`ultradyn/vecnet`). `origin` points to the LAN Git server and does not trigger GitHub Actions.

For a version already committed on `master`, create and push its tag:

```sh
git tag v0.1.2
git push --atomic gh master refs/tags/v0.1.2
```

The tag must exactly match the manifest version and point to a commit included in GitHub's `master`. Stable versions publish under `latest`; prereleases such as `0.2.0-rc.1` publish under `next` and create a GitHub prerelease.

## Release checks and artifacts

`Release` runs the full Go test suite, builds using Go 1.27.4 and npm 12.0.1, then tests the packed package on GitHub-hosted Linux and Windows x64 and ARM64 runners. All platforms install the same tarball with lifecycle scripts disabled and exercise enrolment, peer messaging, hub shutdown, and signal exit status. Builds and tests use two threads.

Only after every platform passes does CI upload the tested tarball once and check registry visibility for a short bounded window. Immediately after npm accepts the upload, the Actions summary records an `accepted` receipt binding the source commit, version, tarball and SHA-512 integrity. That evidence remains if subsequent verification fails. Transport errors, HTTP errors and conflicting integrity still fail the step and produce no pending/verified success output.

If registry metadata matches the tested integrity, publication is `verified` and CI creates a normal GitHub Release (a prerelease for `next`). This metadata check does not compare downloaded public tarball bytes. If only404 responses continue through the visibility window, publication is `pending`: npm accepted the upload, but public availability is unverified. CI creates or retains a clearly described **draft** GitHub Release. The release contains the tested tarball and `SHA256SUMS`; the Actions run retains the artifact for 30 days. Existing drafts remain drafts even on verified recovery, until the controller compares public bytes and finalizes them.

Use **Run workflow** on the Release Actions page to build and test a branch without publishing. A manual run never publishes or creates a GitHub Release.

Local packaging checks:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb just npm-pack
BB_HOST=x-boss BB_JOBS=2 just bb just npm-check
```

## Recover a release

Before any rerun, inspect the original Actions summary and npm upload logs to determine whether the upload was accepted. An `accepted` receipt remains authoritative even if verification or GitHub Release creation later failed. A missing receipt alone does not prove rejection if the process was interrupted. If acceptance is uncertain, check the registry and original upload evidence before taking further action.

**Do not rerun the tagged workflow, rerun the publish command, or reupload that version while an accepted upload is still processing and returns404.** Keep the same version and wait for visibility; processing is not an authentication failure. The receipt is evidence, not a cross-run upload lock. A failed attempt stays failed, and recovery does not require a new tag or successful tag run.

Follow the [read-only public-byte comparison and draft recovery procedure](scripts/ci/README.md#npm-publication-recovery) using the original source/run's tested artifact. After public tarball bytes match and exact-source acceptance evidence passes (accepted source CI is sufficient), the controller may finalize an existing draft and install. An existing visible npm version with matching integrity can also take the existing recovery path without uploading; never enter that path while an accepted version still returns404.

For a confirmed pre-upload infrastructure or authentication failure, correct the setting before rerunning. A source change requires a new commit and version; do not bump the version merely because an accepted upload is processing. npm versions are immutable; do not move a released tag or reuse its version with different package contents.
