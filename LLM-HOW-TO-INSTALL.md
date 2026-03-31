# LLM How-To: Install or Update Heimdallr

Step-by-step guide for Claude Code or any AI agent to install Heimdallr on macOS.

---

## 1. Get the latest version

```bash
VERSION=$(gh release view --repo theburrowhub/heimdallr --json tagName -q '.tagName')
DOWNLOAD_URL=$(gh release view --repo theburrowhub/heimdallr --json assets \
  -q '.assets[] | select(.name | endswith(".dmg")) | .browserDownloadUrl')

echo "Installing Heimdallr $VERSION"
```

---

## 2. Download and install

```bash
# Download
curl -L "$DOWNLOAD_URL" -o "/tmp/Heimdallr.dmg" --progress-bar

# Mount
hdiutil attach /tmp/Heimdallr.dmg -nobrowse -quiet
sleep 2

# Find the app inside the mounted volume
APP_SRC=$(find /Volumes -name "Heimdallr.app" 2>/dev/null | head -1)

# Verify the bundle has both binaries before installing
FLUTTER_BIN="$APP_SRC/Contents/MacOS/Heimdallr"
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
pkill -9 -f Heimdallr 2>/dev/null; sleep 1
rm -rf /Applications/Heimdallr.app
cp -R "$APP_SRC" /Applications/Heimdallr.app
echo "✓ Installed to /Applications"
```

---

## 3. Allow macOS to run it

macOS blocks apps not signed by Apple by default. This one-time command removes that restriction:

```bash
xattr -cr /Applications/Heimdallr.app
echo "✓ macOS restriction removed"
```

---

## 4. Launch

```bash
open /Applications/Heimdallr.app
sleep 5

# Confirm it started (should be exactly 1)
echo "Instances running: $(pgrep -c Heimdallr 2>/dev/null || echo 0)"

# Confirm the background service is up
curl -s http://localhost:7842/health
# Expected: {"status":"ok"}
```

On first launch Heimdallr detects your `gh` token automatically and sets itself up.

---

## 5. Cleanup

```bash
HEIMDALLR_DEV=$(mount | grep -i "Heimdallr" | awk '{print $1}' | head -1)
[ -n "$HEIMDALLR_DEV" ] && hdiutil detach "$HEIMDALLR_DEV" 2>/dev/null
rm -f /tmp/Heimdallr.dmg
```

---

## Troubleshooting

**Hundreds of instances spawn immediately**
```bash
pkill -9 -f Heimdallr
```
The bundle is corrupt — both binaries were the same file. Step 2 guards against this; if it happened anyway, re-download.

**"Operation not permitted" / app killed on launch**
Step 3 was skipped or ran on the DMG volume (read-only). Re-run it on `/Applications/Heimdallr.app`.

**App opens and closes immediately**
Run from Terminal to see errors:
```bash
/Applications/Heimdallr.app/Contents/MacOS/Heimdallr 2>&1 &
sleep 4 && pgrep Heimdallr
```

**Daemon not responding after 10 seconds**
```bash
/Applications/Heimdallr.app/Contents/MacOS/heimdalld &
sleep 2 && curl -s http://localhost:7842/health
```
If it exits, there's no config yet — the app creates it on first launch.

---

## Updating

Repeat from step 1. Config (`~/.config/heimdallr/`) and history (`~/.local/share/heimdallr/`) are preserved.
