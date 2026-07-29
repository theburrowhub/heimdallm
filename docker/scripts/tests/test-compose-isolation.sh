#!/bin/sh
set -eu

TEST_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SCRIPT_DIR=$(CDPATH= cd -- "$TEST_DIR/.." && pwd)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)
COMPOSE_LIB="$SCRIPT_DIR/lib/compose-test.sh"
ORIGINAL_PATH=$PATH
REAL_DOCKER=$(command -v docker || true)

TEST_TMP=$(mktemp -d "${TMPDIR:-/tmp}/heimdallm-compose-isolation-tests.XXXXXX")
FAKE_BIN="$TEST_TMP/bin"
FAKE_DOCKER_LOG="$TEST_TMP/docker.log"
mkdir "$FAKE_BIN"
ln -s "$TEST_DIR/fixtures/docker" "$FAKE_BIN/docker"
ln -s "$TEST_DIR/fixtures/curl" "$FAKE_BIN/curl"
ln -s "$TEST_DIR/fixtures/sleep" "$FAKE_BIN/sleep"
export FAKE_DOCKER_LOG

cleanup_test_files() {
    rm -f "$FAKE_BIN/curl" \
        "$FAKE_BIN/docker" \
        "$FAKE_BIN/sleep" \
        "$FAKE_DOCKER_LOG" \
        "$TEST_TMP/parallel-a" \
        "$TEST_TMP/parallel-b" \
        "$TEST_TMP/config.json" \
        "$TEST_TMP/production-config.json" \
        "$TEST_TMP/runner-cleanup-failure.out"
    rmdir "$FAKE_BIN" 2>/dev/null || true
    rmdir "$TEST_TMP" 2>/dev/null || true
}
trap cleanup_test_files EXIT

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

assert_log_has_safe_down() {
    _expected_project=$1
    _matching_line=$(awk -F '\t' '$0 ~ /\tdown\t-v\t--remove-orphans$/ { print }' "$FAKE_DOCKER_LOG")
    [ -n "$_matching_line" ] || fail "fake Docker did not receive guarded down -v --remove-orphans"
    printf '%s\n' "$_matching_line" | grep -F "	--project-name	${_expected_project}	" >/dev/null ||
        fail "destructive command did not carry the exact isolated project name"
}

# shellcheck disable=SC1090
. "$COMPOSE_LIB"

PATH="$FAKE_BIN:$ORIGINAL_PATH"
export PATH

printf '1/8 unique project identity and destructive wrapper arguments\n'
: >"$FAKE_DOCKER_LOG"
compose_test_init local "$REPO_ROOT"
first_project=$COMPOSE_TEST_PROJECT_NAME
case "$first_project" in
    heimdallm-test-local-*) ;;
    *) fail "unexpected project name: $first_project" ;;
esac
[ "$HEIMDALLM_TEST_DAEMON_CONTAINER_NAME" = "${first_project}-daemon" ] ||
    fail "daemon container name is not run-scoped"
[ "$HEIMDALLM_TEST_WEB_CONTAINER_NAME" = "${first_project}-web" ] ||
    fail "web container name is not run-scoped"
compose_test_down
assert_log_has_safe_down "$first_project"
compose_test_release

printf '2/8 guard fails closed when the project identity is changed\n'
: >"$FAKE_DOCKER_LOG"
compose_test_init local "$REPO_ROOT"
safe_project=$COMPOSE_TEST_PROJECT_NAME
COMPOSE_TEST_PROJECT_NAME=heimdallm
if compose_test_down 2>/dev/null; then
    fail "cleanup accepted a non-test project"
fi
[ ! -s "$FAKE_DOCKER_LOG" ] || fail "Docker ran after the project guard failed"
COMPOSE_TEST_PROJECT_NAME=$safe_project
printf '%s\n%s\n' "heimdallm-test-forged" "$$" >"$COMPOSE_TEST_MARKER"
if compose_test_down 2>/dev/null; then
    fail "cleanup accepted a forged marker"
