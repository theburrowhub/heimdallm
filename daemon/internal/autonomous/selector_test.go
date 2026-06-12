package autonomous

import (
	"context"
	"testing"
)

// fakeStore implements SelectorStore
type fakeStore struct {
	openPRs map[int64]bool // keyed by GithubID
	claimed map[int64]bool // keyed by StoreID
}

func newFakeStore() *fakeStore {
	return &fakeStore{openPRs: make(map[int64]bool), claimed: make(map[int64]bool)}
}

func (f *fakeStore) HasOpenAutoImplementPR(issueGithubID int64) (bool, error) {
	return f.openPRs[issueGithubID], nil
}

func (f *fakeStore) SetIssueClaimedByAutonomous(issueID int64, claimed bool) error {
	f.claimed[issueID] = claimed
	return nil
}

func (f *fakeStore) IsIssueClaimedByAutonomous(issueID int64) (bool, error) {
	return f.claimed[issueID], nil
}

// fakeGH implements SelectorGH
type fakeGH struct {
	branches map[string]bool // key: "repo/branch"
}

func newFakeGH() *fakeGH {
	return &fakeGH{branches: make(map[string]bool)}
}

func (f *fakeGH) BranchExists(repo, branch string) (bool, error) {
	return f.branches[repo+"/"+branch], nil
}

func (f *fakeGH) AddAssignees(repo string, number int, assignees []string) error {
	return nil
}

func (f *fakeGH) PostComment(repo string, number int, body string) error {
	return nil
}

// recordingGH embeds fakeGH but records calls
type recordingGH struct {
	fakeGH
	addAssigneesCalls []addAssigneesCall
	postCommentCalls  []postCommentCall
}

type addAssigneesCall struct {
	repo      string
	number    int
	assignees []string
}

type postCommentCall struct {
	repo   string
	number int
	body   string
}

func newRecordingGH() *recordingGH {
	return &recordingGH{fakeGH: fakeGH{branches: make(map[string]bool)}}
}

func (r *recordingGH) AddAssignees(repo string, number int, assignees []string) error {
	r.addAssigneesCalls = append(r.addAssigneesCalls, addAssigneesCall{repo, number, assignees})
	return nil
}

func (r *recordingGH) PostComment(repo string, number int, body string) error {
	r.postCommentCalls = append(r.postCommentCalls, postCommentCall{repo, number, body})
	return nil
}

// fakeCommentGen implements CommentGenerator
type fakeCommentGen struct {
	comment string
}

func (f *fakeCommentGen) GenerateCoordinationComment(ctx context.Context, c Candidate) (string, error) {
	return f.comment, nil
}

// TestIsEligible tests the isEligible method indirectly via Pick.
// isEligible is unexported; we test via Pick with a single bot-assigned candidate.
func TestIsEligible(t *testing.T) {
	ctx := context.Background()
	const botLogin = "heimdallm-bot"
	const branchPrefix = "heimdallm/issue-"

	t.Run("open PR in store makes candidate ineligible", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()
		store.openPRs[101] = true // GithubID 101 has an open PR

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, nil)

		cands := []Candidate{
			{Repo: "org/repo", Number: 10, GithubID: 101, StoreID: 1, Assignees: []string{botLogin}},
		}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked != nil {
			t.Errorf("expected nil pick when open PR exists, got %+v", picked)
		}
		if bucket != BucketNone {
			t.Errorf("expected BucketNone, got %v", bucket)
		}
	})

	t.Run("existing branch makes candidate ineligible", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()
		// branch heimdallm/issue-20 exists in org/repo
		gh.branches["org/repo/heimdallm/issue-20"] = true

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, nil)

		cands := []Candidate{
			{Repo: "org/repo", Number: 20, GithubID: 202, StoreID: 2, Assignees: []string{botLogin}},
		}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked != nil {
			t.Errorf("expected nil pick when branch exists, got %+v", picked)
		}
		if bucket != BucketNone {
			t.Errorf("expected BucketNone, got %v", bucket)
		}
	})

	t.Run("claimed_by_autonomous makes candidate ineligible", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()
		store.claimed[5] = true // StoreID 5 already claimed by a prior Drive

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, nil)

		cands := []Candidate{
			{Repo: "org/repo", Number: 25, GithubID: 250, StoreID: 5, Assignees: []string{botLogin}},
		}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked != nil {
			t.Errorf("expected nil pick when issue is already claimed, got %+v", picked)
		}
		if bucket != BucketNone {
			t.Errorf("expected BucketNone, got %v", bucket)
		}
	})

	t.Run("no open PR and no branch makes candidate eligible", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, nil)

		cands := []Candidate{
			{Repo: "org/repo", Number: 30, GithubID: 303, StoreID: 3, Assignees: []string{botLogin}},
		}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked == nil {
			t.Fatal("expected a picked candidate, got nil")
		}
		if picked.Number != 30 {
			t.Errorf("expected issue #30, got #%d", picked.Number)
		}
		if bucket != BucketBotAssigned {
			t.Errorf("expected BucketBotAssigned, got %v", bucket)
		}
	})
}

