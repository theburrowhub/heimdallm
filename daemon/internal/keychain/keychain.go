package keychain

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const service = "heimdallm"
const account = "github-token"

// Get retrieves the GitHub token.
// Priority: GITHUB_TOKEN env var > macOS Keychain > token files.
func Get() (string, error) {
	// 1. Environment variable (Docker / CI).
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}

	// 2. macOS Keychain (desktop).
	if runtime.GOOS == "darwin" {
		if tok, err := getFromKeychain(); err == nil && tok != "" {
			return tok, nil
		}
	}

	// 3. Token files (Docker mount or manual).
	for _, p := range tokenFilePaths() {
		if data, err := os.ReadFile(p); err == nil {
			if tok := strings.TrimSpace(string(data)); tok != "" {
				return tok, nil
			}
		}
	}

	return "", fmt.Errorf("no GitHub token found: set GITHUB_TOKEN, use macOS Keychain, or mount a token file at /config/.token")
}

func getFromKeychain() (string, error) {
	out, err := exec.Command(
		"security", "find-generic-password",
		"-s", service, "-a", account, "-w",
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func tokenFilePaths() []string {
	paths := []string{"/config/.token"}
	home, err := os.UserHomeDir()
	if err == nil {
		paths = append(paths, filepath.Join(home, ".config", "heimdallm", ".token"))
	}
	return paths
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
