#!/bin/sh

set -eu

PROGRAM_NAME="macos-install.sh"
APP_PARENT="/Applications"
APP_PATH="$APP_PARENT/Heimdallm.app"
APP_UI_BIN="$APP_PATH/Contents/MacOS/Heimdallm"
APP_DAEMON_BIN="$APP_PATH/Contents/MacOS/heimdalld"
BUNDLE_PROCESS_PATTERN='^/Applications/Heimdallm[.]app/Contents/MacOS/(Heimdallm|heimdalld)([[:space:]]|$)'
LAUNCHAGENT_LABEL="com.heimdallm.daemon"
PORT="7842"
SERVICE_READY_TIMEOUT_SECONDS="60"

info() {
  printf '%s\n' "$*"
}

warn() {
  printf '⚠  %s\n' "$*" >&2
}

error() {
  printf '❌  %s\n' "$*" >&2
}

die() {
  error "$*"
  exit 1
}

# Pure helpers ---------------------------------------------------------------

validate_release_tag() {
  if [ "$#" -ne 1 ]; then
    return 1
  fi
  case "$1" in
    ""|"."|".."|*[!A-Za-z0-9._-]*)
      return 1
      ;;
    *)
      return 0
      ;;
  esac
}

purge_requested() {
  [ "${1-}" = "1" ]
}

validate_purge_value() {
  if [ "$#" -gt 1 ]; then
    return 1
  fi
  case "${1-}" in
    ""|"1") return 0 ;;
    *) return 1 ;;
  esac
}

installed_app_launch_command() {
  printf '/usr/bin/open "%s"\n' "$APP_PATH"
}

validate_home_path() {
  if [ "$#" -ne 1 ]; then
    return 1
  fi
  case "$1" in
    ""|"/"|[!/]*) return 1 ;;
    *"//"*|*"/./"*|*"/."|*"/../"*|*"/.."|*/) return 1 ;;
    *'
'*) return 1 ;;
    *) return 0 ;;
  esac
}

set_user_paths() {
  if ! validate_home_path "${1-}"; then
    return 1
  fi
  USER_HOME=$1
  CONFIG_DIR="$USER_HOME/.config/heimdallm"
  DATA_DIR="$USER_HOME/.local/share/heimdallm"
  LOG_DIR="$USER_HOME/Library/Logs/heimdallm"
  DB_PATH="$DATA_DIR/heimdallm.db"
  UI_PID_PATH="$DATA_DIR/ui.pid"
  PLIST_PATH="$USER_HOME/Library/LaunchAgents/$LAUNCHAGENT_LABEL.plist"
  return 0
}

# Arguments: listener PID, exact executable path, captured LaunchAgent PID.
# Service ownership wins over bundle ownership so the classes are exclusive.
classify_listener() {
  mi_listener_pid=${1-}
  mi_listener_executable=${2-}
  mi_launchagent_pid=${3-}

  if [ -z "$mi_listener_pid" ]; then
    printf '%s\n' "free"
  elif [ -n "$mi_launchagent_pid" ] &&
       [ "$mi_listener_pid" = "$mi_launchagent_pid" ]; then
    printf '%s\n' "service"
  elif [ "$mi_listener_executable" = "$APP_UI_BIN" ] ||
       [ "$mi_listener_executable" = "$APP_DAEMON_BIN" ]; then
    printf '%s\n' "bundle"
  else
    printf '%s\n' "foreign"
  fi
}

# Abort/warn drives preflight; suffixes are a docs/test oracle. Later actions
# derive from LAUNCHAGENT_STATE. Absent/unloaded + service is defensive only.
decide_install_policy() {
  mi_plist_state=${1-}
  mi_listener_class=${2-}

  case "$mi_plist_state:$mi_listener_class" in
    absent:free) printf '%s\n' "proceed" ;;
    absent:bundle) printf '%s\n' "stop-bundle" ;;
    absent:service) printf '%s\n' "abort-inconsistent" ;;
    absent:foreign) printf '%s\n' "warn" ;;

    present-unloaded:free) printf '%s\n' "migrate-unloaded" ;;
    present-unloaded:bundle) printf '%s\n' "stop-bundle+migrate-unloaded" ;;
    present-unloaded:service) printf '%s\n' "abort-inconsistent" ;;
    present-unloaded:foreign) printf '%s\n' "warn+migrate-unloaded" ;;

    loaded-enabled:free) printf '%s\n' "restart-service" ;;
    loaded-enabled:bundle) printf '%s\n' "stop-bundle+restart-service" ;;
    loaded-enabled:service) printf '%s\n' "restart-service" ;;
    loaded-enabled:foreign) printf '%s\n' "abort-foreign" ;;

    loaded-disabled:free) printf '%s\n' "migrate-disabled" ;;
    loaded-disabled:bundle) printf '%s\n' "stop-bundle+migrate-disabled" ;;
    loaded-disabled:service) printf '%s\n' "migrate-disabled" ;;
    loaded-disabled:foreign) printf '%s\n' "warn+migrate-disabled" ;;

    *) return 1 ;;
  esac
}

rollback_old_move_detected() {
  [ "${1-}" = "1" ] ||
    { [ "${2-}" = "1" ] && [ "${3-}" = "1" ]; }
}

rollback_new_move_detected() {
  [ "${1-}" = "1" ] ||
    { [ "${2-}" = "1" ] &&
      [ "${3-}" = "1" ] &&
      [ "${4-}" = "1" ]; }
}

rollback_required() {
  [ "${1-}" = "1" ] && [ "${2-}" != "1" ]
}

# The captured service PID or exact installed executables are controlled by
# uninstall; every other live database/ui.pid holder blocks purge.
classify_purge_holder() {
  mi_holder_pid=${1-}
  mi_holder_executable=${2-}
  mi_holder_uid=${3-}
  mi_known_service_pid=${4-}
  mi_expected_uid=${5-}

  if [ -z "$mi_holder_uid" ] ||
     [ "$mi_holder_uid" != "$mi_expected_uid" ]; then
    printf '%s\n' "foreign"
  elif [ -n "$mi_known_service_pid" ] &&
       [ "$mi_holder_pid" = "$mi_known_service_pid" ]; then
    printf '%s\n' "known-service"
  elif [ "$mi_holder_executable" = "$APP_UI_BIN" ] ||
       [ "$mi_holder_executable" = "$APP_DAEMON_BIN" ]; then
    printf '%s\n' "known-bundle"
  else
    printf '%s\n' "foreign"
  fi
}

init_runtime_state() {
  INSTALL_COMMITTED=0
  ROLLBACK_ARMED=0
  ROLLBACK_LEAVE_UNLOADED=0
  USE_SUDO=0

  TEMP_DIR=""
  DMG_PATH=""
  MOUNT_POINT=""
  MOUNTED=0
  MOUNT_ATTEMPTED=0

  STAGE_ROOT=""
  STAGED_APP=""
  BACKUP_ROOT=""
  BACKUP_APP=""
  APP_MOVED_TO_BACKUP=0
  NEW_APP_INSTALLED=0
  OLD_MOVE_INTENDED=0
  NEW_MOVE_INTENDED=0
  PRESERVE_BACKUP=0
  PRESERVE_TEMP=0

  ORIGINAL_PLIST_PRESENT=0
  ORIGINAL_PLIST_MODE=""
  ORIGINAL_PLIST_FINGERPRINT=""
  ORIGINAL_JOB_PLIST_PATH=""
  ORIGINAL_LOADED=0
  ORIGINAL_DISABLED=0
  ORIGINAL_PID=""
  ORIGINAL_PID_EXECUTABLE=""
  ORIGINAL_PLIST_BACKUP=""
  LAUNCHAGENT_STATE=""

  PORT_CLASS="free"
  PORT_FOREIGN_INFO=""
  PREFLIGHT_FOREIGN_INFO=""
  RESOLVED_RELEASE=""
}

# Runtime helpers ------------------------------------------------------------

require_macos() {
  if [ "$(/usr/bin/uname -s)" != "Darwin" ]; then
    error "This installer requires macOS."
    info "For Linux, use: make install-linux"
    exit 1
  fi
}

require_executable() {
  if [ ! -x "$1" ]; then
    die "Required system tool is unavailable: $1"
  fi
}

require_common_commands() {
  for mi_command in \
    /bin/chmod \
    /bin/cat \
    /bin/cp \
    /bin/launchctl \
    /bin/mkdir \
    /bin/mv \
    /bin/ps \
    /bin/rm \
    /bin/sleep \
    /usr/bin/awk \
    /usr/bin/cksum \
    /usr/bin/grep \
    /usr/bin/id \
    /usr/bin/mktemp \
    /usr/bin/pgrep \
    /usr/bin/pkill \
    /usr/bin/plutil \
    /usr/bin/stat \
    /usr/sbin/lsof
  do
    require_executable "$mi_command"
  done
}

require_install_commands() {
  require_common_commands
  for mi_command in \
    /sbin/mount \
    /usr/bin/cmp \
    /usr/bin/codesign \
    /usr/bin/curl \
    /usr/bin/ditto \
    /usr/bin/hdiutil \
    /usr/bin/sed \
    /usr/bin/xattr \
    /usr/sbin/chown
  do
    require_executable "$mi_command"
  done
}

resolve_invoking_user() {
  USER_UID=$(/usr/bin/id -u)
  USER_GID=$(/usr/bin/id -g)
  USER_NAME=$(/usr/bin/id -un)

  if [ "$USER_UID" = "0" ]; then
    die "Run make install-macos/uninstall-macos as your normal user, never through sudo."
  fi

  mi_home_input=${HOME-}
  if ! validate_home_path "$mi_home_input"; then
    die "HOME must be an absolute, non-root path for the invoking user."
  fi
  if ! mi_home_resolved=$(CDPATH= cd "$mi_home_input" 2>/dev/null && /bin/pwd -P); then
    die "Cannot resolve the invoking user's home directory: $mi_home_input"
  fi
  if ! set_user_paths "$mi_home_resolved"; then
    die "Resolved home directory is unsafe: $mi_home_resolved"
  fi

  LAUNCH_DOMAIN="gui/$USER_UID"
  SERVICE_TARGET="$LAUNCH_DOMAIN/$LAUNCHAGENT_LABEL"
}

