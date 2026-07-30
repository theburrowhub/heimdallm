#!/bin/sh
# Integration smoke test for the Nginx-based web UI.
#
# Brings up daemon + web under the test compose file, waits for both
# healthchecks, then curls the six canary routes to confirm behavior:
#   1. GET /                     — Flutter shell (index.html)
#   2. GET /main.dart.js         — compiled bundle
#   3. GET /healthz              — Nginx static healthcheck
#   4. GET /api/health           — daemon reached through Nginx + token
#   5. GET /api/events (SSE)     — stream opens with text/event-stream
#   6. GET /prs/123              — SPA deep-link fallback (returns HTML)
#
# Assumptions: docker compose is installed; HEIMDALLM_PORT + HEIMDALLM_WEB_PORT
# default to 7842 / 3000. Override via env.
set -eu

HEIMDALLM_WEB_PORT="${HEIMDALLM_WEB_PORT:-3000}"
BASE="http://localhost:${HEIMDALLM_WEB_PORT}"
COMPOSE="docker compose -f docker/docker-compose.yml -f docker/docker-compose.test.yml"

log() { printf '▶  %s\n' "$1"; }
fail() { printf '✗  %s\n' "$1" >&2; exit 1; }

export DOCKER_BUILDKIT=1

cleanup() {
  $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
  rm -f "${INDEX_HTML:-}" "${DEEP_HTML:-}" "${SSE_HDR:-}"
}
trap cleanup EXIT

# Temp files for response bodies/headers. mktemp is parallel-safe on CI.
INDEX_HTML="$(mktemp)"
DEEP_HTML="$(mktemp)"
SSE_HDR="$(mktemp)"

log "docker compose up -d --build"
$COMPOSE up -d --build

log "waiting for heimdallm-web healthcheck"
web_container="$($COMPOSE ps -q web)"
for i in $(seq 1 60); do
  status="$(docker inspect -f '{{.State.Health.Status}}' "$web_container" 2>/dev/null || echo starting)"
  [ "$status" = "healthy" ] && break
  [ "$i" = "60" ] && fail "heimdallm-web did not become healthy in 60s (last status: $status)"
  sleep 1
done

log "1/6 GET /"
curl -sf "$BASE/" -o "$INDEX_HTML"
grep -q "Heimdallm" "$INDEX_HTML" || fail "/ did not return Heimdallm shell"

log "2/6 GET /main.dart.js"
# Use a GET + header-dump instead of curl -I (HEAD). Some Nginx configs
# don't serve identical Content-Type for HEAD vs GET; GET avoids the
# false negative and also verifies the bundle actually delivers content.
curl -sf "$BASE/main.dart.js" -D - -o /dev/null | grep -qi 'content-type: .*javascript' \
  || fail "main.dart.js not served with javascript content-type"

log "3/6 GET /healthz"
body="$(curl -sf "$BASE/healthz")"
[ "$body" = "ok" ] || fail "/healthz returned '$body' (expected 'ok')"

log "4/6 GET /api/health"
# Assert the response is valid JSON, not the SPA fallback (Nginx would
# return index.html if /api/ proxy weren't wired).
api_health="$(curl -sf "$BASE/api/health")"
echo "$api_health" | grep -q '"status"' \
  || fail "/api/health did not return JSON (got: ${api_health:-<empty>})"

log "5/6 GET /api/events (SSE) — expect text/event-stream header within 2s"
curl -sN -H 'Accept: text/event-stream' --max-time 2 "$BASE/api/events" -D "$SSE_HDR" -o /dev/null || true
grep -qi '^content-type: text/event-stream' "$SSE_HDR" \
  || fail "/api/events did not open with text/event-stream"

log "6/6 GET /prs/123 — SPA fallback should return Flutter shell"
curl -sf "$BASE/prs/123" -o "$DEEP_HTML"
grep -q "Heimdallm" "$DEEP_HTML" || fail "deep-link /prs/123 did not fall back to index.html"

printf '✓  all checks passed\n'
