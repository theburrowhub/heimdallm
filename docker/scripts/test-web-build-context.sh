#!/bin/sh
# Verify Dockerfile.web's BuildKit-only allowlist with synthetic canaries.
# All fixtures are dummy files created in a temporary repository-root context.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
repo_root="$(CDPATH= cd "$script_dir/../.." && pwd)"
temp_root="$(mktemp -d)"
context_dir="$temp_root/context"
output_dir="$temp_root/output"
public_context_dir="$temp_root/public-context"
public_output_dir="$temp_root/public-output"
declared_context_resources="$temp_root/declared-context-resources"
generated_allowlist="$temp_root/generated-resource-allowlist"
checked_in_allowlist="$temp_root/checked-in-resource-allowlist"
public_resource_rules_file="$temp_root/public-resource-rules"
production_policy_skeleton="$temp_root/production-policy-skeleton"
public_policy_skeleton="$temp_root/public-policy-skeleton"
pubspec="$repo_root/flutter_app/pubspec.yaml"
resource_policy="$repo_root/flutter_app/Dockerfile.web.dockerignore"
undeclared_asset="assets/heimdallm-undeclared-context-canary-$$.txt"
undeclared_flutter_resource="flutter_app/fonts/heimdallm-undeclared-context-canary-$$.ttf"
public_certificate="assets/certs/heimdallm-public-ca.crt"
blocked_certificate="assets/certs/heimdallm-unlisted-ca.crt"

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

write_policy_variant() {
  source_policy="$1"
  destination_policy="$2"
  resource_rules_file="$3"
  public_exception_rule="$4"

  awk \
    -v resource_rules_file="$resource_rules_file" \
    -v public_exception_rule="$public_exception_rule" '
function invalid(message) {
  print "invalid production Docker ignore policy: " message > "/dev/stderr"
  failed = 1
  exit 1
}

function emit_resource_rules(line) {
  while ((getline line < resource_rules_file) > 0) {
    print line
  }
  close(resource_rules_file)
}

$0 == "# BEGIN flutter-resource-allowlist" {
  if (inside || ++resource_begin_count != 1) {
    invalid("duplicate or nested Flutter resource begin marker")
  }
  print
  emit_resource_rules()
  inside = "resource"
  next
}

$0 == "# END flutter-resource-allowlist" {
  if (inside != "resource" || ++resource_end_count != 1) {
    invalid("Flutter resource end marker has no matching begin marker")
  }
  inside = ""
  print
  next
}

$0 == "# BEGIN flutter-public-secret-like-resource-allowlist" {
  if (inside || ++public_begin_count != 1) {
    invalid("duplicate or nested public-resource begin marker")
  }
  print
  print public_exception_rule
  inside = "public"
  next
}

$0 == "# END flutter-public-secret-like-resource-allowlist" {
  if (inside != "public" || ++public_end_count != 1) {
    invalid("public-resource end marker has no matching begin marker")
  }
  inside = ""
  print
  next
}

inside {
  next
}

{
  print
}

END {
  if (failed) {
    exit 1
  }
  if (inside ||
      resource_begin_count != 1 || resource_end_count != 1 ||
      public_begin_count != 1 || public_end_count != 1) {
    print "invalid production Docker ignore policy: expected one matched " \
      "pair for each generated block" > "/dev/stderr"
    exit 1
  }
}
' "$source_policy" >"$destination_policy"
}

write_policy_skeleton() {
  awk '
$0 == "# BEGIN flutter-resource-allowlist" ||
$0 == "# BEGIN flutter-public-secret-like-resource-allowlist" {
  print
  inside = 1
  next
}
$0 == "# END flutter-resource-allowlist" ||
$0 == "# END flutter-public-secret-like-resource-allowlist" {
  inside = 0
  print
  next
}
!inside {
  print
}
' "$1" >"$2"
}

command -v docker >/dev/null 2>&1 || fail "Docker is required"
docker buildx version >/dev/null 2>&1 \
  || fail "Docker Buildx/BuildKit is required for Dockerfile-specific ignore rules"