process_executable() {
  mi_process_pid=${1-}
  case "$mi_process_pid" in
    ""|*[!0-9]*) return 1 ;;
  esac

  if ! mi_process_lsof=$(
    /usr/sbin/lsof -nP -a -p "$mi_process_pid" -d txt -Ffn 2>/dev/null
  ); then
    return 1
  fi
  mi_process_path=$(
    printf '%s\n' "$mi_process_lsof" |
      /usr/bin/awk '
        /^ftxt$/ { want_path = 1; next }
        want_path && /^n/ { sub(/^n/, ""); print; exit }
      '
  )
  if [ -z "$mi_process_path" ]; then
    return 1
  fi
  printf '%s\n' "$mi_process_path"
}

process_uid() {
  mi_process_pid=${1-}
  case "$mi_process_pid" in
    ""|*[!0-9]*) return 1 ;;
  esac

  if ! mi_process_lsof=$(
    /usr/sbin/lsof -nP -a -p "$mi_process_pid" -d txt -Fpu 2>/dev/null
  ); then
    return 1
  fi
  mi_process_uid=$(
    printf '%s\n' "$mi_process_lsof" |
      /usr/bin/awk '/^u[0-9]+$/ { sub(/^u/, ""); print; exit }'
  )
  if [ -z "$mi_process_uid" ]; then
    return 1
  fi
  printf '%s\n' "$mi_process_uid"
}

pid_exists() {
  mi_exists_pid=${1-}
  case "$mi_exists_pid" in
    ""|*[!0-9]*) return 1 ;;
  esac
  if mi_ps_output=$(/bin/ps -p "$mi_exists_pid" -o pid= 2>/dev/null); then
    :
  else
    mi_ps_status=$?
    if [ "$mi_ps_status" -eq 1 ]; then
      return 1
    fi
    return 2
  fi
  mi_ps_pid=$(
    printf '%s\n' "$mi_ps_output" |
      /usr/bin/awk '$1 ~ /^[0-9]+$/ { print $1; exit }'
  )
  if [ -z "$mi_ps_pid" ]; then
    return 2
  fi
  [ "$mi_ps_pid" = "$mi_exists_pid" ]
}

pid_is_same_process() {
  mi_same_pid=${1-}
  mi_same_executable=${2-}
  case "$mi_same_pid" in
    ""|*[!0-9]*) return 1 ;;
  esac

  if [ -n "$mi_same_executable" ]; then
    if mi_current_executable=$(process_executable "$mi_same_pid" 2>/dev/null); then
      [ "$mi_current_executable" = "$mi_same_executable" ]
      return
    fi
    # Failure to resolve the executable is not proof that the process exited.
    # If the PID still exists, fail closed and keep waiting.
    if pid_exists "$mi_same_pid"; then
      return 0
    else
      mi_pid_status=$?
    fi
    if [ "$mi_pid_status" -eq 1 ]; then
      return 1
    fi
    return 0
  fi
  if pid_exists "$mi_same_pid"; then
    return 0
  else
    mi_pid_status=$?
  fi
  if [ "$mi_pid_status" -eq 1 ]; then
    return 1
  fi
  return 0
}

launchagent_job_state() {
  if mi_state_output=$(/bin/launchctl print "$SERVICE_TARGET" 2>&1); then
    printf '%s\n' "loaded"
    return 0
  else
    mi_state_status=$?
  fi
  if [ "$mi_state_status" -eq 113 ] ||
     printf '%s\n' "$mi_state_output" |
       /usr/bin/grep -q "Could not find service"; then
    printf '%s\n' "unloaded"
    return 0
  fi
  error "Cannot inspect LaunchAgent job:"
  printf '%s\n' "$mi_state_output" >&2
  return 2
}

current_job_pid() {
  if ! mi_job_output=$(/bin/launchctl print "$SERVICE_TARGET" 2>/dev/null); then
    return 1
  fi
  mi_job_pid=$(
    printf '%s\n' "$mi_job_output" |
      /usr/bin/awk '$1 == "pid" && $2 == "=" && $3 ~ /^[0-9]+$/ { print $3; exit }'
  )
  if [ -z "$mi_job_pid" ]; then
    return 1
  fi
  printf '%s\n' "$mi_job_pid"
}

current_job_plist_path() {
  if ! mi_job_output=$(/bin/launchctl print "$SERVICE_TARGET" 2>/dev/null); then
    return 1
  fi
  mi_job_path=$(
    printf '%s\n' "$mi_job_output" |
      /usr/bin/awk '$1 == "path" && $2 == "=" { sub(/^[^=]*=[[:space:]]*/, ""); print; exit }'
  )
  if [ -z "$mi_job_path" ]; then
    return 1
  fi
  printf '%s\n' "$mi_job_path"
}

read_disabled_state() {
  if ! mi_disabled_output=$(
    /bin/launchctl print-disabled "$LAUNCH_DOMAIN" 2>&1
  ); then
    error "Cannot inspect disabled LaunchAgent state:"
    printf '%s\n' "$mi_disabled_output" >&2
    return 1
  fi

  mi_disabled_line=$(
    printf '%s\n' "$mi_disabled_output" |
      /usr/bin/awk -v label="\"$LAUNCHAGENT_LABEL\"" \
        'index($0, label) { print; exit }'
  )
  case "$mi_disabled_line" in
    "") printf '%s\n' "0" ;;
    *"=>"*"disabled"*|*"=>"*"true"*) printf '%s\n' "1" ;;
    *"=>"*"enabled"*|*"=>"*"false"*) printf '%s\n' "0" ;;
    *)
      error "Unrecognized launchctl disabled-state output: $mi_disabled_line"
      return 1
      ;;
  esac
}

snapshot_launchagent() {
  if [ -L "$PLIST_PATH" ]; then
    die "LaunchAgent plist must not be a symlink: $PLIST_PATH"
  fi
  if [ -e "$PLIST_PATH" ]; then
    if [ ! -f "$PLIST_PATH" ]; then
      die "LaunchAgent plist is not a regular file: $PLIST_PATH"
    fi
    mi_plist_owner=$(/usr/bin/stat -f '%u' "$PLIST_PATH")
    if [ "$mi_plist_owner" != "$USER_UID" ]; then
      die "LaunchAgent plist is not owned by the invoking user: $PLIST_PATH"
    fi
    if [ ! -r "$PLIST_PATH" ] || [ ! -w "$PLIST_PATH" ]; then
      die "LaunchAgent plist must be readable and writable: $PLIST_PATH"
    fi
    ORIGINAL_PLIST_PRESENT=1
    ORIGINAL_PLIST_MODE=$(/usr/bin/stat -f '%Lp' "$PLIST_PATH")
    ORIGINAL_PLIST_FINGERPRINT=$(/usr/bin/cksum < "$PLIST_PATH")
  fi

  if mi_job_output=$(/bin/launchctl print "$SERVICE_TARGET" 2>&1); then
    ORIGINAL_LOADED=1
    ORIGINAL_PID=$(
      printf '%s\n' "$mi_job_output" |
        /usr/bin/awk '$1 == "pid" && $2 == "=" && $3 ~ /^[0-9]+$/ { print $3; exit }'
    )
    ORIGINAL_JOB_PLIST_PATH=$(
      printf '%s\n' "$mi_job_output" |
        /usr/bin/awk '$1 == "path" && $2 == "=" { sub(/^[^=]*=[[:space:]]*/, ""); print; exit }'
    )
  else
    mi_job_status=$?
    if [ "$mi_job_status" -ne 113 ] &&
       ! printf '%s\n' "$mi_job_output" |
         /usr/bin/grep -q "Could not find service"; then
      error "Cannot inspect LaunchAgent job:"
      printf '%s\n' "$mi_job_output" >&2
      exit 1
    fi
  fi

  if ! ORIGINAL_DISABLED=$(read_disabled_state); then
    exit 1
  fi

  if [ "$ORIGINAL_LOADED" = "1" ] &&
     [ "$ORIGINAL_PLIST_PRESENT" != "1" ]; then
    die "LaunchAgent is loaded but its plist is missing; refusing an unreconstructable install."
  fi
  if [ "$ORIGINAL_LOADED" = "1" ] &&
     [ "$ORIGINAL_JOB_PLIST_PATH" != "$PLIST_PATH" ]; then
    die "Loaded LaunchAgent came from '$ORIGINAL_JOB_PLIST_PATH', not the canonical plist '$PLIST_PATH'."
  fi

  # KeepAlive may respawn between launchctl calls. Refresh the captured PID once
  # immediately before listener classification.
  if [ "$ORIGINAL_LOADED" = "1" ]; then
    if mi_refreshed_pid=$(current_job_pid 2>/dev/null); then
      ORIGINAL_PID=$mi_refreshed_pid
    fi
    if [ -n "$ORIGINAL_PID" ]; then
      ORIGINAL_PID_EXECUTABLE=$(process_executable "$ORIGINAL_PID" 2>/dev/null || true)
    fi
  fi

  if [ "$ORIGINAL_PLIST_PRESENT" != "1" ]; then
    LAUNCHAGENT_STATE="absent"
  elif [ "$ORIGINAL_LOADED" != "1" ]; then
    LAUNCHAGENT_STATE="present-unloaded"
  elif [ "$ORIGINAL_DISABLED" = "1" ]; then
    LAUNCHAGENT_STATE="loaded-disabled"
  else
    LAUNCHAGENT_STATE="loaded-enabled"
  fi
}

