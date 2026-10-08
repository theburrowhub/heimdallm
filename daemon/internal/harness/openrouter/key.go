// Package openrouter is Heimdallm's own review agent for OpenRouter: an
// HTTP client for the OpenRouter API and a small agentic harness — a
// review-specialist system prompt plus read-only tools confined to the PR
// checkout — that the executor runs in place of a CLI subprocess.
package openrouter

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// KeySource tells where the active API key came from.
const (
	KeySourceNone   = ""
	KeySourceEnv    = "env"
	KeySourceStored = "stored"
)

// EnvKey is the environment variable read when no key was stored.
const EnvKey = "OPENROUTER_API_KEY"

// maxKeyLen bounds a stored key; OpenRouter keys are ~73 characters.
const maxKeyLen = 256

// KeyStore keeps the OpenRouter API key in a 0600 file in the daemon's data
// directory (stored through the API, so it survives restarts and works the
// same on the desktop and in Docker), falling back to OPENROUTER_API_KEY.
// The key never leaves the daemon: the API only reports whether one is set.
type KeyStore struct {
	Path   string
	Getenv func(string) string

	mu sync.Mutex
}

// NewKeyStore stores the key at <dataDir>/openrouter_api_key.
func NewKeyStore(dataDir string) *KeyStore {
	return &KeyStore{Path: filepath.Join(dataDir, "openrouter_api_key"), Getenv: os.Getenv}
}

// Get returns the active key and its source. A stored key wins over the
// environment so the operator can rotate it from the app.
func (k *KeyStore) Get() (key, source string) {
	if k == nil {
		return "", KeySourceNone
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.Path != "" {
		if data, err := os.ReadFile(k.Path); err == nil {
			if key := strings.TrimSpace(string(data)); key != "" {
				return key, KeySourceStored
			}
		}
	}
	if k.Getenv != nil {
		if key := strings.TrimSpace(k.Getenv(EnvKey)); key != "" {
			return key, KeySourceEnv
		}
	}
	return "", KeySourceNone
}

// ValidateKey rejects values that cannot be an API key, so a pasted line of
// text or a header injection never reaches the Authorization header.
func ValidateKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("openrouter: API key is empty")
	}
	if len(key) > maxKeyLen {
		return fmt.Errorf("openrouter: API key longer than %d characters", maxKeyLen)
	}
	for _, r := range key {
		if r <= ' ' || r > '~' {
			return errors.New("openrouter: API key must be printable ASCII without spaces")
		}
	}
	return nil
}

// Set stores key, replacing any previous one, atomically and owner-only.
func (k *KeyStore) Set(key string) error {
	key = strings.TrimSpace(key)
	if err := ValidateKey(key); err != nil {
		return err
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(k.Path), 0o700); err != nil {
		return fmt.Errorf("openrouter: create key dir: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(k.Path), ".openrouter_api_key-*")
	if err != nil {
		return fmt.Errorf("openrouter: store key: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("openrouter: store key: %w", err)
	}
	if _, err := tmp.WriteString(key); err != nil {
		tmp.Close()
		return fmt.Errorf("openrouter: store key: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("openrouter: store key: %w", err)
	}
	if err := os.Rename(tmp.Name(), k.Path); err != nil {
		return fmt.Errorf("openrouter: store key: %w", err)
	}
	return nil
}

// Clear removes the stored key (the environment variable, if any, applies
// again).
func (k *KeyStore) Clear() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := os.Remove(k.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("openrouter: clear key: %w", err)
	}
	return nil
}