# pubspec.yaml is the source of truth. Require the checked-in allowlist to be
# an exact mirror so additions and removals both fail CI until synchronized.
sh "$script_dir/list-flutter-assets.sh" \
  --context-paths \
  --dockerignore-file "$resource_policy" \
  "$pubspec" >"$declared_context_resources"
sh "$script_dir/list-flutter-assets.sh" \
  --dockerignore \
  --dockerignore-file "$resource_policy" \
  "$pubspec" >"$generated_allowlist"

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
' "$resource_policy" \
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
cp "$resource_policy" \
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
  write_fixture "$resource"
done <"$declared_context_resources"

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
  if [ ! -f "$output_dir/context/$resource" ]; then
    fail "declared resource missing from context: $resource"
  fi
done <"$declared_context_resources"

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

# Exercise the public secret-like exception end to end. This second context
# deliberately places an exact public .crt exception after the broad .crt deny;
# a neighbouring certificate without an exception must remain excluded.
mkdir -p \
  "$public_context_dir/flutter_app" \
  "$public_context_dir/assets/certs" \
  "$public_output_dir"
cp "$repo_root/.dockerignore" "$public_context_dir/.dockerignore"

public_pubspec="$public_context_dir/flutter_app/pubspec.yaml"
public_policy="$public_context_dir/flutter_app/Dockerfile.web.dockerignore"
printf '%s\n' \
  'name: public_secret_like_context_fixture' \
  '' \
  'flutter:' \
  '  assets:' \
  "    - $public_certificate" >"$public_pubspec"

# Derive the synthetic policy from production and replace only generated block
# contents. This keeps every base allow/deny rule and their ordering coupled to
# Dockerfile.web.dockerignore instead of maintaining a second policy by hand.
write_policy_variant \
  "$resource_policy" \
  "$public_policy" \
  "$checked_in_allowlist" \
  "!$public_certificate"

public_resource_rules="$(
  sh "$script_dir/list-flutter-assets.sh" \
    --dockerignore \
    --dockerignore-file "$public_policy" \
    "$public_pubspec"
)"
public_context_paths="$(
  sh "$script_dir/list-flutter-assets.sh" \
    --context-paths \
    --dockerignore-file "$public_policy" \
    "$public_pubspec"
)"
[ "$public_context_paths" = "$public_certificate" ] \
  || fail "public certificate mapped to an unexpected context path"

# Give the neighbouring certificate a normal resource exception as a canary:
# the production-derived broad .crt deny must still remove it, while the exact
# public block below re-includes only the audited certificate.
printf '%s\n' \
  "$public_resource_rules" \
  "!$blocked_certificate" >"$public_resource_rules_file"
write_policy_variant \
  "$resource_policy" \
  "$public_policy" \
  "$public_resource_rules_file" \
  "!$public_certificate"

write_policy_skeleton "$resource_policy" "$production_policy_skeleton"
write_policy_skeleton "$public_policy" "$public_policy_skeleton"
cmp -s "$production_policy_skeleton" "$public_policy_skeleton" \
  || fail "synthetic policy changed production rules outside generated blocks"

printf '%s\n' 'FROM scratch' 'COPY . /context' \
  >"$public_context_dir/flutter_app/Dockerfile.web"
printf '%s\n' 'public certificate fixture' \
  >"$public_context_dir/$public_certificate"
printf '%s\n' 'blocked certificate fixture' \
  >"$public_context_dir/$blocked_certificate"

docker buildx build \
  --quiet \
  --output "type=local,dest=$public_output_dir" \
  --file "$public_context_dir/flutter_app/Dockerfile.web" \
  "$public_context_dir" >/dev/null

[ -f "$public_output_dir/context/$public_certificate" ] \
  || fail "exact public secret-like exception did not survive the broad deny"
[ ! -e "$public_output_dir/context/$blocked_certificate" ] \
  || fail "unlisted secret-like certificate was included in the context"

printf '%s\n' \
  'Flutter Web BuildKit context, resource allowlist and public exceptions verified'
