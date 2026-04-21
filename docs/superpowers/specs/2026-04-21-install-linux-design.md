# Native Linux Install Target — Design Spec

**Date**: 2026-04-21
**Scope**: New `install-linux` and `uninstall-linux` Makefile targets that install Heimdallm user-local on Linux. The install target reuses the existing `verify-linux` Docker build — no host Flutter toolchain required — and stages the resulting artifacts into `~/.local/` so the app runs natively outside a container.

**Revision 2026-04-21**: earlier draft used host-native `build-daemon build-app` as the build step, which forced users to install Flutter + GTK dev headers on their workstation. Pivoted to reuse the existing `verify-linux` Docker build, matching `run-linux`'s pattern of consuming artifacts from the `heimdallm-verify` image. `uninstall-linux` design unchanged — it doesn't care how install happened.

---

## Context

The repo currently offers two Linux-related Makefile targets:

- `verify-linux` — builds the `heimdallm-verify` Docker image (runs the full CI pipeline inside).
- `run-linux` — launches the Flutter bundle *from inside that image* over host X11/DBus.

CI (`.github/workflows/release.yml` → `build-linux`) produces `.deb`, `.rpm`, and `.AppImage` for end users, and `LLM-HOW-TO-INSTALL.md` documents the download-and-install flow against those release artifacts.

Missing: a path for developers who have this repo cloned, have Docker (like `verify-linux` already requires), and want the app installed natively on the host so it launches like any other desktop application — without running from inside a container. `verify-linux` builds a complete Flutter bundle and Go daemon *inside* the `heimdallm-verify` image, but those artifacts never leave the container. `run-linux` re-launches them from inside the container with X11 forwarding. Neither target produces a host-installed, launcher-integrated app.

This spec fills that gap by extracting the `verify-linux` build artifacts onto the host and staging them into `~/.local/` with launcher integration.

---

## Decisions

The design questions answered during brainstorming:

| Question | Decision |
|---|---|
| Installation scope | **User-local** (`~/.local/…`) — no sudo, reversible, single-user. |
| Build responsibility | **Reuse `verify-linux` Docker build**: `install-linux: _check-linux verify-linux`. No host Flutter toolchain required. Same compatibility surface as CI's `.deb` (Ubuntu 22.04 base image). |
| Uninstall companion | **Yes** — `uninstall-linux` removes app; `PURGE=1` also wipes config and data. Mirrors Debian `apt remove` vs. `apt purge`. |

---

## 1. Install layout

```
~/.local/opt/heimdallm/                            # bundle root (artifacts from heimdallm-verify image)
├── heimdallm                                      # Flutter binary (entry point)
├── heimdalld                                      # Go daemon (copied from /app/daemon/bin/heimdallm, renamed)
├── data/                                          # Flutter assets (flutter_assets, icudtl.dat, …)
└── lib/                                           # libapp.so, libflutter_linux_gtk.so, …

~/.local/bin/heimdallm                             # → symlink to ~/.local/opt/heimdallm/heimdallm

~/.local/share/applications/
    com.theburrowhub.heimdallm.desktop             # Exec= is absolute path into the bundle

~/.local/share/icons/hicolor/{48,128,256,512}x{same}/apps/heimdallm.png
```

Key properties:

- **Layout mirrors CI's `/opt/heimdallm/`.** `DaemonLifecycle.defaultBinaryPath()` in `daemon_lifecycle.dart` resolves the daemon as `heimdalld` next to the Flutter binary — the CI `.deb` satisfies this, and so does this user-local install. No app-code change required.
- **`~/.local/share/heimdallm/` is reserved** as the daemon's runtime data directory (created by `make run-linux`, written to by the daemon at runtime). The install target must not place anything there; the uninstall target must not remove it except under `PURGE=1`.
- **`~/.config/heimdallm/.token` is seeded at install time** if absent, from `$GITHUB_TOKEN` or `gh auth token`. Needed because the daemon (when spawned by a GUI-launched Flutter app) does not inherit a shell `$GITHUB_TOKEN` and otherwise fails at first run with "token not found". See §2 step 11 for the precedence and safety details; the token file itself lives under `~/.config/heimdallm/` alongside `config.toml`, and is removed by `uninstall-linux PURGE=1`.
- **Desktop entry `Exec=` is absolute** (the expanded value of `$HOME/.local/opt/heimdallm/heimdallm`). `%h` is not a standard Desktop Entry field code and not all launchers expand it.

