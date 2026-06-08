package store_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func newActivityStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestInsertActivity_RoundTrip(t *testing.T) {
	s := newActivityStore(t)
	ts := time.Now().UTC().Truncate(time.Second)

	id, err := s.InsertActivity(ts, "acme", "acme/api", "pr", 42, "Fix bug",
		"review", "major", map[string]any{"cli_used": "claude"})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	entries, truncated, err := s.ListActivity(store.ActivityQuery{From: ts.Add(-time.Hour), To: ts.Add(time.Hour), Limit: 100})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if truncated {
		t.Error("unexpected truncation")
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Repo != "acme/api" || e.Action != "review" || e.Outcome != "major" {
		t.Errorf("unexpected entry: %+v", e)
	}
	var details map[string]any
	if err := json.Unmarshal([]byte(e.DetailsJSON), &details); err != nil {
		t.Fatalf("details unmarshal: %v", err)
	}
	if details["cli_used"] != "claude" {
		t.Errorf("details cli_used = %v", details["cli_used"])
	}
}

func TestListActivity_FilterByOrgAndAction(t *testing.T) {
	s := newActivityStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	must := func(ts time.Time, org, repo, action string) {
		t.Helper()
		if _, err := s.InsertActivity(ts, org, repo, "pr", 1, "t", action, "", nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	must(base.Add(-3*time.Minute), "acme", "acme/api", "review")
	must(base.Add(-2*time.Minute), "acme", "acme/api", "triage")
	must(base.Add(-1*time.Minute), "globex", "globex/web", "review")

	entries, _, err := s.ListActivity(store.ActivityQuery{
		Orgs:    []string{"acme"},
		Actions: []string{"review"},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].Repo != "acme/api" || entries[0].Action != "review" {
		t.Errorf("unexpected entry: %+v", entries[0])
	}
}

func TestListActivity_FilterByItemTypeAndOutcome(t *testing.T) {
	s := newActivityStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	must := func(itemType, action, outcome string, number int) {
		t.Helper()
		if _, err := s.InsertActivity(base, "acme", "acme/api", itemType, number, "t", action, outcome, nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	must("pr", "review_skipped", "draft", 1)
	must("pr", "review_skipped", "not_open", 2)
	must("issue", "triage", "draft", 3)

	entries, _, err := s.ListActivity(store.ActivityQuery{
		ItemTypes: []string{"pr"},
		Actions:   []string{"review_skipped"},
		Outcomes:  []string{"draft"},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d", len(entries))
	}
	if entries[0].ItemNumber != 1 {
		t.Errorf("item_number = %d, want 1", entries[0].ItemNumber)
	}
}

func TestListActivity_Truncation(t *testing.T) {
	s := newActivityStore(t)
	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		if _, err := s.InsertActivity(base.Add(time.Duration(i)*time.Second),
			"acme", "acme/api", "pr", i, "t", "review", "", nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	entries, truncated, err := s.ListActivity(store.ActivityQuery{Limit: 3})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !truncated {
		t.Error("expected truncated=true")
	}
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	if entries[0].ItemNumber != 4 {
		t.Errorf("want newest item_number=4, got %d", entries[0].ItemNumber)
	}
}

func TestPurgeOldActivity_CutoffBoundary(t *testing.T) {
	s := newActivityStore(t)
	now := time.Now().UTC()
	old := now.Add(-100 * 24 * time.Hour)
	recent := now.Add(-1 * 24 * time.Hour)

	if _, err := s.InsertActivity(old, "a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertActivity(recent, "a", "a/b", "pr", 2, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}

	if err := s.PurgeOldActivity(90); err != nil {
		t.Fatalf("purge: %v", err)
	}
	entries, _, err := s.ListActivity(store.ActivityQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 remaining, got %d", len(entries))
	}
	if entries[0].ItemNumber != 2 {
		t.Errorf("wrong entry survived: %+v", entries[0])
	}
}

func TestPurgeOldActivity_ZeroIsNoOp(t *testing.T) {
	s := newActivityStore(t)
	if _, err := s.InsertActivity(time.Now().UTC().Add(-365*24*time.Hour),
		"a", "a/b", "pr", 1, "t", "review", "", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeOldActivity(0); err != nil {
		t.Fatalf("purge: %v", err)
	}
	entries, _, err := s.ListActivity(store.ActivityQuery{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("want 1 remaining (no-op), got %d", len(entries))
	}
}

func TestListActivity_FilterByRepo(t *testing.T) {
	s := newActivityStore(t)
	base := time.Now().UTC().Truncate(time.Second)

	must := func(repo string, n int) {
		t.Helper()
		if _, err := s.InsertActivity(base, "acme", repo, "pr", n, "t", "review", "", nil); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	must("acme/api", 1)
	must("acme/web", 2)
	must("acme/api", 3)

	entries, _, err := s.ListActivity(store.ActivityQuery{Repos: []string{"acme/api"}})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	for _, e := range entries {
		if e.Repo != "acme/api" {
			t.Errorf("unexpected repo in result: %q", e.Repo)
		}
	}
}
