#!/bin/sh
#
# Shared Docker Compose isolation helpers for integration-test runners.
#
# This file is sourced by both test-local.sh (bash) and test-web.sh (POSIX sh),
# so keep it portable and do not enable shell options here.

compose_test_error() {
    printf 'compose-test: %s\n' "$*" >&2
}

compose_test_init() {
    if [ "${COMPOSE_TEST_INITIALIZED:-0}" = "1" ]; then
        compose_test_error "isolation is already initialized"
        return 1
    fi

    COMPOSE_TEST_KIND="${1:-}"
    COMPOSE_TEST_REPO_ROOT="${2:-}"

    case "$COMPOSE_TEST_KIND" in
        ''|*[!a-z0-9-]*)
            compose_test_error "invalid test kind '$COMPOSE_TEST_KIND'"
            return 1
            ;;
    esac

    if [ ! -d "$COMPOSE_TEST_REPO_ROOT" ]; then
        compose_test_error "repository root does not exist: $COMPOSE_TEST_REPO_ROOT"
        return 1
    fi

    COMPOSE_TEST_BASE_FILE="$COMPOSE_TEST_REPO_ROOT/docker/docker-compose.yml"
    COMPOSE_TEST_OVERRIDE_FILE="$COMPOSE_TEST_REPO_ROOT/docker/docker-compose.test.yml"
    if [ ! -f "$COMPOSE_TEST_BASE_FILE" ] || [ ! -f "$COMPOSE_TEST_OVERRIDE_FILE" ]; then
        compose_test_error "Compose files were not found under $COMPOSE_TEST_REPO_ROOT/docker"
        return 1
    fi

    COMPOSE_TEST_STATE_DIR="$(
        mktemp -d "${TMPDIR:-/tmp}/heimdallm-compose-test-${COMPOSE_TEST_KIND}.XXXXXX"
    )" || {
        compose_test_error "could not allocate an isolated run directory"
        return 1
    }

    _compose_test_token=${COMPOSE_TEST_STATE_DIR##*.}
    case "$_compose_test_token" in
        ''|*[!A-Za-z0-9]*)
            compose_test_error "mktemp returned an unsafe run token"
            rmdir "$COMPOSE_TEST_STATE_DIR" 2>/dev/null || true
            return 1
            ;;
    esac
    _compose_test_token=$(printf '%s' "$_compose_test_token" | tr '[:upper:]' '[:lower:]')

    COMPOSE_TEST_PROJECT_NAME="heimdallm-test-${COMPOSE_TEST_KIND}-$$-${_compose_test_token}"
    COMPOSE_TEST_MARKER="$COMPOSE_TEST_STATE_DIR/project"
    if ! (
        umask 077
        printf '%s\n%s\n' "$COMPOSE_TEST_PROJECT_NAME" "$$" >"$COMPOSE_TEST_MARKER"
    ); then
        compose_test_error "could not write the isolation marker"
        rmdir "$COMPOSE_TEST_STATE_DIR" 2>/dev/null || true
        return 1
    fi

    # docker-compose.test.yml requires these names. They retain the normal
    # stack's stable container names while making every test run collision-free.
    HEIMDALLM_TEST_DAEMON_CONTAINER_NAME="${COMPOSE_TEST_PROJECT_NAME}-daemon"
    HEIMDALLM_TEST_WEB_CONTAINER_NAME="${COMPOSE_TEST_PROJECT_NAME}-web"
    # Reuse the base Compose port entries instead of appending override entries.
    # Port 0 asks Docker to allocate a free port for each independent project.
    HEIMDALLM_COMPOSE_DAEMON_HOST_IP=127.0.0.1
    HEIMDALLM_COMPOSE_DAEMON_HOST_PORT=0
    HEIMDALLM_COMPOSE_WEB_HOST_IP=127.0.0.1
    HEIMDALLM_COMPOSE_WEB_HOST_PORT=0
    export HEIMDALLM_TEST_DAEMON_CONTAINER_NAME
    export HEIMDALLM_TEST_WEB_CONTAINER_NAME
    export HEIMDALLM_COMPOSE_DAEMON_HOST_IP
    export HEIMDALLM_COMPOSE_DAEMON_HOST_PORT
    export HEIMDALLM_COMPOSE_WEB_HOST_IP
    export HEIMDALLM_COMPOSE_WEB_HOST_PORT
    export COMPOSE_TEST_PROJECT_NAME

    COMPOSE_TEST_INITIALIZED=1
}