---

## 2. `install-linux` target

Declaration:

```make
install-linux: _check-linux verify-linux
```

Make's dependency graph invokes `verify-linux` first, which builds (or rebuilds) the `heimdallm-verify` Docker image. Docker's own layer cache skips work that has not changed — on a no-op rerun, `verify-linux` returns in seconds. The `install-linux` recipe body then extracts the artifacts from the image and stages them into `~/.local/`.

Artifacts in the image, set by `Dockerfile.linux-verify`:
- Flutter bundle: `/app/flutter_app/build/linux/x64/release/bundle/`
- Go daemon: `/app/daemon/bin/heimdallm`

Steps (all idempotent — every rerun leaves the same end state):

1. **Preflight.** Assert `uname -s` is Linux via a new `_check-linux` guard. On macOS, print: "This target requires Linux. On macOS, use `make release-local` or `make run-linux`." and exit 1. Mirrors the existing `_check-macos`.
2. **Preflight — Docker.** `command -v docker >/dev/null 2>&1` — fail with the same message the other Docker-dependent targets use. `verify-linux` would fail here anyway, but surfacing this first gives a clearer error.
3. **Extract artifacts from the image.** Create a stopped container with `docker create heimdallm-verify` (captures its ID in a shell variable). `docker cp` the bundle directory and the daemon binary out into a temporary extraction directory on the host. Remove the container via a shell `trap` so the container is cleaned up even if `cp` fails mid-way. Use a dedicated path under `/tmp` (e.g. `/tmp/heimdallm-install-extract`) rather than `mktemp -d` — a fixed path makes debugging simpler, and the target's first step (`rm -rf` on it) handles leftover state from prior partial runs.
4. **Stage the bundle.** `rm -rf ~/.local/opt/heimdallm && mkdir -p ~/.local/opt/heimdallm && cp -r <extract-dir>/bundle/. ~/.local/opt/heimdallm/`.
5. **Embed the daemon.** `cp <extract-dir>/daemon ~/.local/opt/heimdallm/heimdalld && chmod +x ~/.local/opt/heimdallm/heimdalld`. The rename (source `heimdallm` → destination `heimdalld`) matches `DaemonLifecycle.defaultBinaryPath()`'s lookup and the CI `.deb`'s layout.
6. **Case-collision guard.** `cmp -s ~/.local/opt/heimdallm/heimdallm ~/.local/opt/heimdallm/heimdalld` must report a difference; if bytes match, fail with "Both binaries are identical — would fork-bomb on launch". Same guard CI's release pipeline uses; protects against a silently-broken rename.
7. **Clean up extraction directory.** `rm -rf <extract-dir>` — the staged copy under `~/.local/opt/heimdallm/` is the source of truth now; the temp dir is no longer needed.
8. **PATH shim.** `mkdir -p ~/.local/bin && ln -sf ~/.local/opt/heimdallm/heimdallm ~/.local/bin/heimdallm`. `ln -sf` atomically replaces any prior symlink.
9. **Icons.** For each size in `{48, 128, 256, 512}`: `mkdir -p ~/.local/share/icons/hicolor/<N>x<N>/apps/ && cp flutter_app/assets/icons/<N>.png ~/.local/share/icons/hicolor/<N>x<N>/apps/heimdallm.png`. (Icons are sourced from the *host's* repo checkout, not from the Docker image — `flutter_app/assets/icons/` is a committed directory, present wherever the repo is cloned.)
10. **Desktop entry.** Write `~/.local/share/applications/com.theburrowhub.heimdallm.desktop` (contents below, with `$$HOME` expanded at Make-recipe-execution time, not stored as `%h`):
    ```ini
    [Desktop Entry]
    Name=Heimdallm
    Comment=AI-powered GitHub PR review agent
    Exec=<expanded $HOME>/.local/opt/heimdallm/heimdallm
    Icon=heimdallm
    Type=Application
    Categories=Development;
    StartupWMClass=com.theburrowhub.heimdallm
    StartupNotify=true
    ```
11. **Seed GitHub token (idempotent, non-fatal).** Gate the entire step behind `[ ! -s "$$HOME/.config/heimdallm/.token" ]` — skip the seeding when the file exists AND is non-empty. A zero-byte file (e.g. disk-full remnant from a prior partial write) is treated as absent so the next `make install-linux` re-seeds it. Never overwrite an existing non-empty token file, even if `$GITHUB_TOKEN` or `gh auth token` would produce a different value. If the file is absent or empty, try two sources in order:
    1. `$GITHUB_TOKEN` env var (whatever was exported in the shell that ran `make install-linux`).
    2. `gh auth token` (only if `gh` is on `$PATH`, authenticated, and the command exits 0 with non-empty stdout).

    On success, `mkdir -p ~/.config/heimdallm` and write the token to `~/.config/heimdallm/.token` with mode **600**. Use `umask 077` *before* the redirect (or `install -m 600` if available) so there is no window where the file sits world-readable — a subsequent `chmod 600` alone is a TOCTOU race on a shared machine. Print `    Seeded ~/.config/heimdallm/.token from <source>` to the terminal.

    On failure (neither source returns a token): print a multi-line warning telling the user how to supply one (`$GITHUB_TOKEN` export / `gh auth login` / manual `.token` write) and **do not fail the install**. The Makefile recipe exits 0. First launch will show the app's own "daemon failed to start" dialog until the user provides a token; re-running `make install-linux` after `gh auth login` seeds the token.

    Motivation: when the Flutter app is launched from the OS app launcher (GNOME overview, KDE menu), its environment is sanitized and does *not* inherit `$GITHUB_TOKEN` from the user's shell. The daemon it spawns reads only two sources on Linux — `$GITHUB_TOKEN` (unset in that sanitized env) and `~/.config/heimdallm/.token` (nonexistent until seeded). Without this step, first launch always fails with "token not found", indistinguishable (to the user) from a broken install.
12. **Refresh caches (best-effort).**
    - `command -v update-desktop-database >/dev/null && update-desktop-database ~/.local/share/applications/ 2>/dev/null || true`
    - `command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache -q -t ~/.local/share/icons/hicolor/ 2>/dev/null || true`
    - Silent no-op if the tools aren't installed. The app appears in the launcher either way after a re-login; the refresh just makes it immediate.
13. **Final report.** Print:
    - The four path groups written (bundle, bin symlink, desktop entry, icons).
    - A "Launch with: `heimdallm` (or from your app launcher)" hint.
    - **If `~/.local/bin` is not on `$PATH`**, a warning plus the snippet `export PATH="$HOME/.local/bin:$PATH"` and a reminder to add it to `~/.bashrc` / `~/.zshrc`. Detection: `case ":$$PATH:" in *":$$HOME/.local/bin:"*) ;; *) warn ;; esac`.

No output suppression via `@` on the core staging commands — users should see what's happening. `@echo` is fine for informational banners. The `docker create` / `docker cp` / `docker rm` block inside step 3 runs under `@` because the raw Docker command output (container hash, cp progress) is noisy and the shell script's own `echo "▶  Extracting…"` banner is sufficient.

### Binary compatibility

The `heimdallm-verify` image is built from `ubuntu:22.04`. The Flutter bundle's binaries link dynamically against the image's glibc, libgtk-3, libgdk-3, libstdc++, etc. When copied to the host and executed there, the host must provide compatible versions of those libraries. This is the same compatibility envelope as the CI-built `.deb` (which itself runs on ubuntu-22.04). In practice the target works on any reasonably current Debian/Ubuntu/Fedora/Arch. Hosts with an older libc than Ubuntu 22.04 may see missing-symbol errors at launch — this is an acceptable trade for not requiring Flutter on the host, and affects the same population of users who cannot install the CI `.deb`.

---

## 3. `uninstall-linux` target

Declaration:

```make
uninstall-linux:           # app only — config and data preserved
uninstall-linux PURGE=1    # also wipes ~/.config/heimdallm and ~/.local/share/heimdallm
```

Steps:

1. **Preflight.** Same `_check-linux` guard.
2. **Stop running instances.** Best-effort:
   - `pkill -f "$$HOME/.local/opt/heimdallm/heimdallm" 2>/dev/null || true` (stops running Flutter binary, which in turn terminates its child daemon).
   - `pkill -f "$$HOME/.local/opt/heimdallm/heimdalld" 2>/dev/null || true` (also stops an orphaned daemon if the Flutter app crashed earlier).
   - Remove stale UI PID file: `rm -f ~/.local/share/heimdallm/ui.pid` (the file itself, not the directory).
   - Avoids "Text file busy" on step 4 when rewriting the bundle.
3. **Remove desktop entry and icons.**
   - `rm -f ~/.local/share/applications/com.theburrowhub.heimdallm.desktop`
   - `rm -f ~/.local/share/icons/hicolor/{48,128,256,512}x{same}/apps/heimdallm.png`
4. **Remove PATH shim — only if it's our symlink.** Guard with `[ -L ~/.local/bin/heimdallm ]` and `readlink` resolving into `~/.local/opt/heimdallm/`. Never clobber an unrelated file named `heimdallm` that happens to live there. If the symlink is ours, `rm -f` it; otherwise print a warning and leave it alone.
5. **Remove bundle directory.** `rm -rf ~/.local/opt/heimdallm/`.
6. **Refresh caches (best-effort).** Same `update-desktop-database` and `gtk-update-icon-cache` calls as install — lets launchers drop the stale entry.
7. **Purge (only when `PURGE=1`).**
   - `rm -rf ~/.config/heimdallm/`
   - `rm -rf ~/.local/share/heimdallm/`
   - Print a loud warning that user data was wiped.
   - Never prompt interactively — Make recipes can't assume a TTY.
8. **Final report.**
   - List removed paths.
   - If `PURGE` was not set, print a note that `~/.config/heimdallm/` and `~/.local/share/heimdallm/` still exist, with the `make uninstall-linux PURGE=1` command to wipe them.

Key safety properties:

- **Never-installed machine.** The symlink check in step 4, and the `-f` on every `rm`, mean running `uninstall-linux` on a machine where `install-linux` never ran is a harmless no-op. No "file not found" errors.
- **Unrelated `~/.local/bin/heimdallm`.** If the user (or another tool) put a different binary at that path, step 4 detects it and leaves it alone.
- **Partial install state.** If a prior `install-linux` failed mid-way, `uninstall-linux` still cleans up whatever subset of paths got written.

---

## 4. Error handling & edge cases

- **Not Linux.** `_check-linux` fails fast with a platform-specific message redirecting to `release-local` / `run-linux`. Same shape as `_check-macos`.
- **Docker not installed.** Preflight `command -v docker` check fails with the same message the other Docker-dependent targets use (`verify-linux`, `run-linux`, `test-docker`). Without this explicit check, `verify-linux` would fail first with a less friendly message.
- **Docker image build fails (inside `verify-linux`).** The error surfaces from `verify-linux` itself — `install-linux`'s recipe never runs. This preserves the existing `verify-linux` error semantics; the install target does not try to catch or re-explain them.
- **`docker cp` fails mid-extraction.** The shell `trap` around the `docker create … cp … cp …` block runs `docker rm` on the container even on failure, so no orphaned containers accumulate. The staged `~/.local/opt/heimdallm/` is untouched (step 4 runs only after extraction succeeds).
- **Case-collision guard trips.** Fatal. The staged bundle is left in place (not on the user's `$PATH` yet — that's step 8, after the guard). User fixes and reruns; step 4 starts with `rm -rf`, so state is clean.
- **`~/.local/bin` not on `$PATH`.** Warning only, not an error. The symlink is still created; the desktop entry uses an absolute `Exec=` path, so launcher menu integration works regardless. User can still launch via `~/.local/opt/heimdallm/heimdallm`.
- **Already installed.** `install-linux` overwrites cleanly: `rm -rf` on the bundle dir, `ln -sf` on the symlink, fresh desktop entry / icons. No version-check logic — source installs always reflect whatever the current `heimdallm-verify` image was built from.
- **Daemon running at install time.** `rm -rf ~/.local/opt/heimdallm/` while the daemon is running is safe on Linux (unlinks the directory entry; the running process continues on its now-unlinked inode until it exits normally). The install target does *not* stop running processes — a rebuild-reinstall during active use won't interrupt a review in progress. Stopping processes is the `uninstall-linux` job.
- **Host glibc too old for the Ubuntu-22.04-built binaries.** Surfaces as a missing-symbol error at first launch (not at install). Out of scope for automatic detection; documented as a caveat in §2 "Binary compatibility".
- **No GitHub token available at install time** (neither `$GITHUB_TOKEN` exported nor `gh` authenticated). Warning, not an error — `install-linux` still exits 0. The user sees a multi-line hint immediately above the `✅ Heimdallm installed:` banner telling them how to supply a token, and the first launch will fail with the app's own "daemon failed to start" dialog until one exists at `~/.config/heimdallm/.token`. Rerunning `make install-linux` after `gh auth login` (or exporting `GITHUB_TOKEN`) seeds the token, because the recipe's `[ ! -s ... ]` guard only writes when the file is absent or empty.
- **Existing `~/.config/heimdallm/.token` with a different token.** Left untouched — the seeding step's guard skips the write entirely. A user who manually set a token, or who uses a different GitHub account than `gh auth token` would return, is not overridden. To force a re-seed, they delete the file themselves and rerun `install-linux`.

---

## 5. Manual testing plan

This target interacts with the user's filesystem and launcher. No automated test covers it — the validation is a manual walk-through:

1. **Fresh install.** On a Linux machine with no prior Heimdallm install: `make install-linux`. Verify:
   - `~/.local/opt/heimdallm/heimdallm` and `…/heimdalld` exist, both executable.
   - `~/.local/bin/heimdallm` is a symlink into the bundle.
   - `~/.local/share/applications/com.theburrowhub.heimdallm.desktop` exists and has absolute `Exec=`.
   - Four icon sizes present under `~/.local/share/icons/hicolor/…/apps/heimdallm.png`.
   - App launcher / GNOME overview shows "Heimdallm" (may require re-login if `update-desktop-database` wasn't available).
2. **Launch from terminal.** `heimdallm &`. Verify the Flutter app opens, daemon starts, `curl -s http://localhost:7842/health` returns `{"status":"ok"}`.
3. **Launch from launcher.** Click the app in the OS launcher — same startup sequence.
4. **Reinstall (idempotence).** `make install-linux` a second time. No error, no duplicate state, final report identical.
5. **Uninstall (app only).** `make uninstall-linux`. Verify:
   - All four path groups from step 1 are gone.
   - `~/.config/heimdallm/` and `~/.local/share/heimdallm/` still exist.
6. **Reinstall + purge.** `make install-linux && make uninstall-linux PURGE=1`. Verify step-1 paths gone AND `~/.config/heimdallm/` and `~/.local/share/heimdallm/` gone.
7. **Uninstall on clean machine.** `make uninstall-linux` on a machine where install-linux never ran. Verify no errors, no unexpected deletions.
8. **Unrelated `~/.local/bin/heimdallm` at uninstall time.** Replace the install's symlink with a regular file (e.g. `rm ~/.local/bin/heimdallm && echo '#!/bin/sh' > ~/.local/bin/heimdallm`), then run `make uninstall-linux`. Verify the symlink-check in step 4 kicks in: the unrelated file is left untouched and the target prints a warning naming it. (Note: the install step's `ln -sf` *will* clobber a pre-existing regular file at that path — this is standard behavior for a CLI install, not something the target guards against.)

---

## Out of scope

- **System-wide install** (`/opt/heimdallm/`, `/usr/local/bin/heimdallm`). Developers who want that path should use the CI-built `.deb` / `.rpm`.
- **Host-native Flutter build path.** Users who have Flutter installed locally can run `cd flutter_app && flutter build linux --release` themselves and copy the result into `~/.local/opt/heimdallm/` by hand if they prefer. The `install-linux` target does *not* offer this as an alternate mode — one path, Docker-reuse, keeps the target small.
- **Automatic daemon autostart / systemd unit.** The Flutter app spawns the daemon itself (`daemon_lifecycle.dart`); no separate service file is needed for the install path. `install-service` on macOS creates a LaunchAgent, but Linux doesn't need one for the spawn-from-app model.
- **Running `verify-linux` in a custom configuration.** The install target depends on the default `verify-linux` as-is. Users who want a different Go / Flutter version for the build should edit `Dockerfile.linux-verify` — not an `install-linux` variable.
