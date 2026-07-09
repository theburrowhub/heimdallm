package config

import "testing"

func TestValidateRepoSlug(t *testing.T) {
	cases := []struct {
		name    string
		repo    string
		wantErr bool
	}{
		{name: "valid owner/name", repo: "freepik-company/ai-api-specs", wantErr: false},
		{name: "valid with dots and underscores", repo: "octo-org/my.repo_name-1", wantErr: false},
		{name: "single char segments", repo: "a/b", wantErr: false},

		{name: "empty", repo: "", wantErr: true},
		{name: "missing slash", repo: "ownerrepo", wantErr: true},
		{name: "empty owner", repo: "/name", wantErr: true},
		{name: "empty name", repo: "owner/", wantErr: true},
		{name: "too many slashes", repo: "owner/group/name", wantErr: true},
		{name: "owner with slash injection", repo: "evil-org archived:false/name", wantErr: true},
		{name: "owner with special chars", repo: "ev!l/name", wantErr: true},
		{name: "owner starting with hyphen", repo: "-owner/name", wantErr: true},
		{name: "name with slash escape", repo: "owner/na/me", wantErr: true},
		{name: "name with special chars", repo: "owner/na me", wantErr: true},
		{name: "name dot traversal", repo: "owner/.", wantErr: true},
		{name: "name dotdot traversal", repo: "owner/..", wantErr: true},
		{name: "name with path traversal segment", repo: "owner/../../etc", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRepoSlug(tc.repo)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateRepoSlug(%q) = nil, want error", tc.repo)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateRepoSlug(%q) = %v, want nil", tc.repo, err)
			}
		})
	}
}