// TestPick_CascadeOrder verifies that the selector picks in order:
// BotAssigned > Unassigned > Others, and respects skip labels.
func TestPick_CascadeOrder(t *testing.T) {
	ctx := context.Background()
	const botLogin = "bot"
	const branchPrefix = "auto/"

	candOthers := Candidate{
		Repo: "org/repo", Number: 1, GithubID: 1, StoreID: 1,
		Assignees: []string{"alice"},
		Labels:    nil,
		Title:     "Others issue",
	}
	candUnassigned := Candidate{
		Repo: "org/repo", Number: 2, GithubID: 2, StoreID: 2,
		Assignees: nil,
		Labels:    nil,
		Title:     "Unassigned issue",
	}
	candBot := Candidate{
		Repo: "org/repo", Number: 3, GithubID: 3, StoreID: 3,
		Assignees: []string{botLogin},
		Labels:    nil,
		Title:     "Bot assigned issue",
	}
	candBotSkipped := Candidate{
		Repo: "org/repo", Number: 4, GithubID: 4, StoreID: 4,
		Assignees: []string{botLogin},
		Labels:    []string{"blocked"},
		Title:     "Blocked bot issue",
	}

	t.Run("all 4 candidates returns bot-assigned one", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, []string{"blocked", "wontfix"})

		cands := []Candidate{candOthers, candUnassigned, candBot, candBotSkipped}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked == nil {
			t.Fatal("expected a candidate, got nil")
		}
		if picked.Number != candBot.Number {
			t.Errorf("expected candBot (#%d), got #%d", candBot.Number, picked.Number)
		}
		if bucket != BucketBotAssigned {
			t.Errorf("expected BucketBotAssigned, got %v", bucket)
		}
	})

	t.Run("only others and unassigned returns unassigned", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, []string{"blocked", "wontfix"})

		cands := []Candidate{candOthers, candUnassigned}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked == nil {
			t.Fatal("expected a candidate, got nil")
		}
		if picked.Number != candUnassigned.Number {
			t.Errorf("expected candUnassigned (#%d), got #%d", candUnassigned.Number, picked.Number)
		}
		if bucket != BucketUnassigned {
			t.Errorf("expected BucketUnassigned, got %v", bucket)
		}
	})

	t.Run("only others returns others candidate", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(true, true, []string{"blocked", "wontfix"})

		cands := []Candidate{candOthers}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked == nil {
			t.Fatal("expected a candidate, got nil")
		}
		if picked.Number != candOthers.Number {
			t.Errorf("expected candOthers (#%d), got #%d", candOthers.Number, picked.Number)
		}
		if bucket != BucketOthers {
			t.Errorf("expected BucketOthers, got %v", bucket)
		}
	})

	t.Run("takeOthers=false with only others returns none", func(t *testing.T) {
		store := newFakeStore()
		gh := newFakeGH()

		sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
		sel.Configure(false, true, []string{"blocked", "wontfix"})

		cands := []Candidate{candOthers}
		picked, bucket, err := sel.Pick(ctx, cands)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if picked != nil {
			t.Errorf("expected nil pick with takeOthers=false, got %+v", picked)
		}
		if bucket != BucketNone {
			t.Errorf("expected BucketNone, got %v", bucket)
		}
	})
}

