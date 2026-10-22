#!/usr/bin/env bash
# Fail unless release TAG exists and is still a draft. Every upload step runs
# this first: a published release is immutable, and a repair dispatch must
# never replace the assets users already downloaded.
# Requires an authenticated `gh` (GH_TOKEN) and GH_REPO.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 TAG" >&2; exit 64; }
TAG=$1
"$(cd "$(dirname "$0")" && pwd -P)/parse-tag.sh" "$TAG" >/dev/null

IS_DRAFT=$(gh release view "$TAG" --json isDraft --jq .isDraft) || {
  echo "::error::Release $TAG does not exist; release-please creates the draft first" >&2
  exit 1
}
[ "$IS_DRAFT" = true ] || {
  echo "::error::Release $TAG is already published and immutable" >&2
  exit 1
}
