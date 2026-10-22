#!/usr/bin/env bash
# Tests for scripts/release/*.sh. Runs offline: `gh` is replaced by a fake that
# serves one release from a state directory, and the git checks run against a
# throwaway repository. Needs bash, git and jq.
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd -P)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

PASSED=0
FAILED=0
pass() { PASSED=$((PASSED + 1)); }
fail() { FAILED=$((FAILED + 1)); echo "FAIL: $*" >&2; }

expect_ok() {
  local name=$1; shift
  if "$@" >"$WORK/out" 2>"$WORK/err"; then pass; else fail "$name (exit $?): $(cat "$WORK/err")"; fi
}
expect_fail() {
  local name=$1; shift
  if "$@" >"$WORK/out" 2>"$WORK/err"; then fail "$name: expected failure, got: $(cat "$WORK/out")"; else pass; fi
}
expect_out() {
  local name=$1 want=$2; shift 2
  local got
  if got=$("$@" 2>"$WORK/err") && [ "$got" = "$want" ]; then pass; else fail "$name: want '$want', got '${got:-}' $(cat "$WORK/err")"; fi
}

# ── parse-tag.sh ────────────────────────────────────────────────────────────
expect_out "unified tag" "unified 0.11.0" "$HERE/parse-tag.sh" v0.11.0
expect_out "daemon tag" "daemon 1.2.3" "$HERE/parse-tag.sh" daemon/v1.2.3
expect_out "gui tag" "gui 0.1.0-rc.1" "$HERE/parse-tag.sh" gui/v0.1.0-rc.1
expect_out "cli tag" "cli 2.0.0" "$HERE/parse-tag.sh" cli/v2.0.0
# shellcheck disable=SC2016  # '$(id)' must reach the parser unexpanded
for bad in 0.11.0 v0.11 vX.Y.Z daemon/0.1.0 web/v1.0.0 daemon-v1.0.0 \
           'v1.0.0;id' 'v1.0.0 x' '$(id)' 'cli/v1.0.0/../v2.0.0' ''; do
  expect_fail "reject tag '$bad'" "$HERE/parse-tag.sh" "$bad"
done

# ── expected-assets.sh ──────────────────────────────────────────────────────
count() { "$HERE/expected-assets.sh" "$1" "$2" | wc -l | tr -d ' '; }
expect_out "unified asset count" 15 count unified 0.11.0
expect_out "daemon asset count" 6 count daemon 0.11.0
expect_out "gui asset count" 6 count gui 0.11.0
expect_out "cli asset count" 8 count cli 0.11.0
for kind in daemon gui cli; do
  if "$HERE/expected-assets.sh" "$kind" 0.11.0 | grep -qx compat.json; then
    pass
  else
    fail "$kind releases ship compat.json"
  fi
done
expect_fail "unknown kind" "$HERE/expected-assets.sh" web 0.11.0
if "$HERE/expected-assets.sh" unified 0.11.0 | grep -qx 'Heimdallm-v0.11.0.dmg' &&
   "$HERE/expected-assets.sh" gui 0.11.0 | grep -qx 'Heimdallm-0.11.0.dmg'; then
  pass
else
  fail "DMG names: unified keeps the v prefix, gui has none"
fi

# ── versions-json.sh ────────────────────────────────────────────────────────
printf '{".":"0.12.0","daemon":"0.11.1","flutter_app":"0.12.0","cli":"0.10.5"}' > "$WORK/m.json"
expect_out "split manifest" '{"daemon":"0.11.1","gui":"0.12.0","cli":"0.10.5"}' \
  sh -c "'$HERE/versions-json.sh' '$WORK/m.json' | jq -c ."
printf '{".":"0.10.4"}' > "$WORK/old.json"
expect_out "pre-split manifest falls back to the root version" \
  '{"daemon":"0.10.4","gui":"0.10.4","cli":"0.10.4"}' \
  sh -c "'$HERE/versions-json.sh' '$WORK/old.json' | jq -c ."
