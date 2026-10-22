# Release Pipeline

Every component of Heimdallm is released on its own semver line, and a unified
installer bundles all of them. `release-please` drives the versions from
conventional commits, and `.github/workflows/release.yml` builds and publishes
whatever it released.

## Releases and tags

| Release | Tag | Bumped by | Assets |
|---------|-----|-----------|--------|
| Unified installer | `vX.Y.Z` | any change in the repository | `Heimdallm-vX.Y.Z.dmg`, `heimdallm_X.Y.Z_amd64.deb` / `.rpm`, `Heimdallm-X.Y.Z-x86_64.AppImage`, `heimdallm-cli_X.Y.Z_{os}_{arch}` archives, `daemon-checksums.txt`, `cli-checksums.txt`, `linux-checksums.txt` (+ `.sig`), `versions.json`; GHCR image `ghcr.io/theburrowhub/heimdallm:X.Y.Z`; Homebrew formula |
| Daemon | `daemon/vX.Y.Z` | changes under `daemon/` | `heimdalld_X.Y.Z_{linux,darwin}_{amd64,arm64}.tar.gz`, `checksums.txt`, `compat.json` |
| GUI | `gui/vX.Y.Z` | changes under `flutter_app/` | `Heimdallm-X.Y.Z.dmg`, `heimdallm_X.Y.Z_amd64.deb` / `.rpm`, `Heimdallm-X.Y.Z-x86_64.AppImage`, `checksums.txt`, `compat.json` |
| CLI | `cli/vX.Y.Z` | changes under `cli/` | `heimdallm-cli_X.Y.Z_{linux,darwin,windows}_{amd64,arm64}` archives (`.zip` on Windows), `checksums.txt`, `compat.json` |

`release-please-config.json` declares four packages: the repository root
(`.`, untagged component, so its tags stay `vX.Y.Z`) and `daemon`,
`flutter_app` (component `gui`) and `cli`, each with its own `CHANGELOG.md`.
The root package sees every commit, so the unified version moves on every
release and is never behind a component's. All four share one release pull
request, titled `chore(main): release X.Y.Z` after the unified version.
`flutter_app/pubspec.yaml` keeps carrying the unified version (local builds use
it); release builds stamp their own version with `--build-name`.

The daemon has no Windows archive: it manages process groups with
`syscall.Kill`, which does not build there.

### The latest release is always the unified one

The CLI updater, the macOS and Linux desktop updaters, `scripts/macos-install.sh`
and `LLM-HOW-TO-INSTALL.md` all resolve GitHub's `releases/latest`. Component
releases are therefore published with `--latest=false`, and their finalizer
fails if the latest release is not a `vX.Y.Z` tag afterwards. The CLI's
`latest` channel lists the 20 most recent releases and ignores the component
tags (they are not plain semver); with up to four releases per release PR, that
window now covers about five release cycles.

### versions.json

Each unified release carries a `versions.json` with the component versions
recorded in `.release-please-manifest.json` at the release commit, plus the
compatibility matrix from `compat.json` (see below):

```json
{
  "daemon": "0.11.0", "gui": "0.11.0", "cli": "0.10.5",
  "daemon_api": 1,
  "min_daemon_api": { "gui": 1, "cli": 1 }
}
```

Releases cut before the split only had the root entry; every component shipped
with the unified version then, so it is used as the fallback. Releases cut
before `compat.json` existed carry no compatibility keys.

### Compatibility matrix

`compat.json` at the repository root is the single source of truth:

```json
{ "daemon_api": 1, "min_daemon_api": { "gui": 1, "cli": 1 } }
```

- `daemon_api` is the revision of the daemon's HTTP API. The daemon reports it
  as `api_version` on `/health` (`APIVersion` in `daemon/internal/server/handlers.go`); daemons
  built before the field existed omit it and count as revision 1.
- `min_daemon_api.gui` / `.cli` are the oldest daemon API each client works
  against (`flutter_app/lib/core/compat.dart`, `cli/internal/compat`).

Tests in each module fail if a constant drifts from `compat.json`, and
`scripts/release/versions-json.sh` rejects a matrix whose minimums exceed
`daemon_api`. Bump `daemon_api` together with the daemon change that adds or
alters something a client depends on, and a client's minimum together with the
client change that starts depending on it.

The matrix is published in the unified `versions.json` and as `compat.json` on
every component release. The existing updaters use it to **warn, never block**:

- `heimdallm-cli update`, `update --check` and `version --check` read the target
  release's `versions.json` and, when the daemon the CLI is configured for
  (`--host`, `HEIMDALLM_HOST`, `cli.toml`) serves an older API than
  `min_daemon_api.cli`, print a warning. The update still installs.
- The desktop update banner shows the same warning when the connected daemon
  serves an older API than `min_daemon_api.gui`. The bundled daemon is replaced
  together with the app, so this matters for a separately managed daemon
  (Docker, another machine).

A missing or unreadable `versions.json`, or an unreachable daemon, prints
nothing: the warning only speaks on evidence.

### Desktop bundles and the bundled CLI

The unified and `gui/v*` desktop bundles have the same layout: the Flutter app,
the daemon and the CLI built from the same commit, all stamped with the version
of the release they belong to. Both Linux packages are named `heimdallm`, so a
`gui/v*` package installs over a unified one and vice versa.

| Platform | CLI path | On `PATH` |
|----------|----------|-----------|
| macOS | `/Applications/Heimdallm.app/Contents/MacOS/heimdallm-cli` | no (sealed inside the app bundle) |
| `.deb` / `.rpm` | `/opt/heimdallm/heimdallm-cli` | `/usr/bin/heimdallm-cli` |
| AppImage | inside the image, next to `heimdallm` | no |

