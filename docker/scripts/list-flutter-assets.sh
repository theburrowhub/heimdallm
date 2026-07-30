#!/bin/sh
# Print file resources declared by flutter.assets, flutter.fonts and
# flutter.shaders in pubspec.yaml.
#
# Default output uses Flutter-relative paths, one per line. --context-paths
# maps those resources to repository-root Docker context paths, while
# --dockerignore emits the exact resource rules for that context.
#
# Directory entries and complex YAML values are rejected deliberately. Resource
# paths must be simple, safe relative file names so they cannot turn a literal
# Docker ignore allowlist into a glob. Secret-like names remain denied unless
# their exact public context path is audited in Dockerfile.web.dockerignore.
set -eu

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
output_mode=paths
output_mode_set=0
pubspec="$script_dir/../../flutter_app/pubspec.yaml"
pubspec_set=0
dockerignore_file="$script_dir/../../flutter_app/Dockerfile.web.dockerignore"
public_allowlist_begin="# BEGIN flutter-public-secret-like-resource-allowlist"
public_allowlist_end="# END flutter-public-secret-like-resource-allowlist"

usage() {
  printf '%s\n' \
    "Usage: $0 [--dockerignore|--context-paths]" \
    "       [--dockerignore-file Dockerfile.web.dockerignore] [pubspec.yaml]" >&2
  exit 1
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dockerignore)
      [ "$output_mode_set" -eq 0 ] || usage
      output_mode=dockerignore
      output_mode_set=1
      shift
      ;;
    --context-paths)
      [ "$output_mode_set" -eq 0 ] || usage
      output_mode=context-paths
      output_mode_set=1
      shift
      ;;
    --dockerignore-file)
      [ "$#" -ge 2 ] || usage
      dockerignore_file="$2"
      shift 2
      ;;
    -*)
      usage
      ;;
    *)
      [ "$pubspec_set" -eq 0 ] || usage
      pubspec="$1"
      pubspec_set=1
      shift
      ;;
  esac
done

if [ ! -f "$pubspec" ]; then
  printf 'Flutter pubspec not found: %s\n' "$pubspec" >&2
  exit 1
fi
if [ ! -f "$dockerignore_file" ]; then
  printf 'Flutter Web Docker ignore policy not found: %s\n' \
    "$dockerignore_file" >&2
  exit 1
fi

if ! public_secret_like_context_paths="$(
  awk \
    -v begin_marker="$public_allowlist_begin" \
    -v end_marker="$public_allowlist_end" '
function invalid(message) {
  print "invalid Flutter public-resource exception block: " message \
    > "/dev/stderr"
  failed = 1
  exit 1
}

$0 == begin_marker {
  if (inside || ++begin_count != 1) {
    invalid("duplicate or nested begin marker")
  }
  inside = 1
  next
}

$0 == end_marker {
  if (!inside || ++end_count != 1) {
    invalid("end marker has no matching begin marker")
  }
  inside = 0
  next
}

inside {
  value = $0
  sub(/^[ \t]+/, "", value)
  sub(/[ \t]+$/, "", value)
  if (value == "" || value ~ /^#/) {
    next
  }
  if (value !~ /^!/) {
    invalid("entries must be exact Docker negation rules beginning with !")
  }
  sub(/^!/, "", value)
  print value
}

END {
  if (failed) {
    exit 1
  }
  if (begin_count != 1 || end_count != 1 || inside) {
    print "invalid Flutter public-resource exception block: expected exactly " \
      "one matched marker pair" > "/dev/stderr"
    exit 1
  }
}
' "$dockerignore_file"
)"; then
  exit 1
fi

awk \
  -v output_mode="$output_mode" \
  -v public_exception_paths="$public_secret_like_context_paths" \
  -v exception_source="$dockerignore_file" \
  -v public_begin="$public_allowlist_begin" \
  -v public_end="$public_allowlist_end" '
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

function validate_literal_path(value, kind, location) {
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
}

function is_secret_like(value) {
  return value ~ /(^|\/)\.env([.]|$)/ ||
    value ~ /[.](pem|key|crt|p12|pfx)$/
}

function to_context_path(path) {
  if (path ~ /^assets\//) {
    return path
  }
  return "flutter_app/" path
}

function validate_public_exception(value) {
  validate_literal_path(value, "public exception", exception_source)
  if (value !~ /^(assets|flutter_app)\//) {
    invalid("\047" value "\047 must be under assets/ or flutter_app/ in " exception_source)
  }
  if (!is_secret_like(value)) {
    invalid("\047" value "\047 is not secret-like and must not be in the public exception block")
  }
  if (public_exception[value]++) {
    invalid("\047" value "\047 is duplicated in the public exception block")
  }
}

function validate_path(value, kind, location, mapped) {
  validate_literal_path(value, kind, location)
  if (is_secret_like(value)) {
    mapped = to_context_path(value)
    if (!(mapped in public_exception)) {
      invalid("\047" value "\047 matches a protected secret-file pattern. " \
        "If it is intentionally public, add the exact rule \047!" mapped \
        "\047 between \047" public_begin "\047 and \047" public_end \
        "\047 in " exception_source)
    }
    used_public_exception[mapped] = 1
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

function emit_dockerignore(path, mapped_path, count, pieces, parent, i, rule) {
  mapped_path = to_context_path(path)
  count = split(mapped_path, pieces, "/")
  parent = pieces[1]
  for (i = 2; i < count; i++) {
    parent = parent "/" pieces[i]
    rule = "!" parent "/"
    if (!emitted_rule[rule]++) {
      print rule
      print parent "/**"
    }
  }

  rule = "!" mapped_path
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

  if (public_exception_paths != "") {
    public_exception_count = split(public_exception_paths, public_exception_entries, "\n")
    for (public_exception_index = 1;
         public_exception_index <= public_exception_count;
         public_exception_index++) {
      validate_public_exception(public_exception_entries[public_exception_index])
    }
  }
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
  for (public_exception_path in public_exception) {
    if (!(public_exception_path in used_public_exception)) {
      invalid("\047" public_exception_path "\047 in " exception_source \
        " does not match a declared protected Flutter resource")
    }
  }

  for (i = 1; i <= resource_count; i++) {
    if (output_mode == "dockerignore") {
      emit_dockerignore(resources[i])
    } else if (output_mode == "context-paths") {
      print to_context_path(resources[i])
    } else {
      print resources[i]
    }
  }
}
' "$pubspec"