compose_test_assert_identity() {
    if [ "${COMPOSE_TEST_INITIALIZED:-0}" != "1" ]; then
        compose_test_error "isolation is not initialized"
        return 1
    fi
    if [ -z "${COMPOSE_TEST_PROJECT_NAME:-}" ] ||
       [ -z "${COMPOSE_TEST_KIND:-}" ] ||
       [ -z "${COMPOSE_TEST_MARKER:-}" ]; then
        compose_test_error "run identity is incomplete"
        return 1
    fi
    if [ "$COMPOSE_TEST_MARKER" != "${COMPOSE_TEST_STATE_DIR:-}/project" ]; then
        compose_test_error "run marker path does not match the private state directory"
        return 1
    fi
    if [ ! -f "$COMPOSE_TEST_MARKER" ] || [ -L "$COMPOSE_TEST_MARKER" ]; then
        compose_test_error "run marker is missing or unsafe"
        return 1
    fi

    _compose_test_expected_project=$(sed -n '1p' "$COMPOSE_TEST_MARKER")
    _compose_test_expected_pid=$(sed -n '2p' "$COMPOSE_TEST_MARKER")
    if [ "$_compose_test_expected_project" != "$COMPOSE_TEST_PROJECT_NAME" ] ||
       [ "$_compose_test_expected_pid" != "$$" ]; then
        compose_test_error "run marker does not match this process"
        return 1
    fi

    _compose_test_prefix="heimdallm-test-${COMPOSE_TEST_KIND}-"
    case "$COMPOSE_TEST_PROJECT_NAME" in
        "$_compose_test_prefix"*) ;;
        *)
            compose_test_error "project is not test-scoped"
            return 1
            ;;
    esac
    case "$COMPOSE_TEST_PROJECT_NAME" in
        *[!a-z0-9_-]*)
            compose_test_error "project contains unsafe characters"
            return 1
            ;;
    esac
    if [ "$COMPOSE_TEST_PROJECT_NAME" = "$_compose_test_prefix" ]; then
        compose_test_error "project identity is incomplete"
        return 1
    fi

    if [ "${COMPOSE_TEST_BASE_FILE:-}" != "${COMPOSE_TEST_REPO_ROOT:-}/docker/docker-compose.yml" ] ||
       [ "${COMPOSE_TEST_OVERRIDE_FILE:-}" != "${COMPOSE_TEST_REPO_ROOT:-}/docker/docker-compose.test.yml" ]; then
        compose_test_error "Compose file identity changed after initialization"
        return 1
    fi
    if [ "${HEIMDALLM_TEST_DAEMON_CONTAINER_NAME:-}" != "${COMPOSE_TEST_PROJECT_NAME}-daemon" ] ||
       [ "${HEIMDALLM_TEST_WEB_CONTAINER_NAME:-}" != "${COMPOSE_TEST_PROJECT_NAME}-web" ]; then
        compose_test_error "container identity changed after initialization"
        return 1
    fi
    if [ "${HEIMDALLM_COMPOSE_DAEMON_HOST_IP:-}" != "127.0.0.1" ] ||
       [ "${HEIMDALLM_COMPOSE_DAEMON_HOST_PORT:-}" != "0" ] ||
       [ "${HEIMDALLM_COMPOSE_WEB_HOST_IP:-}" != "127.0.0.1" ] ||
       [ "${HEIMDALLM_COMPOSE_WEB_HOST_PORT:-}" != "0" ]; then
        compose_test_error "test port isolation changed after initialization"
        return 1
    fi
}

compose_test_assert_safe() {
    if ! compose_test_assert_identity; then
        compose_test_error "refusing destructive Compose command"
        return 1
    fi
}

compose_test() {
    case "${1:-}" in
        '')
            compose_test_error "a Compose subcommand is required"
            return 1
            ;;
        -*)
            compose_test_error "the first argument must be a Compose subcommand"
            return 1
            ;;
    esac

    if [ "${1:-}" = "down" ]; then
        compose_test_assert_safe || return $?
    else
        compose_test_assert_identity || return $?
    fi

    command docker compose \
        --project-name "$COMPOSE_TEST_PROJECT_NAME" \
        -f "$COMPOSE_TEST_BASE_FILE" \
        -f "$COMPOSE_TEST_OVERRIDE_FILE" \
        "$@"
}

compose_test_down() {
    compose_test down -v --remove-orphans
}