inspect_port() {
  PORT_CLASS="free"
  PORT_FOREIGN_INFO=""

  # KeepAlive may replace its process between the initial snapshot and this
  # scan. Refresh only while the job is still loaded; after bootout the last
  # captured PID remains useful for detecting a lingering service process.
  if [ "${ORIGINAL_LOADED:-0}" = "1" ]; then
    if ! mi_live_job_state=$(launchagent_job_state); then
      return 1
    fi
    if [ "$mi_live_job_state" = "loaded" ]; then
      if mi_live_job_pid=$(current_job_pid 2>/dev/null); then
        ORIGINAL_PID=$mi_live_job_pid
        ORIGINAL_PID_EXECUTABLE=$(
          process_executable "$ORIGINAL_PID" 2>/dev/null || true
        )
      fi
    fi
  fi

  mi_port_lsof_error=$(
    /usr/bin/mktemp "/tmp/heimdallm-port-lsof.XXXXXX"
  ) || {
    error "Could not create a scratch file for port inspection."
    return 1
  }
  if mi_port_output=$(
    /usr/sbin/lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -Fp \
      2>"$mi_port_lsof_error"
  ); then
    mi_lsof_status=0
  else
    mi_lsof_status=$?
  fi
  mi_port_lsof_error_text=""
  if [ -s "$mi_port_lsof_error" ]; then
    mi_port_lsof_error_text=$(/bin/cat "$mi_port_lsof_error")
  fi
  /bin/rm -f "$mi_port_lsof_error"

  if [ "$mi_lsof_status" -eq 1 ] &&
     [ -z "$mi_port_output" ] &&
     [ -z "$mi_port_lsof_error_text" ]; then
    return 0
  fi
  if [ "$mi_lsof_status" -ne 0 ] ||
     [ -n "$mi_port_lsof_error_text" ]; then
    error "Unable to inspect TCP port $PORT with lsof."
    if [ -n "$mi_port_lsof_error_text" ]; then
      printf '%s\n' "$mi_port_lsof_error_text" >&2
    fi
    return 1
  fi

  mi_port_pids=$(
    printf '%s\n' "$mi_port_output" |
      /usr/bin/awk '/^p[0-9]+$/ { sub(/^p/, ""); if (!seen[$0]++) print }'
  )
  if [ -z "$mi_port_pids" ]; then
    return 0
  fi

  mi_saw_bundle=0
  mi_saw_service=0
  mi_saw_foreign=0
  for mi_port_pid in $mi_port_pids; do
    mi_port_executable=$(process_executable "$mi_port_pid" 2>/dev/null || true)
    mi_listener_class=$(
      classify_listener "$mi_port_pid" "$mi_port_executable" "$ORIGINAL_PID"
    )
    case "$mi_listener_class" in
      service)
        mi_saw_service=1
        ;;
      bundle)
        mi_saw_bundle=1
        ;;
      foreign)
        mi_saw_foreign=1
        mi_display_executable=${mi_port_executable:-unknown-executable}
        if [ -z "$PORT_FOREIGN_INFO" ]; then
          PORT_FOREIGN_INFO="PID $mi_port_pid ($mi_display_executable)"
        else
          PORT_FOREIGN_INFO="$PORT_FOREIGN_INFO
PID $mi_port_pid ($mi_display_executable)"
        fi
        ;;
    esac
  done

  if [ "$mi_saw_foreign" = "1" ]; then
    PORT_CLASS="foreign"
  elif [ "$mi_saw_service" = "1" ]; then
    PORT_CLASS="service"
  elif [ "$mi_saw_bundle" = "1" ]; then
    PORT_CLASS="bundle"
  fi
}

print_foreign_warning() {
  warn "A daemon outside this installation is listening on port $PORT:"
  printf '%s\n' "$1" >&2
  warn "Stop it before opening Heimdallm; the app reuses any healthy daemon on that port."
}

configure_application_privilege() {
  if [ ! -d "$APP_PARENT" ]; then
    die "Applications directory does not exist: $APP_PARENT"
  fi
  if [ -L "$APP_PATH" ]; then
    die "Installed app path must not be a symlink: $APP_PATH"
  fi

  mi_need_sudo=0
  if [ ! -w "$APP_PARENT" ]; then
    mi_need_sudo=1
  elif [ -e "$APP_PATH" ]; then
    mi_existing_app_owner=$(/usr/bin/stat -f '%u' "$APP_PATH")
    if [ "$mi_existing_app_owner" != "$USER_UID" ]; then
      mi_need_sudo=1
    fi
  fi

  if [ "$mi_need_sudo" != "1" ]; then
    USE_SUDO=0
    return 0
  fi

  require_executable /usr/bin/sudo
  info "▶  /Applications needs administrator access."
  info "   sudo will prompt once now; make itself continues as $USER_NAME."
  if ! /usr/bin/sudo -v; then
    die "Could not obtain an administrator sudo ticket."
  fi
  USE_SUDO=1
}

run_privileged() {
  if [ "${USE_SUDO:-0}" = "1" ]; then
    /usr/bin/sudo -n "$@"
  else
    "$@"
  fi
}

safe_application_work_path() {
  case "${1-}" in
    "$APP_PARENT"/.heimdallm-staging.??????|\
    "$APP_PARENT"/.heimdallm-backup.??????)
      [ "$1" != "$APP_PARENT" ]
      ;;
    *) return 1 ;;
  esac
}

safe_temp_path() {
  mi_temp_candidate=${1-}
  case "$mi_temp_candidate" in
    /tmp/heimdallm-install.??????|\
    /private/tmp/heimdallm-install.??????|\
    /var/folders/*/T/heimdallm-install.??????|\
    /private/var/folders/*/T/heimdallm-install.??????)
      return 0
      ;;
    *) return 1 ;;
  esac
}

cleanup() {
  mi_cleanup_ok=1

  if { [ "${MOUNTED:-0}" = "1" ] ||
       [ "${MOUNT_ATTEMPTED:-0}" = "1" ]; } &&
     [ -n "${MOUNT_POINT:-}" ]; then
    if /usr/bin/hdiutil detach "$MOUNT_POINT" >/dev/null 2>&1 ||
       /usr/bin/hdiutil detach -force "$MOUNT_POINT" >/dev/null 2>&1; then
      MOUNTED=0
      MOUNT_ATTEMPTED=0
    else
      warn "Could not detach private DMG mount: $MOUNT_POINT"
      PRESERVE_TEMP=1
      mi_cleanup_ok=0
    fi
  fi

  if [ -n "${STAGE_ROOT:-}" ] && [ -e "$STAGE_ROOT" ]; then
    if safe_application_work_path "$STAGE_ROOT" &&
       run_privileged /bin/rm -rf "$STAGE_ROOT"; then
      STAGE_ROOT=""
      STAGED_APP=""
    else
      warn "Could not remove staging directory: $STAGE_ROOT"
      mi_cleanup_ok=0
    fi
  fi

  if [ "${PRESERVE_BACKUP:-0}" != "1" ] &&
     [ -n "${BACKUP_ROOT:-}" ] && [ -e "$BACKUP_ROOT" ]; then
    if safe_application_work_path "$BACKUP_ROOT" &&
       run_privileged /bin/rm -rf "$BACKUP_ROOT"; then
      BACKUP_ROOT=""
      BACKUP_APP=""
    else
      warn "Could not remove backup directory: $BACKUP_ROOT"
      mi_cleanup_ok=0
    fi
  fi

  if [ "${PRESERVE_TEMP:-0}" != "1" ] &&
     [ -n "${TEMP_DIR:-}" ] && [ -e "$TEMP_DIR" ]; then
    if safe_temp_path "$TEMP_DIR" && /bin/rm -rf "$TEMP_DIR"; then
      TEMP_DIR=""
      DMG_PATH=""
      MOUNT_POINT=""
    else
      warn "Could not remove installer temp directory: $TEMP_DIR"
      mi_cleanup_ok=0
    fi
  fi

  [ "$mi_cleanup_ok" = "1" ]
}

wait_for_pid_exit() {
  mi_wait_pid=${1-}
  mi_wait_executable=${2-}
  if [ -z "$mi_wait_pid" ]; then
    return 0
  fi

  mi_wait_count=0
  while [ "$mi_wait_count" -lt 6 ]; do
    if ! pid_is_same_process "$mi_wait_pid" "$mi_wait_executable"; then
      return 0
    fi
    /bin/sleep 1
    mi_wait_count=$((mi_wait_count + 1))
  done
  ! pid_is_same_process "$mi_wait_pid" "$mi_wait_executable"
}

wait_for_job_unloaded() {
  mi_wait_count=0
  while [ "$mi_wait_count" -lt 6 ]; do
    if ! mi_wait_job_state=$(launchagent_job_state); then
      return 1
    fi
    if [ "$mi_wait_job_state" = "unloaded" ]; then
      return 0
    fi
    /bin/sleep 1
    mi_wait_count=$((mi_wait_count + 1))
  done
  if ! mi_wait_job_state=$(launchagent_job_state); then
    return 1
  fi
  [ "$mi_wait_job_state" = "unloaded" ]
}

bootout_current_job() {
  if ! mi_bootout_state=$(launchagent_job_state); then
    return 1
  fi
  if [ "$mi_bootout_state" = "unloaded" ]; then
    return 0
  fi

  mi_bootout_pid=$(current_job_pid 2>/dev/null || true)
  mi_bootout_executable=""
  if [ -n "$mi_bootout_pid" ]; then
    mi_bootout_executable=$(process_executable "$mi_bootout_pid" 2>/dev/null || true)
  fi

  if /bin/launchctl bootout "$SERVICE_TARGET" >/dev/null 2>&1; then
    :
  else
    if ! mi_bootout_state=$(launchagent_job_state); then
      return 1
    fi
    if [ "$mi_bootout_state" = "loaded" ]; then
      error "launchctl bootout failed for $SERVICE_TARGET"
      return 1
    fi
  fi

  if ! wait_for_job_unloaded; then
    error "LaunchAgent did not reach a verified unloaded state: $SERVICE_TARGET"
    return 1
  fi
  if ! wait_for_pid_exit "$mi_bootout_pid" "$mi_bootout_executable"; then
    error "LaunchAgent process remains alive after bootout: PID $mi_bootout_pid"
    return 1
  fi
}

list_bundle_pids() {
  if mi_bundle_pids=$(/usr/bin/pgrep -f "$BUNDLE_PROCESS_PATTERN" 2>/dev/null); then
    printf '%s\n' "$mi_bundle_pids"
    return 0
  else
    mi_pgrep_status=$?
  fi
  if [ "$mi_pgrep_status" -eq 1 ]; then
    return 0
  fi
  return "$mi_pgrep_status"
}

