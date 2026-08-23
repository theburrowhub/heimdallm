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
assert_value "derives legacy LaunchAgent path" \
  "/Users/alice/Library/LaunchAgents/com.auto-pr.daemon.plist" \
  "$LEGACY_PLIST_PATH"
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
assert_accepts "accepts exact legacy quarantine path" \
  safe_legacy_quarantine_path \
  "/home/alice/Library/LaunchAgents/.heimdallm-retired-auto-pr.ABC123"
assert_rejects "rejects LaunchAgents root as legacy quarantine path" \
  safe_legacy_quarantine_path "/home/alice/Library/LaunchAgents"
assert_rejects "rejects unrelated legacy quarantine path" \
  safe_legacy_quarantine_path "/tmp/.heimdallm-retired-auto-pr.ABC123"

# The legacy migrator accepts only paths emitted by historical Heimdallm
# installs. The old label alone never authorizes stopping an arbitrary job.
assert_accepts "accepts historical checkout daemon path" \
  validate_legacy_program_path \
  "/home/alice/work/auto-pr/daemon/bin/auto-pr-daemon"
assert_accepts "accepts historical direct-home checkout daemon path" \
  validate_legacy_program_path "/home/alice/daemon/bin/auto-pr-daemon"
assert_accepts "accepts historical hyphenated app daemon path" \
  validate_legacy_program_path \
  "/Applications/auto-pr.app/Contents/MacOS/auto-pr-daemon"
assert_accepts "accepts historical underscored app daemon path" \
  validate_legacy_program_path \
  "/Applications/auto_pr.app/Contents/MacOS/auto-pr-daemon"
assert_rejects "rejects another user's legacy-looking daemon" \
  validate_legacy_program_path \
  "/Users/mallory/auto-pr/daemon/bin/auto-pr-daemon"
assert_rejects "rejects traversal in legacy daemon path" \
  validate_legacy_program_path \
  "/home/alice/work/../auto-pr/daemon/bin/auto-pr-daemon"
assert_rejects "rejects renamed binary under an allowed checkout" \
  validate_legacy_program_path "/home/alice/work/auto-pr/daemon/bin/heimdalld"

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
assert_output "classifies validated legacy LaunchAgent PID separately" \
  "legacy-service" \
  classify_listener "41" "/home/alice/work/auto-pr/daemon/bin/auto-pr-daemon" \
  "42" "41" "/home/alice/work/auto-pr/daemon/bin/auto-pr-daemon"
assert_output "rejects reused legacy PID with a different executable" \
  "foreign" \
  classify_listener "41" "/usr/bin/python3" "42" "41" \
  "/home/alice/work/auto-pr/daemon/bin/auto-pr-daemon"
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

# Reviewed 4 × 5 LaunchAgent/listener matrix.
assert_policy "absent" "free" "proceed"
assert_policy "absent" "bundle" "stop-bundle"
assert_policy "absent" "service" "abort-inconsistent"
assert_policy "absent" "legacy-service" "retire-legacy"
assert_policy "absent" "foreign" "warn"

assert_policy "present-unloaded" "free" "migrate-unloaded"
assert_policy "present-unloaded" "bundle" \
  "stop-bundle+migrate-unloaded"
assert_policy "present-unloaded" "service" "abort-inconsistent"
assert_policy "present-unloaded" "legacy-service" \
  "retire-legacy+migrate-unloaded"
assert_policy "present-unloaded" "foreign" \
  "warn+migrate-unloaded"

assert_policy "loaded-enabled" "free" "restart-service"
assert_policy "loaded-enabled" "bundle" \
  "stop-bundle+restart-service"
assert_policy "loaded-enabled" "service" "restart-service"
assert_policy "loaded-enabled" "legacy-service" \
  "retire-legacy+restart-service"
assert_policy "loaded-enabled" "foreign" "abort-foreign"

