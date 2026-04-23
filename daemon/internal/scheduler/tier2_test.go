package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

// mockPRPublisher records PublishPRReview calls.
type mockPRPublisher struct {
	mu    sync.Mutex
	calls []publishedPR
}

type publishedPR struct {
	Repo     string
	Number   int
	GithubID int64
	HeadSHA  string
}

func (m *mockPRPublisher) PublishPRReview(_ context.Context, repo string, number int, githubID int64, headSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, publishedPR{Repo: repo, Number: number, GithubID: githubID, HeadSHA: headSHA})
	return nil
}

func (m *mockPRPublisher) getCalls() []publishedPR {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]publishedPR, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// mockPRFetcher returns a fixed list of PRs.
type mockPRFetcher struct {
	prs []scheduler.Tier2PR
}

func (m *mockPRFetcher) FetchPRsToReview() ([]scheduler.Tier2PR, error) {
	return m.prs, nil
}

// mockStore controls which PRs are "already reviewed".
type mockStore struct {
	reviewed map[int64]bool
}

func (m *mockStore) PRAlreadyReviewed(githubID int64, _ time.Time) bool {
	return m.reviewed[githubID]
}

// noopPromoter satisfies Tier2Promoter for tests that don't exercise promotion.
type noopPromoter struct{}

func (n *noopPromoter) PromoteReady(_ context.Context, _ []string) (int, error) { return 0, nil }

func TestRunTier2_PublishesPRsToNATS(t *testing.T) {
	prPub := &mockPRPublisher{}
	fetcher := &mockPRFetcher{prs: []scheduler.Tier2PR{
		{ID: 1, Number: 10, Repo: "org/repo1", HeadSHA: "sha1", UpdatedAt: time.Now()},
		{ID: 2, Number: 20, Repo: "org/repo2", HeadSHA: "sha2", UpdatedAt: time.Now()},
		{ID: 3, Number: 30, Repo: "org/other", HeadSHA: "sha3", UpdatedAt: time.Now()},
	}}
	store := &mockStore{reviewed: map[int64]bool{2: true}}

	reposChan := make(chan []string, 1)
	reposChan <- []string{"org/repo1", "org/repo2"}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go scheduler.RunTier2(ctx, scheduler.Tier2Deps{
		Limiter:        scheduler.NewRateLimiter(100),
		WatchQueue:     scheduler.NewWatchQueue(),
		PRFetcher:      fetcher,
		PRProcessor:    &noopPRProcessor{},
		PRPublisher:    prPub,
		IssueProcessor: &noopIssueProcessor{},
		Promoter:       &noopPromoter{},
		Store:          store,
		ConfigFn:       func() []string { return nil },
		Interval:       10 * time.Second,
	}, reposChan, true)

	// Poll until the cold-start processTick publishes (RunTier2 waits 2s
	// for Tier 1's first batch before firing). Bounded by ctx timeout.
	deadline := time.After(5 * time.Second)
	for {
		calls := prPub.getCalls()
		if len(calls) > 0 {
			if len(calls) != 1 {
				t.Fatalf("expected 1 published PR, got %d: %+v", len(calls), calls)
			}
			if calls[0].GithubID != 1 || calls[0].HeadSHA != "sha1" {
				t.Errorf("unexpected PR published: %+v", calls[0])
			}
			return
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for PublishPRReview call")
		case <-time.After(100 * time.Millisecond):
		}
	}
}
