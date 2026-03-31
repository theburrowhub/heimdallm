# LLM How-To: Install or Update Heimdallr

This document is written for Claude Code, Cursor, or any AI agent helping a user install or update Heimdallr on macOS. Follow these steps precisely — each one handles a common failure mode.

---

## Prerequisites check

Before starting, verify:

```bash
# macOS version (requires 13+)
sw_vers -productVersion

# gh CLI installed and authenticated
gh auth status

# Architecture (arm64 = Apple Silicon, x86_64 = Intel)
uname -m
```

If `gh auth status` fails, ask the user to run `gh auth login` first.

---

## Step 1: Get the latest release URL

```bash
DOWNLOAD_URL=$(gh release view --repo theburrowhub/heimdallr --json assets \
  -q '.assets[] | select(.name | endswith(".dmg")) | .browserDownloadUrl' \
  | head -1)

VERSION=$(gh release view --repo theburrowhub/heimdallr --json tagName -q '.tagName')

echo "Installing Heimdallr $VERSION"
echo "From: $DOWNLOAD_URL"
```

---

## Step 2: Download the DMG

```bash
curl -L "$DOWNLOAD_URL" -o "/tmp/Heimdallr-${VERSION}.dmg" --progress-bar
```

Verify the download completed:
```bash
ls -lh "/tmp/Heimdallr-${VERSION}.dmg"
# Should be ~28-35 MB
```

---

## Step 3: Mount the DMG

```bash
hdiutil attach "/tmp/Heimdallr-${VERSION}.dmg" -nobrowse -quiet
sleep 2
```

Find the mounted app:
```bash
APP_SRC=$(find /Volumes -name "Heimdallr.app" -newer "/tmp/Heimdallr-${VERSION}.dmg" 2>/dev/null | head -1)
# Fallback if timestamp comparison fails:
[ -z "$APP_SRC" ] && APP_SRC=$(ls -dt /Volumes/Heimdallr*/Heimdallr.app 2>/dev/null | head -1)
echo "Source: $APP_SRC"
```

**Verify the bundle has both binaries** (critical — if both are same size, the build is broken):
```bash
ls -lh "$APP_SRC/Contents/MacOS/"
# Expected:
#   heimdalld   ~15 MB   ← Go daemon
#   Heimdallr   ~164 KB  ← Flutter launcher
#
# If only one file or both are 15 MB → do NOT install, the build has a bug.
```

---

## Step 4: Install to /Applications

Stop any running instance first:
```bash
pkill -9 -f "Heimdallr" 2>/dev/null; sleep 1
```

Copy and replace:
```bash
rm -rf /Applications/Heimdallr.app
cp -R "$APP_SRC" /Applications/Heimdallr.app
echo "✓ Copied to /Applications"
```

---

## Step 5: Remove quarantine (required for ad-hoc signed builds)

macOS quarantines apps downloaded from the internet. This must be done **after** copying to /Applications (the DMG volume is read-only — xattr would silently fail there).

```bash
xattr -cr /Applications/Heimdallr.app
echo "✓ Quarantine removed"
```

Verify:
```bash
xattr -l /Applications/Heimdallr.app | grep quarantine | wc -l
# Should print 0
```

---

## Step 6: Re-sign with local entitlements

The CI build uses ad-hoc signing. Re-signing locally ensures the entitlements (no sandbox, network access) are preserved and the local Gatekeeper trusts the binary:

```bash
# Find the entitlements file (clone the repo first if needed)
REPO_DIR=$(find ~/personal-workspace ~/workspace ~/dev ~/code -name "flutter_app" -type d 2>/dev/null | head -1 | sed 's|/flutter_app||')

if [ -f "$REPO_DIR/flutter_app/macos/Runner/Release.entitlements" ]; then
  codesign --force --deep --sign - \
    --entitlements "$REPO_DIR/flutter_app/macos/Runner/Release.entitlements" \
    /Applications/Heimdallr.app
  echo "✓ Signed with entitlements"
else
  # Sign without entitlements as fallback
  codesign --force --deep --sign - /Applications/Heimdallr.app
  echo "⚠ Signed without custom entitlements (no repo found)"
fi

codesign --verify /Applications/Heimdallr.app && echo "✓ Signature valid"
```

---

## Step 7: Launch and verify

```bash
open /Applications/Heimdallr.app
sleep 5

# Check it's running
INSTANCES=$(pgrep -c Heimdallr 2>/dev/null || echo 0)
echo "Running instances: $INSTANCES"
# Expected: exactly 1
# If > 10: fork bomb — kill immediately: pkill -9 -f Heimdallr

# Check daemon is up
curl -s http://localhost:7842/health
# Expected: {"status":"ok"}
```

---

## Troubleshooting

### Fork bomb (hundreds of instances spawned)
```bash
pkill -9 -f Heimdallr
```
This means the installed binary is corrupt (daemon and Flutter launcher were the same file). Go back to Step 3 and verify the bundle has **two distinct binaries** before installing.

### "Operation not permitted" on launch
The quarantine xattr is still present. Re-run Step 5. Make sure you ran it on `/Applications/Heimdallr.app` (not on the DMG volume).

### App opens and closes immediately (no window)
```bash
/Applications/Heimdallr.app/Contents/MacOS/Heimdallr 2>&1 &
sleep 3
pgrep Heimdallr
```
If the process exits in < 3 seconds, check macOS Console.app for crash logs. Most likely cause: missing entitlements (re-run Step 6).

### Daemon not responding after 10 seconds
```bash
# Try starting daemon manually
/Applications/Heimdallr.app/Contents/MacOS/heimdalld 2>&1 &
sleep 2
curl -s http://localhost:7842/health
```
If it exits immediately, there's likely no config at `~/.config/heimdallr/config.toml`. The Heimdallr app creates it on first launch — open the app and go through the Setup screen.

### "Daemon binary not found" error screen
The app shows this when `heimdalld` is not in `Heimdallr.app/Contents/MacOS/`. Verify:
```bash
ls /Applications/Heimdallr.app/Contents/MacOS/
# Must show both: Heimdallr AND heimdalld
```
If `heimdalld` is missing, the CI build was broken. Re-download from the releases page.

---

## Cleanup

```bash
# Unmount the DMG
hdiutil detach "/Volumes/Heimdallr v${VERSION#v}" 2>/dev/null || \
  hdiutil detach "/Volumes/Heimdallr ${VERSION}" 2>/dev/null

# Remove downloaded DMG
rm -f "/tmp/Heimdallr-${VERSION}.dmg"
```

---

## Updating

To update, repeat from Step 1. The app preserves config at `~/.config/heimdallr/config.toml` and the SQLite database at `~/.local/share/heimdallr/heimdallr.db` — no data is lost on update.
