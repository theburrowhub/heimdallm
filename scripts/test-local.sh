#!/usr/bin/env bash
#
# Heimdallm Docker — Local Test Runner
#
# Usage:
#   ./scripts/test-local.sh smoke    # Level 1: no credentials needed
#   ./scripts/test-local.sh github   # Level 2: needs GITHUB_TOKEN in .env
#   ./scripts/test-local.sh e2e      # Level 3: needs full .env + AI auth + PR
#
set -euo pipefail

# ─── Config ───────────────────────────────────────────────────────────────────

COMPOSE_TEST="docker compose -f docker-compose.yml -f docker-compose.test.yml"
SERVICE="heimdallm"
BASE_URL="http://localhost:${HEIMDALLM_PORT:-7842}"
HEALTH_TIMEOUT=60   # seconds to wait for /health
HEALTH_INTERVAL=2   # seconds between retries

# ─── Colors ───────────────────────────────────────────────────────────────────

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# ─── Counters ─────────────────────────────────────────────────────────────────

PASS=0
FAIL=0
SKIP=0

# ─── Helpers ──────────────────────────────────────────────────────────────────

pass() { PASS=$((PASS + 1)); echo -e "  ${GREEN}PASS${NC}  $1"; }
fail() { FAIL=$((FAIL + 1)); echo -e "  ${RED}FAIL${NC}  $1${2:+ — $2}"; }
skip() { SKIP=$((SKIP + 1)); echo -e "  ${YELLOW}SKIP${NC}  $1${2:+ — $2}"; }
info() { echo -e "${CYAN}==>${NC} ${BOLD}$1${NC}"; }
warn() { echo -e "${YELLOW}WARNING:${NC} $1"; }

