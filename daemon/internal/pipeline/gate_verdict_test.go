package pipeline_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
	"github.com/heimdallm/daemon/internal/store"
)

// TestPipeline_Run_CachesNoReReviewVerdict covers the poll-loop churn behind
// "Skipped because no rereview request" every few minutes on the same PR: once
// the gate has decided a PR (same previous review, HEAD and updated_at) has no
// re-review request, a repeat evaluation must skip without consulting GitHub
// again, and a later updated_at bump carrying a re-request must still review.
func TestPipeline_Run_CachesNoReReviewVerdict(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	gh := &fakeGHCounter{diff: "+line"}
	pub := &fakePublisher{}
	p := pipeline.New(s, gh, exec, &fakeNotify{})
	p.SetPublisher(pub)
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 78, Number: 78, Title: "feat: x", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/78",
		Head:      github.Branch{SHA: "first-sha"},
	}
	runFirstReview(t, p, pr)

	tl := &fakeTimeline{}
	p.SetTimelineFetcher(tl)
	p.SetReviewerFetcher(&fakeReviewerFetcher{})

	pr.Head.SHA = "second-sha"
	pr.UpdatedAt = time.Now().Add(-30 * time.Minute)
	for i := 0; i < 3; i++ {
		if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if got := tl.callCount(); got != 1 {
		t.Errorf("timeline calls = %d, want 1 (repeats must use the cached verdict)", got)
	}
	if exec.calls != 1 {
		t.Errorf("exec.calls = %d, want 1 (no re-review without a request)", exec.calls)
	}
	skips := 0
	for _, ev := range pub.events {
		if ev.Type == "review_skipped" {
			skips++
			if !strings.Contains(ev.Data, `"reason":"no_rereview_request"`) || !strings.Contains(ev.Data, `"head_sha":"second-sha"`) {
				t.Errorf("skip payload = %s, want no_rereview_request with head_sha", ev.Data)
			}
		}
	}
	if skips != 3 {
		t.Errorf("review_skipped events = %d, want 3 (the SSE still clears spinners)", skips)
	}

	// Operator re-requests: GitHub bumps updated_at and records the event.
	tl.events = []github.TimelineEvent{
		{Event: "review_requested", Actor: "alice", CreatedAt: time.Now().Add(time.Minute)},
	}
	pr.UpdatedAt = time.Now()
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("re-request run: %v", err)
	}
	if exec.calls != 2 {
		t.Errorf("exec.calls = %d after re-request, want 2", exec.calls)
	}
}

// TestPipeline_Run_ZeroUpdatedAtNeverCachesVerdict: callers that do not know
// updated_at cannot tell a repeat from a re-request, so every run must ask
// GitHub.
func TestPipeline_Run_ZeroUpdatedAtNeverCachesVerdict(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 79, Number: 79, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/79",
		Head:      github.Branch{SHA: "first-sha"},
	}
	runFirstReview(t, p, pr)

	tl := &fakeTimeline{}
	p.SetTimelineFetcher(tl)
	pr.Head.SHA = "second-sha"
	pr.UpdatedAt = time.Time{}
	for i := 0; i < 2; i++ {
		if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	if got := tl.callCount(); got != 2 {
		t.Errorf("timeline calls = %d, want 2 without updated_at", got)
	}
}

// A fail-closed lookup error skips the review but is not an answer from
// GitHub: the next poll must ask again, or a re-request that coincided with
// a transient outage would stay hidden until the PR changed again.
func TestPipeline_Run_DoesNotCacheVerdictAfterLookupError(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	exec := &fakeExecCounter{}
	p := pipeline.New(s, &fakeGHCounter{diff: "+line"}, exec, &fakeNotify{})
	p.SetBotLogin("heimdallm-bot")

	pr := &github.PullRequest{
		ID: 80, Number: 80, Title: "t", Repo: "org/repo",
		User: github.User{Login: "alice"}, State: "open",
		UpdatedAt: time.Now().Add(-1 * time.Hour),
		HTMLURL:   "https://github.com/org/repo/pull/80",
		Head:      github.Branch{SHA: "first-sha"},
	}
	runFirstReview(t, p, pr)

	tl := &fakeTimeline{err: errors.New("502 bad gateway")}
	p.SetTimelineFetcher(tl)
	pr.Head.SHA = "second-sha"
	pr.UpdatedAt = time.Now().Add(-30 * time.Minute)
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("run with outage: %v", err)
	}

	// Outage over; the re-request was there all along.
	tl.err = nil
	tl.events = []github.TimelineEvent{
		{Event: "review_requested", Actor: "alice", CreatedAt: time.Now().Add(time.Minute)},
	}
	if _, err := p.Run(pr, pipeline.RunOptions{Primary: "claude"}); err != nil {
		t.Fatalf("run after outage: %v", err)
	}
	if got := tl.callCount(); got != 2 {
		t.Errorf("timeline calls = %d, want 2 (the failed verdict must not be cached)", got)
	}
	if exec.calls != 2 {
		t.Errorf("exec.calls = %d, want 2 (re-request honoured once the lookup works)", exec.calls)
	}
}