fi
[ ! -s "$FAKE_DOCKER_LOG" ] || fail "Docker ran after the marker guard failed"
printf '%s\n%s\n' "$safe_project" "$$" >"$COMPOSE_TEST_MARKER"
compose_test_release

printf '3/8 failed cleanup is non-successful and preserves recovery identity\n'
: >"$FAKE_DOCKER_LOG"
compose_test_init cleanup "$REPO_ROOT"
failed_project=$COMPOSE_TEST_PROJECT_NAME
failed_marker=$COMPOSE_TEST_MARKER
failed_state_dir=$COMPOSE_TEST_STATE_DIR
FAKE_DOCKER_FAIL_DOWN=1
export FAKE_DOCKER_FAIL_DOWN
if compose_test_cleanup 2>/dev/null; then
    fail "simulated cleanup failure was reported as successful"
fi
[ "$COMPOSE_TEST_INITIALIZED" = "1" ] ||
    fail "failed cleanup released the run identity"
[ -f "$failed_marker" ] ||
    fail "failed cleanup deleted the recovery marker"
[ -s "$failed_state_dir/cleanup.log" ] ||
    fail "failed cleanup did not preserve diagnostics"
[ "$COMPOSE_TEST_PROJECT_NAME" = "$failed_project" ] ||
    fail "failed cleanup changed the recovery project"
unset FAKE_DOCKER_FAIL_DOWN
compose_test_cleanup
[ ! -e "$failed_marker" ] ||
    fail "successful retry left the recovery marker behind"
[ ! -d "$failed_state_dir" ] ||
    fail "successful retry left the private state directory behind"

runner_failure_tmp="$TEST_TMP/runner-failure"
mkdir "$runner_failure_tmp"
if FAKE_DOCKER_FAIL_DOWN=1 TMPDIR="$runner_failure_tmp" \
    "$SCRIPT_DIR/test-local.sh" smoke >"$TEST_TMP/runner-cleanup-failure.out" 2>&1; then
    fail "local runner reported success after Compose cleanup failed"
fi
grep -F "cleanup failed; isolated Docker resources may remain" \
    "$TEST_TMP/runner-cleanup-failure.out" >/dev/null ||
    fail "local runner did not report failed cleanup"
grep -F "marker preserved:" "$TEST_TMP/runner-cleanup-failure.out" >/dev/null ||
    fail "local runner did not report its recovery marker"
set -- "$runner_failure_tmp"/heimdallm-compose-test-local.*
[ "$#" -eq 1 ] && [ -d "$1" ] ||
    fail "local runner did not preserve exactly one failed-run directory"
runner_failure_state=$1
[ -f "$runner_failure_state/project" ] &&
    [ -s "$runner_failure_state/cleanup.log" ] ||
    fail "local runner removed failed-cleanup recovery files"
rm -f "$runner_failure_state/http-body" \
    "$runner_failure_state/cleanup.log" \
    "$runner_failure_state/project"
rmdir "$runner_failure_state"
rmdir "$runner_failure_tmp"

