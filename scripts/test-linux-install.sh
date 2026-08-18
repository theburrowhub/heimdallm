#!/bin/sh
#
# Regression guard for the pkill invocations in the Makefile's Linux targets.
#
# Two failure modes, both silent:
#
# 1. `pkill -f <pattern>` matches against /proc/<pid>/cmdline. Inside a make
#    recipe that contains shell metacharacters, make runs the line via `sh -c`,
#    whose own cmdline embeds the pattern — so pkill signals the shell running it
#    and the recipe dies mid-target. Nothing in the output makes this visible:
#    the target simply stops, having already printed its progress lines. That
#    broke `make dev` (dev-stop aborted before launching anything) and
#    `make uninstall-linux` (aborted before removing the desktop entry, icons and
#    PATH symlink), and would have broken install-linux's daemon stop (#661).
#    The fix is `-x`, which requires the whole cmdline to equal the pattern.
#
# 2. The pattern is an ERE, so a path containing regex metacharacters (`+`, `(`,
#    `[` — all legal in a $HOME or a checkout directory) fails to match and
#    leaves the process running, with no error. The fix is PKILL_ESCAPE.
#
# Both are asserted behaviourally, not by inspection, because neither leaves any
# trace in normal output.
#
# The static half also ties the Makefile to the invariant it depends on: `-x` only
# matches while the daemon is exec'd with an empty argv, and that lives in Dart.
# The static tests run everywhere; only the behavioural half needs Linux.

set -eu

SCRIPT_DIR=$(CDPATH= cd "$(dirname "$0")" && /bin/pwd -P)
REPO_ROOT=$(CDPATH= cd "$SCRIPT_DIR/.." && /bin/pwd -P)
MAKEFILE="$REPO_ROOT/Makefile"

TEST_COUNT=0
FAIL_COUNT=0

pass() {
  TEST_COUNT=$((TEST_COUNT + 1))
  printf 'ok %d - %s\n' "$TEST_COUNT" "$1"
}

skip() {
  TEST_COUNT=$((TEST_COUNT + 1))
  printf 'ok %d - %s # SKIP %s\n' "$TEST_COUNT" "$1" "$2"
}

fail() {
  TEST_COUNT=$((TEST_COUNT + 1))
  FAIL_COUNT=$((FAIL_COUNT + 1))
  printf 'not ok %d - %s\n' "$TEST_COUNT" "$1" >&2
  if [ "$#" -gt 1 ]; then
    printf '  %s\n' "$2" >&2
  fi
}

# The behavioural tests read /proc and rely on procps-style pkill semantics.
# pkill and pgrep both exist on macOS/BSD — the platform this repo is primarily
# developed on — while /proc does not. Only that half is gated: the static checks
# below are portable, and they are the ones that catch a dropped -x or a changed
# spawn site, which matters most on the platform where the rest cannot run.
BEHAVIOURAL=yes
SKIP_WHY=""
if [ "$(uname -s)" != Linux ] || [ ! -d /proc ]; then
  BEHAVIOURAL=no
  SKIP_WHY="requires Linux /proc (pkill -f semantics under test)"
elif ! command -v pkill >/dev/null 2>&1 || ! command -v pgrep >/dev/null 2>&1; then
  BEHAVIOURAL=no
  SKIP_WHY="pkill/pgrep unavailable"
fi

WORK=$(mktemp -d)
: > "$WORK/writers"
cleanup() {
  while read -r w; do kill "$w" 2>/dev/null || true; done < "$WORK/writers"
  rm -rf "$WORK"
}
trap cleanup EXIT