printf '{".":"0.10.4","cli":"latest"}' > "$WORK/bad.json"
expect_fail "non-semver version" "$HERE/versions-json.sh" "$WORK/bad.json"
printf '{"cli":"0.10.4"}' > "$WORK/noroot.json"
expect_fail "missing root version" "$HERE/versions-json.sh" "$WORK/noroot.json"
printf '{"daemon_api":2,"min_daemon_api":{"gui":1,"cli":2}}' > "$WORK/compat.json"
expect_out "compat data is merged in" \
  '{"daemon":"0.11.1","gui":"0.12.0","cli":"0.10.5","daemon_api":2,"min_daemon_api":{"gui":1,"cli":2}}' \
  sh -c "'$HERE/versions-json.sh' '$WORK/m.json' '$WORK/compat.json' | jq -c ."
for bad in '{"daemon_api":1,"min_daemon_api":{"gui":2,"cli":1}}' \
           '{"daemon_api":"1","min_daemon_api":{"gui":1,"cli":1}}' \
           '{"daemon_api":1,"min_daemon_api":{"gui":1}}' \
           '{"daemon_api":1.5,"min_daemon_api":{"gui":1,"cli":1}}' \
           '{"daemon_api":0,"min_daemon_api":{"gui":0,"cli":0}}'; do
  printf '%s' "$bad" > "$WORK/badcompat.json"
  expect_fail "reject compat $bad" "$HERE/versions-json.sh" "$WORK/m.json" "$WORK/badcompat.json"
done
if jq -e '.daemon_api >= ([.min_daemon_api[]] | max)' "$HERE/../../compat.json" >/dev/null &&
   "$HERE/versions-json.sh" "$WORK/m.json" "$HERE/../../compat.json" >/dev/null; then
  pass
else
  fail "the repository's compat.json is valid"
fi

# ── fake gh ─────────────────────────────────────────────────────────────────
STATE="$WORK/state"
mkdir -p "$WORK/bin" "$STATE"
cat > "$WORK/bin/gh" <<'FAKE'
#!/usr/bin/env bash
set -euo pipefail
S=$FAKE_GH_STATE
case "$1 ${2:-}" in
  "release view")
    tag=$3
    [ "$tag" = "$(cat "$S/tag")" ] || { echo "release not found" >&2; exit 1; }
    json=$(jq -n --arg tag "$tag" --arg target "$(cat "$S/target")" \
      --argjson draft "$(cat "$S/draft")" \
      --rawfile assets "$S/assets" \
      '{tagName: $tag, isDraft: $draft, targetCommitish: $target,
        assets: ($assets | split("\n") | map(select(length > 0)) | map({name: .}))}')
    if [ "${6:-}" = --jq ]; then jq -r "$7" <<< "$json"; else echo "$json"; fi
    ;;
  "release edit")
    echo "$*" >> "$S/edits"
    for arg in "$@"; do
      case "$arg" in
        --draft=false) echo false > "$S/draft" ;;
        --latest) echo "$3" > "$S/latest" ;;
      esac
    done
    if [ -n "${FAKE_GH_EDIT_MOVES_LATEST:-}" ]; then echo "$3" > "$S/latest"; fi
    ;;
  "api repos/"*)
    [ -s "$S/latest" ] || exit 1
    cat "$S/latest"
    ;;
  *) echo "fake gh: unsupported: $*" >&2; exit 1 ;;
esac
FAKE
chmod +x "$WORK/bin/gh"
export PATH="$WORK/bin:$PATH" FAKE_GH_STATE="$STATE" GH_REPO=theburrowhub/heimdallm

seed_release() { # TAG TARGET DRAFT LATEST ASSETS...
  local tag=$1 target=$2 draft=$3 latest=$4; shift 4
  echo "$tag" > "$STATE/tag"; echo "$target" > "$STATE/target"
  echo "$draft" > "$STATE/draft"; echo "$latest" > "$STATE/latest"
  printf '%s\n' "$@" > "$STATE/assets"; : > "$STATE/edits"
}

# ── verify-assets.sh ────────────────────────────────────────────────────────
mapfile_compat() { "$HERE/expected-assets.sh" "$1" "$2"; }
CLI_ASSETS=()
while IFS= read -r a; do CLI_ASSETS+=("$a"); done < <(mapfile_compat cli 1.2.3)