verify_bundle_process_owners() {
  if ! mi_owner_pids=$(list_bundle_pids); then
    error "Unable to inspect running Heimdallm bundle processes."
    return 1
  fi
  for mi_owner_pid in $mi_owner_pids; do
    if ! mi_owner_uid=$(process_uid "$mi_owner_pid" 2>/dev/null); then
      if pid_exists "$mi_owner_pid"; then
        error "Cannot determine owner of bundle process PID $mi_owner_pid."
        return 1
      else
        mi_owner_pid_status=$?
      fi
      if [ "$mi_owner_pid_status" -eq 1 ]; then
        continue
      fi
      error "Cannot determine owner of bundle process PID $mi_owner_pid."
      return 1
    fi
    if [ "$mi_owner_uid" != "$USER_UID" ]; then
      error "Another user owns running bundle process PID $mi_owner_pid; refusing to replace the shared app."
      return 1
    fi
  done
}

verify_no_bundle_processes() {
  if ! mi_remaining_bundle_pids=$(list_bundle_pids); then
    error "Unable to verify that Heimdallm bundle processes exited."
    return 1
  fi
  if [ -n "$mi_remaining_bundle_pids" ]; then
    error "Heimdallm bundle process remains alive: $mi_remaining_bundle_pids"
    return 1
  fi
}

wait_for_user_bundle_exit() {
  mi_bundle_wait=0
  while [ "$mi_bundle_wait" -lt 6 ]; do
    if mi_bundle_pids=$(
      /usr/bin/pgrep -U "$USER_UID" -f "$BUNDLE_PROCESS_PATTERN" 2>/dev/null
    ); then
      :
    else
      mi_bundle_status=$?
      if [ "$mi_bundle_status" -eq 1 ]; then
        return 0
      fi
      return "$mi_bundle_status"
    fi
    /bin/sleep 1
    mi_bundle_wait=$((mi_bundle_wait + 1))
  done
  if /usr/bin/pgrep -U "$USER_UID" -f "$BUNDLE_PROCESS_PATTERN" \
    >/dev/null 2>&1; then
    return 1
  else
    mi_bundle_status=$?
  fi
  [ "$mi_bundle_status" -eq 1 ]
}

stop_bundle_processes() {
  if ! verify_bundle_process_owners; then
    return 1
  fi

  if /usr/bin/pkill -TERM -U "$USER_UID" -f "$BUNDLE_PROCESS_PATTERN" 2>/dev/null; then
    :
  else
    mi_pkill_status=$?
    if [ "$mi_pkill_status" -ne 1 ]; then
      error "Unable to signal Heimdallm bundle processes."
      return 1
    fi
  fi

  if ! wait_for_user_bundle_exit; then
    warn "Heimdallm did not exit after TERM; sending KILL to the same exact bundle paths."
    if /usr/bin/pkill -KILL -U "$USER_UID" -f "$BUNDLE_PROCESS_PATTERN" 2>/dev/null; then
      :
    else
      mi_pkill_status=$?
      if [ "$mi_pkill_status" -ne 1 ]; then
        error "Unable to kill remaining Heimdallm bundle processes."
        return 1
      fi
    fi
    if ! wait_for_user_bundle_exit; then
      error "A Heimdallm bundle process remains alive."
      return 1
    fi
  fi

  verify_no_bundle_processes
}

restore_plist_contents_and_flag() {
  mi_restore_ok=1

  if [ "$ORIGINAL_PLIST_PRESENT" = "1" ]; then
    if [ -L "$PLIST_PATH" ] ||
       [ -z "$ORIGINAL_PLIST_BACKUP" ] ||
       [ ! -f "$ORIGINAL_PLIST_BACKUP" ] ||
       ! /bin/cp -p "$ORIGINAL_PLIST_BACKUP" "$PLIST_PATH"; then
      error "Could not restore original LaunchAgent plist: $PLIST_PATH"
      mi_restore_ok=0
    fi
  fi

  if [ "$ORIGINAL_DISABLED" = "1" ]; then
    if ! /bin/launchctl disable "$SERVICE_TARGET"; then
      error "Could not restore disabled LaunchAgent flag."
      mi_restore_ok=0
    fi
  else
    if ! /bin/launchctl enable "$SERVICE_TARGET"; then
      error "Could not restore enabled LaunchAgent flag."
      mi_restore_ok=0
    fi
  fi

  [ "$mi_restore_ok" = "1" ]
}

print_manual_service_recovery() {
  warn "The original LaunchAgent plist was restored but left unloaded for safety."
  warn "After PID/port $PORT is free, run:"
  if [ "$ORIGINAL_DISABLED" = "1" ]; then
    printf '   /bin/launchctl enable "%s"\n' "$SERVICE_TARGET" >&2
    printf '   /bin/launchctl bootstrap "%s" "%s"\n' \
      "$LAUNCH_DOMAIN" "$PLIST_PATH" >&2
    printf '   /bin/launchctl disable "%s"\n' "$SERVICE_TARGET" >&2
  else
    printf '   /bin/launchctl bootstrap "%s" "%s"\n' \
      "$LAUNCH_DOMAIN" "$PLIST_PATH" >&2
  fi
}

print_backup_recovery() {
  if [ -n "${BACKUP_APP:-}" ] && [ -e "$BACKUP_APP" ]; then
    warn "The previous app backup was preserved at:"
    printf '   %s\n' "$BACKUP_APP" >&2
    warn "Manual recovery (add sudo only if /Applications requires it):"
    printf '   /bin/rm -rf "%s"\n' "$APP_PATH" >&2
    printf '   /bin/mv "%s" "%s"\n' "$BACKUP_APP" "$APP_PATH" >&2
  fi
  if [ "${PRESERVE_TEMP:-0}" = "1" ] &&
     [ -n "${ORIGINAL_PLIST_BACKUP:-}" ] &&
     [ -f "$ORIGINAL_PLIST_BACKUP" ]; then
    warn "The exact original LaunchAgent plist backup was preserved at:"
    printf '   %s\n' "$ORIGINAL_PLIST_BACKUP" >&2
    warn "Manual plist recovery:"
    printf '   /bin/cp -p "%s" "%s"\n' \
      "$ORIGINAL_PLIST_BACKUP" "$PLIST_PATH" >&2
  fi
}

rollback_install() {
  ROLLBACK_ARMED=0
  mi_rollback_ok=1
  mi_rollback_partial=0
  info "↩  Rolling back the macOS installation..."

  mi_backup_has_app=0
  if [ -n "${BACKUP_APP:-}" ] &&
     { [ -e "$BACKUP_APP" ] || [ -L "$BACKUP_APP" ]; }; then
    mi_backup_has_app=1
  fi
  mi_old_was_moved=0
  if rollback_old_move_detected \
    "$APP_MOVED_TO_BACKUP" "$OLD_MOVE_INTENDED" "$mi_backup_has_app"; then
    mi_old_was_moved=1
  fi
  mi_staged_app_missing=0
  if [ -n "${STAGED_APP:-}" ] && [ ! -e "$STAGED_APP" ]; then
    mi_staged_app_missing=1
  fi
  mi_final_app_present=0
  if [ -e "$APP_PATH" ] || [ -L "$APP_PATH" ]; then
    mi_final_app_present=1
  fi
  mi_new_was_installed=0
  if rollback_new_move_detected \
    "$NEW_APP_INSTALLED" \
    "$NEW_MOVE_INTENDED" \
    "$mi_staged_app_missing" \
    "$mi_final_app_present"; then
    mi_new_was_installed=1
  fi

  if ! bootout_current_job; then
    PRESERVE_BACKUP=1
    if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
      PRESERVE_TEMP=1
    fi
    print_backup_recovery
    return 1
  fi
  if [ "$mi_old_was_moved" = "1" ] ||
     [ "$mi_new_was_installed" = "1" ]; then
    if ! stop_bundle_processes; then
      PRESERVE_BACKUP=1
      if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
        PRESERVE_TEMP=1
      fi
      print_backup_recovery
      return 1
    fi
  fi

  if [ "$mi_old_was_moved" = "1" ]; then
    if [ -z "$BACKUP_APP" ] ||
       { [ ! -e "$BACKUP_APP" ] && [ ! -L "$BACKUP_APP" ]; }; then
      error "Previous app backup is unavailable; cannot roll back the bundle."
      PRESERVE_BACKUP=1
      if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
        PRESERVE_TEMP=1
      fi
      print_backup_recovery
      return 1
    fi
    if { [ -e "$APP_PATH" ] || [ -L "$APP_PATH" ]; } &&
       ! run_privileged /bin/rm -rf "$APP_PATH"; then
      error "Could not remove the replacement app during rollback."
      PRESERVE_BACKUP=1
      if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
        PRESERVE_TEMP=1
      fi
      print_backup_recovery
      return 1
    fi
    if ! run_privileged /bin/mv "$BACKUP_APP" "$APP_PATH"; then
      error "Could not restore the previous app bundle."
      PRESERVE_BACKUP=1
      if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
        PRESERVE_TEMP=1
      fi
      print_backup_recovery
      return 1
    fi
    APP_MOVED_TO_BACKUP=0
    NEW_APP_INSTALLED=0
  elif [ "$mi_new_was_installed" = "1" ] &&
       { [ -e "$APP_PATH" ] || [ -L "$APP_PATH" ]; }; then
    if ! run_privileged /bin/rm -rf "$APP_PATH"; then
      error "Could not remove the newly installed app during rollback."
      mi_rollback_ok=0
    fi
    NEW_APP_INSTALLED=0
  fi
  OLD_MOVE_INTENDED=0
  NEW_MOVE_INTENDED=0

  if ! restore_plist_contents_and_flag; then
    if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
      PRESERVE_TEMP=1
    fi
    mi_rollback_ok=0
  fi

  if [ "$ORIGINAL_LOADED" = "1" ] &&
     [ "$mi_rollback_ok" = "1" ]; then
    if [ "$ROLLBACK_LEAVE_UNLOADED" = "1" ]; then
      print_manual_service_recovery
      mi_rollback_partial=1
    elif ! inspect_port || [ "$PORT_CLASS" != "free" ]; then
      ROLLBACK_LEAVE_UNLOADED=1
      print_manual_service_recovery
      mi_rollback_partial=1
    elif [ "$ORIGINAL_DISABLED" = "1" ]; then
      if /bin/launchctl enable "$SERVICE_TARGET" &&
         /bin/launchctl bootstrap "$LAUNCH_DOMAIN" "$PLIST_PATH" &&
         /bin/launchctl disable "$SERVICE_TARGET"; then
        :
      else
        error "Could not restore the original loaded-and-disabled LaunchAgent state."
        if bootout_current_job &&
           /bin/launchctl disable "$SERVICE_TARGET"; then
          print_manual_service_recovery
          mi_rollback_partial=1
        else
          error "Could not return the LaunchAgent to a verified disabled-and-unloaded state."
          mi_rollback_ok=0
        fi
      fi
    elif ! /bin/launchctl bootstrap "$LAUNCH_DOMAIN" "$PLIST_PATH"; then
      error "Could not reload the original LaunchAgent during rollback."
      if bootout_current_job; then
        print_manual_service_recovery
        mi_rollback_partial=1
      else
        error "Could not verify the failed bootstrap was unloaded."
        mi_rollback_ok=0
      fi
    fi
  fi

  if [ "$mi_rollback_ok" = "1" ] &&
     [ "$mi_rollback_partial" != "1" ]; then
    info "✓  Previous app and LaunchAgent state restored."
    return 0
  fi
  if [ "$mi_rollback_ok" = "1" ]; then
    warn "Previous app and plist were restored, but the LaunchAgent remains unloaded."
    return 1
  fi
  PRESERVE_BACKUP=1
  if [ -n "$ORIGINAL_PLIST_BACKUP" ]; then
    PRESERVE_TEMP=1
  fi
  print_backup_recovery
  return 1
}

