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

Only after every platform passes does CI publish the tested tarball and verify npm serves the same integrity after processing. The GitHub Release contains that tarball and `SHA256SUMS`; the Actions run retains the artifact for 30 days. An existing npm version is accepted on a rerun only when its integrity matches the artifact exactly, allowing recovery if publishing succeeded but GitHub Release creation failed.

Use **Run workflow** on the Release Actions page to build and test a branch without publishing. A manual run never publishes or creates a GitHub Release.

Local packaging checks:

```sh
BB_HOST=x-boss BB_JOBS=2 just bb just npm-pack
BB_HOST=x-boss BB_JOBS=2 just bb just npm-check
```

For a failed release, fix any source problem with a new commit and version. For an infrastructure or authentication failure, correct the setting and rerun the failed workflow. npm versions are immutable; do not move a released tag or reuse its version with different package contents.
