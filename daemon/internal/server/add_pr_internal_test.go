package server

import "testing"

func TestParsePRURL(t *testing.T) {
	cases := []struct {
		name       string
		in         string
		wantRepo   string
		wantNumber int
		wantErr    bool
	}{
		{"canonical", "https://github.com/owner/repo/pull/123", "owner/repo", 123, false},
		{"trailing slash", "https://github.com/owner/repo/pull/7/", "owner/repo", 7, false},
		{"files tab", "https://github.com/owner/repo/pull/42/files", "owner/repo", 42, false},
		{"query string", "https://github.com/owner/repo/pull/9?w=1", "owner/repo", 9, false},
		{"whitespace", "  https://github.com/o/r/pull/5  ", "o/r", 5, false},
		{"www host", "https://www.github.com/o/r/pull/5", "o/r", 5, false},
		{"empty", "", "", 0, true},
		{"invalid escape", "https://github.com/%zz", "", 0, true},
		{"not github", "https://gitlab.com/o/r/pull/5", "", 0, true},
		{"issue not pr", "https://github.com/o/r/issues/5", "", 0, true},
		{"missing repo name", "https://github.com/o//pull/5", "", 0, true},
		{"missing number", "https://github.com/o/r/pull/", "", 0, true},
		{"non-numeric", "https://github.com/o/r/pull/abc", "", 0, true},
		{"zero number", "https://github.com/o/r/pull/0", "", 0, true},
		{"too short", "https://github.com/o/r", "", 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, number, err := parsePRURL(c.in)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got repo=%q number=%d", c.in, repo, number)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", c.in, err)
			}
			if repo != c.wantRepo || number != c.wantNumber {
				t.Errorf("parsePRURL(%q) = (%q, %d), want (%q, %d)", c.in, repo, number, c.wantRepo, c.wantNumber)
			}
		})
	}
}

func TestAddRepoToTOMLMap(t *testing.T) {
	// Fresh map with no github table: creates it and adds the repo.
	m := map[string]any{}
	addRepoToTOMLMap(m, "org/new")
	gh := m["github"].(map[string]any)
	if got := gh["repositories"].([]any); len(got) != 1 || got[0] != "org/new" {
		t.Fatalf("repositories = %v, want [org/new]", gh["repositories"])
	}

	// Existing repo is not duplicated; and it is stripped from non_monitored.
	m = map[string]any{"github": map[string]any{
		"repositories":  []any{"org/a", "org/b"},
		"non_monitored": []any{"org/b", "org/c"},
	}}
	addRepoToTOMLMap(m, "org/b")
	gh = m["github"].(map[string]any)
	repos := gh["repositories"].([]any)
	if len(repos) != 2 {
		t.Errorf("repositories must not duplicate an existing repo, got %v", repos)
	}
	for _, r := range gh["non_monitored"].([]any) {
		if r == "org/b" {
			t.Errorf("org/b must be removed from non_monitored, got %v", gh["non_monitored"])
		}
	}

	// New repo appended and removed from non_monitored simultaneously.
	m = map[string]any{"github": map[string]any{
		"repositories":  []any{"org/a"},
		"non_monitored": []any{"org/x"},
	}}
	addRepoToTOMLMap(m, "org/x")
	gh = m["github"].(map[string]any)
	repos = gh["repositories"].([]any)
	if len(repos) != 2 || repos[1] != "org/x" {
		t.Errorf("repositories = %v, want [org/a org/x]", repos)
	}
	if len(gh["non_monitored"].([]any)) != 0 {
		t.Errorf("non_monitored must be empty after moving org/x, got %v", gh["non_monitored"])
	}
}