# Starts a process whose cmdline is EXACTLY $1 and echoes its pid. /bin/cat on a
# fifo reproduces the daemon's shape: a real binary with argv = [path], blocked
# rather than spinning. A shell script would not — its cmdline would carry the
# interpreter, could never match -x, and every assertion below would pass
# vacuously. The writer outlives the assertions by an hour, and callers re-check
# the process is alive immediately before acting, so a loaded machine cannot turn
# an early exit into a false pass.
start_fixture() {
  _sf_path=$1
  mkdir -p "$(dirname "$_sf_path")"
  cp /bin/cat "$_sf_path"
  chmod +x "$_sf_path"
  # mktemp, not a counter: this runs inside a command substitution, so a shell
  # variable incremented here would not survive into the next call.
  _sf_fifo=$(mktemp -u "$WORK/fifo.XXXXXX")
  mkfifo "$_sf_fifo"
  sleep 3600 > "$_sf_fifo" &
  echo "$!" >> "$WORK/writers"
  "$_sf_path" < "$_sf_fifo" > /dev/null &
  _sf_pid=$!
  # Poll rather than sleep: a fixed sleep is a race on a loaded runner.
  _sf_i=0
  while [ "$_sf_i" -lt 50 ] && [ ! -r "/proc/$_sf_pid/cmdline" ]; do
    _sf_i=$((_sf_i + 1))
    sleep 0.1
  done
  echo "$_sf_pid"
}

cmdline_of() {
  tr '\0' '\n' < "/proc/$1/cmdline" 2>/dev/null | head -1
}

# Waits up to 3s for $1 to exit; returns 0 if it did.
wait_gone() {
  _wg_i=0
  while [ "$_wg_i" -lt 30 ]; do
    kill -0 "$1" 2>/dev/null || return 0
    _wg_i=$((_wg_i + 1))
    sleep 0.1
  done
  ! kill -0 "$1" 2>/dev/null
}

# ── 1. Static: every pkill/pgrep that targets a process must pass -x ──────────
#
# Flag-order independent: -x may appear anywhere among the invocation's flags,
# bundled (-xf) or spelled --exact. Comment lines are excluded — the recipes
# mention plain `pkill -f` on purpose, to explain why -x is required.
# Every invocation on the line is checked, not just the last: extracting with a
# greedy `.*` prefix would inspect only the final one, so a line carrying two
# calls could hide a bare `pkill -f` behind a correct neighbour.
#
# Quoted arguments are blanked out BEFORE extracting, for two reasons: an
# extraction that stops at the first double quote would miss a flag written after
# the pattern (`pkill -f "$PAT" -x`, legal under GNU getopt permutation) and
# report it as an offender; and a bare `*-x*` glob over the raw text would accept
# an "-x" appearing inside an argument rather than as a flag. Flags are then
# matched as whole tokens beginning with "-".
offenders=$(
  grep -nE '(pkill|pgrep)[[:space:]]' "$MAKEFILE" |
    grep -vE '^[0-9]+:[[:space:]]*@?#' |
    while IFS= read -r line; do
      # Replace every "…" with a placeholder token so quoted patterns cannot
      # contribute flag-looking text, and cannot terminate the extraction.
      unquoted=$(printf '%s\n' "$line" | sed 's/"[^"]*"/QUOTED/g')
      printf '%s\n' "$unquoted" | grep -oE '(pkill|pgrep)[^;|&]*' |
        while IFS= read -r invocation; do
          exact=no
          for token in $invocation; do
            case "$token" in
              --exact) exact=yes ;;
              --*) ;;
              -*x*) exact=yes ;;
            esac
          done
          [ "$exact" = yes ] || printf '%s\n' "$line"
        done
    done
)
if [ ! -r "$MAKEFILE" ]; then
  fail 'the Makefile is not readable' "$MAKEFILE — the -x check would pass vacuously"
elif [ -z "$(grep -cE '(pkill|pgrep)[[:space:]]' "$MAKEFILE")" ] ||
  [ "$(grep -cE '(pkill|pgrep)[[:space:]]' "$MAKEFILE")" -eq 0 ]; then
  fail 'no pkill/pgrep invocation found in the Makefile' \
    'the -x check would pass vacuously; update this guard if the calls moved'
elif [ -z "$offenders" ]; then
  pass 'every pkill/pgrep invocation in the Makefile passes -x'
else
  fail 'a pkill/pgrep invocation is missing -x (it will kill its own recipe shell)' \
    "$offenders"
fi

