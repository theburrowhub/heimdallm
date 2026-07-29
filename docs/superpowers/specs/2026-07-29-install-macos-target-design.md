# `make install-macos` / `make uninstall-macos` (#604)

**Date:** 2026-07-29
**Issue:** [#604 — feat(makefile): add install-macos / uninstall-macos targets](https://github.com/theburrowhub/heimdallm/issues/604)
**Status:** Approved after review
**Revision:** 2026-07-29 — hardened LaunchAgent teardown, port preflight,
privilege boundaries, rollback/purge safety, and Linux non-regression.

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
  The new helper does not rely on that path for cleanup: it would be
  unavailable with a missing bundle, and the current implementation ignores a
  `launchctl bootout` error before removing the plist.
- **A LaunchAgent is executable state, not user data.** Its plist uses both
  `RunAtLoad` and `KeepAlive`, so leaving it installed after an uninstall would
  either keep Heimdallm running or repeatedly try to launch a binary that no
  longer exists. `uninstall-macos` therefore removes it even without `PURGE=1`.
- **`make install-service` points at the checkout.** It embeds the absolute
  `daemon/bin/heimdallm` path in the plist, not the copy inside
  `/Applications`. An install/update must migrate an existing LaunchAgent to
  the new bundle or the UI can keep talking to an older checkout daemon. The
  Flutter startup health gate (`main.dart:119-121` and
  `DaemonLifecycle.ensureRunning`) reuses any healthy daemon already listening
  on port 7842.
- **Privilege escalation must not cover user state.** LaunchAgent operations,
  process discovery, `~/.config`, `~/.local/share`, and Keychain all belong to
  the invoking user. Running the complete target through `sudo` changes the
  effective UID and can address the wrong launchd domain or home directory.

## Decision

Add two thin public targets to the root `Makefile`, next to the Linux pair,
reusing the existing `_check-macos` guard (`Makefile:233`):

- add `install-macos`, `uninstall-macos`, `test-install-macos`, and the existing
  `_check-macos` guard to `.PHONY`;
- `install-macos` exports `RELEASE` as a target-specific environment value and
  invokes `scripts/macos-install.sh install`;
- `uninstall-macos` exports `PURGE` the same way and invokes
  `scripts/macos-install.sh uninstall`;
- `test-install-macos` runs a small dependency-free shell harness over the
  helper's pure validation/path/flag logic.

The helper is POSIX shell and owns the transaction, traps, launchd state, and
privilege boundary. Keeping that state machine in one normal script avoids
Make's per-line subshells and double-dollar escaping, and lets the destructive
paths be reviewed without Make escaping. This deliberately departs from
`install-linux`'s inline recipe: the reviewed macOS flow coordinates an app
bundle, a mounted volume, selective `sudo`, and a LaunchAgent, so symmetry is
less valuable than auditable cleanup. Real launchd, mount, privilege, and swap
behavior remains integration-tested on macOS rather than simulated in shell.

Rejected alternatives: one large inline Make recipe (too fragile for the
transaction and signal handling described below), and building locally with
`package-macos` instead of downloading (slower, and the point of the request is
to get a working app without a Flutter toolchain run).

```
make install-macos                  # latest release → /Applications
make install-macos RELEASE=v0.7.5   # pinned release
make uninstall-macos                # remove app + service; user state preserved
make uninstall-macos PURGE=1        # permanently wipe config, history and logs
```

`/Applications` over `~/Applications`: it matches what `README.md:43` and
`LLM-HOW-TO-INSTALL.md` already document, and it is writable without `sudo` for
an admin user in the common case. The targets themselves must never be invoked
as root. When `/Applications` needs elevation, the recipe uses `sudo` only for
the narrowly scoped bundle staging, swap, `xattr`, and removal commands; all
LaunchAgent and home-directory work remains under the invoking user.

## Design

### `install-macos`

1. `_check-macos` — fail fast on Linux with a pointer to `install-linux`.
   Reject effective UID 0 with a clear "run `make install-macos` without
   sudo" message, and validate the invoking user's home/UID before resolving
   any path.
2. Snapshot the LaunchAgent state read-only: whether the plist exists, whether
   the job is loaded (including its PID), and whether its label is disabled in
   the user's launchd domain. Abort before downloading anything if inspection
   fails or a loaded job has no plist and therefore cannot be reconstructed.
   A loaded-and-disabled job is legitimate: `launchctl disable` persists the
   flag without stopping an already-running process, so preserve that state for
   the migration in step 14.
3. Inspect port 7842 before downloading anything and classify its listener:
   - a PID running either exact executable inside
     `/Applications/Heimdallm.app` is bundle-owned;
   - the PID captured from the known LaunchAgent is service-owned;
   - anything else is a foreign daemon.

   Known bundle/service listeners are expected and will be stopped later. A
   foreign daemon normally does **not** block file installation: record its PID
   and executable and print a prominent warning before and after install that
   it must be stopped before opening the app. If an enabled, loaded LaunchAgent
   must be restarted, however, abort here while the operation is still
   read-only; restarting a `KeepAlive` job against an occupied port would cause
   a failure loop.
4. Resolve the tag: use `RELEASE` when set; otherwise `curl -fsSL` the
   `releases/latest` endpoint and extract `tag_name` via `sed`. Fail if the
   result is empty or contains characters outside the release-tag-safe set
   (`A-Z`, `a-z`, digits, `.`, `_`, and `-`).
5. Create a private `mktemp -d` containing the DMG and a dedicated empty mount
   directory. The helper has separate idempotent `cleanup` and `rollback`
   functions plus explicit success/transaction flags. `EXIT` performs cleanup
   and rolls back only an armed, unsuccessful transaction. `HUP`, `INT`, and
   `TERM` disable their own traps, run the same idempotent rollback/cleanup,
   then terminate with the corresponding signal status; execution must never
   resume after a signal handler. A successful commit disarms rollback before
   normal `EXIT` cleanup runs.
6. `curl -fL` the exact
   `releases/download/<tag>/Heimdallm-<tag>.dmg` asset. A missing asset must
   produce a clear "this release has no macOS DMG" message rather than a later
   `hdiutil` failure.
7. Run `hdiutil verify`, then attach read-only with
   `hdiutil attach -readonly -nobrowse -quiet -mountpoint <private-mount>`.
   The source is exactly `<private-mount>/Heimdallm.app`; never search global
   `/Volumes`, where a stale mount from another release could be selected.
8. Validate the source before touching the installed app:
   - `Contents/MacOS/Heimdallm` and `Contents/MacOS/heimdalld` both exist and
     are executable.
   - `cmp -s` shows they differ — abort on identical binaries (fork-bomb state).
   - `codesign --verify --deep --strict` succeeds. The signature is ad-hoc, so
     this detects structural corruption but does not authenticate the publisher.
9. Copy the validated bundle with macOS `ditto` to a unique staging path on
   the `/Applications` filesystem, apply `xattr -cr` there, and re-run the
   binary/signature guards against the staged copy. The current installed app
   remains untouched throughout this step. If elevation is needed, request and
   validate the sudo ticket with one interactive `sudo -v` immediately before
   staging; this is the only operation allowed to prompt. All subsequent
   transactional privileged commands use non-interactive `sudo -n` so a prompt
   can never appear halfway through rollback. Never suggest
   `sudo make install-macos`. Do not preserve UID/GID values from the DMG:
   explicitly run `/usr/sbin/chown -R` on the exact staging path before the
   swap, using `sudo -n` for `root:admin` when elevated or the invoking user's
   UID and primary GID when unprivileged.
10. If a plist was present, back up its exact contents and permissions. If its
    job was loaded, `bootout`, verify it is absent, and wait up to six seconds
    for the captured PID to exit. If that process remains alive, restore the
    original plist but leave the job unloaded and abort; bootstrapping another
    `KeepAlive` instance while the old process lingers is unsafe. Print the
    exact manual bootstrap command to run after the captured PID has exited.
11. Stop bundle-owned processes with one scoped pattern:
   `^/Applications/Heimdallm[.]app/Contents/MacOS/(Heimdallm|heimdalld)([[:space:]]|$)`.
   Anchoring at argv[0] avoids matching diagnostics that merely mention a
   bundle path. Signal only the invoking user's real UID with
   `pkill -TERM -U <uid> -f <pattern>`. This one expression covers both exact
   executables; no second daemon-specific `pkill` is needed. Wait up to six
   seconds, then use `KILL` with the same UID/pattern only as a last resort. If
   a matching process owned by another user remains, abort rather than
   replacing a shared bundle that another login session is using. Never use a
   bare Heimdallm name, so a concurrent `make dev` survives.
12. Recheck the port classification to catch a listener that appeared after
    preflight. A newly foreign listener follows the same policy as step 3:
    record-and-warn when no service must be restarted. When an enabled
    LaunchAgent was loaded, restore its original plist but deliberately leave
    the job unloaded, then abort before swap; re-bootstrapping it against the
    now-occupied port would create the `KeepAlive` loop preflight was designed
    to prevent. Print the exact manual bootstrap command to run after port 7842
    is free. This second check is only a race guard; predictable conflicts were
    handled before download or mutation.
13. Swap on the same filesystem: move the current bundle to a unique backup,
    move the staged bundle to `/Applications/Heimdallm.app`, and keep the
    backup until all remaining checks pass. Rollback first attempts to restore
    the old bundle and service using the prevalidated `sudo -n` ticket. If that
    recovery itself fails, never delete the backup: exit non-zero and print its
    exact path plus explicit manual recovery commands. Do not claim success or
    claim that rollback completed when it did not. Safety overrides exact state
    restoration while a foreign listener owns port 7842 or the original daemon
    has not exited: restore the plist but leave the job unloaded until the
    process/port conflict is gone.
14. Migrate a pre-existing plist according to its original state:
    - **Loaded and enabled (normal `make install-service` case):** invoke
      `/Applications/Heimdallm.app/Contents/MacOS/heimdalld install` as the
      invoking user. `os.Executable()` regenerates the canonical plist pointing
      at the installed daemon and bootstraps it. Verify the new path and loaded
      PID.
    - **Unloaded or disabled:** use system `/usr/bin/plutil` to replace only
      `ProgramArguments[0]` (`-replace ProgramArguments.0 -string`), restore the
      original plist permissions and persistent disabled flag, and do not
      bootstrap it. This includes a job that was both loaded and disabled:
      step 10 boots it out, this branch migrates its plist, and the job remains
      disabled and unloaded.

    If a foreign listener appeared before restarting the normal loaded case,
    roll back and abort rather than starting a `KeepAlive` failure loop.
    Restore the original plist but leave it unloaded, and print the manual
    bootstrap command. Any other migration failure arms restoration of both the
    old bundle and exact original plist/job state.
15. Remove the app/service backups, detach the private mount, and print the
    installed tag, the launch hint (`open -a Heimdallm`), and a note that
    config, history, logs, and the Keychain credential are untouched. Repeat
    the recorded foreign-daemon warning, including PID/executable and the
    instruction to stop it before opening Heimdallm.

### `uninstall-macos`

1. `_check-macos`, reject effective UID 0, and resolve the invoking user's
   absolute home and UID once. Refuse to continue if the home is empty, not
   absolute, or `/`.
2. Remove the LaunchAgent on every uninstall, before signaling processes.
   Address the known `gui/<uid>/com.heimdallm.daemon` job and
   `~/Library/LaunchAgents/com.heimdallm.daemon.plist` directly, so cleanup does
   not depend on an intact bundle. Capture its PID when loaded, `bootout`,
   verify the job is absent, wait up to six seconds for that PID to exit, and
   only then remove the plist. Already-missing job/plist is success; a job or
   process that remains alive is an error and aborts the uninstall.
3. Stop bundle-owned UI and daemon processes with the same single
   exact-executable pattern, invoking-user UID restriction, and bounded
   TERM→wait→KILL sequence used by install. If another user's matching process
   remains, abort. Verify that no match remains before removing the bundle.
4. Clean `~/.local/share/heimdallm/ui.pid` when it is stale or belongs to the
   installed UI. If it names a live Heimdallm UI outside `/Applications`
   (for example `flutter run`), preserve it and print a warning instead of
   breaking that session's single-instance tracking during a normal uninstall;
   with `PURGE=1`, abort before deleting any user data because the purge would
   remove that live development session's data directory.
5. Remove `/Applications/Heimdallm.app`, elevating only this exact operation
   when required. Running `sudo make uninstall-macos` is unsupported.
6. Without `PURGE=1`, preserve and print the config, data/history, logs, and
   Keychain credential locations. The LaunchAgent is still removed because it
   is executable/autostart state, not user data.
7. With `PURGE=1`:
   - Print an explicit warning before deletion:
     **"PURGE=1 permanently deletes the Heimdallm database and all review
     history; this cannot be undone."**
   - Verify the LaunchAgent and bundle processes are gone.
   - If `~/.local/share/heimdallm/heimdallm.db` exists, inspect that exact file
     with system `/usr/sbin/lsof`. A live daemon holds the main SQLite file even
     when WAL mode is active. Abort if it is open. Fail closed if `lsof` is
     unavailable or cannot inspect the file; only its documented "no open
     handles" result permits deletion. Avoid `lsof +D`, which is slower and can
     fail on unrelated files below the data directory.
   - Remove exactly `~/.config/heimdallm`, `~/.local/share/heimdallm`, and
     `~/Library/Logs/heimdallm`, without `sudo`.
   - Do not follow `HEIMDALLM_CONFIG_PATH` or `HEIMDALLM_DATA_DIR` to arbitrary
     locations. State that custom override paths are outside the purge contract.
   - Preserve the macOS Keychain item (`service=heimdallm`,
     `account=github-token`) to match the external credential-store behavior on
     Linux. Print this explicitly and provide
     `security delete-generic-password -s heimdallm -a github-token` for users
     who also want to remove the local credential.

Both modes are idempotent on a never-installed machine and recover cleanly from
partial state such as "bundle absent, plist present".

### Authenticity and integrity

The release has no published checksum covering the DMG, and its ad-hoc
signature carries no trusted Developer ID identity. `hdiutil verify` and
`codesign --verify` detect accidental container/bundle corruption, but neither
authenticates the publisher; provenance is still trusted through GitHub HTTPS.
Publishing a DMG checksum and using a Developer ID/notarization flow are release
pipeline changes deliberately out of scope. The limitation remains recorded in
#604 so these structural checks are not mistaken for authenticity guarantees.

### Documentation

`README.md` and `LLM-HOW-TO-INSTALL.md` lead with the target and keep the
manual commands as the fallback for machines without a checkout. They also
state:

- run the Make targets as the normal user, never through `sudo`;
- an existing LaunchAgent is migrated on install and removed on uninstall;
- normal uninstall preserves config, history, logs, and Keychain;
- `PURGE=1` irreversibly removes all review history at the three canonical
  desktop paths, but preserves Keychain and ignores custom path overrides.

## Testing

### Automated pure-logic checks

Add `scripts/test-macos-install.sh`, invoked by `make test-install-macos`. Keep
it deliberately small: `sh -n` plus unit checks for release-tag validation,
exact `PURGE=1` parsing, safe canonical-path construction, listener
classification from supplied PID/path facts, and idempotent cleanup state when
no external resource is armed.

Do not stub a successful `hdiutil`, `launchctl`, `sudo`, process signal, or
filesystem swap. Such tests would mostly assert that the implementation calls
the commands encoded in the test while missing the behavior that makes an
installer risky. Those integrations are exercised with the real macOS tools.

### Required macOS verification

Record these five scenarios in the PR:

1. **Clean install.** No prior bundle or LaunchAgent. Confirm the release
   version, distinct executables, signature check, expected ownership, launch,
   `/health`, and mount/temp cleanup.
2. **Reinstall while running.** Start the installed UI and bundle daemon, then
   reinstall. Confirm the single UID-scoped process pattern stops both,
   staging/swap succeeds, user state is preserved, and the replacement launches.
3. **LaunchAgent migration.** Start with `make install-service`, then install.
   Confirm preflight classifies the listener as known, the bundled
   `heimdalld install` path regenerates the plist under `/Applications`, and the
   loaded service runs the installed version.
4. **Normal uninstall preserves history.** Seed a review row plus config/log/
   Keychain sentinels. Confirm app and LaunchAgent disappear while the review
   remains queryable and all user-state sentinels remain.
5. **Purge guard and purge.** With a development daemon outside the installed
   bundle/LaunchAgent holding `heimdallm.db`, confirm `PURGE=1` stops the known
   installed components but aborts before deleting any user state. Stop the
   development daemon and rerun; confirm the irreversible warning, removal of
   the three canonical directories, and preservation of Keychain/custom
   override paths.

### Optional macOS edge checklist

Exercise when the implementation or environment touches the relevant path:

- pinned/invalid/missing/truncated releases and the identical-binary guard;
- a forced swap or migration failure, including manual recovery output if
  privileged rollback cannot complete;
- non-writable `/Applications`, root invocation rejection, and ownership in
  both privileged and unprivileged installs;
- a foreign `make dev-daemon` listener: installation succeeds without killing
  it and prints the warning before and after;
- unloaded/disabled plist migration via `plutil`;
- partial states (bundle/plist/job independently absent), another user's
  bundle process, stale versus live-development `ui.pid`, repeated uninstall,
  and values other than exact `PURGE=1`.

### Linux non-regression gate

This feature is macOS-only, but the Makefile change must remain additive:

- do not change the recipes or variables used by `install-linux` and
  `uninstall-linux`;
- keep `RELEASE` and `PURGE` target-specific so they do not leak into other
  targets;
- run no `hdiutil`, `launchctl`, `codesign`, `ditto`, macOS `pkill`, or `lsof`
  command at Make parse time;
- keep `_check-macos` as the Make prerequisite and repeat the `uname -s` guard
  as the helper's first executable check, so direct script invocation is also
  harmless on Linux.

Run the following on a Linux runner/container before merge:

1. `sh -n scripts/macos-install.sh && sh -n scripts/test-macos-install.sh`.
2. `make test-install-macos` — pure logic only, no macOS integration.
3. `make -n install-linux` and `make -n uninstall-linux` — both existing recipes
   still parse unchanged.
4. Execute `make install-macos` and `make uninstall-macos` and assert that both
   fail at `_check-macos`, before invoking the helper or changing any file.
5. `make test-docker`, as required by `AGENTS.md`; never run daemon `go test`
   directly on the host.
