package store_test

import (
	"errors"
	"database/sql"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func newIssue(githubID int64, number int) *store.Issue {
	return &store.Issue{
		GithubID:  githubID,
		Repo:      "org/repo",
		Number:    number,
		Title:     "Bug in main",
		Body:      "reproduce by running foo",
		Author:    "alice",
		Assignees: `["bob"]`,
		Labels:    `["bug","priority:high"]`,
		State:     "open",
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		FetchedAt: time.Now().UTC().Truncate(time.Second),
	}
}

func TestIssue_UpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	i := newIssue(201, 15)

	id, err := s.UpsertIssue(i)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero id")
	}

	got, err := s.GetIssue(id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Title != i.Title {
		t.Errorf("title mismatch: got %q want %q", got.Title, i.Title)
	}
	if got.Labels != i.Labels {
		t.Errorf("labels JSON mismatch: got %q want %q", got.Labels, i.Labels)
	}
	if got.Dismissed {
		t.Error("new issue should not be dismissed")
	}
}

func TestIssue_UpsertDefaultsJSONArrays(t *testing.T) {
	s := newTestStore(t)
	i := newIssue(202, 16)
	i.Assignees = ""
	i.Labels = ""

	id, err := s.UpsertIssue(i)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ := s.GetIssue(id)
	if got.Assignees != "[]" || got.Labels != "[]" {
		t.Errorf("expected [] defaults, got assignees=%q labels=%q", got.Assignees, got.Labels)
	}
}

func TestIssue_UpsertIsIdempotentByGithubID(t *testing.T) {
	s := newTestStore(t)
	i := newIssue(203, 17)

	first, err := s.UpsertIssue(i)
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// Update the same GitHub issue with a new title.
	i.Title = "Bug in main (edited)"
	second, err := s.UpsertIssue(i)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if first != second {
		t.Fatalf("expected same row id on re-upsert, got %d vs %d", first, second)
	}
	got, _ := s.GetIssue(first)
	if got.Title != "Bug in main (edited)" {
		t.Errorf("title not updated on re-upsert: %q", got.Title)
	}
}

func TestIssue_UpsertPreservesDismissed(t *testing.T) {
	s := newTestStore(t)
	i := newIssue(204, 18)

	id, _ := s.UpsertIssue(i)
	if err := s.DismissIssue(id); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	// Re-upserting the same issue (as the poll loop would) must keep
	// dismissed = true. Regression-guarded to match UpsertPR semantics.
	if _, err := s.UpsertIssue(i); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	got, _ := s.GetIssue(id)
	if !got.Dismissed {
		t.Error("dismiss flag was lost on re-upsert")
	}
}

func TestIssue_GetByGithubID(t *testing.T) {
	s := newTestStore(t)
	i := newIssue(205, 19)
	_, _ = s.UpsertIssue(i)

	got, err := s.GetIssueByGithubID(205)
	if err != nil {
		t.Fatalf("get by github id: %v", err)
	}
	if got.Number != 19 {
		t.Errorf("number mismatch: %d", got.Number)
	}
}

func TestIssue_GetByGithubID_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetIssueByGithubID(999999)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows for missing issue, got: %v", err)
	}
}

func TestIssue_ListHidesDismissed(t *testing.T) {
	s := newTestStore(t)
	id1, _ := s.UpsertIssue(newIssue(206, 20))
	id2, _ := s.UpsertIssue(newIssue(207, 21))

	if err := s.DismissIssue(id2); err != nil {
		t.Fatalf("dismiss: %v", err)
	}

	list, err := s.ListIssues()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 issue (dismissed hidden), got %d", len(list))
	}
	if list[0].ID != id1 {
		t.Errorf("expected id1=%d in list, got %d", id1, list[0].ID)
	}
}