printf '4/8 concurrent initializations never share project, container, or body-file names\n'
run_parallel_init() (
    # shellcheck disable=SC1090
    . "$COMPOSE_LIB"
    compose_test_init web "$REPO_ROOT"
    printf '%s|%s|%s|%s\n' \
        "$COMPOSE_TEST_PROJECT_NAME" \
        "$HEIMDALLM_TEST_DAEMON_CONTAINER_NAME" \
        "$HEIMDALLM_TEST_WEB_CONTAINER_NAME" \
        "$(compose_test_run_path http-body)" >"$1"
    compose_test_release
)
run_parallel_init "$TEST_TMP/parallel-a" &
parallel_a_pid=$!
run_parallel_init "$TEST_TMP/parallel-b" &
parallel_b_pid=$!
wait "$parallel_a_pid"
wait "$parallel_b_pid"
parallel_a=$(sed -n '1p' "$TEST_TMP/parallel-a")
parallel_b=$(sed -n '1p' "$TEST_TMP/parallel-b")
[ "$parallel_a" != "$parallel_b" ] || fail "parallel runs received identical identities"
parallel_a_body=${parallel_a##*|}
parallel_b_body=${parallel_b##*|}
[ "$parallel_a_body" != "$parallel_b_body" ] ||
    fail "parallel runs shared the HTTP response body file"

printf '5/8 signal-driven exit cleans only its own isolated project\n'
: >"$FAKE_DOCKER_LOG"
if sh -c '
    set -eu
    . "$1"
    compose_test_init signal "$2"
    cleanup() {
        status=$?
        trap - EXIT
        if ! compose_test_cleanup; then
            [ "$status" -ne 0 ] || status=1
        fi
        exit "$status"
    }
    trap cleanup EXIT
    trap "exit 143" TERM
    kill -TERM "$$"
' signal-cleanup "$COMPOSE_LIB" "$REPO_ROOT"; then
    fail "signal harness unexpectedly exited successfully"
fi
signal_project=$(awk -F '\t' '$0 ~ /\tdown\t-v\t--remove-orphans$/ {
    for (i = 1; i <= NF; i++) if ($i == "--project-name") print $(i + 1)
}' "$FAKE_DOCKER_LOG")
case "$signal_project" in
    heimdallm-test-signal-*) ;;
    *) fail "signal cleanup used an unsafe project: $signal_project" ;;
esac
assert_log_has_safe_down "$signal_project"

printf '6/8 local runner scopes build/start to the daemon and cleans its project\n'
: >"$FAKE_DOCKER_LOG"
if ! "$SCRIPT_DIR/test-local.sh" smoke >/dev/null 2>&1; then
    fail "fake local smoke did not complete successfully"
fi
local_project=$(awk -F '\t' '$0 ~ /\tdown\t-v\t--remove-orphans$/ {
    for (i = 1; i <= NF; i++) if ($i == "--project-name") print $(i + 1)
}' "$FAKE_DOCKER_LOG")
case "$local_project" in
    heimdallm-test-local-*) ;;
    *) fail "local runner used an unsafe cleanup project: $local_project" ;;
esac
awk -F '\t' '
    $0 ~ /\tbuild\t/ {
        builds++
        if ($NF != "heimdallm") bad = 1
        for (i = 1; i <= NF; i++) if ($i == "web") bad = 1
    }
    $0 ~ /\tup\t/ {
        starts++
        if ($NF != "heimdallm") bad = 1
        for (i = 1; i <= NF; i++) if ($i == "web") bad = 1
    }
    END { exit !(builds == 1 && starts == 1 && bad != 1) }
' "$FAKE_DOCKER_LOG" ||
    fail "local runner built or started services beyond heimdallm"
assert_log_has_safe_down "$local_project"

printf '7/8 web runner uses one isolated project for startup and cleanup\n'
: >"$FAKE_DOCKER_LOG"
if "$SCRIPT_DIR/test-web.sh" >/dev/null 2>&1; then
    fail "fake web smoke unexpectedly reached a live HTTP endpoint"
fi
web_up_project=$(awk -F '\t' '$0 ~ /\tup\t-d\t--build$/ {
    for (i = 1; i <= NF; i++) if ($i == "--project-name") print $(i + 1)
}' "$FAKE_DOCKER_LOG")
web_down_project=$(awk -F '\t' '$0 ~ /\tdown\t-v\t--remove-orphans$/ {
    for (i = 1; i <= NF; i++) if ($i == "--project-name") print $(i + 1)
}' "$FAKE_DOCKER_LOG")
case "$web_up_project" in
    heimdallm-test-web-*) ;;
    *) fail "web runner used an unsafe startup project: $web_up_project" ;;
esac
[ "$web_up_project" = "$web_down_project" ] ||
    fail "web runner startup and cleanup used different projects"
