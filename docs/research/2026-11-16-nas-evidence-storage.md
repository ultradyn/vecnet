# NAS storage for vecnet evidence

Historical investigation snapshot. The selected R2 service is now deployed; see the [acceptance receipt](../plans/receipts/2026-11-16-public-evidence.md) for current implementation status.

Feasibility investigation, 2026-11-16. No infrastructure, credentials, Git
attributes, LFS configuration, or repository history was changed.

## Recommendation after public-access and expiry requirements

The operator clarified that artifacts should be publicly readable, inexpensive,
and eligible for automatic deletion. Prefer ordinary artifact storage to LFS:
LFS pointers in old commits continue to reference objects after those objects
are deleted, so expiration makes historical hydration fail.

**Preferred: a dedicated Cloudflare R2 Standard bucket**, separate from private
application attachments. Publish evidence through a public custom domain,
authenticate uploads, and keep only source commit/run identifiers, URLs,
checksums and expected expiry in Git. Proposed key layout:

- `runs/<commit>/<run>/...`: routine screenshots and clips, expire after 30 days.
- `kept/<commit>/<run>/...`: explicitly retained evidence, no automatic expiry.

These are proposed names and policies, not provisioned resources. An expiry
policy must apply only to `runs/`; old review links will intentionally stop
working after retention. A small HTML index beside each run can make the
images and clips browsable without downloading an archive.

R2 supports age-based expiration with prefix filters. Deletion is asynchronous,
typically within 24 hours after expiration. Public access applies to bucket
contents, so use a dedicated bucket. Use a custom domain for production;
`r2.dev` is rate-limited and intended for development. Anonymous public
downloads coexist with authenticated S3 uploads using bucket-scoped credentials.
Official sources:

- https://developers.cloudflare.com/r2/buckets/object-lifecycles/
- https://developers.cloudflare.com/r2/buckets/public-buckets/
- https://developers.cloudflare.com/r2/api/tokens/

Cloudflare's official pricing, checked 2026-11-16, lists Standard storage at
USD 0.015/GB-month, a free allowance of 10 GB-month/month, 1 million Class A
and 10 million Class B operations/month, and no Internet egress charge.
The allowance is shared with other usage in the account. At 100 GB-month,
storage is approximately USD 1.35 after the allowance, plus billable requests.
Source: https://developers.cloudflare.com/r2/pricing/

**NAS alternative:** the existing `nas-public-files` tunnel already exposes a
dedicated static-file origin at `https://public-files-33.xk.io/`. A separate
evidence directory/origin and scheduled run-directory deletion can provide
public artifacts without publishing Forgejo. This avoids a new hosted-storage
bill but depends on NAS availability, home upload bandwidth and maintained
cleanup. The existing `SMOKE.txt` public URL returned HTTP 200 during this
investigation. No directory, tunnel rule or deletion job was created.

Both choices require a separate Git history cleanup to remove the existing
binary blobs. Moving only future evidence does not shrink full clones.

## Verified NAS state

Read `~/lan-swarm/AGENTS.md` and the Forgejo, package-registry, NAS-inventory,
Cloudflare-tunnel and xmesh runbooks. Read-only SSH commands independently
confirmed:

- Forgejo `17.0.1+gitea-1.22.0` is running in `ix-forgejo-forgejo-1`.
- Canonical URL: `http://forgejo.lan:30142/`, a private LAN service.
- `Main/forgejo` uses 15.8 GiB and reports 8.72 TiB available. This is
  dataset availability, not a new reserved allocation or a pool audit.
- The container's `/var/lib/gitea` is persisted at `/mnt/Main/forgejo`.
- `[lfs] PATH = /var/lib/gitea/git/lfs`, so its configured LFS directory
  maps to `/mnt/Main/forgejo/git/lfs`.
- `[server] LFS_START_SERVER` is present but blank. LFS service operation
  has not been demonstrated by an authenticated upload/download test.
- Anonymous `/api/v1/version` returns HTTP 403, requiring sign-in.
- The scheduled snapshot tasks cover `Main/webhook2mcp` and
  `Main/Backups/x-boss`; neither covers `Main/forgejo`. This does not prove
  that other backup mechanisms are absent.
- This workstation currently has no `git lfs` command installed.

Local sources:

- `~/lan-swarm/operations/forgejo-runner-nas.md`
- `~/lan-swarm/operations/forgejo-machine-accounts.md`
- `~/lan-swarm/operations/forgejo-packages-xpp-backend.md`
- `~/lan-swarm/operations/public-files-cloudflare.md`
- `~/lan-swarm/xmesh/README.md`

The package-registry runbook records a deployed artifact-storage precedent:
commit-keyed archives, distinct upload/download credentials, SHA-256
verification, and a retention rule scoped to that package.

## Storage choices

The existing Forgejo instance is a suitable starting point for a NAS-hosted
LFS service. It already supplies authentication, repository permissions and
persistent storage. Retaining evidence for historical source checkouts
requires keeping the referenced LFS objects for the corresponding history.

For disposable screenshots and clips, the already-used Forgejo generic
package registry is also a strong fit: upload a run archive, store its URL,
source commit and checksum in a small Git receipt, and apply an explicit
retention policy. Its API is not an LFS endpoint.

LAN-only storage is directly accessible to LAN clients. Off-LAN clients and
GitHub-hosted CI need a configured network route or an authenticated HTTPS
endpoint. Existing xmesh and Cloudflare runbooks describe possible building
blocks, not a deployed public LFS endpoint.

## Required validation before migration

1. Establish whether LFS serving is enabled and enable it if necessary.
2. Use a disposable Forgejo repository to test authenticated LFS upload,
   download and object integrity from a second clean client.
3. If source remains on GitHub, test an actual GitHub push whose LFS object
   exists only on the NAS. GitHub documents an LFS integrity check that can
   block pushes; external-server acceptance has not been verified here.
4. Configure source checkouts to omit evidence by default and provide an
   explicit download path. Ordinary LFS checkout normally downloads the
   current matching objects.
5. Define snapshot/backup coverage and retention before making the NAS the
   authoritative evidence store.
6. Migrate historical evidence blobs across the intended branches and tags;
   adding LFS tracking only for future commits does not shrink old history.

GitHub integrity-check source:
https://docs.github.com/en/repositories/working-with-files/managing-large-files/resolving-git-large-file-storage-upload-failures
