#!/bin/sh
#
# Escapes a literal path so it can be used as a pkill/pgrep -f pattern.
#
# `pkill -f` and `pgrep -f` treat their argument as an ERE, so a path containing
# regex metacharacters does not match itself: a $HOME or checkout directory with
# `+`, `(` or `[` in it makes the pattern silently miss, and the Makefile's
# daemon-stop leaves the old daemon running with no error at all.
#
# Escaping runs in two passes because a single bracket expression cannot hold
# both `\` and the rest of the metacharacters, and because `[.` inside a bracket
# expression is read as the start of a POSIX collating symbol — hence `[` sits
# last in the class. Backslashes go first so the escapes added by the second pass
# are not escaped again.
#
# Used by the Makefile's dev-stop / install-linux / uninstall-linux and asserted
# by scripts/test-linux-install.sh, so both share one implementation.
#
# Usage: pkill-escape.sh <literal-path>

set -eu

if [ "$#" -ne 1 ]; then
  echo "usage: $(basename "$0") <literal-path>" >&2
  exit 2
fi

# sed is line-oriented, so a path containing an embedded newline would be
# escaped per line and the resulting pattern could never match the target's
# cmdline — pkill would silently no-op, which is the failure this script exists to
# prevent. Refuse it loudly instead.
# Counted rather than glob-matched: "$(printf '\n')" collapses to an empty
# string in a command substitution, so `case $1 in *""*)` matches everything.
# printf '%s' adds no trailing newline, so wc -l counts only embedded ones.
if [ "$(printf '%s' "$1" | wc -l)" -gt 0 ]; then
  echo "$(basename "$0"): refusing a path containing a newline" >&2
  exit 2
fi

printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/[]^$.*+?(){}|[]/\\&/g'