assert_log_has_safe_down "$web_down_project"

printf '8/8 merged Compose config has per-run names, volumes, and ephemeral ports\n'
if [ -n "$REAL_DOCKER" ] && "$REAL_DOCKER" compose version >/dev/null 2>&1; then
    PATH=$ORIGINAL_PATH
    export PATH
    (
        unset HEIMDALLM_COMPOSE_DAEMON_HOST_IP
        unset HEIMDALLM_COMPOSE_DAEMON_HOST_PORT
        unset HEIMDALLM_COMPOSE_WEB_HOST_IP
        unset HEIMDALLM_COMPOSE_WEB_HOST_PORT
        GITHUB_TOKEN=dummy \
        HEIMDALLM_AI_PRIMARY=gemini \
        HEIMDALLM_REPOSITORIES=test/repo \
        HEIMDALLM_PORT=7842 \
        HEIMDALLM_WEB_PORT=3000 \
            "$REAL_DOCKER" compose \
                -f "$REPO_ROOT/docker/docker-compose.yml" \
                config --format json
    ) >"$TEST_TMP/production-config.json"

    jq -e \
        '.services.heimdallm.ports | length == 1 and
         .[0].target == 7842 and .[0].published == "7842" and
         (.[0] | has("host_ip") | not)' \
        "$TEST_TMP/production-config.json" >/dev/null ||
        fail "normal daemon port binding changed"
    jq -e \
        '.services.web.ports | length == 1 and
         .[0].target == 3000 and .[0].published == "3000" and
         (.[0] | has("host_ip") | not)' \
        "$TEST_TMP/production-config.json" >/dev/null ||
        fail "normal web port binding changed"

    compose_test_init config "$REPO_ROOT"
    GITHUB_TOKEN=dummy \
    HEIMDALLM_AI_PRIMARY=gemini \
    HEIMDALLM_REPOSITORIES=test/repo \
    HEIMDALLM_PORT=7842 \
        compose_test config --format json >"$TEST_TMP/config.json"

    jq -e --arg project "$COMPOSE_TEST_PROJECT_NAME" \
        '.name == $project' "$TEST_TMP/config.json" >/dev/null ||
        fail "Compose config did not retain the isolated project"
    jq -e --arg name "$HEIMDALLM_TEST_DAEMON_CONTAINER_NAME" \
        '.services.heimdallm.container_name == $name' "$TEST_TMP/config.json" >/dev/null ||
        fail "daemon container name is not unique"
    jq -e --arg name "$HEIMDALLM_TEST_WEB_CONTAINER_NAME" \
        '.services.web.container_name == $name' "$TEST_TMP/config.json" >/dev/null ||
        fail "web container name is not unique"
    jq -e \
        '.services.heimdallm.ports | length == 1 and
         .[0].target == 7842 and .[0].published == "0" and .[0].host_ip == "127.0.0.1"' \
        "$TEST_TMP/config.json" >/dev/null ||
        fail "daemon test port is not loopback-only and ephemeral"
    jq -e \
        '.services.web.ports | length == 1 and
         .[0].target == 3000 and .[0].published == "0" and .[0].host_ip == "127.0.0.1"' \
        "$TEST_TMP/config.json" >/dev/null ||
        fail "web test port is not loopback-only and ephemeral"
    jq -e \
        '[.services.heimdallm.volumes[] | select(.target == "/data" and .source == "heimdallm-test-data")] | length == 1' \
        "$TEST_TMP/config.json" >/dev/null ||
        fail "daemon data volume is not test-scoped"
    jq -e \
        '[.services.heimdallm.volumes[] | select(.target == "/config" and .source == "heimdallm-test-config")] | length == 1' \
        "$TEST_TMP/config.json" >/dev/null ||
        fail "daemon config volume is not test-scoped"
    compose_test_release
else
    printf 'SKIP: Docker Compose is not installed; fake-binary checks still passed\n'
fi

printf 'PASS: Compose test isolation regression suite\n'
