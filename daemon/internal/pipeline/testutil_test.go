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

// PRAlreadyReviewed mirrors tier2Adapter.PRAlreadyReviewed in
// cmd/heimdallm/main.go. Keep the two in sync — the external reloop tests
// exercise this copy, but production traffic flows through the main.go
// version. See theburrowhub/heimdallm#243.
func (a *testAdapter) PRAlreadyReviewed(githubID int64, repo string, number int, updatedAt time.Time) bool {
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
	rev, err := a.store.LatestReviewForPR(existing.ID)
	if err != nil || rev == nil {
		return false
	}
	anchor := rev.PublishedAt
	if anchor.IsZero() {
		anchor = rev.CreatedAt
	}
	return ReviewFreshEnough(anchor, updatedAt, GraceDefault)
}
