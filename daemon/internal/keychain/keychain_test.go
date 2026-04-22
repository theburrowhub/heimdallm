package keychain

import (
	"os"
	"strings"
	"testing"
)

func TestGetAll_DefaultOnly(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_default")
	for _, e := range os.Environ() {
		key, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(key, "GITHUB_TOKEN_") {
			t.Setenv(key, "")
		}
	}

	def, orgs, err := GetAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def != "ghp_default" {
		t.Errorf("default = %q, want ghp_default", def)
	}
	if len(orgs) != 0 {
		t.Errorf("orgTokens = %v, want empty", orgs)
	}
}

func TestGetAll_WithOrgTokens(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_default")
	t.Setenv("GITHUB_TOKEN_FREEPIK_COMPANY", "github_pat_freepik")
	t.Setenv("GITHUB_TOKEN_ACME_CORP", "github_pat_acme")

	def, orgs, err := GetAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if def != "ghp_default" {
		t.Errorf("default = %q, want ghp_default", def)
	}
	if got := orgs["freepik-company"]; got != "github_pat_freepik" {
		t.Errorf("freepik-company = %q, want github_pat_freepik", got)
	}
	if got := orgs["acme-corp"]; got != "github_pat_acme" {
		t.Errorf("acme-corp = %q, want github_pat_acme", got)
	}
}

func TestGetAll_IgnoresEmptyOrgToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "ghp_default")
	t.Setenv("GITHUB_TOKEN_EMPTYORG", "")

	_, orgs, err := GetAll()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := orgs["emptyorg"]; ok {
		t.Error("empty token should be skipped")
	}
}

func TestGetAll_NoDefaultToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	_, _, err := GetAll()
	if err == nil {
		t.Error("expected error when no default token")
	}
}
