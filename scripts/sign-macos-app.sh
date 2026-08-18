#!/bin/bash
# Sign a Flutter + Sparkle app from the inside out with Developer ID.
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

sign_nested() {
  local code_path=$1
  if /usr/bin/codesign --display "$code_path" >/dev/null 2>&1; then
    # Sparkle's Downloader.xpc carries security-sensitive entitlements. Never
    # preserve upstream designated requirements when replacing its signer:
    # those requirements describe the old identity and can invalidate the new
    # signature chain. This follows Sparkle's manual code-signing guidance.
    if [[ "$code_path" == */Downloader.xpc ]]; then
      /usr/bin/codesign \
        --force \
        --timestamp \
        --options runtime \
        --preserve-metadata=entitlements \
        "${keychain_args[@]}" \
        --sign "$signing_identity" \
        "$code_path"
      return
    fi
    /usr/bin/codesign \
      --force \
      --timestamp \
      --options runtime \
      --preserve-metadata=identifier,entitlements \
      "${keychain_args[@]}" \
      --sign "$signing_identity" \
      "$code_path"
    return
  fi
  /usr/bin/codesign \
    --force \
    --timestamp \
    --options runtime \
    "${keychain_args[@]}" \
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
# executable, dylibs, and the executables nested inside Sparkle helpers.
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
  --timestamp \
  --options runtime \
  --entitlements "$entitlements" \
  "${keychain_args[@]}" \
  --sign "$signing_identity" \
  "$app_bundle"

/usr/bin/codesign --verify --deep --strict --verbose=4 "$app_bundle"
