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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go scheduler.RunTier2(ctx, scheduler.Tier2Deps{
		Limiter:        scheduler.NewRateLimiter(100),
		WatchQueue:     scheduler.NewWatchQueue(),
		PRFetcher:      fetcher,
		PRProcessor:    &noopPRProcessor{},
		PRPublisher:    prPub,
		IssueProcessor: &noopIssueProcessor{},
		Store:          store,
		ConfigFn:       func() []string { return nil },
		Interval:       10 * time.Second, // long interval so only the cold-start tick fires
	}, reposChan, true)

	// RunTier2 waits 2s for Tier 1's first batch before firing processTick,
	// so we need to wait >2s for the cold-start tick to execute.
	time.Sleep(3 * time.Second)
	cancel()

	calls := prPub.getCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 published PR, got %d: %+v", len(calls), calls)
	}
	if calls[0].GithubID != 1 || calls[0].HeadSHA != "sha1" {
		t.Errorf("unexpected PR published: %+v", calls[0])
	}
}