The desktop updater owns that copy: `heimdallm-cli update` refuses to rewrite a
binary inside an app bundle or under `/opt/heimdallm` (it would break the macOS
code seal or a package-owned file) and points to the desktop app's update,
exactly as it defers to `brew upgrade` for a Homebrew install.

## Pipeline architecture

```
push to main                         workflow_dispatch (existing tag, or dry run)
      │                                             │
      ▼                                             │
release-please                                     │
(tags + one draft per released package)            │
      └───────────────────────┬─────────────────────┘
                              ▼
                     plan (validate tags)
                              ▼
                            tests
         ┌───────────────┬────┴──────────┬───────────────┐
         ▼               ▼               ▼               ▼
release-unified   release-daemon    release-gui     release-cli
 GHCR image       GoReleaser        build-desktop   GoReleaser
 build-desktop    archives          (gui assets)    archives
 CLI + Homebrew       │                  │               │
 versions.json        │                  │               │
         ▼            ▼                  ▼               ▼
 finalizer: exact   finalizer: exact assets → publish --latest=false
 assets → publish
 as latest → GHCR
 aliases → Homebrew
```

The four pipelines are reusable workflows chained from the release-please run,
not triggered by their tags: release-please may create the tags with
`GITHUB_TOKEN`, and a tag pushed by `GITHUB_TOKEN` never starts another
workflow. Each one depends only on the shared test gate, so a failing component
pipeline does not hold back the unified release (or another component).
`build-desktop.yml` builds the desktop bundles for both `release-unified.yml`
and `release-gui.yml`. Every finalizer compares its draft with the exact asset
list in `scripts/release/expected-assets.sh` before publishing.

GoReleaser OSS cannot read a `daemon/` or `cli/` tag prefix (that is a Pro
feature), so the component configs (`daemon/.goreleaser-component.yml`,
`cli/.goreleaser-component.yml`) get the bare `vX.Y.Z` as
`GORELEASER_CURRENT_TAG`, have GitHub publishing disabled, and the workflow
uploads `dist/` to the component draft itself.

## Artifacts and tooling

| Artifact | Tooling | GoReleaser? |
|----------|---------|-------------|
| CLI binaries, unified release (linux, macOS, Windows) | GoReleaser `cli/.goreleaser.yml` | Yes |
| CLI binaries, `cli/v*` release | GoReleaser `cli/.goreleaser-component.yml` | Yes |
| Daemon binaries, `daemon/v*` release | GoReleaser `daemon/.goreleaser-component.yml` | Yes |
| Homebrew formula | GoReleaser brews (`cli/.goreleaser.yml`) | Yes |
| Docker image (GHCR) | GoReleaser `daemon/.goreleaser-docker.yml` | Yes |
| Linux `.deb` / `.rpm` | GoReleaser nfpms `daemon/.goreleaser.yml` | Yes |
| Linux `.AppImage` | appimagetool (manual) | No |
| macOS `.dmg` | ad-hoc codesign, create-dmg | No |
| `versions.json`, component `checksums.txt` for gui | `scripts/release/` | No |

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

The unified finalizer verifies the release tag/commit and exact asset set,
validates the attested immutable GHCR image plus its version/revision labels,
then publishes GitHub before moving the `major.minor`, `major`, and `latest`
Docker aliases. An alias failure therefore leaves the release assets and
immutable Docker version tag usable. Each boundary is idempotent, so rerunning
the failed finalizer reconciles the aliases without duplicating assets.

If a pipeline fails while its release is still a draft, dispatch the release
workflow with that release's `tag` (`vX.Y.Z`, `daemon/vX.Y.Z`, `gui/vX.Y.Z` or
`cli/vX.Y.Z`) and `channels_only=false`. This repeats the test gate and that
release's builds, replaces its draft assets, and runs the same finalizer as a
normal release. Every upload step refuses a missing or already-published release
(`scripts/release/require-draft.sh`): published assets are immutable.

For a later alias-only repair, dispatch the release workflow with the published
latest `tag` and `channels_only=true` (unified releases only). It skips builds
and uploads and refuses older tags, then reconciles GHCR aliases to the
attested digest. Homebrew remains best-effort after normal publication: failure
may leave the previous formula in place but does not affect Linux desktop
updates or immutable artifacts.

### Dry runs

`dry_run=true` builds the pipeline for the dispatched `tag` from the selected
branch, using the tag only as the version: it needs no existing tag or draft,
and it uploads, pushes and publishes nothing (no GHCR push, no release asset,
no Homebrew). The built assets stay available as workflow artifacts. Use it to
exercise a release pipeline change before merging it, e.g.
`gh workflow run release.yml --ref <branch> -f tag=cli/v0.0.0-dryrun -f dry_run=true`.

## Linux package contents

The `.deb`, `.rpm`, and `.AppImage` packages (unified and `gui/v*`) all include:

```
/opt/heimdallm/
├── heimdallm          # Flutter GUI application
├── heimdalld          # Daemon binary
├── heimdallm-cli      # CLI / TUI
├── lib/               # Flutter runtime libraries
└── data/              # Flutter assets
/usr/bin/heimdallm → /opt/heimdallm/heimdallm   (symlink)
/usr/bin/heimdallm-cli → /opt/heimdallm/heimdallm-cli   (symlink, .deb/.rpm)
/usr/share/applications/com.theburrowhub.heimdallm.desktop
/usr/share/icons/hicolor/{48,128,256,512}x{48,128,256,512}/apps/heimdallm.png
```

The unified release also contains `linux-checksums.txt`, with one SHA-256 entry for
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
