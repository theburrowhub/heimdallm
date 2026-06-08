package pipeline

import (
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// testAdapter is a stand-in for daemon/cmd/heimdallm.tier2Adapter used by
// the reloop tests. It mirrors the real PRAlreadyReviewed logic so we can
// regression-test the dedup anchor semantics without standing up the full
// scheduler + cfg + broker plumbing.
type testAdapter struct {
	store *store.Store
}

// NewTestAdapter is exported only for the regression tests in
// pipeline_reloop_test.go. Because this file is named *_test.go it is
// compiled only in test builds — production binaries never see this
// symbol. Do NOT use in production code; the real adapter lives in
// cmd/heimdallm/main.go with the full scheduler plumbing.
func NewTestAdapter(s *store.Store) *testAdapter {
	return &testAdapter{store: s}
}

// PRAlreadyReviewed mirrors the persisted freshness part of
// tier2Adapter.PRAlreadyReviewed in cmd/heimdallm/main.go. The former
// 2-minute PublishedAt grace window has been removed — the tier-2
// FetchPRsToReview loop now confirms the bot is still in
// requested_reviewers via the Pulls API before a PR reaches this point.
// PRAlreadyReviewed now only checks dismissed state and circuit breaker
// (circuit breaker omitted here since it requires daemon config plumbing).
func (a *testAdapter) PRAlreadyReviewed(githubID int64, repo string, number int, updatedAt time.Time, _ string) bool {
	existing, _ := a.store.GetPRByGithubID(githubID)
	if existing == nil && repo != "" && number > 0 {
		existing, _ = a.store.GetPRByRepoNumber(repo, number)
	}
	if existing == nil {
		return false
	}
	if existing.Dismissed {
		return true
	}
	return false
}
