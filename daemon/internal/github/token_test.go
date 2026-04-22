package github_test

import (
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestTokenRouter_ForRepo(t *testing.T) {
	tr := gh.NewTokenRouter("default-tok", map[string]string{
		"freepik-company": "fgpat-freepik",
		"AcmeCorp":        "fgpat-acme",
	})

	tests := []struct {
		repo string
		want string
	}{
		{"freepik-company/ai-platform", "fgpat-freepik"},
		{"Freepik-Company/ai-platform", "fgpat-freepik"}, // case-insensitive
		{"theburrowhub/heimdallm", "default-tok"},
		{"AcmeCorp/tool", "fgpat-acme"},
		{"acmecorp/tool", "fgpat-acme"}, // case-insensitive
		{"unknown/repo", "default-tok"},
		{"noslash", "default-tok"},
		{"", "default-tok"},
	}
	for _, tt := range tests {
		got := tr.ForRepo(tt.repo)
		if got != tt.want {
			t.Errorf("ForRepo(%q) = %q, want %q", tt.repo, got, tt.want)
		}
	}
}

func TestTokenRouter_ForOrg(t *testing.T) {
	tr := gh.NewTokenRouter("default-tok", map[string]string{
		"freepik-company": "fgpat-freepik",
	})

	if got := tr.ForOrg("freepik-company"); got != "fgpat-freepik" {
		t.Errorf("ForOrg exact = %q", got)
	}
	if got := tr.ForOrg("FREEPIK-COMPANY"); got != "fgpat-freepik" {
		t.Errorf("ForOrg upper = %q", got)
	}
	if got := tr.ForOrg("other"); got != "default-tok" {
		t.Errorf("ForOrg fallback = %q", got)
	}
}

func TestTokenRouter_Default(t *testing.T) {
	tr := gh.NewTokenRouter("my-default", nil)
	if got := tr.Default(); got != "my-default" {
		t.Errorf("Default() = %q", got)
	}
}

func TestTokenRouter_EmptyOrgTokens(t *testing.T) {
	tr := gh.NewTokenRouter("default-tok", nil)
	if got := tr.ForRepo("any/repo"); got != "default-tok" {
		t.Errorf("ForRepo with nil orgTokens = %q", got)
	}
	if got := tr.ForOrg("any"); got != "default-tok" {
		t.Errorf("ForOrg with nil orgTokens = %q", got)
	}
}
