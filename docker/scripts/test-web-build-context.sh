#!/bin/sh
# Verify Dockerfile.web's BuildKit-only allowlist with synthetic canaries.
# All fixtures are dummy files created in a temporary repository-root context.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
repo_root="$(CDPATH= cd "$script_dir/../.." && pwd)"
temp_root="$(mktemp -d)"
context_dir="$temp_root/context"
output_dir="$temp_root/output"
declared_resources="$temp_root/declared-resources"
generated_allowlist="$temp_root/generated-resource-allowlist"
checked_in_allowlist="$temp_root/checked-in-resource-allowlist"
undeclared_asset="assets/heimdallm-undeclared-context-canary-$$.txt"
undeclared_flutter_resource="flutter_app/fonts/heimdallm-undeclared-context-canary-$$.ttf"

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

resource_context_path() {
  case "$1" in
    assets/*) printf '%s\n' "$1" ;;
    *) printf 'flutter_app/%s\n' "$1" ;;
  esac
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
docker buildx version >/dev/null 2>&1 \
  || fail "Docker Buildx/BuildKit is required for Dockerfile-specific ignore rules"

# pubspec.yaml is the source of truth. Require the checked-in allowlist to be
# an exact mirror so additions and removals both fail CI until synchronized.
sh "$script_dir/list-flutter-assets.sh" \
  "$repo_root/flutter_app/pubspec.yaml" >"$declared_resources"
sh "$script_dir/list-flutter-assets.sh" \
  --dockerignore \
  "$repo_root/flutter_app/pubspec.yaml" >"$generated_allowlist"

if ! awk '
  $0 == "# BEGIN flutter-resource-allowlist" {
    start_count++
    inside = 1
    next
  }
  $0 == "# END flutter-resource-allowlist" {
    end_count++
    inside = 0
    next
  }
  inside && $0 !~ /^[[:space:]]*(#|$)/ {
    print
  }
  END {
    if (start_count != 1 || end_count != 1 || inside) {
      exit 2
    }
  }
' "$repo_root/flutter_app/Dockerfile.web.dockerignore" \
  >"$checked_in_allowlist"; then
  fail "Dockerfile.web.dockerignore has an invalid resource allowlist block"
fi

if ! cmp -s "$generated_allowlist" "$checked_in_allowlist"; then
  printf '%s\n' \
    'Flutter resources and Dockerfile.web.dockerignore are out of sync:' >&2
  diff -u "$generated_allowlist" "$checked_in_allowlist" >&2 || true
  exit 1
fi

mkdir -p "$context_dir/flutter_app" "$output_dir"
cp "$repo_root/.dockerignore" "$context_dir/.dockerignore"
cp "$repo_root/flutter_app/Dockerfile.web.dockerignore" \
  "$context_dir/flutter_app/Dockerfile.web.dockerignore"
cp "$repo_root/flutter_app/pubspec.yaml" \
  "$context_dir/flutter_app/pubspec.yaml"
cp "$repo_root/flutter_app/pubspec.lock" \
  "$context_dir/flutter_app/pubspec.lock"

# Keep the synthetic Dockerfile body tiny, but place it at the same path and
# name as the production Dockerfile. This is what makes BuildKit discover the
# adjacent Dockerfile.web.dockerignore instead of falling back to .dockerignore.
printf '%s\n' 'FROM scratch' 'COPY . /context' \
  >"$context_dir/flutter_app/Dockerfile.web"

# Every declared resource must survive the allowlist. Undeclared canaries below
# prove this is exact inclusion rather than a broad assets/** or fonts/**
# exception.
while IFS= read -r resource; do
  write_fixture "$(resource_context_path "$resource")"
done <"$declared_resources"

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
  "$undeclared_flutter_resource" \
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
  --file "$context_dir/flutter_app/Dockerfile.web" \
  "$context_dir" >/dev/null

while IFS= read -r resource; do
  context_resource="$(resource_context_path "$resource")"
  if [ ! -f "$output_dir/context/$context_resource" ]; then
    fail "declared resource missing from context: $context_resource"
  fi
done <"$declared_resources"

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
  "$undeclared_flutter_resource" \
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

printf 'Flutter Web BuildKit context and resource allowlist verified\n'