# ── 2. Static: the spawn site must keep passing an empty argument list ────────
#
# `-x` requires the target's whole cmdline to equal the binary path, which holds
# only while the daemon is exec'd with no arguments. That invariant lives
# in Dart, far from the Makefile that depends on it: if a future change adds a
# flag to the spawn, `pkill -x -f` silently stops matching and install-linux
# again leaves the old daemon running with no error. Comments cannot catch that,
# so it is asserted here.
SPAWN_SITES="flutter_app/lib/core/platform/platform_services_desktop.dart"
spawn_offenders=""
for rel in $SPAWN_SITES; do
  f="$REPO_ROOT/$rel"
  if [ ! -f "$f" ]; then
    spawn_offenders="$spawn_offenders$rel: file not found
"
    continue
  fi
  # Read the canonical daemon call from one logical line so wrapping does not
  # affect the assertion. Other Process.start sites in this file are allowed:
  # the platform command runner intentionally passes arguments to launchctl.
  # `binaryPath` identifies the one detached daemon boundary whose empty argv
  # is coupled to the Makefile's pkill -x pattern.
  joined=$(tr '\n' ' ' < "$f" | sed 's/  */ /g')
  daemon_calls=$(printf '%s\n' "$joined" |
    grep -oE 'Process\.start\([[:space:]]*binaryPath,[^)]*' || true)
  call_count=$(printf '%s\n' "$daemon_calls" | sed '/^[[:space:]]*$/d' | wc -l | tr -d '[:space:]')
  if [ "${call_count:-0}" -ne 1 ]; then
    printf '%s: found %s daemon Process.start(binaryPath, ...) calls, want exactly 1\n' \
      "$rel" "${call_count:-0}" >> "$WORK/spawn_offenders"
    continue
  fi
  case "$daemon_calls" in
    *", []"*|*", const []"*) ;;
    *) printf '%s: %s\n' "$rel" "$daemon_calls" >> "$WORK/spawn_offenders" ;;
  esac
done
[ -f "$WORK/spawn_offenders" ] || : > "$WORK/spawn_offenders"
spawn_offenders="$spawn_offenders$(cat "$WORK/spawn_offenders")"
if [ -n "$(printf '%s' "$spawn_offenders" | tr -d '[:space:]')" ]; then
  fail 'the daemon spawn breaks the pkill -x contract (arguments, or site moved)' \
    "$spawn_offenders"
else
  pass 'the daemon spawn site still passes an empty argument list'
fi

# ── Behavioural half (tests 3-7): gated on Linux + /proc ─────────────────────
if [ "$BEHAVIOURAL" = no ]; then
  skip 'pkill -f alone kills its own recipe shell — -x is load-bearing' "$SKIP_WHY"
  skip 'pkill -x -f leaves the recipe shell alone' "$SKIP_WHY"
  skip 'pkill -x -f signals a process whose cmdline is exactly the path' "$SKIP_WHY"
  skip 'an unescaped metacharacter path fails to match (escaping is needed)' "$SKIP_WHY"
  skip 'PKILL_ESCAPE makes a metacharacter path match' "$SKIP_WHY"
  skip 'the escaped pattern does not match an unrelated path' "$SKIP_WHY"
  printf '1..%d\n' "$TEST_COUNT"
  # Honours FAIL_COUNT: an early exit that ignored it would let a failing static
  # test report success on every non-Linux machine.
  if [ "$FAIL_COUNT" -gt 0 ]; then
    printf '# %d of %d tests failed\n' "$FAIL_COUNT" "$TEST_COUNT" >&2
    exit 1
  fi
  printf '# static tests passed; behavioural tests skipped (%s)\n' "$SKIP_WHY"
  exit 0
fi

# ── 3. Behavioural: -f alone kills the recipe, -x -f does not ─────────────────
#
# No target process exists here on purpose: the self-match is what ends the
# recipe, so the bug reproduces against nothing at all.
#
# PAT goes through the ENVIRONMENT, not make's command line: a command-line
# `PAT=…` lands in make's own /proc cmdline, so `pkill -f` would match the make
# process too and the test would only prove "something in the chain self-matched"
# rather than pinning it on the recipe shell.
mkdir -p "$WORK/opt"
: > "$WORK/opt/heimdalld"

