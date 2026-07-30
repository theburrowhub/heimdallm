#!/bin/sh
# Smoke-test the built Flutter Web image, including its real Nginx entrypoint.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
repo_root="$(CDPATH= cd "$script_dir/../.." && pwd)"
image="${1:-heimdallm-web:test}"
container_name="heimdallm-web-smoke-$$"

fail() {
  printf 'Flutter Web image smoke failed: %s\n' "$1" >&2
  exit 1
}

cleanup() {
  exit_code=$?
  trap - EXIT

  if [ "$exit_code" -ne 0 ] &&
     docker inspect "$container_name" >/dev/null 2>&1; then
    printf '%s\n' \
      "Flutter Web smoke container logs (${container_name}):" >&2
    docker logs "$container_name" >&2 || true
    printf '%s\n' \
      "Flutter Web smoke container inspect (${container_name}):" >&2
    docker inspect "$container_name" >&2 || true
  fi

  docker rm -f "$container_name" >/dev/null 2>&1 || true
  exit "$exit_code"
}
trap cleanup EXIT

command -v docker >/dev/null 2>&1 || fail "Docker is required"
docker image inspect "$image" >/dev/null 2>&1 \
  || fail "image '$image' does not exist locally"

flutter_resources="$(
  sh "$script_dir/list-flutter-assets.sh" \
    --dockerignore-file \
    "$repo_root/flutter_app/Dockerfile.web.dockerignore" \
    "$repo_root/flutter_app/pubspec.yaml"
)"

# Validate immutable image contents. Resource paths come directly from pubspec,
# so adding or removing an asset, font or shader cannot leave this list stale.
docker run --rm \
  --entrypoint sh \
  --env "FLUTTER_RESOURCES=$flutter_resources" \
  "$image" -ec '
    web_root=/usr/share/nginx/html
    test -s "$web_root/index.html"
    test -s "$web_root/main.dart.js"
    test -s /etc/nginx/heimdallm.conf.template
    test -x /docker-entrypoint.d/10-heimdallm-token.sh

    printf "%s\n" "$FLUTTER_RESOURCES" |
      while IFS= read -r resource; do
        test -n "$resource"
        test -s "$web_root/assets/$resource"
      done
  ' || fail "required bundle, resource, template, or executable is missing"

# Start the image with its stock entrypoint/CMD. This catches failures that
# static file checks miss, including template rendering and Nginx startup.
docker rm -f "$container_name" >/dev/null 2>&1 || true
docker run -d \
  --name "$container_name" \
  --env HEIMDALLM_API_TOKEN=smoke-token \
  --env DAEMON_URL=http://127.0.0.1:9 \
  "$image" >/dev/null \
  || fail "container did not start"

attempt=0
health_body=""
while [ "$attempt" -lt 30 ]; do
  if health_body="$(
    docker exec "$container_name" \
      wget -qO- http://127.0.0.1:3000/healthz 2>/dev/null
  )"; then
    break
  fi
  attempt=$((attempt + 1))
  sleep 1
done

[ "$health_body" = "ok" ] || fail "Nginx did not become healthy"

docker exec "$container_name" sh -ec '
  test -s /etc/nginx/conf.d/default.conf
  grep -Fq "proxy_pass http://127.0.0.1:9/;" \
    /etc/nginx/conf.d/default.conf
  grep -Fq "proxy_set_header X-Heimdallm-Token \"smoke-token\";" \
    /etc/nginx/conf.d/default.conf
  wget -qO- http://127.0.0.1:3000/ |
    grep -q Heimdallm
' || fail "entrypoint output or served Flutter shell is invalid"

printf 'Flutter Web image contents and runtime verified\n'
