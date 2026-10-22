#!/usr/bin/env bash
# Print the versions.json manifest of a unified release: the component versions
# recorded by release-please at the release commit.
#
# Releases cut before the per-component split only have the root (".") entry;
# every component shipped with the unified version then, so it is the fallback.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 RELEASE_PLEASE_MANIFEST" >&2; exit 64; }

jq -e '
  def semver: test("^[0-9]+\\.[0-9]+\\.[0-9]+([.-][0-9A-Za-z.-]+)?$");
  (.["."] // error("manifest has no root version")) as $root
  | {
      daemon: (.daemon // $root),
      gui: (.flutter_app // $root),
      cli: (.cli // $root)
    }
  | if all(.[]; type == "string" and semver) then .
    else error("manifest has a non-semver component version") end
' "$1"
