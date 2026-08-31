#!/bin/bash
# Seal the macOS app from the inside out with Developer ID or an ad-hoc
# identity so the bundle remains launchable after embedding the daemon.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 APP_BUNDLE SIGNING_IDENTITY ENTITLEMENTS" >&2
  exit 64
fi

app_bundle=$1
signing_identity=$2
entitlements=$3

[[ -d "$app_bundle" ]] || { echo "app bundle not found: $app_bundle" >&2; exit 66; }
[[ -f "$entitlements" ]] || { echo "entitlements not found: $entitlements" >&2; exit 66; }
[[ -n "$signing_identity" ]] || { echo "signing identity is empty" >&2; exit 64; }

keychain_args=()
if [[ -n "${SIGNING_KEYCHAIN:-}" ]]; then
  [[ -f "$SIGNING_KEYCHAIN" ]] || {
    echo "signing keychain not found: $SIGNING_KEYCHAIN" >&2
    exit 66
  }
  keychain_args=(--keychain "$SIGNING_KEYCHAIN")
fi

timestamp_args=(--timestamp)
runtime_args=(--options runtime)
if [[ "$signing_identity" == "-" ]]; then
  timestamp_args=(--timestamp=none)
  # Hardened runtime's library validation requires a real Team ID. Ad-hoc
  # identities have none, so enabling it makes dyld reject bundled frameworks.
  runtime_args=()
fi

# macOS still ships Bash 3.2, where expanding an empty array under `set -u`
# fails. These guarded forms expand to zero arguments when an option is unused.

sign_nested() {
  local code_path=$1
  if /usr/bin/codesign --display "$code_path" >/dev/null 2>&1; then
    /usr/bin/codesign \
      --force \
      "${timestamp_args[@]}" \
      "${runtime_args[@]+"${runtime_args[@]}"}" \
      --preserve-metadata=identifier,entitlements \
      "${keychain_args[@]+"${keychain_args[@]}"}" \
      --sign "$signing_identity" \
      "$code_path"
    return
  fi
  /usr/bin/codesign \
    --force \
    "${timestamp_args[@]}" \
    "${runtime_args[@]+"${runtime_args[@]}"}" \
    "${keychain_args[@]+"${keychain_args[@]}"}" \
    --sign "$signing_identity" \
    "$code_path"
}

bundle_contains_macho() {
  local bundle=$1
  local candidate
  while IFS= read -r -d '' candidate; do
    if /usr/bin/file -b "$candidate" | /usr/bin/grep -q 'Mach-O'; then
      return 0
    fi
  done < <(/usr/bin/find "$bundle" -type f -print0)
  return 1
}

# Sign every Mach-O first. This includes the bundled Go daemon, Flutter's main
# executable and dylibs.
while IFS= read -r -d '' candidate; do
  if /usr/bin/file -b "$candidate" | /usr/bin/grep -q 'Mach-O'; then
    sign_nested "$candidate"
  fi
done < <(/usr/bin/find "$app_bundle/Contents" -type f -print0)

# Seal nested code containers deepest-first after their contents are signed.
while IFS= read -r -d '' container; do
  # Privacy manifests are resource-only bundles. Their parent framework seals
  # them; treating them as standalone code trips Bash 3.2's empty-array path
  # and creates a meaningless signature.
  if [[ "$container" == *.bundle ]] && ! bundle_contains_macho "$container"; then
    continue
  fi
  sign_nested "$container"
done < <(
  /usr/bin/find "$app_bundle/Contents" -depth -type d \
    \( -name '*.framework' -o -name '*.xpc' -o -name '*.app' \
       -o -name '*.appex' -o -name '*.bundle' \) -print0
)

# The outer signature deliberately supplies only the app's reviewed release
# entitlements. --deep is verification-only; it is not used as a signing crutch.
/usr/bin/codesign \
  --force \
  "${timestamp_args[@]}" \
  "${runtime_args[@]+"${runtime_args[@]}"}" \
  --entitlements "$entitlements" \
  "${keychain_args[@]+"${keychain_args[@]}"}" \
  --sign "$signing_identity" \
  "$app_bundle"

/usr/bin/codesign --verify --deep --strict --verbose=4 "$app_bundle"
