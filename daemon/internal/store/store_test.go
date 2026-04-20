package store_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPR_UpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	pr := &store.PR{
		GithubID:  101,
		Repo:      "org/repo",
		Number:    42,
		Title:     "Fix bug",
		Author:    "alice",
		URL:       "https://github.com/org/repo/pull/42",
		State:     "open",
		UpdatedAt: time.Now().UTC().Truncate(time.Second),
		FetchedAt: time.Now().UTC().Truncate(time.Second),
	}
	id, err := s.UpsertPR(pr)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}
	got, err := s.GetPR(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != pr.Title {
		t.Errorf("title mismatch: got %q want %q", got.Title, pr.Title)
	}
}

func TestReview_InsertAndList(t *testing.T) {
	s := newTestStore(t)
	pr := &store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()}
	prID, _ := s.UpsertPR(pr)

	rev := &store.Review{
		PRID:        prID,
		CLIUsed:     "claude",
		Summary:     "Looks good",
		Issues:      `[{"file":"main.go","line":10,"description":"nil deref","severity":"high"}]`,
		Suggestions: `["add nil check"]`,
		Severity:    "high",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	revID, err := s.InsertReview(rev)
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}
	if revID == 0 {
		t.Fatal("expected non-zero review id")
	}

	reviews, err := s.ListReviewsForPR(prID)
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(reviews))
	}
	if reviews[0].Summary != "Looks good" {
		t.Errorf("summary mismatch: %q", reviews[0].Summary)
	}
}

func TestPR_ListAll(t *testing.T) {
	s := newTestStore(t)
	for i := 0; i < 3; i++ {
		s.UpsertPR(&store.PR{GithubID: int64(i + 1), Repo: "org/r", Number: i + 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
	}
	prs, err := s.ListPRs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(prs) != 3 {
		t.Errorf("expected 3 prs, got %d", len(prs))
	}
}

func TestRetentionPurge(t *testing.T) {
	s := newTestStore(t)
	prID, _ := s.UpsertPR(&store.PR{GithubID: 1, Repo: "org/r", Number: 1, Title: "t", Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now()})
	old := &store.Review{
		PRID: prID, CLIUsed: "claude", Summary: "s", Issues: "[]", Suggestions: "[]", Severity: "low",
		CreatedAt: time.Now().Add(-100 * 24 * time.Hour),
	}
	s.InsertReview(old)
	err := s.PurgeOldReviews(90)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	reviews, _ := s.ListReviewsForPR(prID)
	if len(reviews) != 0 {
		t.Errorf("expected 0 reviews after purge, got %d", len(reviews))
	}
}

func TestConfigs_ListReturnsAllRows(t *testing.T) {
	s := newTestStore(t)

	if _, err := s.SetConfig("poll_interval", "30m"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if _, err := s.SetConfig("repositories", `["org/a","org/b"]`); err != nil {
		t.Fatalf("set: %v", err)
	}

	got, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d: %v", len(got), got)
	}
	if got["poll_interval"] != "30m" {
		t.Errorf("poll_interval = %q, want 30m", got["poll_interval"])
	}
	if got["repositories"] != `["org/a","org/b"]` {
		t.Errorf("repositories = %q", got["repositories"])
	}
}

func TestConfigs_ListOnEmptyTableReturnsEmptyMap(t *testing.T) {
	s := newTestStore(t)

	got, err := s.ListConfigs()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestStore_AgentImplementFieldsRoundTrip(t *testing.T) {
	s := newTestStore(t)

	in := &store.Agent{
		ID:                    "go-impl",
		Name:                  "Go implementer",
		CLI:                   "claude",
		ImplementPrompt:       "custom full template for implementation",
		ImplementInstructions: "use go 1.22 generics where helpful",
	}
	if err := s.UpsertAgent(in); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.ListAgents()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 agent, got %d", len(got))
	}
	if got[0].ImplementPrompt != in.ImplementPrompt {
		t.Errorf("ImplementPrompt = %q, want %q", got[0].ImplementPrompt, in.ImplementPrompt)
	}
	if got[0].ImplementInstructions != in.ImplementInstructions {
		t.Errorf("ImplementInstructions = %q, want %q", got[0].ImplementInstructions, in.ImplementInstructions)
	}
}
