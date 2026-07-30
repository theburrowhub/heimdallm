#!/bin/sh
# Print the file assets declared under flutter.assets in pubspec.yaml.
#
# The web Docker context and image smoke tests both consume this output so
# pubspec.yaml remains the source of truth. Directory entries and complex YAML
# values are rejected deliberately: supporting either requires updating the
# Docker context contract rather than silently widening it.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
pubspec="${1:-$script_dir/../../flutter_app/pubspec.yaml}"

if [ ! -f "$pubspec" ]; then
  printf 'Flutter pubspec not found: %s\n' "$pubspec" >&2
  exit 1
fi

awk '
function indentation(line, stripped) {
  stripped = line
  sub(/^[ \t]*/, "", stripped)
  return length(line) - length(stripped)
}

function trim(value) {
  sub(/^[ \t]+/, "", value)
  sub(/[ \t]+$/, "", value)
  return value
}

function invalid(message) {
  print "invalid flutter.assets entry: " message > "/dev/stderr"
  failed = 1
  exit 1
}

BEGIN {
  in_flutter = 0
  in_assets = 0
  found_flutter = 0
  found_assets = 0
  asset_count = 0
  failed = 0
}

/^[ \t]*(#|$)/ {
  next
}

{
  line = $0
  current_indent = indentation(line)

  if (!in_flutter) {
    if (line ~ /^flutter:[ \t]*(#.*)?$/) {
      in_flutter = 1
      found_flutter = 1
      flutter_indent = current_indent
    }
    next
  }

  if (current_indent <= flutter_indent) {
    in_flutter = 0
    in_assets = 0
    next
  }

  if (!in_assets) {
    if (line ~ /^[ \t]+assets:[ \t]*(#.*)?$/) {
      in_assets = 1
      found_assets = 1
      assets_indent = current_indent
    }
    next
  }

  if (current_indent <= assets_indent) {
    in_assets = 0
    next
  }

  if (line !~ /^[ \t]*-[ \t]+/) {
    invalid("expected a scalar list item in " FILENAME ":" FNR)
  }

  value = line
  sub(/^[ \t]*-[ \t]+/, "", value)
  value = trim(value)
  quote = substr(value, 1, 1)

  if (quote == "\"" || quote == "\047") {
    if (length(value) < 2 || substr(value, length(value), 1) != quote) {
      invalid("unterminated quoted value in " FILENAME ":" FNR)
    }
    value = substr(value, 2, length(value) - 2)
  } else {
    sub(/[ \t]+#.*/, "", value)
    value = trim(value)
  }

  if (value !~ /^assets\//) {
    invalid("\047" value "\047 must be under assets/")
  }
  if (value ~ /(^|\/)\.\.(\/|$)/) {
    invalid("\047" value "\047 must not traverse parent directories")
  }
  if (value ~ /\/$/) {
    invalid("\047" value "\047 is a directory; declare individual files")
  }
  if (seen[value]++) {
    invalid("\047" value "\047 is declared more than once")
  }

  print value
  asset_count++
}

END {
  if (failed) {
    exit 1
  }
  if (!found_flutter) {
    print "pubspec has no top-level flutter section: " FILENAME > "/dev/stderr"
    exit 1
  }
  if (!found_assets || asset_count == 0) {
    print "pubspec has no flutter.assets file entries: " FILENAME > "/dev/stderr"
    exit 1
  }
}
' "$pubspec"