assert_policy "loaded-disabled" "free" "migrate-disabled"
assert_policy "loaded-disabled" "bundle" \
  "stop-bundle+migrate-disabled"
assert_policy "loaded-disabled" "service" "migrate-disabled"
assert_policy "loaded-disabled" "legacy-service" \
  "retire-legacy+migrate-disabled"
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
assert_rejects "legacy move intent before rename is not retired" \
  legacy_move_detected "0" "1" "0" "0"
assert_accepts "legacy path evidence closes move signal window" \
  legacy_move_detected "0" "1" "1" "1"
assert_accepts "completed legacy move flag is authoritative" \
  legacy_move_detected "1" "0" "0" "0"
assert_rejects "legacy stop intent alone is not a completed bootout" \
  legacy_stop_detected "0" "1" "0"
assert_accepts "unloaded job closes legacy bootout signal window" \
  legacy_stop_detected "0" "1" "1"
assert_accepts "completed legacy stop flag is authoritative" \
  legacy_stop_detected "1" "0" "0"
assert_rejects "canonical move intent alone is not publication" \
  legacy_canonical_move_detected "0" "1" "0" "0"
assert_accepts "canonical path evidence closes move signal window" \
  legacy_canonical_move_detected "0" "1" "1" "1"
assert_accepts "completed canonical move flag is authoritative" \
  legacy_canonical_move_detected "1" "0" "0" "0"

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

