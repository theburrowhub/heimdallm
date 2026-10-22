#!/usr/bin/env bash
# Fail unless release TAG carries exactly the asset set expected-assets.sh
# lists for it: nothing missing, nothing duplicated, nothing extra.
# Requires an authenticated `gh` (GH_TOKEN) and GH_REPO.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 TAG" >&2; exit 64; }
TAG=$1
HERE=$(cd "$(dirname "$0")" && pwd -P)

read -r KIND VERSION <<< "$("$HERE/parse-tag.sh" "$TAG")"
EXPECTED=$("$HERE/expected-assets.sh" "$KIND" "$VERSION" | LC_ALL=C sort)
ACTUAL=$(gh release view "$TAG" --json assets --jq '.assets[].name' | LC_ALL=C sort)

DUPLICATES=$(printf '%s\n' "$ACTUAL" | uniq -d)
if [ -n "$DUPLICATES" ]; then
  echo "::error::Release $TAG has duplicated assets:" >&2
  printf '%s\n' "$DUPLICATES" | sed 's/^/  /' >&2
  exit 1
fi
if [ "$EXPECTED" != "$ACTUAL" ]; then
  echo "::error::Release $TAG does not carry the expected asset set" >&2
  diff <(printf '%s\n' "$EXPECTED") <(printf '%s\n' "$ACTUAL") | sed 's/^/  /' >&2 || true
  exit 1
fi
echo "Release $TAG carries the exact $KIND asset set ($(printf '%s\n' "$EXPECTED" | wc -l | tr -d ' ') assets)"
