#!/bin/sh

set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && /bin/pwd -P)
HEIMDALLM_MACOS_INSTALL_SOURCE_ONLY=1
export HEIMDALLM_MACOS_INSTALL_SOURCE_ONLY
. "$SCRIPT_DIR/macos-install.sh"
unset HEIMDALLM_MACOS_INSTALL_SOURCE_ONLY

TEST_COUNT=0
FAIL_COUNT=0

pass() {
  TEST_COUNT=$((TEST_COUNT + 1))
  printf 'ok %d - %s\n' "$TEST_COUNT" "$1"
}

fail() {
  TEST_COUNT=$((TEST_COUNT + 1))
  FAIL_COUNT=$((FAIL_COUNT + 1))
  printf 'not ok %d - %s\n' "$TEST_COUNT" "$1" >&2
  if [ "$#" -gt 1 ]; then
    printf '  %s\n' "$2" >&2
  fi
}

assert_accepts() {
  mi_test_name=$1
  shift
  if "$@"; then
    pass "$mi_test_name"
  else
    fail "$mi_test_name" "expected success"
  fi
}

assert_rejects() {
  mi_test_name=$1
  shift
  if "$@"; then
    fail "$mi_test_name" "expected failure"
  else
    pass "$mi_test_name"
  fi
}

assert_output() {
  mi_test_name=$1
  mi_expected=$2
  shift 2
  if mi_actual=$("$@" 2>&1); then
    if [ "$mi_actual" = "$mi_expected" ]; then
      pass "$mi_test_name"
    else
      fail "$mi_test_name" "expected '$mi_expected', got '$mi_actual'"
    fi
  else
    fail "$mi_test_name" "command failed; expected '$mi_expected'"
  fi
}

assert_value() {
  mi_test_name=$1
  mi_expected=$2
  mi_actual=$3
  if [ "$mi_actual" = "$mi_expected" ]; then
    pass "$mi_test_name"
  else
    fail "$mi_test_name" "expected '$mi_expected', got '$mi_actual'"
  fi
}

assert_policy() {
  mi_state=$1
  mi_listener=$2
  mi_expected=$3
  assert_output \
    "matrix $mi_state × $mi_listener" \
    "$mi_expected" \
    decide_install_policy "$mi_state" "$mi_listener"
}

# Release tags: URL interpolation is allowed only after this pure validation.
assert_accepts "accepts semver release tag" validate_release_tag "v0.7.8"
assert_accepts "accepts safe named release tag" \
  validate_release_tag "release_2026-07.29"
assert_rejects "rejects empty release tag" validate_release_tag ""
assert_rejects "rejects dot release path segment" validate_release_tag "."
assert_rejects "rejects dot-dot release path segment" validate_release_tag ".."
assert_rejects "rejects release path traversal" validate_release_tag "../v1"
assert_rejects "rejects release slash" validate_release_tag "feature/v1"
assert_rejects "rejects release spaces" validate_release_tag "v1 latest"
assert_rejects "rejects release query characters" validate_release_tag "v1?"
assert_rejects "rejects shell metacharacters" validate_release_tag 'v1;id'
mi_newline_tag='v1
v2'
assert_rejects "rejects release newline" validate_release_tag "$mi_newline_tag"

# PURGE mode is exact, and the public command accepts only empty/unset or 1.
assert_accepts "PURGE=1 requests purge" purge_requested "1"
assert_rejects "empty PURGE does not request purge" purge_requested ""
assert_rejects "unset-style PURGE does not request purge" purge_requested
assert_accepts "accepts unset PURGE value" validate_purge_value
assert_accepts "accepts empty PURGE value" validate_purge_value ""
assert_accepts "accepts exact PURGE=1 value" validate_purge_value "1"
assert_rejects "rejects PURGE=0 before mutation" validate_purge_value "0"
assert_rejects "rejects PURGE=true before mutation" \
  validate_purge_value "true"
assert_rejects "rejects PURGE=01 before mutation" validate_purge_value "01"
assert_rejects "rejects PURGE with trailing space before mutation" \
  validate_purge_value "1 "

# LaunchServices may know about development and installed bundles with the same
# name and identifier. The install hint must select the installed path exactly.
assert_output "launch hint opens the installed bundle by exact path" \
  "/usr/bin/open '/Applications/Heimdallm.app'" \
  installed_app_launch_command "/Applications/Heimdallm.app"
mi_special_app_path="/Applications/\$Team \`beta\` \"quoted\" \\ path's.app"
mi_special_launch_command="/usr/bin/open '/Applications/\$Team \`beta\` \"quoted\" \\ path'\\''s.app'"
assert_output "launch hint safely quotes a non-default bundle path" \
  "$mi_special_launch_command" \
  installed_app_launch_command "$mi_special_app_path"