# Transaction-level legacy migration tests use real temporary files and fake
# launchctl/process seams. They run on Linux CI without touching a user's
# LaunchAgents and cover the state transitions that pure policy tests cannot.
run_legacy_migration_case() (
  mi_case_loaded=$1
  mi_case_disabled=$2
  mi_expect_canonical_loaded=$3
  mi_case_root=$(mktemp -d "/tmp/heimdallm-legacy-test.XXXXXX")
  trap '/bin/rm -rf "$mi_case_root"' 0 HUP INT TERM
  /bin/mkdir -p "$mi_case_root/Library/LaunchAgents"
  set_user_paths "$mi_case_root"
  USER_UID=501
  LAUNCH_DOMAIN="gui/$USER_UID"
  SERVICE_TARGET="$LAUNCH_DOMAIN/$LAUNCHAGENT_LABEL"
  LEGACY_SERVICE_TARGET="$LAUNCH_DOMAIN/$LEGACY_LAUNCHAGENT_LABEL"
  mi_case_log="$mi_case_root/launchctl.log"
  : > "$mi_case_log"
  printf '%s\n' "legacy plist" > "$LEGACY_PLIST_PATH"

  legacy_plist_uid() { printf '%s\n' "$USER_UID"; }
  legacy_plist_mode() { printf '%s\n' "600"; }
  legacy_plist_fingerprint() { /usr/bin/cksum < "$1"; }

  init_runtime_state
  ORIGINAL_PLIST_PRESENT=0
  ORIGINAL_LOADED=0
  ORIGINAL_DISABLED=0
  LEGACY_PLIST_PRESENT=1
  LEGACY_PLIST_MODE=600
  LEGACY_PLIST_FINGERPRINT=$(legacy_plist_fingerprint "$LEGACY_PLIST_PATH")
  LEGACY_PROGRAM_PATH="$USER_HOME/work/daemon/bin/auto-pr-daemon"
  LEGACY_LOADED=$mi_case_loaded
  LEGACY_DISABLED=$mi_case_disabled
  LEGACY_PID=4242
  LEGACY_PID_EXECUTABLE=$LEGACY_PROGRAM_PATH
  LEGACY_PLIST_BACKUP="$mi_case_root/legacy-backup.plist"
  /bin/cp -p "$LEGACY_PLIST_PATH" "$LEGACY_PLIST_BACKUP"
  MOCK_LEGACY_LOADED=$mi_case_loaded
  MOCK_CANONICAL_LOADED=0

  revalidate_legacy_launchagent() { return 0; }
  legacy_current_job_plist_path() { printf '%s\n' "$LEGACY_PLIST_PATH"; }
  legacy_current_job_pid() { printf '%s\n' "$LEGACY_PID"; }
  legacy_process_matches() { return 0; }
  legacy_launchagent_job_state() {
    if [ "$MOCK_LEGACY_LOADED" = "1" ]; then
      printf '%s\n' loaded
    else
      printf '%s\n' unloaded
    fi
  }
  launchagent_job_state() {
    if [ "$MOCK_CANONICAL_LOADED" = "1" ]; then
      printf '%s\n' loaded
    else
      printf '%s\n' unloaded
    fi
  }
  legacy_launchctl() {
    printf '%s\n' "$*" >> "$mi_case_log"
    case "$1:$2" in
      bootout:"$LEGACY_SERVICE_TARGET") MOCK_LEGACY_LOADED=0 ;;
      bootout:"$SERVICE_TARGET") MOCK_CANONICAL_LOADED=0 ;;
      bootstrap:"$LAUNCH_DOMAIN")
        if [ "${3-}" = "$LEGACY_PLIST_PATH" ]; then
          MOCK_LEGACY_LOADED=1
        elif [ "${3-}" = "$PLIST_PATH" ]; then
          MOCK_CANONICAL_LOADED=1
        fi
        ;;
    esac
    return 0
  }
  wait_for_legacy_job_unloaded() { [ "$MOCK_LEGACY_LOADED" = "0" ]; }
  wait_for_pid_exit() { return 0; }
  replace_plist_string() {
    printf '%s=%s\n' "$1" "$2" >> "$3"
  }
  verify_plist_program_path() { [ -f "$PLIST_PATH" ]; }
  inspect_port() { PORT_CLASS=free; return 0; }
  wait_for_loaded_service_ready() { [ "$MOCK_CANONICAL_LOADED" = "1" ]; }
  bootout_current_job() { MOCK_CANONICAL_LOADED=0; return 0; }
  restore_plist_contents_and_flag() { return 0; }
  stop_bundle_processes() { return 0; }

  retire_legacy_launchagent
  [ ! -e "$LEGACY_PLIST_PATH" ]
  [ -f "$LEGACY_QUARANTINE_PATH" ]
  migrate_launchagent
  [ -f "$PLIST_PATH" ]
  [ "$MOCK_LEGACY_LOADED" = "0" ]
  [ "$MOCK_CANONICAL_LOADED" = "$mi_expect_canonical_loaded" ]

  if [ "$mi_case_loaded" = "1" ] && [ "$mi_case_disabled" = "0" ]; then
    rollback_install
    [ ! -e "$PLIST_PATH" ]
    [ -f "$LEGACY_PLIST_PATH" ]
    [ "$MOCK_CANONICAL_LOADED" = "0" ]
    [ "$MOCK_LEGACY_LOADED" = "1" ]
  fi
)

assert_accepts "loaded enabled legacy migrates once and rolls back exactly" \
  run_legacy_migration_case "1" "0" "1"
assert_accepts "loaded disabled legacy becomes disabled and unloaded canonical" \
  run_legacy_migration_case "1" "1" "0"
assert_accepts "unloaded legacy remains unloaded after canonical migration" \
  run_legacy_migration_case "0" "0" "0"

