package launchagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/template"
)

const plistName = "com.heimdallr.daemon.plist"

var plistTemplate = template.Must(template.New("plist").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.heimdallr.daemon</string>
	<key>ProgramArguments</key>
	<array>
		<string>{{.BinaryPath}}</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>{{.LogDir}}/heimdallr-daemon.log</string>
	<key>StandardErrorPath</key>
	<string>{{.LogDir}}/heimdallr-daemon-error.log</string>
</dict>
</plist>
`))

func plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", plistName), nil
}

// Install writes the plist and loads it with launchctl.
func Install(binaryPath string) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	logDir := filepath.Join(home, "Library", "Logs", "heimdallr")
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return fmt.Errorf("launchagent: mkdir logs: %w", err)
	}

	path, err := plistPath()
	if err != nil {
		return err
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("launchagent: create plist: %w", err)
	}
	defer f.Close()

	if err := plistTemplate.Execute(f, map[string]string{
		"BinaryPath": binaryPath,
		"LogDir":     logDir,
	}); err != nil {
		return fmt.Errorf("launchagent: render plist: %w", err)
	}

	uid := fmt.Sprintf("%d", os.Getuid())
	domain := "gui/" + uid
	if out, err := exec.Command("launchctl", "bootstrap", domain, path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchagent: launchctl bootstrap: %w (%s)", err, out)
	}
	fmt.Printf("LaunchAgent installed: %s\n", path)
	return nil
}

// Uninstall unloads and removes the plist.
func Uninstall() error {
	path, err := plistPath()
	if err != nil {
		return err
	}
	uid := fmt.Sprintf("%d", os.Getuid())
	domain := "gui/" + uid
	exec.Command("launchctl", "bootout", domain, path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("launchagent: remove plist: %w", err)
	}
	fmt.Printf("LaunchAgent removed: %s\n", path)
	return nil
}