# Canonical user paths.
assert_accepts "accepts macOS absolute home" set_user_paths "/Users/alice"
assert_value "derives config path" \
  "/Users/alice/.config/heimdallm" "$CONFIG_DIR"
assert_value "derives data path" \
  "/Users/alice/.local/share/heimdallm" "$DATA_DIR"
assert_value "derives database path" \
  "/Users/alice/.local/share/heimdallm/heimdallm.db" "$DB_PATH"
assert_value "derives ui.pid path" \
  "/Users/alice/.local/share/heimdallm/ui.pid" "$UI_PID_PATH"
assert_value "derives log path" \
  "/Users/alice/Library/Logs/heimdallm" "$LOG_DIR"
assert_value "derives LaunchAgent path" \
  "/Users/alice/Library/LaunchAgents/com.heimdallm.daemon.plist" \
  "$PLIST_PATH"
assert_accepts "accepts Linux home for cross-platform pure tests" \
  set_user_paths "/home/alice"
assert_rejects "rejects empty home" set_user_paths ""
assert_rejects "rejects relative home" set_user_paths "Users/alice"
assert_rejects "rejects root home" set_user_paths "/"
assert_rejects "rejects dot component" set_user_paths "/Users/./alice"
assert_rejects "rejects dot-dot component" set_user_paths "/Users/bob/../alice"
assert_rejects "rejects duplicate slash" set_user_paths "/Users//alice"
assert_rejects "rejects trailing slash" set_user_paths "/Users/alice/"
assert_accepts "accepts canonical /tmp installer workspace" \
  safe_temp_path "/tmp/heimdallm-install.ABC123"
assert_accepts "accepts macOS per-user TMPDIR workspace" \
  safe_temp_path "/var/folders/ab/cdef/T/heimdallm-install.ABC123"
assert_rejects "rejects arbitrary TMPDIR cleanup path" \
  safe_temp_path "/Users/alice/tmp/heimdallm-install.ABC123"
assert_rejects "rejects unrelated /tmp cleanup path" \
  safe_temp_path "/tmp/unrelated.ABC123"
assert_rejects "rejects short installer workspace suffix" \
  safe_temp_path "/tmp/heimdallm-install.ABC"
assert_accepts "accepts exact staging work path" \
  safe_application_work_path "/Applications/.heimdallm-staging.ABC123"
assert_accepts "accepts exact backup work path" \
  safe_application_work_path "/Applications/.heimdallm-backup.ABC123"
assert_rejects "rejects Applications root as work path" \
  safe_application_work_path "/Applications"
assert_rejects "rejects short staging work path suffix" \
  safe_application_work_path "/Applications/.heimdallm-staging.ABC"

# Flutter writes ui.pid without a trailing newline; retain the assigned value
# even though POSIX read reports EOF as a non-zero status.
mi_pid_file=$(mktemp "/tmp/heimdallm-ui-pid.XXXXXX")
printf '%s' "12345" > "$mi_pid_file"
assert_accepts "reads ui.pid without a trailing newline" \
  read_pid_file "$mi_pid_file"
assert_value "preserves ui.pid value assigned at EOF" "12345" "$PID_FILE_VALUE"
rm -f "$mi_pid_file"

# Listener classification uses exact executable paths and prioritizes the
# captured LaunchAgent PID.
assert_output "classifies empty listener as free" \
  "free" classify_listener "" "" ""
assert_output "classifies captured PID as service-owned" \
  "service" classify_listener "42" "$APP_DAEMON_BIN" "42"
assert_output "classifies exact UI binary as bundle-owned" \
  "bundle" classify_listener "43" "$APP_UI_BIN" "42"
assert_output "classifies exact daemon binary as bundle-owned" \
  "bundle" classify_listener "43" "$APP_DAEMON_BIN" "42"
assert_output "rejects bundle-path prefix as foreign" \
  "foreign" classify_listener "43" "$APP_DAEMON_BIN.old" "42"
assert_output "classifies checkout daemon as foreign" \
  "foreign" classify_listener "43" "/work/daemon/bin/heimdallm" "42"

# PURGE preflight permits only the invoking user's known service or exact
# installed-bundle executables. A checkout daemon is known only by service PID.
assert_output "purge permits captured checkout service PID" \
  "known-service" \
  classify_purge_holder "42" "/work/daemon/bin/heimdallm" "501" "42" "501"
assert_output "purge permits exact installed UI" \
  "known-bundle" \
  classify_purge_holder "43" "$APP_UI_BIN" "501" "42" "501"
assert_output "purge permits exact installed daemon" \
  "known-bundle" \
  classify_purge_holder "44" "$APP_DAEMON_BIN" "501" "42" "501"
