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