seed_release cli/v1.2.3 x true v0.11.0 "${CLI_ASSETS[@]}"
expect_ok "exact asset set" "$HERE/verify-assets.sh" cli/v1.2.3
seed_release cli/v1.2.3 x true v0.11.0 "${CLI_ASSETS[@]:1}"
expect_fail "missing asset" "$HERE/verify-assets.sh" cli/v1.2.3
seed_release cli/v1.2.3 x true v0.11.0 "${CLI_ASSETS[@]}" extra.txt
expect_fail "extra asset" "$HERE/verify-assets.sh" cli/v1.2.3
seed_release cli/v1.2.3 x true v0.11.0 "${CLI_ASSETS[@]}" checksums.txt
expect_fail "duplicated asset" "$HERE/verify-assets.sh" cli/v1.2.3

# ── require-draft.sh ────────────────────────────────────────────────────────
seed_release cli/v1.2.3 x true v0.11.0 "${CLI_ASSETS[@]}"
expect_ok "draft accepts uploads" "$HERE/require-draft.sh" cli/v1.2.3
seed_release cli/v1.2.3 x false v0.11.0 "${CLI_ASSETS[@]}"
expect_fail "published release refuses uploads" "$HERE/require-draft.sh" cli/v1.2.3
expect_fail "missing release refuses uploads" "$HERE/require-draft.sh" cli/v9.9.9
expect_fail "invalid tag refuses uploads" "$HERE/require-draft.sh" 'cli/v1;id'

# ── finalize-component.sh ───────────────────────────────────────────────────
REPO="$WORK/repo"
git init -q "$REPO"
git -C "$REPO" -c user.name=t -c user.email=t@t commit -q --allow-empty -m one
git -C "$REPO" tag cli/v1.2.3
git -C "$REPO" tag v0.11.0
OLD=$(git -C "$REPO" rev-parse HEAD)
git -C "$REPO" -c user.name=t -c user.email=t@t commit -q --allow-empty -m two
HEAD_SHA=$(git -C "$REPO" rev-parse HEAD)
git -C "$REPO" tag cli/v1.3.0
finalize() { (cd "$REPO" && "$HERE/finalize-component.sh" "$@"); }

seed_release cli/v1.3.0 "$HEAD_SHA" true v0.11.0 "${CLI_ASSETS[@]//1.2.3/1.3.0}"
expect_ok "publish component draft" finalize cli/v1.3.0 "$HEAD_SHA"
if grep -q -- '--latest=false' "$STATE/edits" && [ "$(cat "$STATE/draft")" = false ] &&
   [ "$(cat "$STATE/latest")" = v0.11.0 ]; then pass; else fail "published with --latest=false"; fi

expect_ok "already published is idempotent" finalize cli/v1.3.0 "$HEAD_SHA"
if [ ! -s "$STATE/edits" ] || [ "$(wc -l < "$STATE/edits" | tr -d ' ')" = 1 ]; then pass; else fail "no second edit"; fi

seed_release cli/v1.3.0 "$HEAD_SHA" true v0.11.0 "${CLI_ASSETS[@]//1.2.3/1.3.0}"
FAKE_GH_EDIT_MOVES_LATEST=1 expect_fail "component became latest" finalize cli/v1.3.0 "$HEAD_SHA"

seed_release cli/v1.2.3 "$OLD" true v0.11.0 "${CLI_ASSETS[@]}"
expect_ok "repair of an ancestor tag" finalize cli/v1.2.3 "$HEAD_SHA"
seed_release cli/v1.3.0 "$HEAD_SHA" true v0.11.0 "${CLI_ASSETS[@]//1.2.3/1.3.0}"
expect_fail "tag not on the workflow ref" finalize cli/v1.3.0 "$OLD"
seed_release cli/v1.3.0 "$OLD" true v0.11.0 "${CLI_ASSETS[@]//1.2.3/1.3.0}"
expect_fail "release target differs from the tag" finalize cli/v1.3.0 "$HEAD_SHA"
seed_release cli/v1.3.0 "$HEAD_SHA" true v0.11.0 "${CLI_ASSETS[@]:1}"
expect_fail "incomplete draft is not published" finalize cli/v1.3.0 "$HEAD_SHA"
if [ "$(cat "$STATE/draft")" = true ]; then pass; else fail "incomplete draft stayed a draft"; fi
expect_fail "unified tags are refused" finalize v0.11.0 "$HEAD_SHA"

echo "release scripts: $PASSED passed, $FAILED failed"
[ "$FAILED" -eq 0 ]