// TestPick_SkipsStarted verifies that a bot-assigned candidate with an open PR
// is skipped and Pick returns nil, BucketNone, nil.
func TestPick_SkipsStarted(t *testing.T) {
	ctx := context.Background()
	const botLogin = "heimdallm-bot"
	const branchPrefix = "feat/issue-"

	store := newFakeStore()
	gh := newFakeGH()
	store.openPRs[999] = true // GithubID 999 has an open PR

	sel := NewSelector(store, gh, botLogin, branchPrefix, nil)
	sel.Configure(true, true, nil)

	cands := []Candidate{
		{Repo: "org/repo", Number: 50, GithubID: 999, StoreID: 50, Assignees: []string{botLogin}},
	}
	picked, bucket, err := sel.Pick(ctx, cands)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if picked != nil {
		t.Errorf("expected nil pick for started issue, got %+v", picked)
	}
	if bucket != BucketNone {
		t.Errorf("expected BucketNone, got %v", bucket)
	}
}

// TestClaim_OthersReassignsAndComments verifies that claiming a BucketOthers
// candidate triggers reassign and posts a coordination comment.
func TestClaim_OthersReassignsAndComments(t *testing.T) {
	ctx := context.Background()
	const botLogin = "heimdallm-bot"
	const branchPrefix = "auto/"

	store := newFakeStore()
	rgh := newRecordingGH()
	commentGen := &fakeCommentGen{comment: "coordination comment text"}

	sel := NewSelector(store, rgh, botLogin, branchPrefix, commentGen)
	sel.Configure(true, true, nil)

	c := Candidate{
		Repo: "org/repo", Number: 77, GithubID: 777, StoreID: 42,
		Assignees: []string{"alice"},
		Title:     "Some issue",
	}

	if err := sel.Claim(ctx, c, BucketOthers); err != nil {
		t.Fatalf("unexpected error from Claim: %v", err)
	}

	if len(rgh.addAssigneesCalls) != 1 {
		t.Errorf("expected 1 AddAssignees call, got %d", len(rgh.addAssigneesCalls))
	} else {
		call := rgh.addAssigneesCalls[0]
		if len(call.assignees) != 1 || call.assignees[0] != botLogin {
			t.Errorf("expected assignees=[%s], got %v", botLogin, call.assignees)
		}
	}

	if len(rgh.postCommentCalls) != 1 {
		t.Errorf("expected 1 PostComment call, got %d", len(rgh.postCommentCalls))
	} else {
		call := rgh.postCommentCalls[0]
		if call.body != "coordination comment text" {
			t.Errorf("expected comment body %q, got %q", "coordination comment text", call.body)
		}
	}

	if !store.claimed[42] {
		t.Errorf("expected store.claimed[42] == true, got false")
	}
}

// TestClaim_BotAssignedSkipsReassign verifies that claiming a BucketBotAssigned
// candidate does NOT trigger reassign or post a comment.
func TestClaim_BotAssignedSkipsReassign(t *testing.T) {
	ctx := context.Background()
	const botLogin = "heimdallm-bot"
	const branchPrefix = "auto/"

	store := newFakeStore()
	rgh := newRecordingGH()
	commentGen := &fakeCommentGen{comment: "should not appear"}

	sel := NewSelector(store, rgh, botLogin, branchPrefix, commentGen)
	sel.Configure(true, true, nil)

	c := Candidate{
		Repo: "org/repo", Number: 88, GithubID: 888, StoreID: 7,
		Assignees: []string{botLogin},
		Title:     "Bot-assigned issue",
	}

	if err := sel.Claim(ctx, c, BucketBotAssigned); err != nil {
		t.Fatalf("unexpected error from Claim: %v", err)
	}

	if len(rgh.addAssigneesCalls) != 0 {
		t.Errorf("expected 0 AddAssignees calls, got %d", len(rgh.addAssigneesCalls))
	}

	if len(rgh.postCommentCalls) != 0 {
		t.Errorf("expected 0 PostComment calls, got %d", len(rgh.postCommentCalls))
	}

	if !store.claimed[7] {
		t.Errorf("expected store.claimed[7] == true, got false")
	}
}
