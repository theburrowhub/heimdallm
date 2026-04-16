package keychain

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
)

const service = "heimdallm"
const account = "github-token"

// Get retrieves the GitHub token from macOS Keychain.
// Falls back to GITHUB_TOKEN env var if not found in Keychain.
func Get() (string, error) {
	out, err := exec.Command(
		"security", "find-generic-password",
		"-s", service, "-a", account, "-w",
	).Output()
	if err == nil {
		token := strings.TrimSpace(string(out))
		if token != "" {
			return token, nil
		}
	}
	// Fallback to environment variable — less secure than Keychain because env
	// vars can be read by other processes of the same user. Warn so the user
	// knows to prefer Keychain storage.
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
		slog.Warn("keychain: using GITHUB_TOKEN env var as fallback — consider storing the token in macOS Keychain for better security")
		return t, nil
	}
	return "", fmt.Errorf("keychain: GitHub token not found in Keychain or GITHUB_TOKEN env var")
}

// Set stores the GitHub token in macOS Keychain.
func Set(token string) error {
	// Delete existing entry first (ignore error if not found)
	exec.Command("security", "delete-generic-password", "-s", service, "-a", account).Run()

	cmd := exec.Command(
		"security", "add-generic-password",
		"-s", service, "-a", account, "-w", token,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("keychain: store token: %w (%s)", err, out)
	}
	return nil
}