run_legacy_bootout_signal_window_case() (
  mi_case_root=$(mktemp -d "/tmp/heimdallm-legacy-signal.XXXXXX")
  trap '/bin/rm -rf "$mi_case_root"' 0 HUP INT TERM
  /bin/mkdir -p "$mi_case_root/Library/LaunchAgents"
  set_user_paths "$mi_case_root"
  USER_UID=501
  LAUNCH_DOMAIN="gui/$USER_UID"
  SERVICE_TARGET="$LAUNCH_DOMAIN/$LAUNCHAGENT_LABEL"
  LEGACY_SERVICE_TARGET="$LAUNCH_DOMAIN/$LEGACY_LAUNCHAGENT_LABEL"
  printf '%s\n' "legacy plist" > "$LEGACY_PLIST_PATH"
  init_runtime_state
  LEGACY_PLIST_PRESENT=1
  LEGACY_PLIST_MODE=600
  LEGACY_PLIST_FINGERPRINT=$(/usr/bin/cksum < "$LEGACY_PLIST_PATH")
  LEGACY_LOADED=1
  LEGACY_DISABLED=0
  LEGACY_STOP_INTENDED=1
  LEGACY_STOPPED=0
  MOCK_LEGACY_LOADED=0
  ORIGINAL_PLIST_PRESENT=0
  ORIGINAL_LOADED=0

  legacy_plist_fingerprint() { /usr/bin/cksum < "$1"; }
  legacy_launchagent_job_state() {
    if [ "$MOCK_LEGACY_LOADED" = "1" ]; then
      printf '%s\n' loaded
    else
      printf '%s\n' unloaded
    fi
  }
  legacy_launchctl() {
    if [ "$1" = bootstrap ]; then MOCK_LEGACY_LOADED=1; fi
    return 0
  }
  inspect_port() { PORT_CLASS=free; return 0; }
  bootout_current_job() { return 0; }
  restore_plist_contents_and_flag() { return 0; }
  stop_bundle_processes() { return 0; }

  rollback_install
  [ "$LEGACY_STOPPED" = "1" ]
  [ "$MOCK_LEGACY_LOADED" = "1" ]
)

assert_accepts "rollback recovers a bootout completed inside signal window" \
  run_legacy_bootout_signal_window_case

run_signal_handler_case() (
  mi_case_root=$(mktemp -d "/tmp/heimdallm-signal-handler.XXXXXX")
  trap '/bin/rm -rf "$mi_case_root"' 0
  mi_rollback_marker="$mi_case_root/rollback"
  mi_cleanup_marker="$mi_case_root/cleanup"
  rollback_install() { : > "$mi_rollback_marker"; }
  cleanup() { : > "$mi_cleanup_marker"; }
  ROLLBACK_ARMED=1
  INSTALL_COMMITTED=0
  if (handle_signal TERM 143); then
    mi_signal_status=0
  else
    mi_signal_status=$?
  fi
  [ "$mi_signal_status" -eq 143 ] &&
    [ -f "$mi_rollback_marker" ] && [ -f "$mi_cleanup_marker" ]
)

assert_accepts "TERM handler invokes rollback and cleanup with signal status" \
  run_signal_handler_case

run_legacy_uninstall_guard_case() (
  mi_case_root=$(mktemp -d "/tmp/heimdallm-legacy-uninstall.XXXXXX")
  trap '/bin/rm -rf "$mi_case_root"' 0 HUP INT TERM
  mi_mutation_marker="$mi_case_root/mutated"
  require_common_commands() { return 0; }
  resolve_invoking_user() { return 0; }
  snapshot_legacy_launchagent() {
    LEGACY_PLIST_PRESENT=1
    LEGACY_LOADED=0
  }
  remove_launchagent() { : > "$mi_mutation_marker"; }
  if mi_guard_output=$(uninstall_macos 2>&1); then
    return 1
  fi
  [ ! -e "$mi_mutation_marker" ] &&
    printf '%s\n' "$mi_guard_output" | /usr/bin/grep -q "install-macos"
)

assert_accepts "uninstall rejects legacy state before any mutation" \
  run_legacy_uninstall_guard_case

if [ "$FAIL_COUNT" -ne 0 ]; then
  printf '\n%d of %d tests failed\n' "$FAIL_COUNT" "$TEST_COUNT" >&2
  exit 1
fi

printf '\n%d tests passed\n' "$TEST_COUNT"