func TestIssue_Undismiss(t *testing.T) {
	s := newTestStore(t)
	id, _ := s.UpsertIssue(newIssue(208, 22))

	_ = s.DismissIssue(id)
	if err := s.UndismissIssue(id); err != nil {
		t.Fatalf("undismiss: %v", err)
	}
	got, _ := s.GetIssue(id)
	if got.Dismissed {
		t.Error("expected undismissed")
	}
}

func TestIssueReview_InsertAndList(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(newIssue(209, 23))

	rev := &store.IssueReview{
		IssueID:     issueID,
		CLIUsed:     "claude",
		Summary:     "looks like a config bug",
		Triage:      `{"severity":"medium","category":"config"}`,
		Suggestions: `["document the flag","add a validation step"]`,
		ActionTaken: "review_only",
		PRCreated:   0,
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
	}
	revID, err := s.InsertIssueReview(rev)
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}
	if revID == 0 {
		t.Fatal("expected non-zero review id")
	}

	reviews, err := s.ListIssueReviews(issueID)
	if err != nil {
		t.Fatalf("list reviews: %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("expected 1 review, got %d", len(reviews))
	}
	if reviews[0].Triage != rev.Triage {
		t.Errorf("triage mismatch: %q", reviews[0].Triage)
	}
}

func TestIssueReview_InsertDefaults(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(newIssue(210, 24))

	// All JSON-typed fields left blank on purpose — the store should normalise
	// them to valid-JSON defaults so json.Unmarshal downstream doesn't choke.
	rev := &store.IssueReview{
		IssueID:   issueID,
		CLIUsed:   "claude",
		Summary:   "no suggestions yet",
		CreatedAt: time.Now(),
	}
	_, err := s.InsertIssueReview(rev)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	got, _ := s.LatestIssueReview(issueID)
	if got.Triage != "{}" {
		t.Errorf("triage default mismatch: %q", got.Triage)
	}
	if got.Suggestions != "[]" {
		t.Errorf("suggestions default mismatch: %q", got.Suggestions)
	}
	if got.ActionTaken != "review_only" {
		t.Errorf("action_taken default mismatch: %q", got.ActionTaken)
	}
}

func TestIssueReview_LatestReturnsNewest(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(newIssue(211, 25))
	t0 := time.Now().UTC().Truncate(time.Second)

	older := &store.IssueReview{IssueID: issueID, CLIUsed: "claude", Summary: "first", Triage: "{}", CreatedAt: t0.Add(-time.Hour)}
	newer := &store.IssueReview{IssueID: issueID, CLIUsed: "claude", Summary: "second", Triage: "{}", CreatedAt: t0}

	_, _ = s.InsertIssueReview(older)
	_, _ = s.InsertIssueReview(newer)

	got, err := s.LatestIssueReview(issueID)
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if got.Summary != "second" {
		t.Errorf("expected newest review 'second', got %q", got.Summary)
	}
}

func TestIssueReview_LatestReturnsErrNoRowsWhenNone(t *testing.T) {
	s := newTestStore(t)
	issueID, _ := s.UpsertIssue(newIssue(212, 26))

	_, err := s.LatestIssueReview(issueID)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Errorf("expected sql.ErrNoRows, got: %v", err)
	}
}

func TestIssues_SchemaMigrationIdempotent(t *testing.T) {
	// Opening the same path twice re-runs the schema; CREATE IF NOT EXISTS
	// should make this a no-op. Regression guard for the migration story.
	path := t.TempDir() + "/heimdallm.db"
	s1, err := store.Open(path)
	if err != nil {
		t.Fatalf("open 1: %v", err)
	}
	s1.Close()
	s2, err := store.Open(path)
	if err != nil {
		t.Fatalf("open 2: %v", err)
	}
	defer s2.Close()

	// Write + read through issues should work on the second open.
	id, err := s2.UpsertIssue(newIssue(213, 27))
	if err != nil {
		t.Fatalf("upsert after re-open: %v", err)
	}
	if _, err := s2.GetIssue(id); err != nil {
		t.Fatalf("get after re-open: %v", err)
	}
}
