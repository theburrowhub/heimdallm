#!/bin/sh
#
# Regression guard for the pkill invocations in the Makefile's Linux targets.
#
# `pkill -f <pattern>` matches against /proc/<pid>/cmdline. Inside a make recipe
# that contains shell metacharacters, make runs the line via `sh -c`, whose own
# cmdline embeds the pattern — so pkill signals the shell running it and the
# recipe dies mid-target. Nothing about the output makes this visible: the
# target simply stops, having already printed its progress lines.
#
# That silently broke `make dev` (dev-stop aborted before launching anything)
# and `make uninstall-linux` (aborted before removing the desktop entry, icons
# and PATH symlink), and would have broken install-linux's daemon stop (#661).
#
# The fix is `-x`, which requires the whole cmdline to equal the pattern: the
# daemon and app are spawned with no arguments so they match, while the longer
# shell cmdline cannot. These tests pin that down behaviourally rather than by
# inspection, because the failure mode is invisible in normal output.

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

fail() {
  TEST_COUNT=$((TEST_COUNT + 1))
  FAIL_COUNT=$((FAIL_COUNT + 1))
  printf 'not ok %d - %s\n' "$TEST_COUNT" "$1" >&2
  if [ "$#" -gt 1 ]; then
    printf '  %s\n' "$2" >&2
  fi
}

if ! command -v pkill >/dev/null 2>&1 || ! command -v pgrep >/dev/null 2>&1; then
  printf '1..0 # SKIP pkill/pgrep unavailable\n'
  exit 0
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

# ── 1. Static: every pkill/pgrep that targets a process must pass -x ──────────
#
# Matches invocations only (not the explanatory comments, which mention `pkill
# -f` deliberately). Comment lines in these recipes start with `@#` or `#`.
offenders=$(
  grep -nE '^[[:space:]]*@?[^#]*(pkill|pgrep)[[:space:]]' "$MAKEFILE" |
    grep -vE '(pkill|pgrep)[[:space:]]+(-[a-zA-Z]*x[a-zA-Z]*[[:space:]])' ||
    true
)
if [ -z "$offenders" ]; then
  pass 'every pkill/pgrep invocation in the Makefile passes -x'
else
  fail 'a pkill/pgrep invocation is missing -x (it will kill its own recipe shell)' \
    "$offenders"
fi

# ── 2. Behavioural: -f alone kills the recipe, -x -f does not ─────────────────
#
# No target process exists here on purpose: the self-match is what ends the
# recipe, so the bug reproduces against nothing at all.
mkdir -p "$WORK/opt"
FAKE="$WORK/opt/heimdalld"
: > "$FAKE"

cat > "$WORK/Makefile" <<EOF
without-x:
	@pkill -f "$FAKE" 2>/dev/null || true
	@echo RECIPE-COMPLETED

with-x:
	@pkill -x -f "$FAKE" 2>/dev/null || true
	@echo RECIPE-COMPLETED
EOF

out=$(make -C "$WORK" without-x 2>/dev/null || true)
case "$out" in
  *RECIPE-COMPLETED*)
    fail 'pkill -f should self-match the recipe shell (test is no longer proving anything)' \
      "recipe survived; this environment's pkill may differ" ;;
  *)
    pass 'pkill -f alone kills its own recipe shell — -x is load-bearing' ;;
esac

out=$(make -C "$WORK" with-x 2>/dev/null || true)
case "$out" in
  *RECIPE-COMPLETED*) pass 'pkill -x -f leaves the recipe shell alone' ;;
  *) fail 'pkill -x -f killed its own recipe shell' "output: $out" ;;
esac

# ── 3. Behavioural: -x -f still signals the intended process ─────────────────
#
# The daemon's cmdline is exactly its absolute path (spawned with no arguments).
# /bin/cat reading a fifo reproduces that shape: a real binary, argv = [path],
# blocked rather than spinning. A shell script would not — its cmdline carries
# the interpreter, so it could never match -x and the test would pass vacuously.
cp /bin/cat "$FAKE"
chmod +x "$FAKE"
mkfifo "$WORK/fifo"
sleep 30 > "$WORK/fifo" &
WRITER=$!
"$FAKE" < "$WORK/fifo" > /dev/null &
TARGET=$!
sleep 1

if ! kill -0 "$TARGET" 2>/dev/null; then
  fail 'fixture process did not start' 'cannot verify -x matches the daemon shape'
else
  actual=$(tr '\0' ' ' < "/proc/$TARGET/cmdline" 2>/dev/null | sed 's/ *$//' || echo '')
  if [ "$actual" != "$FAKE" ]; then
    fail 'fixture cmdline is not exactly the binary path' "got: '$actual'"
  else
    make -C "$WORK" with-x >/dev/null 2>&1 || true
    sleep 1
    if kill -0 "$TARGET" 2>/dev/null; then
      fail 'pkill -x -f did not signal a process whose cmdline is exactly the path' \
        'install-linux would leave the previous daemon running'
    else
      pass 'pkill -x -f signals a process whose cmdline is exactly the path'
    fi
  fi
fi
kill "$TARGET" "$WRITER" 2>/dev/null || true
wait "$TARGET" "$WRITER" 2>/dev/null || true

printf '1..%d\n' "$TEST_COUNT"
if [ "$FAIL_COUNT" -gt 0 ]; then
  printf '# %d of %d tests failed\n' "$FAIL_COUNT" "$TEST_COUNT" >&2
  exit 1
fi
printf '# all %d tests passed\n' "$TEST_COUNT"