assert_output "purge rejects a development daemon" \
  "foreign" \
  classify_purge_holder "45" "/work/daemon/bin/heimdallm" "501" "42" "501"
assert_output "purge rejects another user's installed process" \
  "foreign" \
  classify_purge_holder "46" "$APP_DAEMON_BIN" "502" "42" "501"
assert_output "purge rejects an unresolved executable" \
  "foreign" classify_purge_holder "47" "" "501" "42" "501"

# Reviewed 4 × 4 LaunchAgent/listener matrix.
assert_policy "absent" "free" "proceed"
assert_policy "absent" "bundle" "stop-bundle"
assert_policy "absent" "service" "abort-inconsistent"
assert_policy "absent" "foreign" "warn"

assert_policy "present-unloaded" "free" "migrate-unloaded"
assert_policy "present-unloaded" "bundle" \
  "stop-bundle+migrate-unloaded"
assert_policy "present-unloaded" "service" "abort-inconsistent"
assert_policy "present-unloaded" "foreign" \
  "warn+migrate-unloaded"

assert_policy "loaded-enabled" "free" "restart-service"
assert_policy "loaded-enabled" "bundle" \
  "stop-bundle+restart-service"
assert_policy "loaded-enabled" "service" "restart-service"
assert_policy "loaded-enabled" "foreign" "abort-foreign"

assert_policy "loaded-disabled" "free" "migrate-disabled"
assert_policy "loaded-disabled" "bundle" \
  "stop-bundle+migrate-disabled"
assert_policy "loaded-disabled" "service" "migrate-disabled"
assert_policy "loaded-disabled" "foreign" \
  "warn+migrate-disabled"

# Signal-safe swap detection: rollback must not touch the installed app before
# a rename, but must recognize a rename that completed before its post-command
# flag assignment.
assert_rejects "pre-swap rollback does not infer an app mutation" \
  rollback_old_move_detected "0" "0" "0"
assert_rejects "old-move intent alone is not a completed rename" \
  rollback_old_move_detected "0" "1" "0"
assert_accepts "backup presence closes old-move signal window" \
  rollback_old_move_detected "0" "1" "1"
assert_accepts "completed old-move flag is authoritative" \
  rollback_old_move_detected "1" "0" "0"
assert_rejects "new-move intent before rename is not installed" \
  rollback_new_move_detected "0" "1" "0" "0"
assert_accepts "path evidence closes new-move signal window" \
  rollback_new_move_detected "0" "1" "1" "1"
assert_accepts "completed new-app flag is authoritative" \
  rollback_new_move_detected "1" "0" "0" "0"

# EXIT/signal handlers roll back only an armed, uncommitted transaction.
assert_rejects "disarmed failed install does not roll back" \
  rollback_required "0" "0"
assert_accepts "armed failed install requires rollback" \
  rollback_required "1" "0"
assert_rejects "committed install does not roll back" \
  rollback_required "1" "1"
assert_rejects "disarmed committed install does not roll back" \
  rollback_required "0" "1"

# Cleanup exercises only real private /tmp state; external macOS integrations
# (hdiutil/launchctl/sudo/swap) remain in the manual verification scenarios.
init_runtime_state
assert_accepts "cleanup succeeds with no resources armed" cleanup
assert_accepts "cleanup is idempotent with no resources armed" cleanup

mi_cleanup_dir=$(mktemp -d "/tmp/heimdallm-install.XXXXXX")
init_runtime_state
TEMP_DIR=$mi_cleanup_dir
assert_accepts "cleanup removes an armed private temp directory" cleanup
assert_rejects "armed private temp directory is gone" \
  test -e "$mi_cleanup_dir"
assert_value "cleanup disarms the private temp directory" "" "$TEMP_DIR"
assert_accepts "cleanup stays idempotent after removing private temp" cleanup

mi_preserved_dir=$(mktemp -d "/tmp/heimdallm-install.XXXXXX")
init_runtime_state
TEMP_DIR=$mi_preserved_dir
PRESERVE_TEMP=1
assert_accepts "cleanup preserves an explicitly protected temp directory" \
  cleanup
assert_accepts "protected temp directory remains present" \
  test -d "$mi_preserved_dir"
PRESERVE_TEMP=0
assert_accepts "cleanup removes temp after protection is cleared" cleanup
assert_rejects "formerly protected temp directory is gone" \
  test -e "$mi_preserved_dir"

if [ "$FAIL_COUNT" -ne 0 ]; then
  printf '\n%d of %d tests failed\n' "$FAIL_COUNT" "$TEST_COUNT" >&2
  exit 1
fi

printf '\n%d tests passed\n' "$TEST_COUNT"
