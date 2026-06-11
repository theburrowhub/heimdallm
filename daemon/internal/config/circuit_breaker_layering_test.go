package config

import "testing"

func TestCircuitBreakerForRepo_GlobalOrgRepoPrecedence(t *testing.T) {
	repoCB := CircuitBreakerConfig{PerImplRepoHr: 9}
	orgCB := CircuitBreakerConfig{PerImplRepoHr: 7, PerRepoHr: 50}
	c := &Config{
		CircuitBreaker: CircuitBreakerConfig{
			PerPR24h: 3, PerRepoHr: 20, PerIssue24h: 3, PerIssueRepoHr: 10, PerImplRepoHr: 5,
		},
		AI: AIConfig{
			Orgs:  map[string]OrgAI{"acme": {CircuitBreaker: &orgCB}},
			Repos: map[string]RepoAI{"acme/widget": {CircuitBreaker: &repoCB}},
		},
	}

	got := c.CircuitBreakerForRepo("acme/widget")
	if got.PerImplRepoHr != 9 {
		t.Errorf("PerImplRepoHr: want 9 (repo), got %d", got.PerImplRepoHr)
	}
	if got.PerRepoHr != 50 {
		t.Errorf("PerRepoHr: want 50 (org), got %d", got.PerRepoHr)
	}
	if got.PerPR24h != 3 {
		t.Errorf("PerPR24h: want 3 (global), got %d", got.PerPR24h)
	}
	// Fields present only in global must survive the merge unchanged — the
	// repo/org overlays must not zero unrelated axes.
	if got.PerIssue24h != 3 {
		t.Errorf("PerIssue24h: want 3 (global, untouched by overlays), got %d", got.PerIssue24h)
	}
	if got.PerIssueRepoHr != 10 {
		t.Errorf("PerIssueRepoHr: want 10 (global, untouched by overlays), got %d", got.PerIssueRepoHr)
	}

	gotOrg := c.CircuitBreakerForRepo("acme/other")
	if gotOrg.PerImplRepoHr != 7 {
		t.Errorf("org repo PerImplRepoHr: want 7, got %d", gotOrg.PerImplRepoHr)
	}

	gotGlobal := c.CircuitBreakerForRepo("none/none")
	if gotGlobal.PerImplRepoHr != 5 {
		t.Errorf("global PerImplRepoHr: want 5, got %d", gotGlobal.PerImplRepoHr)
	}
}
