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

cleanup() {
  $COMPOSE down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

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
curl -sf "$BASE/" -o /tmp/index.html
grep -q "Heimdallm" /tmp/index.html || fail "/ did not return Heimdallm shell"

log "2/6 GET /main.dart.js"
curl -sfI "$BASE/main.dart.js" | grep -qi 'content-type: .*javascript' \
  || fail "main.dart.js not served with javascript content-type"

log "3/6 GET /healthz"
body="$(curl -sf "$BASE/healthz")"
[ "$body" = "ok" ] || fail "/healthz returned '$body' (expected 'ok')"

log "4/6 GET /api/health"
# Daemon /health is public but the request still goes through the token-injecting
# proxy — a 200 proves Nginx can reach the daemon over the compose network.
curl -sf "$BASE/api/health" -o /dev/null \
  || fail "/api/health failed (daemon unreachable through proxy)"

log "5/6 GET /api/events (SSE) — expect text/event-stream header within 2s"
tmp="$(mktemp)"
curl -sN -H 'Accept: text/event-stream' --max-time 2 "$BASE/api/events" -D "$tmp" -o /dev/null || true
grep -qi '^content-type: text/event-stream' "$tmp" \
  || fail "/api/events did not open with text/event-stream"
rm -f "$tmp"

log "6/6 GET /prs/123 — SPA fallback should return Flutter shell"
curl -sf "$BASE/prs/123" -o /tmp/deep.html
grep -q "Heimdallm" /tmp/deep.html || fail "deep-link /prs/123 did not fall back to index.html"

printf '✓  all checks passed\n'
