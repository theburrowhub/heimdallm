# `make install-macos` / `make uninstall-macos` (#604)

**Date:** 2026-07-29
**Issue:** [#604 — feat(makefile): add install-macos / uninstall-macos targets](https://github.com/theburrowhub/heimdallm/issues/604)
**Status:** Approved

## Problem

The repo cannot install itself on macOS. `Makefile:586-754` provides
`install-linux` / `uninstall-linux` — user-local, no sudo, driven by Docker —
but the macOS side only has `package-macos` (build a DMG) and `release-local`
(build, sign, notarize, publish). Neither puts an app on the machine.

So the documented macOS path is manual: `curl` the release asset, `hdiutil
attach`, `cp -R` to `/Applications`, `xattr -cr` (`README.md:38-48`,
`LLM-HOW-TO-INSTALL.md` steps 1-3). In practice developers skip it and run
`make dev`, which builds the daemon and launches Flutter in **debug** mode —
a development loop, not an installation.

## Context that shapes the design

- **`VERSION` is already taken.** `Makefile:146` defines it as the *next*
  auto-incremented semver, consumed by `release-local`. A target reusing it
  would resolve to a tag that does not exist yet (today: `v0.7.8`). The new
  targets use `RELEASE`, empty meaning "latest".
- **The repo is public**, so `api.github.com/.../releases/latest` needs no
  authentication and `gh` is not required. `jq` is not a dependency of this
  repo either, so the tag is extracted with `sed`.
- **The release DMG is ad-hoc signed, not notarized.** `release.yml:487-505`
  runs `codesign --force --deep --sign -`. Gatekeeper therefore blocks it and
  `xattr -cr` is mandatory; `spctl --assess` is not usable as a verification
  step.
- **The DMG has no published checksum.** `checksums.txt` in a release covers
  only the GoReleaser CLI binaries, and `heimdallm_<v>_checksums.txt` only the
  `.deb`/`.rpm`. Neither lists the DMG or the AppImage.
- **The bundle can be born corrupt.** On case-insensitive APFS, `heimdallm`
  resolves to the Flutter binary `Heimdallm`; if the daemon copy step in CI
  degrades, both binaries end up identical and spawning the daemon fork-bombs
  the machine. `install-linux` guards against exactly this
  (`Makefile:605-608`) and `LLM-HOW-TO-INSTALL.md` repeats the check.
- **The daemon can uninstall its own LaunchAgent.** `daemon/cmd/heimdallm/main.go:84`
  handles the `uninstall` subcommand, calling `launchagent.Uninstall()`.

## Decision

Add two inline recipes to the root `Makefile`, next to the Linux pair, reusing
the existing `_check-macos` guard (`Makefile:233`).

Rejected alternatives: a `scripts/install-macos.sh` (breaks symmetry with
`install-linux`, which is inline, and splits install logic across two places),
and building locally with `package-macos` instead of downloading (slower, and
the point of the request is to get a working app without a Flutter toolchain
run).

```
make install-macos                  # latest release → /Applications
make install-macos RELEASE=v0.7.5   # pinned release
make uninstall-macos                # remove app; config and data preserved
make uninstall-macos PURGE=1        # also wipe config, data and logs
```

`/Applications` over `~/Applications`: it matches what `README.md:43` and
`LLM-HOW-TO-INSTALL.md` already document, and it is writable without `sudo` for
an admin user, which is the normal case on a personal Mac.

## Design

### `install-macos`

1. `_check-macos` — fail fast on Linux with a pointer to `install-linux`.
2. Resolve the tag: use `RELEASE` when set; otherwise `curl -fsSL` the
   `releases/latest` endpoint and extract `tag_name` via `sed`. Fail with an
   explicit message if resolution yields nothing.
3. `mktemp -d`, and install a `trap` that runs `hdiutil detach` (if mounted)
   and `rm -rf` the temp dir on every exit path.
4. `curl -fL` the `Heimdallm-<tag>.dmg` asset. A missing asset must produce a
   clear "this release has no macOS DMG" message rather than a `hdiutil` error.
5. `hdiutil attach -nobrowse -quiet`, then locate `Heimdallm.app` inside the
   mounted volume.
6. Validate before touching `/Applications`:
   - `Contents/MacOS/Heimdallm` and `Contents/MacOS/heimdalld` both exist.
   - `cmp -s` shows they differ — abort on identical binaries (fork-bomb state).
7. Stop running instances: `pkill -f "/Applications/Heimdallm.app"`, which
   covers both the Flutter binary and the `heimdalld` it spawned (the daemon is
   always launched by absolute path from inside the bundle, see
   `DaemonLifecycle.defaultBinaryPath`). Scoped to the install path, never a bare
   `pkill -f heimdallm`, so a concurrent `make dev` survives — the same
   reasoning documented in `uninstall-linux` (`Makefile:691-705`).
8. `rm -rf /Applications/Heimdallm.app` then `cp -R` from the volume.
   If `/Applications` is not writable, say so and suggest `sudo make install-macos`.
9. `xattr -cr /Applications/Heimdallm.app`.
10. Print the installed tag, the launch hint (`open -a Heimdallm`), and a note
    that `~/.config/heimdallm` and `~/.local/share/heimdallm` are untouched —
    the app picks up the existing database and config, sandbox being disabled
    (`flutter_app/macos/Runner/Release.entitlements`).

### `uninstall-macos`

1. `_check-macos`.
2. `pkill -f` the app and daemon paths, best-effort.
3. If `/Applications/Heimdallm.app/Contents/MacOS/heimdalld` exists, run it with
   `uninstall` to unload and remove the LaunchAgent — **before** deleting the
   bundle, since the binary is inside it.
4. `rm -rf /Applications/Heimdallm.app`.
5. `PURGE=1` additionally removes `~/.config/heimdallm`,
   `~/.local/share/heimdallm` and `~/Library/Logs/heimdallm`. Default keeps
   them and prints where they live, mirroring `uninstall-linux`.

### Integrity

The download is trusted on HTTPS alone. Publishing a DMG checksum is a change
to the release pipeline and is deliberately out of scope; the limitation is
recorded in #604 so it is not mistaken for an oversight.

### Documentation

`README.md` and `LLM-HOW-TO-INSTALL.md` lead with the target and keep the
manual commands as the fallback for machines without a checkout.

## Testing

The `Makefile` has no automated coverage and this change does not introduce a
framework for it. Verification is manual and recorded in the PR:

1. `make -n install-macos` — inspect the expanded recipe.
2. `make install-macos` on a clean `/Applications` — app launches, version
   matches the release, existing repos and config still present.
3. `make install-macos RELEASE=v0.7.5` — pinned download works.
4. `make uninstall-macos` — app gone, config and data intact.
5. `make uninstall-macos PURGE=1` — config and data removed.
6. `make install-macos` on Linux — guard fires.
