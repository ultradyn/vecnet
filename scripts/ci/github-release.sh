#!/usr/bin/env bash
set -euo pipefail
if gh release view "$TAG" >/dev/null 2>&1; then
  gh release upload "$TAG" "dist/$TARBALL" dist/SHA256SUMS --clobber
else
  flags=()
  if [ "$NPM_TAG" = next ]; then flags+=(--prerelease); fi
  gh release create "$TAG" "dist/$TARBALL" dist/SHA256SUMS \
    --verify-tag --title "$TAG" --generate-notes "${flags[@]}"
fi
