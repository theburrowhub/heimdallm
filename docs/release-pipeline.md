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
| macOS `.dmg` + Sparkle appcast | Developer ID, notarytool, create-dmg, Sparkle | No |

## Integrated desktop updates

Desktop builds with a configured native updater check once every 24 hours. A
newer stable release is announced with a persistent dashboard banner, a desktop
notification, and an **Update now** action in both the app and the system-tray
menu.

- **macOS** uses Sparkle's Ed25519-signed appcast and archive, verifies the
  archive before extraction, and performs atomic bundle replacement. Official
  releases are additionally signed and notarized with Developer ID.
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

Every desktop installation uses the same daemon handoff: stop admitting new
work, drain active transactions, persist and seal the update lease, stop the
exact daemon PID, install, then verify the replacement daemon's version and
boot identity before reopening admission. The process-lifetime data lock and
the UI's guarded port probe independently prevent a second daemon from being
started during that transition.

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
- **Developer ID Application signing** with hardened runtime
- Apple **notarization and stapling** for both the app and DMG
- **`create-dmg`** for the installer image with custom window layout
- Sparkle's **Ed25519-signed** archive and appcast tooling

None of these steps have GoReleaser equivalents. The `build-macos` job is
self-contained on a `macos-14` runner and would not benefit from GoReleaser
integration.

## macOS updater trust chain

The macOS app uses Sparkle 2.9.6 through the repository-owned
`flutter_app/macos/Sparkle.podspec`. That podspec pins the upstream URL and
archive SHA-256, so CocoaPods verifies the download before extracting the
framework or exposing its release tools. Every release job:

1. builds both daemon architectures and combines them into one universal binary;
2. signs nested code from the inside out and seals the app with Developer ID;
3. notarizes and staples the app, then the final DMG;
4. generates `appcast.xml` with Sparkle's pinned `generate_appcast` tool;
5. publishes the DMG, its SHA-256 checksum, and the signed appcast together.

The app downloads only the appcast at
`releases/latest/download/appcast.xml`. The appcast and archive both require the
Ed25519 public key embedded in `Info.plist`, and Sparkle validates the archive
before extraction (`SUVerifyUpdateBeforeExtraction = true`). Signed-feed
failures never expire or fall back to an unsigned feed
(`SUSignedFeedFailureExpirationInterval = 0`). This Ed25519 trust chain also
protects ad-hoc local builds, so they may consume the production updater without
a Developer ID certificate on the currently installed bundle.

The first release containing Sparkle must be installed manually from its signed
DMG because older versions have no updater. Every later signed release can be
installed in place from **Check for updates**.

The updater becomes operational only after the release workflow publishes
`appcast.xml` and its signed enclosure alongside the DMG. If the public
`releases/latest/download/appcast.xml` URL is missing (for example, while the
next release is still a draft), checks fail closed and the error remains visible
in Settings and the system tray. Publish the next release through the current
workflow; do not hand-author or upload an unsigned appcast as a workaround.

### Required GitHub Actions secrets

| Secret | Contents |
|---|---|
| `MACOS_DEVELOPER_ID_P12` | Base64-encoded Developer ID Application certificate plus private key (`.p12`) |
| `MACOS_DEVELOPER_ID_P12_PASSWORD` | Password used when exporting the `.p12` |
| `APPLE_NOTARIZATION_APPLE_ID` | Apple ID used by `notarytool` |
| `APPLE_NOTARIZATION_PASSWORD` | App-specific password for that Apple ID |
| `APPLE_TEAM_ID` | Ten-character Apple Developer team identifier |
| `SPARKLE_EDDSA_PRIVATE_KEY` | Private Sparkle key exported by `generate_keys` |

The Sparkle secret has already been generated as a project-specific key. The
workflow refuses to create an ad-hoc or unsigned fallback if any credential is
missing. It verifies executable build dependencies before materializing the
P12; the P12 is written with `umask 077`, removed immediately after import, and
the isolated signing keychain exists only for the signing step. Notarization
credentials are scoped directly to the two `notarytool` steps.

Credential provisioning remains external: the workflow cannot manufacture a
Developer ID identity or Apple notarization credentials. At the 2026-08-18
audit, only `SPARKLE_EDDSA_PRIVATE_KEY` was present as a repository-level
secret; the five Apple secrets above still had to be supplied at repository or
organization scope before the first signed release. The macOS job does not use
a GitHub environment, so environment-only secrets are not visible to it.
`TAP_GITHUB_TOKEN` is optional and affects only the best-effort Homebrew update.

### Runtime handoff

Before invoking Sparkle, the desktop app renews a short daemon drain lease. The
daemon atomically refuses new review/issue/implementation runs while existing
ones finish. Once idle, the app fsyncs a recovery journal and converts the lease
to a durable, non-expiring seal before it unloads the canonical LaunchAgent
(preventing its `KeepAlive` policy from respawning the old binary). It waits for
both the PID and lifecycle lock to disappear before allowing bundle replacement.

On relaunch, the app verifies the exact daemon path, PID, lease owner, bundled
version and random process boot ID through the daemon's minimal startup surface.
It then performs an authenticated two-phase handoff: `seal` (idempotent
recovery), process-bound `confirm` (initialize dependencies while work admission
remains closed), exact `/health.version` verification, and finally a
process-bound owner-authenticated lease deletion. Both mutation requests echo
the verified boot ID, so a request delayed across a daemon crash receives a 409
before it can initialize dependencies or open the replacement gate.
The recovery journal is removed only after that last acknowledgement. A crash,
lost response or failed bootstrap therefore remains fail-closed and converges
through the same sequence on the next launch, instead of admitting work to a
mixed old/new installation or spawning a duplicate daemon.

The daemon also consumes the native recovery intent before it loads config,
opens/migrates SQLite, or starts NATS and workers. It accepts only a private,
current-user, regular `app-update-recovery.json` opened without following
symlinks. A `preparing` or `sealed` journal atomically promotes an absent or
expired `update-drain.json` for the same owner to a non-expiring seal. This
covers an autonomous `launchd` respawn before the desktop app has relaunched.
`pendingInstall` never blocks normal work. An absent marker is not recreated
for `installing`, because it may be the legitimate result of the final DELETE;
an existing marker is still restored normally.

Drain polling has one monotonic ten-minute deadline, including stalled HTTP
requests; cancellation tears down the in-flight URL session task before
rollback. Each recovery stage has a separate monotonic 60-second deadline, and
native subprocesses are terminated and then killed on a bounded escalation.
A `pendingInstall` journal may also coexist briefly with a new app and the old
daemon after interrupted replacement. Recovery seals the exact old PID/boot
ID/version it observed, stops it, and requires the new bundled version only
after restart.

If the native bridge fails to configure, an ordinary unsigned/debug run may
continue only when no recovery journal exists. Desktop bootstrap checks for the
journal independently and refuses to enter the UI against the daemon's minimal
sealed router; the daemon-side reconciliation above remains authoritative even
when the app is not running.

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
place but does not affect signed desktop updates or immutable artifacts.

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
