#!/bin/sh
# Print file resources declared by flutter.assets, flutter.fonts and
# flutter.shaders in pubspec.yaml.
#
# Default output uses Flutter-relative paths, one per line. With
# --dockerignore, output the exact negation rules required by the repository-
# root Docker context. The context check and image smoke both consume this
# parser so pubspec.yaml remains the single source of truth.
#
# Directory entries and complex YAML values are rejected deliberately. Resource
# paths must be simple, safe relative file names so they cannot turn a literal
# Docker ignore allowlist into a glob.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
output_mode=paths

if [ "${1:-}" = "--dockerignore" ]; then
  output_mode=dockerignore
  shift
fi
if [ "$#" -gt 1 ]; then
  printf 'Usage: %s [--dockerignore] [pubspec.yaml]\n' "$0" >&2
  exit 1
fi

pubspec="${1:-$script_dir/../../flutter_app/pubspec.yaml}"

if [ ! -f "$pubspec" ]; then
  printf 'Flutter pubspec not found: %s\n' "$pubspec" >&2
  exit 1
fi

awk -v output_mode="$output_mode" '
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
  print "invalid Flutter resource declaration: " message > "/dev/stderr"
  failed = 1
  exit 1
}

function scalar_value(raw, location, value, quote) {
  value = trim(raw)
  quote = substr(value, 1, 1)

  if (quote == "\"" || quote == "\047") {
    sub(/[ \t]+#.*/, "", value)
    value = trim(value)
    if (length(value) < 2 || substr(value, length(value), 1) != quote) {
      invalid("unterminated quoted value at " location)
    }
    value = substr(value, 2, length(value) - 2)
  } else {
    sub(/[ \t]+#.*/, "", value)
    value = trim(value)
  }
  return value
}

function validate_path(value, kind, location) {
  if (value == "") {
    invalid(kind " path is empty at " location)
  }
  if (value ~ /^\//) {
    invalid("\047" value "\047 must be relative at " location)
  }
  if (value ~ /(^|\/)\.\.?($|\/)/) {
    invalid("\047" value "\047 must not contain dot path segments at " location)
  }
  if (value ~ /\/$/) {
    invalid("\047" value "\047 is a directory; declare individual files")
  }
  if (value ~ /[^A-Za-z0-9_@.+\/-]/) {
    invalid("\047" value "\047 contains characters unsafe for a literal Docker allowlist")
  }
  if (value ~ /(^|\/)\.env([.]|$)/ ||
      value ~ /[.](pem|key|crt|p12|pfx)$/) {
    invalid("\047" value "\047 matches a protected secret-file pattern")
  }
}

function add_resource(raw, kind, location, value) {
  value = scalar_value(raw, location)
  validate_path(value, kind, location)
  if (seen[value]++) {
    invalid("\047" value "\047 is declared more than once")
  }
  resources[++resource_count] = value
  if (kind == "asset") {
    asset_count++
  } else if (kind == "font") {
    font_count++
  } else if (kind == "shader") {
    shader_count++
  }
}

function finish_font_family(location) {
  if (font_family_seen && current_family_asset_count == 0) {
    invalid("font family declared at " font_family_location \
      " has no supported asset entries before " location)
  }
  font_family_seen = 0
  current_family_asset_count = 0
  font_family_location = ""
}

function emit_dockerignore(path, context_path, count, pieces, parent, i, rule) {
  context_path = path
  if (path !~ /^assets\//) {
    context_path = "flutter_app/" path
  }

  count = split(context_path, pieces, "/")
  parent = pieces[1]
  for (i = 2; i < count; i++) {
    parent = parent "/" pieces[i]
    rule = "!" parent "/"
    if (!emitted_rule[rule]++) {
      print rule
      print parent "/**"
    }
  }

  rule = "!" context_path
  if (!emitted_rule[rule]++) {
    print rule
  }
}

BEGIN {
  in_flutter = 0
  child_indent_set = 0
  section = ""
  found_flutter = 0
  found_assets = 0
  found_fonts = 0
  found_shaders = 0
  asset_count = 0
  font_count = 0
  shader_count = 0
  resource_count = 0
  font_family_seen = 0
  current_family_asset_count = 0
  failed = 0
}

/^[ \t]*(#|$)/ {
  next
}

{
  line = $0
  current_indent = indentation(line)
  stripped = line
  sub(/^[ \t]*/, "", stripped)

  if (!in_flutter) {
    if (current_indent == 0 && line ~ /^flutter:[ \t]*(#.*)?$/) {
      in_flutter = 1
      child_indent_set = 0
      section = ""
      found_flutter = 1
      flutter_indent = current_indent
    }
    next
  }

  if (current_indent <= flutter_indent) {
    if (section == "fonts" && !failed) {
      finish_font_family(FILENAME ":" FNR)
    }
    in_flutter = 0
    section = ""
    next
  }

  if (!child_indent_set) {
    child_indent = current_indent
    child_indent_set = 1
  }
  if (current_indent < child_indent) {
    invalid("inconsistent indentation at " FILENAME ":" FNR)
  }

  if (current_indent == child_indent) {
    if (section == "fonts" && !failed) {
      finish_font_family(FILENAME ":" FNR)
    }
    section = ""

    if (stripped ~ /^assets[ \t]*:/) {
      if (stripped !~ /^assets[ \t]*:[ \t]*(#.*)?$/) {
        invalid("inline flutter.assets values are unsupported at " FILENAME ":" FNR)
      }
      section = "assets"
      found_assets = 1
    } else if (stripped ~ /^fonts[ \t]*:/) {
      if (stripped !~ /^fonts[ \t]*:[ \t]*(#.*)?$/) {
        invalid("inline flutter.fonts values are unsupported at " FILENAME ":" FNR)
      }
      section = "fonts"
      found_fonts = 1
      font_family_seen = 0
      current_family_asset_count = 0
    } else if (stripped ~ /^shaders[ \t]*:/) {
      if (stripped !~ /^shaders[ \t]*:[ \t]*(#.*)?$/) {
        invalid("inline flutter.shaders values are unsupported at " FILENAME ":" FNR)
      }
      section = "shaders"
      found_shaders = 1
    }
    next
  }

  if (section == "assets" || section == "shaders") {
    if (stripped !~ /^-[ \t]+/) {
      invalid("expected a scalar flutter." section " list item at " FILENAME ":" FNR)
    }
    value = stripped
    sub(/^-[ \t]+/, "", value)
    kind = section == "assets" ? "asset" : "shader"
    add_resource(value, kind, FILENAME ":" FNR)
    next
  }

  if (section == "fonts") {
    if (stripped ~ /^-[ \t]+family[ \t]*:/) {
      if (font_family_seen) {
        finish_font_family(FILENAME ":" FNR)
      }
      font_family_seen = 1
      current_family_asset_count = 0
      font_family_location = FILENAME ":" FNR
    } else if (stripped ~ /^fonts[ \t]*:/) {
      if (!font_family_seen) {
        invalid("font asset list has no family at " FILENAME ":" FNR)
      }
      if (stripped !~ /^fonts[ \t]*:[ \t]*(#.*)?$/) {
        invalid("inline font asset lists are unsupported at " FILENAME ":" FNR)
      }
    } else if (stripped ~ /^-[ \t]+asset[ \t]*:/) {
      if (!font_family_seen) {
        invalid("font asset has no family at " FILENAME ":" FNR)
      }
      value = stripped
      sub(/^-[ \t]+asset[ \t]*:[ \t]*/, "", value)
      add_resource(value, "font", FILENAME ":" FNR)
      current_family_asset_count++
    } else if (stripped ~ /^asset[ \t]*:/) {
      invalid("font asset must be a list item at " FILENAME ":" FNR)
    } else if (stripped ~ /^(style|weight)[ \t]*:/) {
      next
    } else {
      invalid("unsupported flutter.fonts entry at " FILENAME ":" FNR)
    }
  }
}

END {
  if (failed) {
    exit 1
  }
  if (section == "fonts") {
    finish_font_family("end of file")
  }
  if (!found_flutter) {
    print "pubspec has no top-level flutter section: " FILENAME > "/dev/stderr"
    exit 1
  }
  if (found_assets && asset_count == 0) {
    print "pubspec has flutter.assets but no scalar file entries: " FILENAME > "/dev/stderr"
    exit 1
  }
  if (found_fonts && font_count == 0) {
    print "pubspec has flutter.fonts but no supported font asset entries: " FILENAME > "/dev/stderr"
    exit 1
  }
  if (found_shaders && shader_count == 0) {
    print "pubspec has flutter.shaders but no scalar file entries: " FILENAME > "/dev/stderr"
    exit 1
  }
  if (resource_count == 0) {
    print "pubspec has no supported Flutter file resources: " FILENAME > "/dev/stderr"
    exit 1
  }

  for (i = 1; i <= resource_count; i++) {
    if (output_mode == "dockerignore") {
      emit_dockerignore(resources[i])
    } else {
      print resources[i]
    }
  }
}
' "$pubspec"
