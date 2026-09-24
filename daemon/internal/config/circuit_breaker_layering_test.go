package config

import "testing"

func TestCircuitBreakerForRepo_GlobalOrgRepoPrecedence(t *testing.T) {
	repoCB := CircuitBreakerConfig{PerPR24h: 9, PerReviewFailureRepoHr: 70}
	orgCB := CircuitBreakerConfig{PerPR24h: 7, PerRepoHr: 50, PerReviewFailureRepoHr: 60}
	c := &Config{
		CircuitBreaker: CircuitBreakerConfig{
			PerPR24h: 3, PerRepoHr: 20, PerReviewFailureRepoHr: 20,
		},
		AI: AIConfig{
			Orgs:  map[string]OrgAI{"acme": {CircuitBreaker: &orgCB}},
			Repos: map[string]RepoAI{"acme/widget": {CircuitBreaker: &repoCB}},
		},
	}

	got := c.CircuitBreakerForRepo("acme/widget")
	if got.PerPR24h != 9 {
		t.Errorf("PerPR24h: want 9 (repo), got %d", got.PerPR24h)
	}
	if got.PerRepoHr != 50 {
		t.Errorf("PerRepoHr: want 50 (org), got %d", got.PerRepoHr)
	}
	if got.PerReviewFailureRepoHr != 70 {
		t.Errorf("PerReviewFailureRepoHr: want 70 (repo), got %d", got.PerReviewFailureRepoHr)
	}

	gotOrg := c.CircuitBreakerForRepo("acme/other")
	if gotOrg.PerPR24h != 7 {
		t.Errorf("org repo PerPR24h: want 7, got %d", gotOrg.PerPR24h)
	}
	if gotOrg.PerReviewFailureRepoHr != 60 {
		t.Errorf("org repo PerReviewFailureRepoHr: want 60, got %d", gotOrg.PerReviewFailureRepoHr)
	}

	gotGlobal := c.CircuitBreakerForRepo("none/none")
	if gotGlobal.PerPR24h != 3 {
		t.Errorf("global PerPR24h: want 3, got %d", gotGlobal.PerPR24h)
	}
	// Fields present only in global must survive the merge unchanged — the
	// repo/org overlays must not zero unrelated axes.
	if gotGlobal.PerRepoHr != 20 {
		t.Errorf("global PerRepoHr: want 20, got %d", gotGlobal.PerRepoHr)
	}
	if gotGlobal.PerReviewFailureRepoHr != 20 {
		t.Errorf("global PerReviewFailureRepoHr: want 20, got %d", gotGlobal.PerReviewFailureRepoHr)
	}
}