handle_exit() {
  mi_exit_status=$1
  trap - 0
  trap '' HUP INT TERM

  if rollback_required \
    "${ROLLBACK_ARMED:-0}" "${INSTALL_COMMITTED:-0}"; then
    if ! rollback_install; then
      mi_exit_status=1
    fi
  fi
  if ! cleanup; then
    mi_exit_status=1
  fi
  exit "$mi_exit_status"
}

handle_signal() {
  mi_signal_name=$1
  mi_signal_status=$2
  trap - 0
  trap '' HUP INT TERM
  warn "Received $mi_signal_name; cleaning up."
  if rollback_required \
    "${ROLLBACK_ARMED:-0}" "${INSTALL_COMMITTED:-0}"; then
    rollback_install || true
  fi
  cleanup || true
  exit "$mi_signal_status"
}

resolve_release() {
  mi_requested_release=${RELEASE-}
  if [ -n "$mi_requested_release" ]; then
    if ! validate_release_tag "$mi_requested_release"; then
      die "Invalid RELEASE tag. Use only letters, digits, dots, underscores, and hyphens."
    fi
    RESOLVED_RELEASE=$mi_requested_release
    return 0
  fi

  info "▶  Resolving the latest Heimdallm release..."
  if ! mi_release_json=$(
    /usr/bin/curl -fsSL \
      "https://api.github.com/repos/theburrowhub/heimdallm/releases/latest"
  ); then
    die "Could not resolve the latest release from GitHub."
  fi
  RESOLVED_RELEASE=$(
    printf '%s\n' "$mi_release_json" |
      /usr/bin/sed -n \
        's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' |
      /usr/bin/sed -n '1p'
  )
  if ! validate_release_tag "$RESOLVED_RELEASE"; then
    die "GitHub returned an empty or unsafe release tag."
  fi
}

create_private_temp() {
  mi_tmp_base=${TMPDIR:-/tmp}
  mi_tmp_base=${mi_tmp_base%/}
  case "$mi_tmp_base" in
    /tmp|/private/tmp|/var/folders/*/T|/private/var/folders/*/T) ;;
    *) mi_tmp_base="/tmp" ;;
  esac

  trap 'handle_exit $?' 0
  trap 'handle_signal HUP 129' HUP
  trap 'handle_signal INT 130' INT
  trap 'handle_signal TERM 143' TERM

  TEMP_DIR=$(/usr/bin/mktemp -d "$mi_tmp_base/heimdallm-install.XXXXXX")
  if ! mi_canonical_temp=$(
    CDPATH= cd "$TEMP_DIR" 2>/dev/null && /bin/pwd -P
  ); then
    die "Could not canonicalize installer temp directory: $TEMP_DIR"
  fi
  TEMP_DIR=$mi_canonical_temp
  if ! safe_temp_path "$TEMP_DIR"; then
    die "System mktemp returned an unsafe installer path: $TEMP_DIR"
  fi
  /bin/chmod 700 "$TEMP_DIR"
  DMG_PATH="$TEMP_DIR/Heimdallm-$RESOLVED_RELEASE.dmg"
  MOUNT_POINT="$TEMP_DIR/mount"
  /bin/mkdir "$MOUNT_POINT"
}

download_and_mount_release() {
  mi_download_url="https://github.com/theburrowhub/heimdallm/releases/download/$RESOLVED_RELEASE/Heimdallm-$RESOLVED_RELEASE.dmg"
  info "▶  Downloading Heimdallm $RESOLVED_RELEASE..."
  if ! /usr/bin/curl -fL --progress-bar "$mi_download_url" -o "$DMG_PATH"; then
    die "Release $RESOLVED_RELEASE has no downloadable macOS DMG at the expected URL."
  fi

  info "▶  Verifying and mounting the DMG..."
  if ! /usr/bin/hdiutil verify "$DMG_PATH" >/dev/null; then
    die "DMG verification failed; refusing to install."
  fi
  MOUNT_ATTEMPTED=1
  if ! /usr/bin/hdiutil attach -readonly -nobrowse -quiet \
    -mountpoint "$MOUNT_POINT" "$DMG_PATH"; then
    if mi_mount_table=$(/sbin/mount 2>/dev/null); then
      if ! printf '%s\n' "$mi_mount_table" |
        /usr/bin/grep -F " on $MOUNT_POINT " >/dev/null 2>&1; then
        MOUNT_ATTEMPTED=0
      fi
    else
      warn "Could not inspect the mount table after attach failed; cleanup will fail closed."
    fi
    die "Could not mount the release DMG."
  fi
  MOUNTED=1
  MOUNT_ATTEMPTED=0
  SOURCE_APP="$MOUNT_POINT/Heimdallm.app"
}

validate_bundle() {
  mi_bundle_path=$1
  mi_ui_binary="$mi_bundle_path/Contents/MacOS/Heimdallm"
  mi_daemon_binary="$mi_bundle_path/Contents/MacOS/heimdalld"

  if [ ! -d "$mi_bundle_path" ] || [ -L "$mi_bundle_path" ]; then
    error "Bundle must be a real application directory, not a symlink: $mi_bundle_path"
    return 1
  fi
  if [ ! -f "$mi_ui_binary" ] || [ -L "$mi_ui_binary" ] ||
     [ ! -f "$mi_daemon_binary" ] || [ -L "$mi_daemon_binary" ] ||
     [ ! -x "$mi_ui_binary" ] || [ ! -x "$mi_daemon_binary" ]; then
    error "Bundle is missing an executable UI or daemon: $mi_bundle_path"
    return 1
  fi
  if /usr/bin/cmp -s "$mi_ui_binary" "$mi_daemon_binary"; then
    error "Bundle binaries are identical (case-collision fork-bomb state)."
    return 1
  fi
  if ! /usr/bin/codesign --verify --deep --strict "$mi_bundle_path"; then
    error "Bundle signature structure is invalid: $mi_bundle_path"
    return 1
  fi
}

stage_bundle() {
  configure_application_privilege
  if ! STAGE_ROOT=$(
    run_privileged /usr/bin/mktemp -d \
      "$APP_PARENT/.heimdallm-staging.XXXXXX"
  ); then
    die "Could not create a staging directory on the /Applications filesystem."
  fi
  if ! safe_application_work_path "$STAGE_ROOT"; then
    die "System mktemp returned an unsafe staging path: $STAGE_ROOT"
  fi
  if [ "$USE_SUDO" = "1" ] &&
     ! run_privileged /bin/chmod 711 "$STAGE_ROOT"; then
    die "Could not make the privileged staging root traversable for validation."
  fi
  STAGED_APP="$STAGE_ROOT/Heimdallm.app"

  info "▶  Staging the validated app bundle..."
  if ! run_privileged /usr/bin/ditto "$SOURCE_APP" "$STAGED_APP"; then
    die "Could not copy the app bundle to staging."
  fi
  if ! run_privileged /usr/bin/xattr -cr "$STAGED_APP"; then
    die "Could not clear quarantine attributes on the staged app."
  fi
  if [ "$USE_SUDO" = "1" ]; then
    if ! run_privileged /usr/sbin/chown -R root:admin "$STAGED_APP"; then
      die "Could not normalize staged bundle ownership to root:admin."
    fi
    if [ "$(/usr/bin/stat -f '%Su:%Sg' "$STAGED_APP")" != "root:admin" ]; then
      die "Staged bundle ownership is not root:admin after normalization."
    fi
  elif ! /usr/sbin/chown -R "$USER_UID:$USER_GID" "$STAGED_APP"; then
    die "Could not normalize staged bundle ownership to $USER_UID:$USER_GID."
  elif [ "$(/usr/bin/stat -f '%u:%g' "$STAGED_APP")" != "$USER_UID:$USER_GID" ]; then
    die "Staged bundle ownership does not match the invoking user and primary group."
  fi
  if ! validate_bundle "$STAGED_APP"; then
    die "The staged app failed post-copy validation."
  fi
}

