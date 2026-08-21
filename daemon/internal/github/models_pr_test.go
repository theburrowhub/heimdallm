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
