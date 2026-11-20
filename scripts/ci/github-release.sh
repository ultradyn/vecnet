#!/usr/bin/env bash
set -euo pipefail
case "${NPM_STATE:-}" in
  pending|verified) ;;
  *) echo 'Expected explicit pending or verified npm publication state' >&2; exit 1 ;;
esac
pending_notes="npm accepted the upload for $TAG, but it is not yet verified visible in the public registry. This draft does not establish npm availability. Do not republish this version. The controller must verify the public tarball against the tested artifact before finalizing this draft or installing."
if draft=$(gh release view "$TAG" --json isDraft --jq .isDraft 2>/dev/null); then
  if [ "$NPM_STATE" = pending ] && [ "$draft" != true ]; then
    echo 'npm visibility is pending but the GitHub Release is already public; controller review required' >&2
    exit 1
  fi
  gh release upload "$TAG" "dist/$TARBALL" dist/SHA256SUMS --clobber
  if [ "$NPM_STATE" = pending ]; then
    gh release edit "$TAG" --draft --notes "$pending_notes"
  fi
  # Existing drafts require controller verification of public tarball bytes.
  # Matching npm metadata alone must not finalize them automatically.
else
  flags=()
  if [ "$NPM_TAG" = next ]; then flags+=(--prerelease); fi
  if [ "$NPM_STATE" = pending ]; then flags+=(--draft --notes "$pending_notes"); fi
  gh release create "$TAG" "dist/$TARBALL" dist/SHA256SUMS \
    --verify-tag --title "$TAG" --generate-notes "${flags[@]}"
fi
