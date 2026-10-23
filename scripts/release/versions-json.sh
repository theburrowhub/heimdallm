#!/usr/bin/env bash
# Print the versions.json manifest of a unified release: the component versions
# recorded by release-please at the release commit and, when COMPAT_JSON is
# given, the compatibility matrix from the repository's compat.json
# (daemon_api, min_daemon_api).
#
# Releases cut before the per-component split only have the root (".") entry;
# every component shipped with the unified version then, so it is the fallback.
# Releases cut before compat.json existed get no compatibility keys.
set -euo pipefail

[ "$#" -ge 1 ] && [ "$#" -le 2 ] || {
  echo "usage: $0 RELEASE_PLEASE_MANIFEST [COMPAT_JSON]" >&2
  exit 64
}
COMPAT='{}'
if [ "$#" -eq 2 ]; then
  COMPAT=$(jq -e '
    def positive: type == "number" and . == floor and . > 0;
    if (.daemon_api | positive) and
       (.min_daemon_api | type == "object") and
       (.min_daemon_api.gui | positive) and (.min_daemon_api.cli | positive) and
       ([.min_daemon_api[]] | max) <= .daemon_api
    then {daemon_api, min_daemon_api: {gui: .min_daemon_api.gui, cli: .min_daemon_api.cli}}
    else error("compat.json is invalid: positive integers, minimums <= daemon_api") end
  ' "$2")
fi

jq -e --argjson compat "$COMPAT" '
  def semver: test("^[0-9]+\\.[0-9]+\\.[0-9]+([.-][0-9A-Za-z.-]+)?$");
  (.["."] // error("manifest has no root version")) as $root
  | {
      daemon: (.daemon // $root),
      gui: (.flutter_app // $root),
      cli: (.cli // $root)
    }
  | if all(.[]; type == "string" and semver) then . + $compat
    else error("manifest has a non-semver component version") end
' "$1"