cleanup() {
    info "Cleaning up..."
    $COMPOSE_TEST down -v --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

wait_for_health() {
    local elapsed=0
    info "Waiting for /health (timeout ${HEALTH_TIMEOUT}s)..."
    while [ $elapsed -lt $HEALTH_TIMEOUT ]; do
        if curl -sf "${BASE_URL}/health" >/dev/null 2>&1; then
            return 0
        fi
        sleep $HEALTH_INTERVAL
        elapsed=$((elapsed + HEALTH_INTERVAL))
    done
    return 1
}

# check_http <description> <method> <path> <expected_status> [<body_check>]
# body_check: jq expression that must return "true"
check_http() {
    local desc="$1" method="$2" path="$3" expected="$4" body_check="${5:-}"
    local extra_headers="${6:-}"
    local url="${BASE_URL}${path}"
    local args=(-s -o /tmp/heimdallm_test_body -w "%{http_code}" -X "$method")

    if [ -n "$extra_headers" ]; then
        args+=(-H "$extra_headers")
    fi

    local status
    status=$(curl "${args[@]}" "$url" 2>/dev/null) || status="000"

    if [ "$status" != "$expected" ]; then
        fail "$desc" "expected HTTP $expected, got $status"
        return
    fi

    if [ -n "$body_check" ]; then
        local result
        result=$(jq -r "$body_check" /tmp/heimdallm_test_body 2>/dev/null) || result="false"
        if [ "$result" != "true" ]; then
            fail "$desc" "body check failed: $body_check"
            return
        fi
    fi

    pass "$desc"
}

# check_http_header <description> <method> <path> <header_name> <header_contains>
check_http_header() {
    local desc="$1" method="$2" path="$3" header="$4" contains="$5"
    local url="${BASE_URL}${path}"
    local headers
    headers=$(curl -sI -X "$method" "$url" 2>/dev/null) || headers=""

    if echo "$headers" | grep -qi "${header}:.*${contains}"; then
        pass "$desc"
    else
        fail "$desc" "header '$header' does not contain '$contains'"
    fi
}

# check_exec <description> <command inside container>
check_exec() {
    local desc="$1"
    shift
    if $COMPOSE_TEST exec -T "$SERVICE" "$@" >/dev/null 2>&1; then
        pass "$desc"
    else
        fail "$desc"
    fi
}

# check_logs <description> <grep pattern>
check_logs() {
    local desc="$1" pattern="$2"
    if $COMPOSE_TEST logs 2>&1 | grep -qiE "$pattern"; then
        pass "$desc"
    else
        fail "$desc" "pattern '$pattern' not found in logs"
    fi
}

summary() {
    echo ""
    echo -e "${BOLD}────────────────────────────────────────${NC}"
    echo -e "  ${GREEN}PASS: $PASS${NC}  ${RED}FAIL: $FAIL${NC}  ${YELLOW}SKIP: $SKIP${NC}"
    echo -e "${BOLD}────────────────────────────────────────${NC}"
    if [ $FAIL -gt 0 ]; then
        echo -e "  ${RED}${BOLD}RESULT: FAILED${NC}"
        return 1
    else
        echo -e "  ${GREEN}${BOLD}RESULT: PASSED${NC}"
        return 0
    fi
}

# ─── Level 1: Smoke Test ─────────────────────────────────────────────────────

test_smoke() {
    info "Level 1: Smoke Test (no credentials required)"
    echo ""

    # Export dummy credentials for the entire smoke test session.
    # These are used by docker-compose.yml variable substitution.
    export GITHUB_TOKEN=dummy
    export HEIMDALLM_AI_PRIMARY=gemini
    export HEIMDALLM_REPOSITORIES=test/repo

    # Build
    info "Building Docker image..."
    if $COMPOSE_TEST build --quiet 2>&1; then
        pass "Docker image builds"
    else
        fail "Docker image builds"
        echo -e "${RED}Cannot continue without a working image.${NC}"
        return 1
    fi

    # Start with dummy credentials
    info "Starting container with dummy credentials..."
    $COMPOSE_TEST up -d 2>&1

    if wait_for_health; then
        pass "Container starts and /health responds"
    else
        fail "Container starts and /health responds" "timeout after ${HEALTH_TIMEOUT}s"
        echo ""
        info "Container logs:"
        $COMPOSE_TEST logs --tail=30 2>&1
        return 1
    fi

    echo ""
    info "Checking HTTP endpoints..."

    # Health
    check_http "GET /health returns ok" \
        GET /health 200 '.status == "ok"'

    # Config
    check_http "GET /config returns valid JSON with ai_primary" \
        GET /config 200 '.ai_primary == "gemini"'

    # PRs
    check_http "GET /prs returns JSON array" \
        GET /prs 200 'type == "array"'

    # Stats
    check_http "GET /stats returns valid JSON" \
        GET /stats 200 'type == "object"'

    # Agents
    check_http "GET /agents returns JSON array" \
        GET /agents 200 'type == "array"'

    # SSE endpoint
    check_http_header "GET /events returns SSE content type" \
        GET /events "Content-Type" "text/event-stream"

    echo ""
    info "Checking authentication..."

    # Auth rejection (POST without token)
    check_http "POST /reload without token returns 401" \
        POST /reload 401

    # Read API token from container
    local api_token
    api_token=$($COMPOSE_TEST exec -T "$SERVICE" cat /data/api_token 2>/dev/null | tr -d '[:space:]') || api_token=""

    if [ ${#api_token} -ge 32 ]; then
        pass "API token exists and is >= 32 chars"
    else
        fail "API token exists and is >= 32 chars" "got ${#api_token} chars"
    fi

    # Auth acceptance (POST with valid token)
    if [ -n "$api_token" ]; then
        check_http "POST /reload with valid token returns 200" \
            POST /reload 200 '.status == "reloaded"' \
            "X-Heimdallm-Token: ${api_token}"
    else
        skip "POST /reload with valid token" "no API token available"
    fi

    echo ""
    info "Checking AI CLIs installed..."

    check_exec "claude CLI installed" claude --version
    check_exec "gemini CLI installed" gemini --version
    check_exec "codex CLI installed" codex --version
    check_exec "opencode CLI installed" opencode --version

    echo ""
    info "Checking daemon logs..."

    # Give the daemon a moment to attempt a poll
    sleep 3

    check_logs "Daemon started successfully" "daemon started"
    check_logs "Poll attempted (expected auth failure with dummy token)" "poll|fetch|github"

    echo ""
    summary
}

# ─── Level 2: GitHub Connection ──────────────────────────────────────────────

test_github() {
    info "Level 2: GitHub Connection Test (requires .env with GITHUB_TOKEN)"
    echo ""

    # Check .env exists
    if [ ! -f .env ]; then
        echo -e "${RED}ERROR: .env file not found.${NC}"
        echo "Create it from .env.example:"
        echo "  cp .env.example .env"
        echo "  # Edit .env — set GITHUB_TOKEN and HEIMDALLM_REPOSITORIES"
        return 1
    fi

    # Check required vars
    # shellcheck disable=SC1091
    source .env 2>/dev/null || true
    if [ -z "${GITHUB_TOKEN:-}" ] || [ "$GITHUB_TOKEN" = "ghp_your_token_here" ]; then
        echo -e "${RED}ERROR: GITHUB_TOKEN not set or still has placeholder value.${NC}"
        return 1
    fi
    if [ -z "${HEIMDALLM_AI_PRIMARY:-}" ]; then
        echo -e "${RED}ERROR: HEIMDALLM_AI_PRIMARY not set in .env.${NC}"
        return 1
    fi

    # Build
    info "Building Docker image..."
    if $COMPOSE_TEST build --quiet 2>&1; then
        pass "Docker image builds"
    else
        fail "Docker image builds"
        return 1
    fi

    # Start with real .env
    info "Starting container with real credentials..."
    $COMPOSE_TEST up -d 2>&1

    if wait_for_health; then
        pass "Container starts and /health responds"
    else
        fail "Container starts and /health responds"
        $COMPOSE_TEST logs --tail=30 2>&1
        return 1
    fi

    # Wait for first poll to complete
    info "Waiting for first poll cycle..."
    sleep 10

    echo ""
    info "Checking GitHub connection..."

    # /me should return a real login
    local me_body
    me_body=$(curl -sf "${BASE_URL}/me" 2>/dev/null) || me_body="{}"
    local login
    login=$(echo "$me_body" | jq -r '.login // empty' 2>/dev/null) || login=""

    if [ -n "$login" ]; then
        pass "GET /me returns GitHub user: $login"
    else
        fail "GET /me returns GitHub user" "empty or error"
    fi

    # Config should have repositories
    local config_body
    config_body=$(curl -sf "${BASE_URL}/config" 2>/dev/null) || config_body="{}"
    local repo_count
    repo_count=$(echo "$config_body" | jq '.repositories | length' 2>/dev/null) || repo_count=0

    if [ "$repo_count" -gt 0 ]; then
        pass "Config has $repo_count monitored repositories"
    else
        warn "No repositories configured — set HEIMDALLM_REPOSITORIES in .env"
        skip "Config has monitored repositories" "none configured"
    fi

    # Logs should show poll result
    check_logs "GitHub API poll executed" "PRs to review|poll.*fetch"

    # Check if any PRs were detected
    local prs_body
    prs_body=$(curl -sf "${BASE_URL}/prs" 2>/dev/null) || prs_body="[]"
    local pr_count
    pr_count=$(echo "$prs_body" | jq 'length' 2>/dev/null) || pr_count=0

    if [ "$pr_count" -gt 0 ]; then
        pass "Detected $pr_count PR(s) in the database"
        echo ""
        info "Detected PRs:"
        echo "$prs_body" | jq -r '.[] | "  #\(.number) \(.repo) — \(.title) (by \(.author))"' 2>/dev/null
    else
        skip "PRs detected" "none found — ensure you are a requested reviewer on a PR in a monitored repo"
    fi

    echo ""
    summary
}

# ─── Level 3: Full E2E ───────────────────────────────────────────────────────

test_e2e() {
    info "Level 3: Full End-to-End Test"
    echo ""
    echo -e "${BOLD}Prerequisites:${NC}"
    echo "  1. .env file with GITHUB_TOKEN, HEIMDALLM_AI_PRIMARY, HEIMDALLM_REPOSITORIES"
    echo "  2. AI CLI authentication:"
    echo "     - Gemini: GEMINI_API_KEY in .env, or mount ~/.gemini in docker-compose.test.yml"
    echo "     - Claude: ANTHROPIC_API_KEY or CLAUDE_CODE_OAUTH_TOKEN in .env"
    echo "     - Codex:  OPENAI_API_KEY in .env"
    echo "  3. An open PR where the GITHUB_TOKEN user is a requested reviewer"
    echo ""

    # Check .env
    if [ ! -f .env ]; then
        echo -e "${RED}ERROR: .env file not found. See .env.example${NC}"
        return 1
    fi

    # shellcheck disable=SC1091
    source .env 2>/dev/null || true

    if [ -z "${GITHUB_TOKEN:-}" ] || [ "$GITHUB_TOKEN" = "ghp_your_token_here" ]; then
        echo -e "${RED}ERROR: GITHUB_TOKEN not configured.${NC}"
        return 1
    fi

    # Check at least one AI key
    local has_ai_key=false
    [ -n "${ANTHROPIC_API_KEY:-}" ] && has_ai_key=true
    [ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] && has_ai_key=true
    [ -n "${GEMINI_API_KEY:-}" ] && has_ai_key=true
    [ -n "${OPENAI_API_KEY:-}" ] && has_ai_key=true
    [ -n "${CODEX_API_KEY:-}" ] && has_ai_key=true

    if [ "$has_ai_key" = false ]; then
        warn "No AI API keys found in .env."
        echo "  If using Gemini OAuth, uncomment the ~/.gemini volume in docker-compose.test.yml"
        echo ""
    fi

    # Build & start
    info "Building Docker image..."
    $COMPOSE_TEST build --quiet 2>&1

    info "Starting container..."
    $COMPOSE_TEST up -d 2>&1

    if ! wait_for_health; then
        fail "Container health check"
        $COMPOSE_TEST logs --tail=30 2>&1
        return 1
    fi
    pass "Container is healthy"

    local api_token
    api_token=$($COMPOSE_TEST exec -T "$SERVICE" cat /data/api_token 2>/dev/null | tr -d '[:space:]') || api_token=""

    echo ""
    echo -e "${BOLD}Container is running. Use the commands below to verify:${NC}"
    echo ""
    echo "  # Check authenticated user"
    echo "  curl -s ${BASE_URL}/me | jq"
    echo ""
    echo "  # List detected PRs"
    echo "  curl -s ${BASE_URL}/prs | jq"
    echo ""
    echo "  # View stats"
    echo "  curl -s ${BASE_URL}/stats | jq"
    echo ""
    echo "  # Follow SSE events (in another terminal)"
    echo "  curl -N ${BASE_URL}/events"
    echo ""
    echo "  # Trigger manual review (replace <id> with PR ID from /prs)"
    echo "  curl -X POST ${BASE_URL}/prs/<id>/review -H 'X-Heimdallm-Token: ${api_token}'"
    echo ""
    echo "  # Dismiss / undismiss a PR"
    echo "  curl -X POST ${BASE_URL}/prs/<id>/dismiss -H 'X-Heimdallm-Token: ${api_token}'"
    echo "  curl -X POST ${BASE_URL}/prs/<id>/undismiss -H 'X-Heimdallm-Token: ${api_token}'"
    echo ""
    echo "  # Reload config"
    echo "  curl -X POST ${BASE_URL}/reload -H 'X-Heimdallm-Token: ${api_token}'"
    echo ""
    echo -e "${BOLD}Follow logs:${NC}"
    echo "  $COMPOSE_TEST logs -f"
    echo ""
    echo -e "${BOLD}What to look for in logs:${NC}"
    echo "  1. 'daemon started'              — service is up"
    echo "  2. 'github: PRs to review'       — poll executed"
    echo "  3. 'pipeline: reviewing PR'      — review started"
    echo "  4. 'pipeline: using CLI'         — AI CLI selected"
    echo "  5. 'pipeline: review stored'     — saved to SQLite"
    echo "  6. 'pipeline: review published'  — posted to GitHub"
    echo ""
    echo -e "${YELLOW}Press Ctrl+C to stop and clean up.${NC}"

    # Follow logs until interrupted (cleanup trap handles shutdown)
    $COMPOSE_TEST logs -f 2>&1 || true
}

# ─── Main ─────────────────────────────────────────────────────────────────────

case "${1:-smoke}" in
    smoke)
        test_smoke
        ;;
    github)
        test_github
        ;;
    e2e)
        test_e2e
        ;;
    *)
        echo "Usage: $0 {smoke|github|e2e}"
        echo ""
        echo "  smoke   Level 1: Automated checks, no credentials needed"
        echo "  github  Level 2: GitHub API connection, needs GITHUB_TOKEN in .env"
        echo "  e2e     Level 3: Full review pipeline, needs .env + AI auth + PR"
        exit 1
        ;;
esac