prepare_transaction() {
  if [ -L "$PLIST_PATH" ]; then
    die "LaunchAgent plist became a symlink during staging; refusing to mutate state."
  fi
  mi_current_plist_present=0
  if [ -e "$PLIST_PATH" ]; then
    mi_current_plist_present=1
  fi
  if [ "$mi_current_plist_present" != "$ORIGINAL_PLIST_PRESENT" ]; then
    die "LaunchAgent plist presence changed during staging; retry after the other operation finishes."
  fi

  if [ "$ORIGINAL_PLIST_PRESENT" = "1" ]; then
    if [ ! -f "$PLIST_PATH" ] ||
       [ "$(/usr/bin/stat -f '%u' "$PLIST_PATH")" != "$USER_UID" ] ||
       [ "$(/usr/bin/stat -f '%Lp' "$PLIST_PATH")" != "$ORIGINAL_PLIST_MODE" ] ||
       [ "$(/usr/bin/cksum < "$PLIST_PATH")" != "$ORIGINAL_PLIST_FINGERPRINT" ]; then
      die "LaunchAgent plist content, owner, or mode changed during staging."
    fi
    ORIGINAL_PLIST_BACKUP="$TEMP_DIR/original-launchagent.plist"
    if ! /bin/cp -p "$PLIST_PATH" "$ORIGINAL_PLIST_BACKUP"; then
      die "Could not back up the LaunchAgent plist."
    fi
  fi

  if ! mi_current_job_state=$(launchagent_job_state); then
    die "Cannot revalidate LaunchAgent state before mutation."
  fi
  mi_current_loaded=0
  if [ "$mi_current_job_state" = "loaded" ]; then
    mi_current_loaded=1
  fi
  if [ "$mi_current_loaded" != "$ORIGINAL_LOADED" ]; then
    die "LaunchAgent loaded state changed during staging; retry the install."
  fi
  if [ "$mi_current_loaded" = "1" ]; then
    if ! mi_current_job_path=$(current_job_plist_path) ||
       [ "$mi_current_job_path" != "$PLIST_PATH" ]; then
      die "Loaded LaunchAgent plist identity changed during staging."
    fi
  fi
  if ! mi_current_disabled=$(read_disabled_state); then
    die "Cannot revalidate LaunchAgent disabled state before mutation."
  fi
  if [ "$mi_current_disabled" != "$ORIGINAL_DISABLED" ]; then
    die "LaunchAgent disabled state changed during staging; retry the install."
  fi

  ROLLBACK_ARMED=1
}

stop_original_launchagent() {
  if [ "$ORIGINAL_LOADED" != "1" ]; then
    return 0
  fi

  info "▶  Stopping the existing LaunchAgent..."
  if ! mi_current_original_path=$(current_job_plist_path) ||
     [ "$mi_current_original_path" != "$PLIST_PATH" ]; then
    die "LaunchAgent plist identity changed immediately before bootout."
  fi
  # Capture the process that launchd owns now, not the potentially stale PID
  # seen before download/staging.
  if mi_current_original_pid=$(current_job_pid 2>/dev/null); then
    ORIGINAL_PID=$mi_current_original_pid
    ORIGINAL_PID_EXECUTABLE=$(
      process_executable "$ORIGINAL_PID" 2>/dev/null || true
    )
  else
    ORIGINAL_PID=""
    ORIGINAL_PID_EXECUTABLE=""
  fi
  if /bin/launchctl bootout "$SERVICE_TARGET" >/dev/null 2>&1; then
    :
  else
    if ! mi_original_job_state=$(launchagent_job_state); then
      die "Could not verify LaunchAgent state after bootout failed."
    fi
    if [ "$mi_original_job_state" = "loaded" ]; then
      die "Could not boot out the existing LaunchAgent."
    fi
  fi
  if ! wait_for_job_unloaded; then
    ROLLBACK_LEAVE_UNLOADED=1
    die "LaunchAgent remains loaded after bootout."
  fi
  if ! wait_for_pid_exit "$ORIGINAL_PID" "$ORIGINAL_PID_EXECUTABLE"; then
    ROLLBACK_LEAVE_UNLOADED=1
    die "LaunchAgent PID $ORIGINAL_PID remains alive; plist restored but service will stay unloaded."
  fi
}

late_port_guard_before_swap() {
  if ! inspect_port; then
    ROLLBACK_LEAVE_UNLOADED=1
    die "Cannot recheck port $PORT safely."
  fi
  if [ "$PORT_CLASS" = "free" ]; then
    return 0
  fi

  if [ "$LAUNCHAGENT_STATE" = "loaded-enabled" ]; then
    ROLLBACK_LEAVE_UNLOADED=1
    die "Port $PORT became occupied after preflight; refusing to restart a KeepAlive service."
  fi
  if [ "$PORT_CLASS" = "foreign" ]; then
    if [ -n "$PORT_FOREIGN_INFO" ]; then
      if [ -z "$PREFLIGHT_FOREIGN_INFO" ]; then
        PREFLIGHT_FOREIGN_INFO=$PORT_FOREIGN_INFO
      elif [ "$PREFLIGHT_FOREIGN_INFO" != "$PORT_FOREIGN_INFO" ]; then
        PREFLIGHT_FOREIGN_INFO="$PREFLIGHT_FOREIGN_INFO
$PORT_FOREIGN_INFO"
      fi
      print_foreign_warning "$PORT_FOREIGN_INFO"
    fi
    return 0
  fi
  die "A known Heimdallm process appeared again after shutdown; refusing to swap the running app."
}

swap_staged_bundle() {
  if [ -L "$APP_PATH" ]; then
    die "Installed app path must not be a symlink: $APP_PATH"
  fi
  if [ -e "$APP_PATH" ]; then
    if [ ! -d "$APP_PATH" ]; then
      die "Installed app path is not a regular application directory: $APP_PATH"
    fi
    if ! BACKUP_ROOT=$(
      run_privileged /usr/bin/mktemp -d \
        "$APP_PARENT/.heimdallm-backup.XXXXXX"
    ); then
      die "Could not create the app rollback directory."
    fi
    if ! safe_application_work_path "$BACKUP_ROOT"; then
      die "System mktemp returned an unsafe backup path: $BACKUP_ROOT"
    fi
    if [ "$USE_SUDO" = "1" ] &&
       ! run_privileged /bin/chmod 711 "$BACKUP_ROOT"; then
      die "Could not make the privileged rollback root traversable."
    fi
    BACKUP_APP="$BACKUP_ROOT/Heimdallm.app"
    OLD_MOVE_INTENDED=1
    if ! run_privileged /bin/mv "$APP_PATH" "$BACKUP_APP"; then
      die "Could not move the current app into the rollback directory."
    fi
    APP_MOVED_TO_BACKUP=1
    OLD_MOVE_INTENDED=0
  fi

  NEW_MOVE_INTENDED=1
  if ! run_privileged /bin/mv "$STAGED_APP" "$APP_PATH"; then
    die "Could not move the staged app into /Applications."
  fi
  NEW_APP_INSTALLED=1
  NEW_MOVE_INTENDED=0
  STAGED_APP=""
}

plist_program_path() {
  /usr/bin/plutil -extract ProgramArguments.0 raw -o - "$PLIST_PATH" 2>/dev/null
}

verify_plist_program_path() {
  if ! mi_installed_program=$(plist_program_path); then
    return 1
  fi
  [ "$mi_installed_program" = "$APP_DAEMON_BIN" ]
}

wait_for_loaded_service_ready() {
  mi_service_wait=0
  while [ "$mi_service_wait" -lt "$SERVICE_READY_TIMEOUT_SECONDS" ]; do
    if ! mi_service_state=$(launchagent_job_state); then
      return 1
    fi
    if [ "$mi_service_state" = "loaded" ] &&
       mi_service_pid=$(current_job_pid 2>/dev/null); then
      if mi_service_executable=$(process_executable "$mi_service_pid" 2>/dev/null) &&
         [ "$mi_service_executable" = "$APP_DAEMON_BIN" ] &&
         inspect_port &&
         [ "$PORT_CLASS" = "service" ]; then
        /bin/sleep 1
        mi_service_wait=$((mi_service_wait + 1))
        if ! mi_service_state=$(launchagent_job_state); then
          return 1
        fi
        if [ "$mi_service_state" = "loaded" ] &&
           [ "$(current_job_pid 2>/dev/null || true)" = "$mi_service_pid" ] &&
           [ "$(process_executable "$mi_service_pid" 2>/dev/null || true)" = "$APP_DAEMON_BIN" ] &&
           inspect_port &&
           [ "$PORT_CLASS" = "service" ]; then
          return 0
        fi
        continue
      fi
    fi
    /bin/sleep 1
    mi_service_wait=$((mi_service_wait + 1))
  done
  return 1
}

migrate_launchagent() {
  if [ "$ORIGINAL_PLIST_PRESENT" != "1" ]; then
    return 0
  fi

  if [ "$LAUNCHAGENT_STATE" = "loaded-enabled" ]; then
    # This check is intentionally stricter than the general foreign-listener
    # policy: any listener would make launchd spin the KeepAlive daemon.
    if ! inspect_port || [ "$PORT_CLASS" != "free" ]; then
      ROLLBACK_LEAVE_UNLOADED=1
      die "Port $PORT is no longer free; refusing to bootstrap the migrated LaunchAgent."
    fi
    info "▶  Migrating and restarting the LaunchAgent from the installed bundle..."
    if ! /bin/launchctl enable "$SERVICE_TARGET"; then
      die "Could not preserve the enabled LaunchAgent state."
    fi
    if ! PATH=/usr/bin:/bin:/usr/sbin:/sbin \
      "$APP_DAEMON_BIN" install; then
      die "The bundled daemon could not regenerate the LaunchAgent plist."
    fi
    if ! verify_plist_program_path; then
      die "Migrated LaunchAgent does not point at the installed daemon."
    fi
    if ! wait_for_loaded_service_ready; then
      die "Migrated LaunchAgent did not keep the installed daemon listening on port $PORT."
    fi
    return 0
  fi

  info "▶  Migrating the unloaded LaunchAgent plist without starting it..."
  if ! /usr/bin/plutil -replace ProgramArguments.0 \
    -string "$APP_DAEMON_BIN" "$PLIST_PATH"; then
    die "Could not update ProgramArguments[0] in the LaunchAgent plist."
  fi
  if ! /bin/chmod "$ORIGINAL_PLIST_MODE" "$PLIST_PATH"; then
    die "Could not restore LaunchAgent plist permissions."
  fi
  if [ "$ORIGINAL_DISABLED" = "1" ]; then
    if ! /bin/launchctl disable "$SERVICE_TARGET"; then
      die "Could not restore the disabled LaunchAgent flag."
    fi
  elif ! /bin/launchctl enable "$SERVICE_TARGET"; then
    die "Could not restore the enabled LaunchAgent flag."
  fi
  if ! mi_migrated_job_state=$(launchagent_job_state); then
    die "Could not verify unloaded LaunchAgent state after migration."
  fi
  if [ "$mi_migrated_job_state" = "loaded" ]; then
    die "LaunchAgent was unexpectedly loaded during unloaded-state migration."
  fi
  if ! verify_plist_program_path; then
    die "Migrated LaunchAgent does not point at the installed daemon."
  fi
}

