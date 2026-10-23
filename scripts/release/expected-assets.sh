#!/usr/bin/env bash
# Print, one per line, the exact asset set a published release must carry.
# The finalizers compare a draft against this list before publishing it, so a
# missing, duplicated or unexpected asset blocks the release.
#
# Component releases ship the source's compat.json. A source ref that predates
# it has none to ship: COMPAT_ASSET=0 drops it from the list.
set -euo pipefail

[ "$#" -eq 2 ] || { echo "usage: $0 KIND VERSION" >&2; exit 64; }
KIND=$1
VERSION=$2
case "${COMPAT_ASSET:-1}" in
  1) ;;
  0) COMPAT_ASSET=1 "$0" "$KIND" "$VERSION" | grep -vx compat.json; exit 0 ;;
  *) echo "COMPAT_ASSET must be 0 or 1" >&2; exit 64 ;;
esac

case "$KIND" in
  unified)
    cat <<LIST
Heimdallm-v${VERSION}.dmg
Heimdallm-${VERSION}-x86_64.AppImage
heimdallm_${VERSION}_amd64.deb
heimdallm_${VERSION}_amd64.rpm
daemon-checksums.txt
linux-checksums.txt
linux-checksums.txt.sig
cli-checksums.txt
heimdallm-cli_${VERSION}_darwin_amd64.tar.gz
heimdallm-cli_${VERSION}_darwin_arm64.tar.gz
heimdallm-cli_${VERSION}_linux_amd64.tar.gz
heimdallm-cli_${VERSION}_linux_arm64.tar.gz
heimdallm-cli_${VERSION}_windows_amd64.zip
heimdallm-cli_${VERSION}_windows_arm64.zip
versions.json
LIST
    ;;
  daemon)
    cat <<LIST
heimdalld_${VERSION}_darwin_amd64.tar.gz
heimdalld_${VERSION}_darwin_arm64.tar.gz
heimdalld_${VERSION}_linux_amd64.tar.gz
heimdalld_${VERSION}_linux_arm64.tar.gz
checksums.txt
compat.json
LIST
    ;;
  gui)
    cat <<LIST
Heimdallm-${VERSION}.dmg
Heimdallm-${VERSION}-x86_64.AppImage
heimdallm_${VERSION}_amd64.deb
heimdallm_${VERSION}_amd64.rpm
checksums.txt
compat.json
LIST
    ;;
  cli)
    cat <<LIST
heimdallm-cli_${VERSION}_darwin_amd64.tar.gz
heimdallm-cli_${VERSION}_darwin_arm64.tar.gz
heimdallm-cli_${VERSION}_linux_amd64.tar.gz
heimdallm-cli_${VERSION}_linux_arm64.tar.gz
heimdallm-cli_${VERSION}_windows_amd64.zip
heimdallm-cli_${VERSION}_windows_arm64.zip
checksums.txt
compat.json
LIST
    ;;
  *)
    echo "unknown release kind: $KIND" >&2
    exit 2
    ;;
esac