cat > "$WORK/Makefile" <<EOF
without-x:
	@pkill -f "\$(PAT)" 2>/dev/null || true
	@echo RECIPE-COMPLETED

with-x:
	@pkill -x -f "\$(PAT)" 2>/dev/null || true
	@echo RECIPE-COMPLETED
EOF

out=$(PAT="$WORK/opt/heimdalld" make -C "$WORK" without-x 2>/dev/null || true)
case "$out" in
  *RECIPE-COMPLETED*)
    # BusyBox and other non-procps pkill may not match ancestors this way. The
    # -x fix stays correct; this environment just cannot demonstrate the bug.
    skip 'pkill -f alone kills its own recipe shell — -x is load-bearing' \
      'this pkill does not self-match the recipe shell' ;;
  *)
    pass 'pkill -f alone kills its own recipe shell — -x is load-bearing' ;;
esac

out=$(PAT="$WORK/opt/heimdalld" make -C "$WORK" with-x 2>/dev/null || true)
case "$out" in
  *RECIPE-COMPLETED*) pass 'pkill -x -f leaves the recipe shell alone' ;;
  *) fail 'pkill -x -f killed its own recipe shell' "output: $out" ;;
esac

# ── 4. Behavioural: -x -f signals the intended process ───────────────────────
TARGET=$(start_fixture "$WORK/opt/heimdalld")
actual=$(cmdline_of "$TARGET")
if [ "$actual" != "$WORK/opt/heimdalld" ]; then
  fail 'fixture cmdline is not exactly the binary path' "got: '$actual'"
elif ! kill -0 "$TARGET" 2>/dev/null; then
  fail 'fixture exited before the assertion' 'cannot verify -x matches the daemon shape'
else
  PAT="$WORK/opt/heimdalld" make -C "$WORK" with-x >/dev/null 2>&1 || true
  if wait_gone "$TARGET"; then
    pass 'pkill -x -f signals a process whose cmdline is exactly the path'
  else
    fail 'pkill -x -f did not signal a process whose cmdline is exactly the path' \
      'install-linux would leave the previous daemon running'
  fi
fi

# ── 5. Behavioural: the Makefile's escaping survives regex metacharacters ────
#
# Calls the same scripts/pkill-escape.sh the Makefile uses, so this tracks the
# real implementation rather than a copy of it.
ESCAPE="$SCRIPT_DIR/pkill-escape.sh"
if [ ! -x "$ESCAPE" ]; then
  fail 'scripts/pkill-escape.sh is missing or not executable' 'escaping is unverified'
else
  META="$WORK/we+ird(dir)[x]/opt/heimdalld"
  TARGET=$(start_fixture "$META")
  if ! kill -0 "$TARGET" 2>/dev/null; then
    fail 'metacharacter fixture did not start' 'escaping is unverified'
  else
    if pgrep -x -f "$META" >/dev/null 2>&1; then
      skip 'an unescaped metacharacter path fails to match' \
        'this pgrep matched it literally, so escaping cannot be demonstrated'
    else
      pass 'an unescaped metacharacter path fails to match (escaping is needed)'
    fi

    META_RE=$("$ESCAPE" "$META")
    if pgrep -x -f "$META_RE" >/dev/null 2>&1; then
      pass 'PKILL_ESCAPE makes a metacharacter path match'
    else
      fail 'PKILL_ESCAPE does not match a path containing regex metacharacters' \
        "pattern: $META_RE"
    fi

    OTHER_RE=$("$ESCAPE" "$WORK/other/heimdalld")
    if pgrep -x -f "$OTHER_RE" >/dev/null 2>&1; then
      fail 'the escaped pattern matched an unrelated path' 'escaping is too loose'
    else
      pass 'the escaped pattern does not match an unrelated path'
    fi
    kill "$TARGET" 2>/dev/null || true
  fi
fi

printf '1..%d\n' "$TEST_COUNT"
if [ "$FAIL_COUNT" -gt 0 ]; then
  printf '# %d of %d tests failed\n' "$FAIL_COUNT" "$TEST_COUNT" >&2
  exit 1
fi
printf '# all %d tests passed\n' "$TEST_COUNT"