finish_install() {
  INSTALL_COMMITTED=1
  ROLLBACK_ARMED=0

  if ! cleanup; then
    die "Heimdallm was installed, but installer cleanup was incomplete."
  fi

  info ""
  info "✅  Heimdallm $RESOLVED_RELEASE installed."
  info "    App: $APP_PATH"
  info "    Launch with: $(installed_app_launch_command)"
  info ""
  info "    Preserved:"
  info "      Config:   $CONFIG_DIR"
  info "      History:  $DATA_DIR"
  info "      Logs:     $LOG_DIR"
  info "      Keychain: service=heimdallm, account=github-token"
  if [ -n "$PREFLIGHT_FOREIGN_INFO" ]; then
    info ""
    print_foreign_warning "$PREFLIGHT_FOREIGN_INFO"
  fi
}

install_macos() {
  require_install_commands
  resolve_invoking_user
  snapshot_launchagent

  if ! inspect_port; then
    die "Cannot safely inspect port $PORT."
  fi
  if ! mi_install_policy=$(
    decide_install_policy "$LAUNCHAGENT_STATE" "$PORT_CLASS"
  ); then
    die "Unsupported LaunchAgent/listener state: $LAUNCHAGENT_STATE/$PORT_CLASS"
  fi
  case "$mi_install_policy" in
    abort-inconsistent)
      die "Port ownership is inconsistent with LaunchAgent state; refusing to install."
      ;;
    abort-foreign)
      print_foreign_warning "$PORT_FOREIGN_INFO"
      die "Stop the foreign daemon before updating an enabled, loaded LaunchAgent."
      ;;
    *warn*)
      PREFLIGHT_FOREIGN_INFO=$PORT_FOREIGN_INFO
      print_foreign_warning "$PREFLIGHT_FOREIGN_INFO"
      ;;
  esac

  resolve_release
  warn "This release DMG has no published checksum and is ad-hoc signed."
  warn "hdiutil/codesign validate structure, not publisher identity; download trust relies on GitHub HTTPS."
  create_private_temp
  download_and_mount_release
  if ! validate_bundle "$SOURCE_APP"; then
    die "Downloaded app bundle failed validation."
  fi
  stage_bundle
  prepare_transaction
  stop_original_launchagent

  info "▶  Stopping running app-bundle processes..."
  if ! stop_bundle_processes; then
    die "Could not safely stop all installed bundle processes."
  fi
  late_port_guard_before_swap
  swap_staged_bundle
  migrate_launchagent
  finish_install
}

# Uninstall helpers ----------------------------------------------------------

remove_launchagent() {
  mi_uninstall_pid=""
  mi_uninstall_executable=""
  if mi_uninstall_job=$(/bin/launchctl print "$SERVICE_TARGET" 2>&1); then
    mi_uninstall_pid=$(
      printf '%s\n' "$mi_uninstall_job" |
        /usr/bin/awk '$1 == "pid" && $2 == "=" && $3 ~ /^[0-9]+$/ { print $3; exit }'
    )
    if [ -n "$mi_uninstall_pid" ]; then
      mi_uninstall_executable=$(
        process_executable "$mi_uninstall_pid" 2>/dev/null || true
      )
    fi
    if /bin/launchctl bootout "$SERVICE_TARGET" >/dev/null 2>&1; then
      :
    else
      if ! mi_uninstall_state=$(launchagent_job_state); then
        die "Could not verify LaunchAgent state after bootout failed."
      fi
      if [ "$mi_uninstall_state" = "loaded" ]; then
        die "Could not boot out LaunchAgent $SERVICE_TARGET."
      fi
    fi
    if ! wait_for_job_unloaded; then
      die "LaunchAgent remains loaded; uninstall aborted before removing its plist."
    fi
    if ! wait_for_pid_exit "$mi_uninstall_pid" "$mi_uninstall_executable"; then
      die "LaunchAgent PID $mi_uninstall_pid remains alive; uninstall aborted."
    fi
  else
    mi_uninstall_job_status=$?
    if [ "$mi_uninstall_job_status" -ne 113 ] &&
       ! printf '%s\n' "$mi_uninstall_job" |
         /usr/bin/grep -q "Could not find service"; then
      error "Cannot inspect LaunchAgent job; uninstall aborted:"
      printf '%s\n' "$mi_uninstall_job" >&2
      exit 1
    fi
  fi

  if ! mi_uninstall_disabled=$(read_disabled_state); then
    die "Cannot inspect the persistent LaunchAgent disabled flag."
  fi
  if [ "$mi_uninstall_disabled" = "1" ]; then
    if ! /bin/launchctl enable "$SERVICE_TARGET"; then
      die "Could not clear the persistent disabled flag for the removed LaunchAgent."
    fi
    if ! mi_uninstall_disabled=$(read_disabled_state) ||
       [ "$mi_uninstall_disabled" != "0" ]; then
      die "LaunchAgent disabled flag remains set; uninstall aborted before removing the plist."
    fi
  fi

  if [ -L "$PLIST_PATH" ]; then
    die "LaunchAgent plist must not be a symlink: $PLIST_PATH"
  fi
  if [ -e "$PLIST_PATH" ]; then
    if [ ! -f "$PLIST_PATH" ]; then
      die "LaunchAgent plist is not a regular file: $PLIST_PATH"
    fi
    if ! /bin/rm -f "$PLIST_PATH"; then
      die "Could not remove LaunchAgent plist: $PLIST_PATH"
    fi
    info "↓  Removed LaunchAgent: $PLIST_PATH"
  fi
}

ui_pid_is_live() {
  mi_ui_pid=${1-}
  case "$mi_ui_pid" in
    ""|*[!0-9]*) return 1 ;;
  esac
  if pid_exists "$mi_ui_pid"; then
    return 0
  else
    mi_ui_pid_status=$?
  fi
  if [ "$mi_ui_pid_status" -eq 1 ]; then
    return 1
  fi
  return 0
}

read_pid_file() {
  mi_pid_file_path=${1-}
  PID_FILE_VALUE=""
  if [ "$#" -ne 1 ] || [ -z "$mi_pid_file_path" ]; then
    return 1
  fi
  if IFS= read -r PID_FILE_VALUE < "$mi_pid_file_path"; then
    return 0
  fi
  # Dart omits the newline; POSIX read assigns at EOF but returns non-zero.
  [ -n "$PID_FILE_VALUE" ] || [ -r "$mi_pid_file_path" ]
}

inspect_purge_holder() {
  mi_purge_holder_pid=${1-}
  mi_purge_service_pid=${2-}
  PURGE_HOLDER_CLASS=""
  PURGE_HOLDER_EXECUTABLE=""

  if ! mi_purge_holder_uid=$(
    process_uid "$mi_purge_holder_pid" 2>/dev/null
  ); then
    if pid_exists "$mi_purge_holder_pid"; then
      error "Cannot determine owner of live purge holder PID $mi_purge_holder_pid."
      return 2
    else
      mi_purge_holder_status=$?
    fi
    if [ "$mi_purge_holder_status" -eq 1 ]; then
      return 1
    fi
    error "Cannot determine whether purge holder PID $mi_purge_holder_pid still exists."
    return 2
  fi

  PURGE_HOLDER_EXECUTABLE=$(
    process_executable "$mi_purge_holder_pid" 2>/dev/null || true
  )
  if [ -z "$PURGE_HOLDER_EXECUTABLE" ] &&
     { [ -z "$mi_purge_service_pid" ] ||
       [ "$mi_purge_holder_pid" != "$mi_purge_service_pid" ]; }; then
    if pid_exists "$mi_purge_holder_pid"; then
      error "Cannot resolve executable for live purge holder PID $mi_purge_holder_pid."
      return 2
    else
      mi_purge_holder_status=$?
    fi
    if [ "$mi_purge_holder_status" -eq 1 ]; then
      return 1
    fi
    error "Cannot determine whether purge holder PID $mi_purge_holder_pid still exists."
    return 2
  fi

  PURGE_HOLDER_CLASS=$(
    classify_purge_holder \
      "$mi_purge_holder_pid" \
      "$PURGE_HOLDER_EXECUTABLE" \
      "$mi_purge_holder_uid" \
      "$mi_purge_service_pid" \
      "$USER_UID"
  )
  return 0
}

preflight_purge_ui_pid() {
  mi_purge_service_pid=${1-}
  if [ ! -e "$UI_PID_PATH" ]; then
    return 0
  fi
  if [ ! -f "$UI_PID_PATH" ] || [ -L "$UI_PID_PATH" ]; then
    die "PURGE=1 refuses an unsafe ui.pid path before uninstall: $UI_PID_PATH"
  fi

  if ! read_pid_file "$UI_PID_PATH"; then
    die "PURGE=1 cannot read ui.pid safely before uninstall: $UI_PID_PATH"
  fi
  mi_preflight_ui_pid=$PID_FILE_VALUE
  case "$mi_preflight_ui_pid" in
    ""|*[!0-9]*) return 0 ;;
  esac

  if pid_exists "$mi_preflight_ui_pid"; then
    :
  else
    mi_preflight_ui_status=$?
    if [ "$mi_preflight_ui_status" -eq 1 ]; then
      return 0
    fi
    die "PURGE=1 cannot determine whether ui.pid PID $mi_preflight_ui_pid is live."
  fi

  if inspect_purge_holder \
    "$mi_preflight_ui_pid" "$mi_purge_service_pid"; then
    :
  else
    mi_preflight_ui_status=$?
    if [ "$mi_preflight_ui_status" -eq 1 ]; then
      return 0
    fi
    die "PURGE=1 cannot verify live ui.pid PID $mi_preflight_ui_pid."
  fi
  if [ "$PURGE_HOLDER_CLASS" = "foreign" ]; then
    mi_preflight_ui_executable=${PURGE_HOLDER_EXECUTABLE:-unknown-executable}
    die "PURGE=1 would affect live external ui.pid PID $mi_preflight_ui_pid ($mi_preflight_ui_executable); close it before uninstall."
  fi
}

