#!/usr/bin/env bash
# Classify a release tag and print "<kind> <version>".
#
#   v1.2.3          -> unified 1.2.3   (full installer, the `latest` release)
#   daemon/v1.2.3   -> daemon  1.2.3
#   gui/v1.2.3      -> gui     1.2.3
#   cli/v1.2.3      -> cli     1.2.3
#
# Anything else exits 2. Every release job validates its tag through this
# script before using it, so a crafted workflow_dispatch input never reaches a
# shell command or a release API call unvalidated.
set -euo pipefail

[ "$#" -eq 1 ] || { echo "usage: $0 TAG" >&2; exit 64; }
TAG=$1
SEMVER='[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?'

if [[ "$TAG" =~ ^v($SEMVER)$ ]]; then
  echo "unified ${BASH_REMATCH[1]}"
elif [[ "$TAG" =~ ^(daemon|gui|cli)/v($SEMVER)$ ]]; then
  echo "${BASH_REMATCH[1]} ${BASH_REMATCH[2]}"
else
  echo "invalid release tag: $TAG" >&2
  exit 2
fi