compose_test_cleanup() {
    if [ -z "${COMPOSE_TEST_STATE_DIR:-}" ] ||
       [ ! -d "$COMPOSE_TEST_STATE_DIR" ] ||
       [ "${COMPOSE_TEST_MARKER:-}" != "$COMPOSE_TEST_STATE_DIR/project" ]; then
        compose_test_error "cleanup state is invalid; refusing to invoke Docker"
        return 1
    fi

    COMPOSE_TEST_CLEANUP_LOG="$COMPOSE_TEST_STATE_DIR/cleanup.log"
    if compose_test_down >"$COMPOSE_TEST_CLEANUP_LOG" 2>&1; then
        rm -f "$COMPOSE_TEST_CLEANUP_LOG"
        compose_test_release
        return 0
    fi

    compose_test_error "cleanup failed; isolated Docker resources may remain"
    if compose_test_assert_identity >/dev/null 2>&1; then
        compose_test_error "project: $COMPOSE_TEST_PROJECT_NAME"
        compose_test_error "inspect: $(compose_test_display) ps -a"
    fi
    compose_test_error "marker preserved: $COMPOSE_TEST_MARKER"
    compose_test_error "cleanup log: $COMPOSE_TEST_CLEANUP_LOG"
    return 1
}

compose_test_run_path() {
    _compose_test_run_file="${1:-}"
    case "$_compose_test_run_file" in
        ''|*[!a-z0-9-]*)
            compose_test_error "invalid per-run file name '$_compose_test_run_file'"
            return 1
            ;;
    esac
    compose_test_assert_identity || return $?
    printf '%s/%s\n' "$COMPOSE_TEST_STATE_DIR" "$_compose_test_run_file"
}

compose_test_published_port() {
    _compose_test_service="${1:-}"
    _compose_test_container_port="${2:-}"
    if [ -z "$_compose_test_service" ] || [ -z "$_compose_test_container_port" ]; then
        compose_test_error "service and container port are required"
        return 1
    fi

    _compose_test_binding=$(compose_test port "$_compose_test_service" "$_compose_test_container_port" | tail -n 1)
    _compose_test_host_port=${_compose_test_binding##*:}
    case "$_compose_test_host_port" in
        ''|*[!0-9]*)
            compose_test_error "could not resolve published port for ${_compose_test_service}:${_compose_test_container_port}"
            return 1
            ;;
    esac
    printf '%s\n' "$_compose_test_host_port"
}

compose_test_display() {
    printf 'env HEIMDALLM_TEST_DAEMON_CONTAINER_NAME=%s HEIMDALLM_TEST_WEB_CONTAINER_NAME=%s HEIMDALLM_COMPOSE_DAEMON_HOST_IP=127.0.0.1 HEIMDALLM_COMPOSE_DAEMON_HOST_PORT=0 HEIMDALLM_COMPOSE_WEB_HOST_IP=127.0.0.1 HEIMDALLM_COMPOSE_WEB_HOST_PORT=0 docker compose --project-name %s -f "%s" -f "%s"' \
        "$HEIMDALLM_TEST_DAEMON_CONTAINER_NAME" \
        "$HEIMDALLM_TEST_WEB_CONTAINER_NAME" \
        "$COMPOSE_TEST_PROJECT_NAME" \
        "$COMPOSE_TEST_BASE_FILE" \
        "$COMPOSE_TEST_OVERRIDE_FILE"
}

compose_test_release() {
    if [ -n "${COMPOSE_TEST_MARKER:-}" ] &&
       [ "$COMPOSE_TEST_MARKER" = "${COMPOSE_TEST_STATE_DIR:-}/project" ]; then
        rm -f "$COMPOSE_TEST_MARKER"
    fi
    if [ -n "${COMPOSE_TEST_STATE_DIR:-}" ]; then
        rmdir "$COMPOSE_TEST_STATE_DIR" 2>/dev/null || true
    fi

    unset HEIMDALLM_TEST_DAEMON_CONTAINER_NAME
    unset HEIMDALLM_TEST_WEB_CONTAINER_NAME
    unset HEIMDALLM_COMPOSE_DAEMON_HOST_IP
    unset HEIMDALLM_COMPOSE_DAEMON_HOST_PORT
    unset HEIMDALLM_COMPOSE_WEB_HOST_IP
    unset HEIMDALLM_COMPOSE_WEB_HOST_PORT
    unset COMPOSE_TEST_PROJECT_NAME
    unset COMPOSE_TEST_KIND
    unset COMPOSE_TEST_REPO_ROOT
    unset COMPOSE_TEST_BASE_FILE
    unset COMPOSE_TEST_OVERRIDE_FILE
    unset COMPOSE_TEST_STATE_DIR
    unset COMPOSE_TEST_MARKER
    unset COMPOSE_TEST_CLEANUP_LOG
    COMPOSE_TEST_INITIALIZED=0
}