handle_ui_pid_file() {
  if [ ! -e "$UI_PID_PATH" ]; then
    return 0
  fi
  if [ ! -f "$UI_PID_PATH" ] || [ -L "$UI_PID_PATH" ]; then
    if purge_requested "${PURGE-}"; then
      die "PURGE=1 refuses an unsafe ui.pid path: $UI_PID_PATH"
    fi
    warn "Preserving non-regular ui.pid path: $UI_PID_PATH"
    return 0
  fi

  if ! read_pid_file "$UI_PID_PATH"; then
    warn "Preserving unreadable ui.pid path: $UI_PID_PATH"
    return 0
  fi
  mi_ui_pid=$PID_FILE_VALUE
  if ! ui_pid_is_live "$mi_ui_pid"; then
    /bin/rm -f "$UI_PID_PATH"
    return 0
  fi

  mi_ui_executable=$(process_executable "$mi_ui_pid" 2>/dev/null || true)
  case "$mi_ui_executable" in
    "$APP_UI_BIN")
      die "Installed UI PID $mi_ui_pid remains alive after shutdown."
      ;;
    */Heimdallm.app/Contents/MacOS/Heimdallm)
      if purge_requested "${PURGE-}"; then
        die "PURGE=1 would delete data used by live development UI PID $mi_ui_pid ($mi_ui_executable)."
      fi
      warn "Preserving ui.pid for live development UI PID $mi_ui_pid ($mi_ui_executable)."
      ;;
    *)
      if purge_requested "${PURGE-}"; then
        die "PURGE=1 cannot verify live ui.pid owner PID $mi_ui_pid; refusing to delete user data."
      fi
      warn "Preserving ui.pid for unrecognized live PID $mi_ui_pid."
      ;;
  esac
}

remove_installed_app() {
  if [ -L "$APP_PATH" ]; then
    die "Installed app path must not be a symlink: $APP_PATH"
  fi
  if [ ! -e "$APP_PATH" ]; then
    return 0
  fi
  if [ ! -d "$APP_PATH" ]; then
    die "Installed app path is not a regular application directory: $APP_PATH"
  fi

  if ! run_privileged /bin/rm -rf "$APP_PATH"; then
    die "Could not remove $APP_PATH"
  fi
  info "↓  Removed $APP_PATH"
}

inspect_database_holders() {
  DATABASE_HOLDER_PIDS=""
  if [ ! -e "$DB_PATH" ]; then
    return 0
  fi
  if [ ! -f "$DB_PATH" ] || [ -L "$DB_PATH" ]; then
    error "Database path is not a regular file: $DB_PATH"
    return 2
  fi

  mi_lsof_error=$(
    /usr/bin/mktemp "/tmp/heimdallm-lsof.XXXXXX"
  ) || {
    error "Could not create a scratch file for database inspection."
    return 2
  }
  if mi_lsof_output=$(
    /usr/sbin/lsof -nP -Fp "$DB_PATH" 2>"$mi_lsof_error"
  ); then
    mi_lsof_status=0
  else
    mi_lsof_status=$?
  fi

  mi_lsof_error_text=""
  if [ -s "$mi_lsof_error" ]; then
    mi_lsof_error_text=$(/bin/cat "$mi_lsof_error")
  fi
  /bin/rm -f "$mi_lsof_error"

  if [ "$mi_lsof_status" -eq 1 ] &&
     [ -z "$mi_lsof_output" ] &&
     [ -z "$mi_lsof_error_text" ]; then
    return 0
  fi
  if [ "$mi_lsof_status" -ne 0 ] || [ -n "$mi_lsof_error_text" ]; then
    error "lsof could not inspect the database safely."
    if [ -n "$mi_lsof_error_text" ]; then
      printf '%s\n' "$mi_lsof_error_text" >&2
    fi
    return 2
  fi

  DATABASE_HOLDER_PIDS=$(
    printf '%s\n' "$mi_lsof_output" |
      /usr/bin/awk '/^p[0-9]+$/ { sub(/^p/, ""); if (!seen[$0]++) print }'
  )
  if [ -z "$DATABASE_HOLDER_PIDS" ]; then
    error "lsof reported an open database without an inspectable PID."
    return 2
  fi
}

preflight_purge_database() {
  mi_purge_service_pid=${1-}
  if ! inspect_database_holders; then
    die "Cannot inspect database holders before PURGE=1."
  fi

  for mi_preflight_db_pid in $DATABASE_HOLDER_PIDS; do
    if inspect_purge_holder \
      "$mi_preflight_db_pid" "$mi_purge_service_pid"; then
      :
    else
      mi_preflight_db_status=$?
      if [ "$mi_preflight_db_status" -eq 1 ]; then
        continue
      fi
      die "PURGE=1 cannot verify database holder PID $mi_preflight_db_pid."
    fi
    if [ "$PURGE_HOLDER_CLASS" = "foreign" ]; then
      mi_preflight_db_executable=${PURGE_HOLDER_EXECUTABLE:-unknown-executable}
      die "PURGE=1 found external database holder PID $mi_preflight_db_pid ($mi_preflight_db_executable); stop it before uninstall."
    fi
  done
}

preflight_purge() {
  warn "PURGE=1 permanently deletes the Heimdallm database and all review history; this cannot be undone."

  if ! mi_preflight_job_state=$(launchagent_job_state); then
    die "Cannot inspect the LaunchAgent before PURGE=1."
  fi
  mi_preflight_service_pid=""
  if [ "$mi_preflight_job_state" = "loaded" ]; then
    mi_preflight_service_pid=$(current_job_pid 2>/dev/null || true)
  fi

  preflight_purge_ui_pid "$mi_preflight_service_pid"
  preflight_purge_database "$mi_preflight_service_pid"
}

verify_database_closed() {
  if ! inspect_database_holders; then
    die "lsof could not prove that the database is closed; refusing PURGE=1."
  fi
  if [ -n "$DATABASE_HOLDER_PIDS" ]; then
    error "Database is still open; refusing PURGE=1:"
    for mi_database_holder_pid in $DATABASE_HOLDER_PIDS; do
      mi_database_holder_executable=$(
        process_executable "$mi_database_holder_pid" 2>/dev/null || true
      )
      printf 'PID %s (%s)\n' \
        "$mi_database_holder_pid" \
        "${mi_database_holder_executable:-unknown-executable}" >&2
    done
    exit 1
  fi
}

safe_purge_directory() {
  mi_purge_path=$1
  case "$mi_purge_path" in
    "$CONFIG_DIR"|"$DATA_DIR"|"$LOG_DIR") ;;
    *)
      error "Refusing unexpected purge path: $mi_purge_path"
      return 1
      ;;
  esac
  if [ -e "$mi_purge_path" ] || [ -L "$mi_purge_path" ]; then
    /bin/rm -rf "$mi_purge_path"
    info "    Removed $mi_purge_path"
  fi
}

purge_user_data() {
  if ! mi_purge_job_state=$(launchagent_job_state); then
    die "Cannot prove that the LaunchAgent is unloaded; refusing to purge."
  fi
  if [ "$mi_purge_job_state" = "loaded" ]; then
    die "LaunchAgent became loaded again; refusing to purge."
  fi
  if ! verify_bundle_process_owners; then
    die "Cannot prove that installed bundle processes are stopped; refusing to purge."
  fi
  if ! verify_no_bundle_processes; then
    die "Cannot prove that installed bundle processes are absent; refusing to purge."
  fi

  verify_database_closed
  safe_purge_directory "$DATA_DIR"
  safe_purge_directory "$CONFIG_DIR"
  safe_purge_directory "$LOG_DIR"

  info ""
  info "✅  Heimdallm fully uninstalled; canonical config, history, and logs were removed."
  info "    Preserved Keychain item: service=heimdallm, account=github-token"
  info "    Custom HEIMDALLM_CONFIG_PATH/HEIMDALLM_DATA_DIR locations were not followed."
  info "    To remove the Keychain item too:"
  info "      security delete-generic-password -s heimdallm -a github-token"
}

print_preserved_state() {
  info ""
  info "✅  Heimdallm uninstalled; user state was preserved."
  info ""
  info "    Config:   $CONFIG_DIR"
  info "    History:  $DATA_DIR"
  info "    Logs:     $LOG_DIR"
  info "    Keychain: service=heimdallm, account=github-token"
  info ""
  info "    To remove canonical config, history, and logs:"
  info "      make uninstall-macos PURGE=1"
}

uninstall_macos() {
  require_common_commands
  resolve_invoking_user

  if ! validate_purge_value "${PURGE-}"; then
    die "Invalid PURGE value; omit PURGE to preserve data or use PURGE=1 to delete it."
  fi
  if purge_requested "${PURGE-}"; then
    info ""
    preflight_purge
  fi

  # Authenticate while uninstall is still read-only.
  if [ -L "$APP_PATH" ]; then
    die "Installed app path must not be a symlink: $APP_PATH"
  elif [ -e "$APP_PATH" ]; then
    if [ ! -d "$APP_PATH" ]; then
      die "Installed app path is not a regular application directory: $APP_PATH"
    fi
    configure_application_privilege
  fi

  info "▶  Removing Heimdallm LaunchAgent..."
  remove_launchagent
  info "▶  Stopping installed Heimdallm processes..."
  if ! stop_bundle_processes; then
    die "Could not safely stop all installed bundle processes."
  fi
  handle_ui_pid_file
  remove_installed_app

  if purge_requested "${PURGE-}"; then
    purge_user_data
  else
    print_preserved_state
  fi
}

usage() {
  printf 'Usage: %s install|uninstall\n' "$PROGRAM_NAME" >&2
}

main() {
  # Keep this as the first runtime check. Make has its own prerequisite guard,
  # and this second guard makes direct script invocation harmless on Linux.
  require_macos
  init_runtime_state

  case "${1-}" in
    install)
      if [ "$#" -ne 1 ]; then
        usage
        exit 2
      fi
      install_macos
      ;;
    uninstall)
      if [ "$#" -ne 1 ]; then
        usage
        exit 2
      fi
      uninstall_macos
      ;;
    *)
      usage
      exit 2
      ;;
  esac
}

# The test harness sources this file to exercise pure helpers. Normal direct
# execution always enters main(), whose first runtime action is the Darwin guard.
case "${HEIMDALLM_MACOS_INSTALL_SOURCE_ONLY:-0}" in
  1) ;;
  *) main "$@" ;;
esac
