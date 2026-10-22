#!/usr/bin/env bash
# Publish a per-component draft release (daemon/vX, gui/vX, cli/vX) once every
# asset is in place.
#
# A component release must never become the repository's "latest" release:
# the CLI and desktop updaters, scripts/macos-install.sh and the install docs
# resolve /releases/latest and expect the unified vX.Y.Z installer there.
# GitHub marks a newly published release as latest by default, so this script
# passes --latest=false and then checks that the latest release is still a
# unified one. (The unified finalizer of the same run may legitimately move it
# to the new vX.Y.Z in parallel, so "unchanged" would be the wrong check.)
#
# Run from a full checkout (tags fetched). Requires an authenticated `gh`
# (GH_TOKEN) and GH_REPO. EXPECTED_COMMIT is the commit the workflow runs on;
# the tag must point at it (push) or at one of its ancestors (repair dispatch).
set -euo pipefail

[ "$#" -eq 2 ] || { echo "usage: $0 TAG EXPECTED_COMMIT" >&2; exit 64; }
TAG=$1
EXPECTED_COMMIT=$2
HERE=$(cd "$(dirname "$0")" && pwd -P)

read -r KIND _ <<< "$("$HERE/parse-tag.sh" "$TAG")"
if [ "$KIND" = unified ]; then
  echo "::error::$TAG is a unified release; it is finalized by release-unified.yml" >&2
  exit 2
fi

TAG_COMMIT=$(git rev-parse --verify "${TAG}^{commit}")
git merge-base --is-ancestor "$TAG_COMMIT" "$EXPECTED_COMMIT" || {
  echo "::error::$TAG ($TAG_COMMIT) is not on the workflow ref $EXPECTED_COMMIT" >&2
  exit 1
}

RELEASE_JSON=$(gh release view "$TAG" --json tagName,isDraft,targetCommitish)
test "$(jq -r .tagName <<< "$RELEASE_JSON")" = "$TAG"
RELEASE_TARGET=$(jq -r .targetCommitish <<< "$RELEASE_JSON")
test "$RELEASE_TARGET" = "$TAG_COMMIT" || {
  echo "::error::Release target $RELEASE_TARGET does not match $TAG ($TAG_COMMIT)" >&2
  exit 1
}

"$HERE/verify-assets.sh" "$TAG"

latest_tag() {
  gh api "repos/${GH_REPO}/releases/latest" --jq .tag_name 2>/dev/null || true
}

if [ "$(jq -r .isDraft <<< "$RELEASE_JSON")" = true ]; then
  gh release edit "$TAG" --draft=false --latest=false >/dev/null
fi

for _ in $(seq 1 12); do
  IS_DRAFT=$(gh release view "$TAG" --json isDraft --jq .isDraft)
  LATEST_AFTER=$(latest_tag)
  if [ "$IS_DRAFT" = false ]; then
    if [ -n "$LATEST_AFTER" ] &&
       [ "$("$HERE/parse-tag.sh" "$LATEST_AFTER" 2>/dev/null | cut -d' ' -f1)" != unified ]; then
      echo "::error::The latest release is now '$LATEST_AFTER', not a unified vX.Y.Z release" >&2
      exit 1
    fi
    echo "Published $TAG; the latest release is '${LATEST_AFTER:-<none>}'"
    exit 0
  fi
  sleep 5
done
echo "::error::Release $TAG was not observable as published" >&2
exit 1
