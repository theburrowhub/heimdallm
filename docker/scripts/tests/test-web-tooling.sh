#!/bin/sh
# Deterministic regression tests for the Flutter Web shell tooling.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
repo_root="$(CDPATH= cd "$script_dir/../../.." && pwd)"
fixture_dir="$script_dir/fixtures/web-tooling"
parser="$repo_root/docker/scripts/list-flutter-assets.sh"
temp_root="$(mktemp -d)"
fake_log="$temp_root/fake-docker.log"

fail() {
  printf 'Flutter Web tooling test failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [ -n "${temp_root:-}" ] && [ -d "$temp_root" ]; then
    rm -rf "$temp_root"
  fi
}
trap cleanup EXIT

assert_contains() {
  haystack="$1"
  needle="$2"
  description="$3"
  case "$haystack" in
    *"$needle"*) ;;
    *) fail "$description (missing: $needle)" ;;
  esac
}

assert_parser_fails() {
  fixture="$1"
  expected_error="$2"

  if parser_error="$(sh "$parser" "$fixture" 2>&1)"; then
    fail "parser accepted unsupported fixture: $(basename "$fixture")"
  fi
  assert_contains \
    "$parser_error" \
    "$expected_error" \
    "parser did not explain why $(basename "$fixture") is unsupported"
}

expected_resources='assets/icon.png
fonts/Fixture-Regular.ttf
fonts/Fixture-Bold.ttf
shaders/fixture.frag'
actual_resources="$(
  sh "$parser" "$fixture_dir/resources.yaml"
)"
[ "$actual_resources" = "$expected_resources" ] \
  || fail "assets, font assets and shaders were not parsed in declaration order"

expected_allowlist='!assets/icon.png
!flutter_app/fonts/
flutter_app/fonts/**
!flutter_app/fonts/Fixture-Regular.ttf
!flutter_app/fonts/Fixture-Bold.ttf
!flutter_app/shaders/
flutter_app/shaders/**
!flutter_app/shaders/fixture.frag'
actual_allowlist="$(
  sh "$parser" --dockerignore "$fixture_dir/resources.yaml"
)"
[ "$actual_allowlist" = "$expected_allowlist" ] \
  || fail "repository-root Docker paths were not generated literally"

assert_parser_fails \
  "$fixture_dir/inline-fonts.yaml" \
  "inline flutter.fonts values are unsupported"
assert_parser_fails \
  "$fixture_dir/missing-font-assets.yaml" \
  "has no supported asset entries"
assert_parser_fails \
  "$fixture_dir/unsafe-asset.yaml" \
  "unsafe for a literal Docker allowlist"
assert_parser_fails \
  "$fixture_dir/directory-asset.yaml" \
  "is a directory; declare individual files"
assert_parser_fails \
  "$fixture_dir/duplicate-resource.yaml" \
  "is declared more than once"
assert_parser_fails \
  "$fixture_dir/secret-resource.yaml" \
  "matches a protected secret-file pattern"
assert_parser_fails \
  "$fixture_dir/partial-fonts.yaml" \
  "unsupported flutter.fonts entry"

fake_path="$fixture_dir:$PATH"

if ! buildkit_output="$(
  PATH="$fake_path" \
    DOCKER_BUILDKIT=0 \
    make --no-print-directory -C "$repo_root" _check-buildkit 2>&1
)"; then
  fail "_check-buildkit rejected DOCKER_BUILDKIT=0 even though wrappers override it"
fi
assert_contains \
  "$buildkit_output" \
  "DOCKER_BUILDKIT=0 is overridden with 1" \
  "_check-buildkit did not explain its environment override"

if compose_error="$(
  PATH="$fake_path" \
    FAKE_DOCKER_COMPOSE_FAIL=1 \
    make --no-print-directory -C "$repo_root" _check-buildkit 2>&1
)"; then
  fail "_check-buildkit accepted a missing Docker Compose v2 plugin"
fi
assert_contains \
  "$compose_error" \
  "Docker Compose v2 is required" \
  "_check-buildkit did not report the missing Compose v2 plugin"

if buildx_error="$(
  PATH="$fake_path" \
    FAKE_DOCKER_BUILDX_FAIL=1 \
    make --no-print-directory -C "$repo_root" _check-buildkit 2>&1
)"; then
  fail "_check-buildkit accepted missing Docker Buildx"
fi
assert_contains \
  "$buildx_error" \
  "Docker Buildx/BuildKit is required" \
  "_check-buildkit did not report missing Buildx"

: >"$fake_log"
if smoke_error="$(
  PATH="$fake_path" \
    FAKE_WEB_DOCKER_LOG="$fake_log" \
    sh "$repo_root/docker/scripts/test-web-image.sh" fake-web:test 2>&1
)"; then
  fail "image smoke unexpectedly passed with an unhealthy fake container"
fi
assert_contains \
  "$smoke_error" \
  "fake nginx startup failure" \
  "image smoke did not print container logs on failure"
assert_contains \
  "$smoke_error" \
  '"ExitCode":42' \
  "image smoke did not print docker inspect output on failure"

logs_line="$(
  awk -F '	' '$1 == "logs" { print NR; exit }' "$fake_log"
)"
inspect_after_logs_line="$(
  awk -F '	' -v logs_line="$logs_line" \
    '$1 == "inspect" && NR > logs_line { print NR; exit }' \
    "$fake_log"
)"
final_remove_line="$(
  awk -F '	' '$1 == "rm" && $2 == "-f" { line = NR } END { print line + 0 }' \
    "$fake_log"
)"

[ -n "$logs_line" ] \
  || fail "image smoke did not call docker logs"
[ -n "$inspect_after_logs_line" ] \
  || fail "image smoke did not inspect the container after collecting logs"
[ "$logs_line" -lt "$inspect_after_logs_line" ] \
  || fail "image smoke inspected the container before collecting logs"
[ "$inspect_after_logs_line" -lt "$final_remove_line" ] \
  || fail "image smoke destroyed the container before collecting diagnostics"

printf 'Flutter Web parser, tooling guards and failure diagnostics verified\n'
