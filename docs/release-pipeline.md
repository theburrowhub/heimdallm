# Release Pipeline

The release pipeline builds and publishes all distribution artifacts when a
new version is tagged on `main`. It is driven by
`.github/workflows/release.yml`.

## Pipeline architecture

```
push to main                    workflow_dispatch (existing tag only)
      │                                      │
      ▼                                      │
release-please                              │
(tag + draft release)                       │
      └──────────────────┬───────────────────┘
                         ▼
                       tests
                         │
          ┌──────────────┼──────────────┬──────────────┐
          ▼              ▼              ▼              ▼
 immutable GHCR     Linux packages    macOS DMG    CLI binaries
          └──────────────┴──────────────┴──────────────┘
                         ▼
                 publication finalizer
         (exact assets/attestation → publish → GHCR aliases)
                         ▼
                Homebrew update (best effort)
```

All four build jobs run in parallel after the test gate passes. There is one
Docker producer: `goreleaser-daemon` publishes the immutable version tag. The
finalizer validates the complete release and only then publishes GitHub and
promotes the mutable GHCR aliases.

## Artifacts and tooling

| Artifact | Tooling | GoReleaser? |
|----------|---------|-------------|
| CLI binaries (linux, macOS, Windows) | GoReleaser `cli/.goreleaser.yml` | Yes |
| Homebrew formula | GoReleaser brews (same config) | Yes |
| Docker image (GHCR) | GoReleaser `daemon/.goreleaser-docker.yml` | Yes |
| Linux `.deb` / `.rpm` | GoReleaser nfpms `daemon/.goreleaser.yml` | Yes |
| Linux `.AppImage` | appimagetool (manual) | No |
| macOS `.dmg` | ad-hoc codesign, create-dmg | No |

## Integrated desktop updates

Desktop builds with a configured native updater check once every 24 hours. A
newer stable release is announced with a persistent dashboard banner, a desktop
notification, and an **Update now** action in both the app and the system-tray
menu.

- **macOS** polls GitHub's latest release, downloads its DMG, replaces the app
  bundle, and relaunches. It does not use an appcast or update signature.
- **Linux AppImage** downloads and checksum-verifies the new AppImage, keeps the
  previous image as a rollback copy, then atomically replaces and relaunches it.
- **Linux `.deb` / `.rpm`** downloads the matching package, verifies it against
  the Ed25519-signed `linux-checksums.txt`, and invokes the native package tool
  through PolicyKit.
  The update still starts with one click; the OS may show its normal
  administrator-authentication prompt.

Source builds and user-local `make install-linux` builds are intentionally not
self-modifying because they have no published package owner to replace. Docker
and web deployments remain image-managed.

Linux installations retain their package-specific daemon handoff. The macOS
updater stops the daemon immediately before replacing the app bundle and starts
it again from the replacement.

## Why AppImage stays outside GoReleaser

GoReleaser has no AppImage packager. AppImage requires:

- A custom `AppDir/` directory layout with an `AppRun` entry-point script
- A `.desktop` file and icon at the AppDir root (not in XDG paths)
- The `appimagetool` binary from AppImageKit to assemble the final image

These steps are straightforward shell commands and don't benefit from
GoReleaser's build/publish pipeline. The AppImage is built in the same
`build-linux` job that prepares the Flutter bundle, so there is no extra CI
cost.

## Why macOS DMG stays outside GoReleaser

GoReleaser does not support DMG creation. The macOS build requires:

- A **macOS runner** (GoReleaser runs on Linux)
- A **Flutter build** for macOS (not a Go binary)
- A universal (`arm64` + `x86_64`) bundled daemon
- Ad-hoc code sealing for the complete app bundle
- **`create-dmg`** for the installer image with custom window layout

None of these steps have GoReleaser equivalents. The `build-macos` job is
self-contained on a `macos-14` runner and would not benefit from GoReleaser
integration.

## macOS updater

The release job builds the universal daemon and Flutter app, seals the bundle
with an ad-hoc code signature so macOS can launch it, packages it as
`Heimdallm-v<VERSION>.dmg`, and publishes that DMG. The code seal is a macOS
packaging requirement; it is not an update signature and uses no release key.

Once a day, the running app requests GitHub's `releases/latest` endpoint. When
a newer version exists, it downloads the matching DMG, mounts it, and copies
`Heimdallm.app` to a staging path next to the current installation. A detached
helper then stops the daemon, replaces the installed bundle, restarts the
daemon, and opens the new app. A staging failure leaves the installed app
untouched; a failed second rename restores the immediate backup.

There is no Sparkle dependency, appcast, update signature, checksum gate, drain,
lease, or recovery journal in the macOS update path.

`SPARKLE_EDDSA_PRIVATE_KEY` remains required only for the signed Linux package
manifest. It is consumed by the `Generate Linux desktop checksums` release
step described under [Linux package contents](#linux-package-contents); the
macOS build and updater do not read it.

`TAP_GITHUB_TOKEN` is optional and affects only the best-effort Homebrew update.

### Publication and retry semantics

The finalizer verifies the release tag/commit and exact asset set, validates the
attested immutable GHCR image plus its version/revision labels, then publishes
GitHub before moving the `major.minor`, `major`, and `latest` Docker aliases. An
alias failure therefore leaves the release assets and immutable Docker version
tag usable. Each boundary is idempotent, so rerunning the failed finalizer
reconciles the aliases without duplicating assets.

If the initial run fails while the release is still a draft, dispatch the
release workflow with the existing `tag` and `channels_only=false`. This repeats
the test and build gates, replaces the draft assets, and runs the same finalizer
used by a normal release. The workflow refuses a missing or already-published
release in this mode.

For a later alias-only repair, dispatch the release workflow with the published
latest `tag` and `channels_only=true`. It skips builds/uploads and refuses older
tags, then reconciles GHCR aliases to the attested digest. Homebrew remains
best-effort after normal publication: failure may leave the previous formula in
place but does not affect Linux desktop updates or immutable artifacts.

## Linux package contents

The `.deb`, `.rpm`, and `.AppImage` packages all include:

```
/opt/heimdallm/
├── heimdallm          # Flutter GUI application
├── heimdalld          # Daemon binary
├── lib/               # Flutter runtime libraries
└── data/              # Flutter assets
/usr/bin/heimdallm → /opt/heimdallm/heimdallm   (symlink)
/usr/share/applications/com.theburrowhub.heimdallm.desktop
/usr/share/icons/hicolor/{48,128,256,512}x{48,128,256,512}/apps/heimdallm.png
```

The release also contains `linux-checksums.txt`, with one SHA-256 entry for
each of those three package formats, plus `linux-checksums.txt.sig`. The latter
is an Ed25519 signature made with the Sparkle release key. The app verifies the
embedded public key, then the manifest signature, then the exact asset digest.

### Dependencies

| Format | Packages |
|--------|----------|
| `.deb` | libgtk-3-0, libayatana-appindicator3-1, libnotify4, libsecret-1-0 |
| `.rpm` | gtk3, libayatana-appindicator-gtk3, libnotify, libsecret |

RPM dependencies use RPM package names (not Debian names).

## SLSA attestation

The Docker image includes a build provenance attestation generated by
`actions/attest-build-provenance`. The attestation is pushed to the GHCR
registry alongside the image.
