package github_test

import (
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestPullRequestReviewRequestedFor(t *testing.T) {
	pr := &gh.PullRequest{RequestedReviewers: []gh.User{{Login: "Heimdallm-Bot"}}}
	if !pr.ReviewRequestedFor("@heimdallm-bot") {
		t.Fatal("case-insensitive requested reviewer was not matched")
	}
	if pr.ReviewRequestedFor("someone-else") {
		t.Fatal("unrequested reviewer matched")
	}
}

func TestPullRequestReviewRequestedForRejectsMissingInputs(t *testing.T) {
	var nilPR *gh.PullRequest
	if nilPR.ReviewRequestedFor("heimdallm-bot") {
		t.Fatal("nil PR matched a reviewer")
	}

	withoutReviewers := &gh.PullRequest{}
	if withoutReviewers.ReviewRequestedFor("heimdallm-bot") {
		t.Fatal("nil requested-reviewers list matched a reviewer")
	}

	pr := &gh.PullRequest{RequestedReviewers: []gh.User{{Login: "heimdallm-bot"}}}
	for _, login := range []string{"", " ", "@", " @ "} {
		if pr.ReviewRequestedFor(login) {
			t.Fatalf("empty reviewer login %q matched", login)
		}
	}
}
