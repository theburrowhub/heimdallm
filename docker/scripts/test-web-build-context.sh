#!/bin/sh
# Verify that Dockerfile.web's allow-listed context cannot re-include secrets.
# All fixtures are dummy files created in a temporary build context.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
repo_root="$(CDPATH= cd "$script_dir/../.." && pwd)"
temp_root="$(mktemp -d)"
context_dir="$temp_root/context"
output_dir="$temp_root/output"

cleanup() {
  if [ -n "${temp_root:-}" ] && [ -d "$temp_root" ]; then
    rm -rf "$temp_root"
  fi
}
trap cleanup EXIT

mkdir -p \
  "$context_dir/assets" \
  "$context_dir/flutter_app/lib/nested" \
  "$context_dir/flutter_app/web/nested" \
  "$context_dir/flutter_app/docker-entrypoint.d/nested" \
  "$output_dir"

cp "$repo_root/flutter_app/Dockerfile.web.dockerignore" \
  "$context_dir/Dockerfile.web.dockerignore"
printf '%s\n' 'FROM scratch' 'COPY . /context' \
  >"$context_dir/Dockerfile.web"

# Allowed canaries prove that the temporary context exercises re-included
# source paths rather than passing because the whole directory stayed ignored.
printf '%s\n' 'dummy public asset' >"$context_dir/assets/icon.png"
printf '%s\n' 'dummy source' >"$context_dir/flutter_app/lib/allowed.txt"
printf '%s\n' 'dummy example config' \
  >"$context_dir/flutter_app/lib/.env.example"

# These names contain no credentials; they only exercise the exclusion rules.
for fixture in \
  flutter_app/lib/.env \
  flutter_app/lib/.env.local \
  flutter_app/lib/nested/dummy.pem \
  flutter_app/lib/nested/dummy.key \
  flutter_app/web/nested/dummy.crt \
  flutter_app/web/nested/dummy.p12 \
  flutter_app/docker-entrypoint.d/nested/dummy.pfx
do
  printf '%s\n' 'dummy excluded fixture' >"$context_dir/$fixture"
done

docker buildx build \
  --quiet \
  --output "type=local,dest=$output_dir" \
  --file "$context_dir/Dockerfile.web" \
  "$context_dir" >/dev/null

for allowed in \
  assets/icon.png \
  flutter_app/lib/allowed.txt \
  flutter_app/lib/.env.example
do
  if [ ! -f "$output_dir/context/$allowed" ]; then
    printf 'missing allowed build-context fixture: %s\n' "$allowed" >&2
    exit 1
  fi
done

for excluded in \
  flutter_app/lib/.env \
  flutter_app/lib/.env.local \
  flutter_app/lib/nested/dummy.pem \
  flutter_app/lib/nested/dummy.key \
  flutter_app/web/nested/dummy.crt \
  flutter_app/web/nested/dummy.p12 \
  flutter_app/docker-entrypoint.d/nested/dummy.pfx
do
  if [ -e "$output_dir/context/$excluded" ]; then
    printf 'sensitive build-context fixture was included: %s\n' "$excluded" >&2
    exit 1
  fi
done

printf 'Flutter Web build-context exclusions verified\n'
