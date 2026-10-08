package store

import (
	"testing"
	"time"
)

func TestReviewTimesSince_FiltersByScope(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	mkPR := func(id int64, repo string, number int) int64 {
		prID, err := s.UpsertPR(&PR{GithubID: id, Repo: repo, Number: number, Title: "t", State: "open", UpdatedAt: now})
		if err != nil {
			t.Fatalf("upsert PR: %v", err)
		}
		return prID
	}
	api := mkPR(1, "acme/api", 1)
	web := mkPR(2, "acme/web", 2)
	// "acme_x" would match a naive LIKE 'acme_%' without escaping.
	other := mkPR(3, "acmex/api", 3)

	add := func(prID int64, cli string, at time.Time) {
		if _, err := s.InsertReview(&Review{PRID: prID, CLIUsed: cli, Issues: "[]", Suggestions: "[]", CreatedAt: at}); err != nil {
			t.Fatalf("insert review: %v", err)
		}
	}
	add(api, "claude", now.Add(-30*time.Second))
	add(api, "codex", now.Add(-2*time.Hour))
	add(web, "claude", now.Add(-10*time.Minute))
	add(web, "peer", now.Add(-time.Minute)) // a peer's review, not ours
	add(other, "claude", now.Add(-time.Minute))
	add(api, "claude", now.Add(-48*time.Hour)) // outside the window

	since := now.Add(-24 * time.Hour)
	cases := []struct {
		name   string
		filter ReviewCountFilter
		want   int
	}{
		{"global", ReviewCountFilter{}, 4},
		{"org", ReviewCountFilter{Org: "acme"}, 3},
		{"repo", ReviewCountFilter{Repo: "acme/api"}, 2},
		{"agent", ReviewCountFilter{CLI: "claude"}, 3},
		{"repo+agent", ReviewCountFilter{Repo: "acme/api", CLI: "codex"}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.ReviewTimesSince(tc.filter, since)
			if err != nil {
				t.Fatalf("ReviewTimesSince: %v", err)
			}
			if len(got) != tc.want {
				t.Fatalf("got %d times (%v), want %d", len(got), got, tc.want)
			}
			for i := 1; i < len(got); i++ {
				if got[i].Before(got[i-1]) {
					t.Fatalf("times not ascending: %v", got)
				}
			}
		})
	}
}

func TestReviewTokenColumnsAndStats(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	now := time.Now().UTC()
	prID, err := s.UpsertPR(&PR{GithubID: 1, Repo: "acme/api", Number: 1, Title: "t", State: "open", UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.InsertReview(&Review{
		PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]", CreatedAt: now,
		InputTokens: 1200, OutputTokens: 80, CacheReadTokens: 400, CostUSD: 0.05, PromptBytes: 4800,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertReview(&Review{
		PRID: prID, CLIUsed: "codex", Issues: "[]", Suggestions: "[]", CreatedAt: now,
		InputTokens: 300, OutputTokens: 20, TokensEstimated: true, PromptBytes: 1200,
	}); err != nil {
		t.Fatal(err)
	}
	// Legacy row (no prompt_bytes) and a peer placeholder are left out.
	if _, err := s.InsertReview(&Review{PRID: prID, CLIUsed: "claude", Issues: "[]", Suggestions: "[]", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertReview(&Review{PRID: prID, CLIUsed: "peer", Issues: "[]", Suggestions: "[]", CreatedAt: now, PromptBytes: 99}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetReview(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 1200 || got.OutputTokens != 80 || got.CacheReadTokens != 400 ||
		got.CostUSD != 0.05 || got.TokensEstimated || got.PromptBytes != 4800 {
		t.Errorf("round trip = %+v", got)
	}

	stats, err := s.ComputeStats(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	tk := stats.TokensLast7Days
	if tk.Reviews != 2 || tk.EstimatedReviews != 1 || tk.InputTokens != 1500 || tk.OutputTokens != 100 ||
		tk.CacheReadTokens != 400 || tk.CostUSD != 0.05 || tk.AvgPromptBytes != 3000 {
		t.Errorf("token stats = %+v", tk)
	}
	scoped, err := s.ComputeStats([]string{"other/repo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if scoped.TokensLast7Days.Reviews != 0 {
		t.Errorf("repo filter must apply to token stats: %+v", scoped.TokensLast7Days)
	}
}
