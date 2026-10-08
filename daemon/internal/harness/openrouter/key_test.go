package openrouter

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyStore(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{}
	k := NewKeyStore(filepath.Join(dir, "data"))
	k.Getenv = func(name string) string { return env[name] }

	if key, src := k.Get(); key != "" || src != KeySourceNone {
		t.Fatalf("empty store = %q/%q", key, src)
	}
	env[EnvKey] = "  sk-or-env  "
	if key, src := k.Get(); key != "sk-or-env" || src != KeySourceEnv {
		t.Fatalf("env key = %q/%q", key, src)
	}
	if err := k.Set(" sk-or-stored "); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if key, src := k.Get(); key != "sk-or-stored" || src != KeySourceStored {
		t.Fatalf("stored key must win: %q/%q", key, src)
	}
	info, err := os.Stat(k.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key file mode = %v (%v), want 0600", info.Mode().Perm(), err)
	}
	if err := k.Clear(); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if err := k.Clear(); err != nil {
		t.Fatalf("Clear twice: %v", err)
	}
	if key, src := k.Get(); key != "sk-or-env" || src != KeySourceEnv {
		t.Fatalf("after clear the env key applies again: %q/%q", key, src)
	}
	var nilStore *KeyStore
	if key, _ := nilStore.Get(); key != "" {
		t.Error("nil store must have no key")
	}
}

func TestValidateKey(t *testing.T) {
	for _, bad := range []string{"", "   ", "has space", "line\nbreak", "tab\tx", "ünicode", strings.Repeat("a", maxKeyLen+1)} {
		if ValidateKey(bad) == nil {
			t.Errorf("ValidateKey(%q) accepted", bad)
		}
		if (&KeyStore{Path: filepath.Join(t.TempDir(), "k")}).Set(bad) == nil {
			t.Errorf("Set(%q) accepted", bad)
		}
	}
	if err := ValidateKey("sk-or-v1-0123abcd"); err != nil {
		t.Errorf("valid key rejected: %v", err)
	}
}

func TestKeyStoreSetFailsOnUnwritableDir(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	k := &KeyStore{Path: filepath.Join(file, "sub", "key")}
	if err := k.Set("sk-or-x"); err == nil {
		t.Fatal("Set under a file must fail")
	}
}
