# ADR: native macOS install and uninstall targets

**Date:** 2026-07-29

**Issue:** [#604](https://github.com/theburrowhub/heimdallm/issues/604)

**Status:** Accepted design; implementation under review

## Context

macOS users can build a DMG or install one manually, but the repository has no
equivalent to `install-linux` / `uninstall-linux`. A safe native installer must
coordinate a release DMG, `/Applications`, running UI/daemon processes, and a
per-user LaunchAgent without changing the existing Linux flow.

Several facts constrain the design:

- `VERSION` names the next local release, so the installer uses `RELEASE`
  (empty means latest).
- Release DMGs are ad-hoc signed and have no published DMG checksum.
- `make install-service` writes the checkout daemon path into a `KeepAlive`
  LaunchAgent; an installed app must not continue serving that old daemon.
- The UI and daemon names can collide on case-insensitive APFS, so identical
  bundle executables are an unsafe, known corruption mode.
- LaunchAgent state belongs to the invoking user. Running the target as root
  selects the wrong home and launchd domain.

## Decision

### Public interface and platform boundary

Add these POSIX-shell-backed targets:

```text
make install-macos
make install-macos RELEASE=v0.7.5
make uninstall-macos
make uninstall-macos PURGE=1
make test-install-macos
```

`install-macos` and `uninstall-macos` depend on `_check-macos-user`, which first
depends on `_check-macos` and then rejects effective UID 0. The script repeats
both guards, with the OS check as its first runtime action. This prerequisite
is exclusive to install/uninstall: packaging and release targets retain their
existing behavior.

The recipes invoke `scripts/macos-install.sh` directly. `RELEASE` and `PURGE`
supplied on the make command line or inherited from the environment already
reach recipe commands; neither variable is defined or exported globally.
Linux install/uninstall recipes remain unchanged.

### Release and bundle integrity

The helper resolves latest release metadata without `gh` or `jq`, or accepts a
safe `RELEASE` containing only letters, digits, `.`, `_`, and `-`. It downloads
the exact `Heimdallm-<tag>.dmg` asset into a private temporary directory,
verifies it, and mounts it read-only at a dedicated private mountpoint.

Before download, runtime output states that the DMG has no published checksum
and uses an ad-hoc signature. `hdiutil verify` and
`codesign --verify --deep --strict` detect structural corruption; they do not
authenticate the publisher.

Both source and staged bundles must be real directories, not symlinks. Their
UI and daemon executables must be real, executable, and different files.
Staging uses `ditto` on the `/Applications` filesystem, clears quarantine with
`xattr -cr`, and repeats all validation before any installed bundle is moved.

### LaunchAgent and port preflight

Preflight is read-only. It snapshots plist presence/content/mode, loaded PID,
and persistent disabled state. Loaded-and-disabled is valid. Inspection errors,
or a loaded job without a reconstructable plist, abort before download.

Port 7842 is classified as:

- `free`;
- `bundle`, for either exact executable in the installed app;
- `service`, for the captured LaunchAgent PID; or
- `foreign`, for everything else.

This matrix is the authoritative preflight policy and the pure-test oracle.
The two inconsistent `service` cells are defensive states:

| LaunchAgent | Free | Bundle | Service | Foreign |
|---|---|---|---|---|
| Absent | proceed | stop bundle | abort | warn |
| Present, unloaded | migrate unloaded | stop + migrate | abort | warn + migrate |
| Loaded, enabled | restart | stop + restart | restart | abort |
| Loaded, disabled | migrate disabled | stop + migrate | migrate disabled | warn + migrate |

A foreign listener does not prevent copying files unless a loaded/enabled
service must restart; otherwise it is reported before and after install. A
second classification immediately before swap closes the preflight race and
may fail more strictly, but cannot reinterpret the table silently.

### Privileges, transaction, and rollback

All LaunchAgent, process-discovery, and home-directory operations run as the
invoking user. If `/Applications` requires elevation, one interactive
`sudo -v` is allowed immediately before staging. Every later privileged
staging, ownership, swap, cleanup, and rollback command uses `sudo -n`.
There is no second interactive refresh: scoped sudo commands renew the normal
timestamp, while expiration remains a fail-closed rollback condition.

Staged ownership is explicit: `root:admin` for an elevated install, otherwise
the invoking UID and primary GID. The current bundle is moved to a same-filesystem
backup, then staging is renamed into place. The backup survives until bundle
and LaunchAgent checks succeed.

Cleanup and rollback are separate, idempotent functions driven by explicit
intent/completion flags plus filesystem evidence for signal windows. `EXIT`,
`HUP`, `INT`, and `TERM` cannot resume the transaction after handling. Commit
disarms rollback. If privileged restoration fails, backups are preserved and
the exact manual recovery commands are printed; success or complete rollback
is never claimed falsely. A lingering process or foreign port may force the
restored plist to remain unloaded for safety.

Only exact installed executables are stopped, for the invoking UID, with one
anchored UI/daemon pattern. Shutdown is TERM, up to six seconds of waiting,
then KILL as a last resort. Checkout/dev processes and other users are never
killed implicitly.

### LaunchAgent migration

An uninstall always removes the LaunchAgent because `RunAtLoad` + `KeepAlive`
is executable state, not user data. Cleanup addresses the exact launchctl label
and plist directly rather than relying on a still-present daemon binary.

Install preserves the original state:

- Loaded and enabled: run the bundled `heimdalld install`, which regenerates
  the canonical plist and therefore discards manual plist edits. Require a
  stable loaded PID at the installed path, listening on port 7842, within
  60 seconds; otherwise restore the prior bundle and service state.
- Unloaded or disabled: replace only `ProgramArguments[0]` with `plutil`,
  preserve other keys/mode/disabled flag, and do not bootstrap. A previously
  loaded-and-disabled job ends disabled and unloaded.

### Uninstall and purge

Empty `PURGE` means normal uninstall; exact `PURGE=1` requests deletion. Every
other non-empty value aborts before the LaunchAgent, processes, app, or user
data is changed.

Normal uninstall unloads/removes the LaunchAgent, stops exact installed
processes, removes the app, and preserves config, review history, logs, custom
paths, and the Keychain credential. A stale/installed `ui.pid` is removed; a
live development UI's PID tracking is preserved.

For `PURGE=1`, print the irreversible review-history warning before a read-only
preflight. The preflight:

- allows holders proven to be the installed bundle or captured LaunchAgent,
  because teardown owns them;
- rejects a live foreign `ui.pid` or foreign holder of the exact SQLite file;
- fails closed on ambiguous ownership or inspection errors; and
- makes no launchd, process, app, plist, or data mutation.

After known components are removed, a late strict `lsof` check of
`~/.local/share/heimdallm/heimdallm.db` permits no holder; this is the race
guard before deletion. It does not use slow/broad `lsof +D`.

Purge removes exactly these canonical paths, without sudo:

- `~/.config/heimdallm`
- `~/.local/share/heimdallm`
- `~/Library/Logs/heimdallm`

It never follows `HEIMDALLM_CONFIG_PATH` or `HEIMDALLM_DATA_DIR`. The Keychain
item (`heimdallm` / `github-token`) is preserved and its explicit `security
delete-generic-password` command is printed. Both uninstall modes are
idempotent across absent/partial bundle, plist, and job states.

## Consequences

- The installed bundle is swapped transactionally rather than overwritten.
- A normal uninstall cannot leave launchd retrying a missing executable.
- Purge matches Linux data-directory semantics while preserving the external
  credential store.
- Authenticity still depends on GitHub HTTPS. Publishing a DMG checksum and
  Developer ID/notarization are separate release-pipeline work.
- The manual DMG fallback remains available, but it cannot migrate an existing
  LaunchAgent and must document the same six-second TERM→KILL grace period.

## Verification

### Automated

`make test-install-macos` performs shell syntax checks and dependency-free pure
tests for release/PURGE validation, canonical paths, listener classification,
all 16 matrix cells, rollback-required decisions, unarmed/armed/idempotent
cleanup, and known-versus-foreign purge-holder decisions. It does not fake
successful `hdiutil`, launchd, sudo, process signals, or filesystem swaps.

Linux CI additionally:

1. asserts both scripts are executable and exercises their shebangs;
2. dry-runs unchanged Linux install/uninstall recipes;
3. checks Make targets fail at the macOS guard with explicit diagnostics;
4. checks direct helper operations fail at their Linux guard with diagnostics.

The existing daemon, CLI, and Flutter CI jobs remain unchanged. On an
EDR-protected development machine, the local daemon gate is `make test-docker`
as required by `AGENTS.md`; GitHub's macOS runner may run its existing native
Go test job.

### Required macOS scenarios

1. Clean install: integrity, ownership, launch/health, and temp/mount cleanup.
2. Reinstall while UI/daemon run: scoped shutdown, swap, preserved user state.
3. LaunchAgent migration: checkout service becomes the installed 60-second-
   readiness-verified service.
4. Normal uninstall: app/service gone; database/config/log/Keychain preserved.
5. Purge: a foreign development DB holder aborts read-only; after it stops,
   known holders are torn down, the late DB guard passes, canonical data is
   removed, and Keychain/custom paths remain.

Optional edge checks cover invalid/missing releases, identical binaries,
forced rollback failure, writable/non-writable `/Applications`, root rejection,
foreign listeners, unloaded/disabled plist migration, partial states,
other-user processes, `ui.pid`, repeated uninstall, and invalid `PURGE`.
