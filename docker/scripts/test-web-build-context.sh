#!/bin/sh
# Verify Dockerfile.web's BuildKit-only allowlist with synthetic canaries.
# All fixtures are dummy files created in a temporary build context.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
repo_root="$(CDPATH= cd "$script_dir/../.." && pwd)"
temp_root="$(mktemp -d)"
context_dir="$temp_root/context"
output_dir="$temp_root/output"
declared_assets="$temp_root/declared-assets"
allowlisted_assets="$temp_root/allowlisted-assets"
undeclared_asset="assets/heimdallm-undeclared-context-canary-$$.txt"

fail() {
  printf 'Flutter Web build-context check failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  if [ -n "${temp_root:-}" ] && [ -d "$temp_root" ]; then
    rm -rf "$temp_root"
  fi
}
trap cleanup EXIT

write_fixture() {
  fixture="$1"
  mkdir -p "$(dirname "$context_dir/$fixture")"
  printf '%s\n' 'dummy build-context fixture' >"$context_dir/$fixture"
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
docker buildx version >/dev/null 2>&1 \
  || fail "Docker Buildx/BuildKit is required for Dockerfile-specific ignore rules"

# pubspec.yaml is the source of truth. Require the checked-in allowlist to be
# an exact mirror so additions and removals both fail CI until synchronized.
sh "$script_dir/list-flutter-assets.sh" \
  "$repo_root/flutter_app/pubspec.yaml" >"$temp_root/declared-assets-raw"
LC_ALL=C sort -u "$temp_root/declared-assets-raw" >"$declared_assets"

sed -n '/^!assets\/./ { s/^!//; p; }' \
  "$repo_root/flutter_app/Dockerfile.web.dockerignore" \
  >"$temp_root/allowlisted-assets-raw"
LC_ALL=C sort -u "$temp_root/allowlisted-assets-raw" >"$allowlisted_assets"

if ! cmp -s "$declared_assets" "$allowlisted_assets"; then
  printf '%s\n' \
    'flutter.assets and Dockerfile.web.dockerignore are out of sync:' >&2
  diff -u "$declared_assets" "$allowlisted_assets" >&2 || true
  exit 1
fi

mkdir -p "$context_dir/flutter_app" "$output_dir"
cp "$repo_root/flutter_app/Dockerfile.web.dockerignore" \
  "$context_dir/Dockerfile.web.dockerignore"
cp "$repo_root/flutter_app/pubspec.yaml" \
  "$context_dir/flutter_app/pubspec.yaml"
cp "$repo_root/flutter_app/pubspec.lock" \
  "$context_dir/flutter_app/pubspec.lock"
printf '%s\n' 'FROM scratch' 'COPY . /context' \
  >"$context_dir/Dockerfile.web"

# Every declared asset must survive the allowlist. The undeclared canary below
# proves this is exact inclusion rather than a broad assets/** exception.
while IFS= read -r asset; do
  write_fixture "$asset"
done <"$declared_assets"

for allowed in \
  flutter_app/.metadata \
  flutter_app/lib/allowed.txt \
  flutter_app/web/nested/allowed.txt \
  flutter_app/nginx.conf.template \
  flutter_app/docker-entrypoint.d/nested/allowed.sh
do
  write_fixture "$allowed"
done
ln -s ../assets "$context_dir/flutter_app/assets"

# These names contain no credentials; they only exercise exclusion rules.
for excluded in \
  "$undeclared_asset" \
  flutter_app/build/blocked.txt \
  flutter_app/macos/blocked.txt \
  flutter_app/test/blocked.txt \
  flutter_app/lib/.env \
  flutter_app/lib/.env.local \
  flutter_app/lib/.env.example \
  flutter_app/lib/nested/dummy.pem \
  flutter_app/lib/nested/dummy.key \
  flutter_app/web/nested/dummy.crt \
  flutter_app/web/nested/dummy.p12 \
  flutter_app/docker-entrypoint.d/nested/dummy.pfx
do
  write_fixture "$excluded"
done

docker buildx build \
  --quiet \
  --output "type=local,dest=$output_dir" \
  --file "$context_dir/Dockerfile.web" \
  "$context_dir" >/dev/null

while IFS= read -r asset; do
  if [ ! -f "$output_dir/context/$asset" ]; then
    fail "declared asset missing from context: $asset"
  fi
done <"$declared_assets"

for allowed in \
  flutter_app/.metadata \
  flutter_app/pubspec.yaml \
  flutter_app/pubspec.lock \
  flutter_app/lib/allowed.txt \
  flutter_app/web/nested/allowed.txt \
  flutter_app/assets \
  flutter_app/nginx.conf.template \
  flutter_app/docker-entrypoint.d/nested/allowed.sh
do
  if [ ! -e "$output_dir/context/$allowed" ]; then
    fail "allowed fixture missing from context: $allowed"
  fi
done

for excluded in \
  "$undeclared_asset" \
  flutter_app/build/blocked.txt \
  flutter_app/macos/blocked.txt \
  flutter_app/test/blocked.txt \
  flutter_app/lib/.env \
  flutter_app/lib/.env.local \
  flutter_app/lib/.env.example \
  flutter_app/lib/nested/dummy.pem \
  flutter_app/lib/nested/dummy.key \
  flutter_app/web/nested/dummy.crt \
  flutter_app/web/nested/dummy.p12 \
  flutter_app/docker-entrypoint.d/nested/dummy.pfx
do
  if [ -e "$output_dir/context/$excluded" ]; then
    fail "excluded fixture was included: $excluded"
  fi
done

printf 'Flutter Web BuildKit context and asset allowlist verified\n'
