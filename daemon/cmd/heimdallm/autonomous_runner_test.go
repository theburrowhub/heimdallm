package main

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/autonomous"
	gh "github.com/heimdallm/daemon/internal/github"
)

func TestToReviewInputs_PreservesOrderAndFields(t *testing.T) {
	reviews := []gh.PRReview{
		{State: "COMMENTED", Body: "first pass", SubmittedAt: time.Unix(1, 0)},
		{State: "CHANGES_REQUESTED", Body: "please fix the lint", SubmittedAt: time.Unix(2, 0)},
		{State: "APPROVED", Body: "lgtm", SubmittedAt: time.Unix(3, 0)},
	}
	got := toReviewInputs(reviews)
	if len(got) != len(reviews) {
		t.Fatalf("len = %d, want %d", len(got), len(reviews))
	}
	for i := range reviews {
		if got[i].State != reviews[i].State {
			t.Errorf("input[%d].State = %q, want %q", i, got[i].State, reviews[i].State)
		}
		if got[i].Body != reviews[i].Body {
			t.Errorf("input[%d].Body = %q, want %q", i, got[i].Body, reviews[i].Body)
		}
		// No unresolved-thread API: always 0.
		if got[i].UnresolvedComments != 0 {
			t.Errorf("input[%d].UnresolvedComments = %d, want 0", i, got[i].UnresolvedComments)
		}
	}
}

func TestToReviewInputs_Empty(t *testing.T) {
	if got := toReviewInputs(nil); len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// toReviewInputs must feed ClassifyReview correctly: the latest review
// dominates, so a trailing clean APPROVED routes to the merge gate while a
// trailing CHANGES_REQUESTED routes to a fix.
func TestToReviewInputs_IntegratesWithClassifyReview(t *testing.T) {
	cases := []struct {
		name    string
		reviews []gh.PRReview
		want    autonomous.ReviewDecision
	}{
		{
			name:    "empty -> wait",
			reviews: nil,
			want:    autonomous.DecisionWait,
		},
		{
			name: "trailing clean approval -> merge gate",
			reviews: []gh.PRReview{
				{State: "CHANGES_REQUESTED", Body: "fix"},
				{State: "APPROVED", Body: "lgtm"},
			},
			want: autonomous.DecisionMergeGate,
		},
		{
			name: "trailing changes requested -> fix",
			reviews: []gh.PRReview{
				{State: "APPROVED", Body: "lgtm"},
				{State: "CHANGES_REQUESTED", Body: "regressed"},
			},
			want: autonomous.DecisionFix,
		},
		{
			name: "approved with actionable body -> fix",
			reviews: []gh.PRReview{
				{State: "APPROVED", Body: "please rename the field before merge"},
			},
			want: autonomous.DecisionFix,
		},
		{
			name: "commented -> fix",
			reviews: []gh.PRReview{
				{State: "COMMENTED", Body: "thoughts"},
			},
			want: autonomous.DecisionFix,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autonomous.ClassifyReview(toReviewInputs(tc.reviews))
			if got != tc.want {
				t.Errorf("ClassifyReview = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssueToCandidate_MapsFields(t *testing.T) {
	iss := &gh.Issue{
		ID:     987654,
		Number: 42,
		Title:  "Add retry to fetcher",
		Body:   "We should retry transient 5xx.",
		Assignees: []gh.User{
			{Login: "alice"},
			{Login: "bot[bot]"},
		},
		Labels: []gh.Label{
			{Name: "bug"},
			{Name: "develop"},
		},
		Repo: "org/repo",
	}
	const storeID int64 = 555

	c := issueToCandidate(iss, storeID)

	if c.Repo != "org/repo" {
		t.Errorf("Repo = %q, want org/repo", c.Repo)
	}
	if c.Number != 42 {
		t.Errorf("Number = %d, want 42", c.Number)
	}
	if c.GithubID != 987654 {
		t.Errorf("GithubID = %d, want 987654", c.GithubID)
	}
	if c.StoreID != storeID {
		t.Errorf("StoreID = %d, want %d", c.StoreID, storeID)
	}
	if c.Title != "Add retry to fetcher" {
		t.Errorf("Title = %q", c.Title)
	}
	if c.Body != "We should retry transient 5xx." {
		t.Errorf("Body = %q", c.Body)
	}
	wantAssignees := []string{"alice", "bot[bot]"}
	if len(c.Assignees) != len(wantAssignees) {
		t.Fatalf("Assignees = %v, want %v", c.Assignees, wantAssignees)
	}
	for i := range wantAssignees {
		if c.Assignees[i] != wantAssignees[i] {
			t.Errorf("Assignees[%d] = %q, want %q", i, c.Assignees[i], wantAssignees[i])
		}
	}
	wantLabels := []string{"bug", "develop"}
	if len(c.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", c.Labels, wantLabels)
	}
	for i := range wantLabels {
		if c.Labels[i] != wantLabels[i] {
			t.Errorf("Labels[%d] = %q, want %q", i, c.Labels[i], wantLabels[i])
		}
	}
}
