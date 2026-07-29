# LLM How-To: Install or Update Heimdallm

Step-by-step guide for Claude Code or any AI agent to install Heimdallm on macOS or Linux.

- **macOS with a repository checkout**: use the preferred Make target below
- **macOS without a checkout**: use steps 1–5 (manual DMG fallback)
- **Linux**: jump to [Linux installation](#linux-installation)

---

## macOS from a repository checkout (preferred)

Run from the repository root as the normal user:

```bash
make install-macos                  # latest release
make install-macos RELEASE=v0.7.5   # specific release
```

Never run `sudo make install-macos` or `sudo make uninstall-macos`. If writing
to `/Applications` requires elevation, the helper obtains one sudo ticket and
elevates only the app-bundle operations. It keeps LaunchAgent and home-directory
work in the invoking user's domain.

If a Heimdallm LaunchAgent already exists, install migrates it to the daemon
inside `/Applications/Heimdallm.app`. A loaded, enabled LaunchAgent is
regenerated from the bundled daemon, which discards manual plist edits. For an
unloaded or disabled LaunchAgent, only the daemon executable path changes and
the remaining plist keys are preserved. Uninstall removes the app and
LaunchAgent even without purge:

```bash
make uninstall-macos                # preserve config, history, logs, and Keychain
make uninstall-macos PURGE=1        # irreversibly delete canonical user state
```

`PURGE=1` deletes exactly `~/.config/heimdallm`,
`~/.local/share/heimdallm` (including the review database), and
`~/Library/Logs/heimdallm`. It does not follow custom config/data path
overrides, and it preserves the Keychain item. To remove the credential too,
run:

```bash
security delete-generic-password -s heimdallm -a github-token
```

Leave `PURGE` empty for a normal uninstall. Any non-empty value other than
exact `PURGE=1` is an error and aborts before changing the LaunchAgent, app, or
user data.

After a successful install, skip the manual download steps and continue at
[Launch](#4-launch).

---

## macOS manual DMG fallback (no checkout)

Use this path only when the repository and its Make target are unavailable.
Unlike the preferred target, these commands do not migrate a pre-existing
LaunchAgent. If
`~/Library/LaunchAgents/com.heimdallm.daemon.plist` exists, stop here and use
the repository target; the manual fallback can otherwise leave an old
`KeepAlive` service running against the replacement app.

## 1. Get the latest version

```bash
VERSION=$(gh release view --repo theburrowhub/heimdallm --json tagName -q '.tagName')
DOWNLOAD_URL=$(gh release view --repo theburrowhub/heimdallm --json assets \
  -q '.assets[] | select(.name | endswith(".dmg")) | .browserDownloadUrl')

echo "Installing Heimdallm $VERSION"
```

---

## 2. Download and install

```bash
# Download
curl -L "$DOWNLOAD_URL" -o "/tmp/Heimdallm.dmg" --progress-bar

# Mount
hdiutil attach /tmp/Heimdallm.dmg -nobrowse -quiet
sleep 2

# Find the app inside the mounted volume
APP_SRC=$(find /Volumes -name "Heimdallm.app" 2>/dev/null | head -1)

# Verify the bundle has both binaries before installing
FLUTTER_BIN="$APP_SRC/Contents/MacOS/Heimdallm"
DAEMON_BIN="$APP_SRC/Contents/MacOS/heimdalld"

if [ ! -f "$FLUTTER_BIN" ] || [ ! -f "$DAEMON_BIN" ]; then
  echo "❌ Bundle is incomplete. Do not install — re-download from releases."
  exit 1
fi

if cmp -s "$FLUTTER_BIN" "$DAEMON_BIN"; then
  echo "❌ Bundle is corrupt (both binaries are identical). Do not install."
  exit 1
fi

echo "✓ Bundle looks good ($(ls -lh "$DAEMON_BIN" | awk '{print $5}') daemon, $(ls -lh "$FLUTTER_BIN" | awk '{print $5}') launcher)"

# Install
APP_PATTERN='^/Applications/Heimdallm[.]app/Contents/MacOS/(Heimdallm|heimdalld)([[:space:]]|$)'
pkill -TERM -U "$(id -u)" -f "$APP_PATTERN" 2>/dev/null || true
sleep 6
pkill -KILL -U "$(id -u)" -f "$APP_PATTERN" 2>/dev/null || true
rm -rf /Applications/Heimdallm.app
cp -R "$APP_SRC" /Applications/Heimdallm.app
echo "✓ Installed to /Applications"
```

---

## 3. Allow macOS to run it

macOS blocks apps not signed by Apple by default. This one-time command removes that restriction:

```bash
xattr -cr /Applications/Heimdallm.app
echo "✓ macOS restriction removed"
```

---

## 4. Launch

```bash
open /Applications/Heimdallm.app
sleep 5

# Confirm it started (should be exactly 1)
echo "Instances running: $(pgrep -c Heimdallm 2>/dev/null || echo 0)"

# Confirm the background service is up
curl -s http://localhost:7842/health
# Expected: {"status":"ok"}
```

On first launch Heimdallm detects your `gh` token automatically and sets itself up.

---

## 5. Cleanup

```bash
HEIMDALLM_DEV=$(mount | grep -i "Heimdallm" | awk '{print $1}' | head -1)
[ -n "$HEIMDALLM_DEV" ] && hdiutil detach "$HEIMDALLM_DEV" 2>/dev/null
rm -f /tmp/Heimdallm.dmg
```

---

## Linux installation

### 1. Get the latest version

```bash
VERSION=$(gh release view --repo theburrowhub/heimdallm --json tagName -q '.tagName')
echo "Installing Heimdallm $VERSION"
```

### 2. Download and install for your distro

**Ubuntu / Debian / Mint / Pop!_OS (.deb):**
```bash
curl -L "$(gh release view --repo theburrowhub/heimdallm --json assets \
  -q '.assets[] | select(.name | endswith(".deb")) | .browserDownloadUrl')" \
  -o /tmp/heimdallm.deb
sudo dpkg -i /tmp/heimdallm.deb
sudo apt-get install -f -y   # install any missing dependencies
```

**Fedora / RHEL / openSUSE (.rpm):**
```bash
curl -L "$(gh release view --repo theburrowhub/heimdallm --json assets \
  -q '.assets[] | select(.name | endswith(".rpm")) | .browserDownloadUrl')" \
  -o /tmp/heimdallm.rpm
sudo rpm -i /tmp/heimdallm.rpm
```

**Any distro (AppImage):**
```bash
APPIMAGE_URL=$(gh release view --repo theburrowhub/heimdallm --json assets \
  -q '.assets[] | select(.name | endswith(".AppImage")) | .browserDownloadUrl')
curl -L "$APPIMAGE_URL" -o ~/bin/Heimdallm.AppImage
chmod +x ~/bin/Heimdallm.AppImage
```

**From a clone of this repo (Docker-built, no release download):**

If the agent already has the repo cloned and Docker available, skip the release download entirely and build from source inside the `heimdallm-verify` Docker image:

```bash
cd /path/to/heimdallm      # must be the repo root
make install-linux         # verify-linux (Docker build) → docker cp → ~/.local/ staging
```

Installs to `~/.local/opt/heimdallm/`, adds a `~/.local/bin/heimdallm` symlink, a desktop entry, and four icon sizes under `~/.local/share/icons/hicolor/`. Seeds `~/.config/heimdallm/.token` from `$GITHUB_TOKEN` or `gh auth token` so the GUI-launched app finds the token on first run. No host Flutter SDK needed; Docker does the build. To remove: `make uninstall-linux` (add `PURGE=1` to wipe `~/.config/heimdallm` and `~/.local/share/heimdallm` too).

### 3. Launch

```bash
# .deb/.rpm — binary is in PATH
heimdallm &
sleep 5

# AppImage
~/bin/Heimdallm.AppImage &
sleep 5

# Confirm daemon is responding
curl -s http://localhost:7842/health
# Expected: {"status":"ok"}
```

### 4. Cleanup

```bash
rm -f /tmp/heimdallm.deb /tmp/heimdallm.rpm
```

### Troubleshooting (Linux)

**Missing dependencies after dpkg install**
```bash
sudo apt-get install -f -y
```
Installs `libgtk-3-0`, `libayatana-appindicator3-1`, `libnotify4`, `libsecret-1-0`.

**Token not detected**

**Token resolution order:**
- **macOS**: Keychain → `GITHUB_TOKEN` env var → `~/.config/heimdallm/.token`
- **Linux**: `GITHUB_TOKEN` env var → `~/.config/heimdallm/.token`
- **Docker**: `GITHUB_TOKEN` env var → `/config/.token` mount → `~/.config/heimdallm/.token`

**AppImage won't run**
```bash
sudo apt-get install -y libfuse2   # Ubuntu 22.04+
```

---

## Troubleshooting (macOS)

**Hundreds of instances spawn immediately**
```bash
pkill -9 -f Heimdallm
```
The bundle is corrupt — both binaries were the same file. Step 2 guards against this; if it happened anyway, re-download.

**"Operation not permitted" / app killed on launch**
Step 3 was skipped or ran on the DMG volume (read-only). Re-run it on `/Applications/Heimdallm.app`.

**App opens and closes immediately**
Run from Terminal to see errors:
```bash
/Applications/Heimdallm.app/Contents/MacOS/Heimdallm 2>&1 &
sleep 4 && pgrep Heimdallm
```

**Daemon not responding after 10 seconds**
```bash
/Applications/Heimdallm.app/Contents/MacOS/heimdalld &
sleep 2 && curl -s http://localhost:7842/health
```
If it exits, there's no config yet — the app creates it on first launch.

---

## Updating

From a repository checkout, rerun `make install-macos` (optionally with
`RELEASE=vX.Y.Z`). It preserves config (`~/.config/heimdallm/`), history
(`~/.local/share/heimdallm/`), logs, custom override paths, and the Keychain
credential. Without a checkout, repeat the manual macOS steps from step 1.
