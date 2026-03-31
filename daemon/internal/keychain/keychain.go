package keychain

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const service = "heimdallr"
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
	// Fallback to environment variable
	if t := os.Getenv("GITHUB_TOKEN"); t != "" {
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
