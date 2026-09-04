package pipeline_test

import (
	"errors"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/pipeline"
)

// heimdallmBody builds a review body carrying the same provenance footer every
// Heimdallm instance appends, so the tests exercise the real marker rather than
// a hand-written approximation of it.
func heimdallmBody(t *testing.T, summary string) string {
	t.Helper()
	body := summary + "\n\n---\n🤖 *" + pipeline.ReviewFooterMarker + "(https://theburrowhub.github.io/heimdallm/)*"
	if !pipeline.BodyIsHeimdallm(body) {
		t.Fatalf("test helper built a body the marker does not recognise: %q", body)
	}
	return body
}

func TestReviewFooterMarkerMatchesTheRealFooter(t *testing.T) {
	// The marker is the cross-instance claim key: if reviewFooter ever stops
	// containing it, the duplicate-review guard silently stops matching and
	// #765 comes back with no test failing.
	for _, result := range []*executor.ReviewResult{
		{Summary: "nothing to flag", Severity: "low"},
		{Summary: "found things", Severity: "high", Issues: []executor.Issue{
			{File: "a.go", Line: 1, Description: "boom", Severity: "high"},
		}},
	} {
		body := pipeline.BuildGitHubBody(result)
		if !pipeline.BodyIsHeimdallm(body) {
			t.Errorf("BuildGitHubBody() output does not contain ReviewFooterMarker %q:\n%s",
				pipeline.ReviewFooterMarker, body)
		}
	}
}

func TestPeerPublishedReviewIDFindsAPeerReviewOnTheSameCommit(t *testing.T) {
	reviews := []github.PRReview{
		{ID: 11, CommitID: "aaa", State: "COMMENTED", Body: heimdallmBody(t, "older commit")},
		{ID: 22, CommitID: "bbb", State: "CHANGES_REQUESTED", Body: heimdallmBody(t, "this commit")},
	}
	id, state, found := pipeline.PeerPublishedReviewID(reviews, "bbb", nil)
	if !found {
		t.Fatal("PeerPublishedReviewID() found = false, want true")
	}
	if id != 22 {
		t.Errorf("id = %d, want 22", id)
	}
	if state != "CHANGES_REQUESTED" {
		t.Errorf("state = %q, want CHANGES_REQUESTED", state)
	}
}

func TestPeerPublishedReviewIDIgnoresOurOwnPublishedReviews(t *testing.T) {
	// A forced re-review of an unchanged commit must still publish. Without
	// this exclusion the guard would mistake the daemon's own earlier review
	// for a peer's and refuse every manual re-review.
	reviews := []github.PRReview{
		{ID: 22, CommitID: "bbb", State: "COMMENTED", Body: heimdallmBody(t, "ours")},
	}
	if _, _, found := pipeline.PeerPublishedReviewID(reviews, "bbb", map[int64]bool{22: true}); found {
		t.Error("PeerPublishedReviewID() found = true, want false for a review this daemon published")
	}
}

func TestPeerPublishedReviewIDIgnoresNonHeimdallmReviews(t *testing.T) {
	reviews := []github.PRReview{
		{ID: 22, CommitID: "bbb", State: "CHANGES_REQUESTED", Body: "Please rename this variable."},
	}
	if _, _, found := pipeline.PeerPublishedReviewID(reviews, "bbb", nil); found {
		t.Error("PeerPublishedReviewID() found = true, want false for a human review")
	}
}

func TestPeerPublishedReviewIDIgnoresOtherCommits(t *testing.T) {
	reviews := []github.PRReview{
		{ID: 22, CommitID: "aaa", State: "COMMENTED", Body: heimdallmBody(t, "previous head")},
	}
	if _, _, found := pipeline.PeerPublishedReviewID(reviews, "bbb", nil); found {
		t.Error("PeerPublishedReviewID() found = true, want false for a review anchored elsewhere")
	}
}

func TestPeerPublishedReviewIDIgnoresPendingAndDismissed(t *testing.T) {
	// PENDING is a draft nobody can see; DISMISSED was explicitly retired by a
	// human, who is entitled to a fresh verdict on the same commit.
	for _, state := range []string{"PENDING", "DISMISSED"} {
		reviews := []github.PRReview{
			{ID: 22, CommitID: "bbb", State: state, Body: heimdallmBody(t, "not standing")},
		}
		if _, _, found := pipeline.PeerPublishedReviewID(reviews, "bbb", nil); found {
			t.Errorf("PeerPublishedReviewID() found = true for state %q, want false", state)
		}
	}
}

func TestPeerPublishedReviewIDRequiresACommitToAnchorOn(t *testing.T) {
	// An empty commit id would match every unanchored legacy review and start
	// suppressing legitimate publishes. Fail open instead.
	reviews := []github.PRReview{
		{ID: 22, CommitID: "", State: "COMMENTED", Body: heimdallmBody(t, "legacy")},
	}
	if _, _, found := pipeline.PeerPublishedReviewID(reviews, "", nil); found {
		t.Error("PeerPublishedReviewID() found = true for an empty commit id, want false")
	}
}

func TestPeerPublishedReviewIDPrefersTheMostRecentMatch(t *testing.T) {
	reviews := []github.PRReview{
		{ID: 22, CommitID: "bbb", State: "COMMENTED", Body: heimdallmBody(t, "first")},
		{ID: 33, CommitID: "bbb", State: "APPROVED", Body: heimdallmBody(t, "second")},
	}
	id, state, found := pipeline.PeerPublishedReviewID(reviews, "bbb", nil)
	if !found || id != 33 || state != "APPROVED" {
		t.Errorf("PeerPublishedReviewID() = (%d, %q, %v), want (33, APPROVED, true)", id, state, found)
	}
}

// fakeReviewLister is the optional GetPRReviews capability, nothing else.
type fakeReviewLister struct {
	reviews []github.PRReview
	err     error
	calls   int
}

func (f *fakeReviewLister) GetPRReviews(string, int) ([]github.PRReview, error) {
	f.calls++
	return f.reviews, f.err
}

func TestPublishedPeerReviewFailsOpenWithoutTheCapability(t *testing.T) {
	// A nil fetcher is every test double and any adapter predating the
	// capability: publishing must not stop because the lookup is unavailable.
	if _, _, found := pipeline.PublishedPeerReview(nil, "o/r", 1, "bbb", nil); found {
		t.Error("PublishedPeerReview(nil) found = true, want false")
	}
}

func TestPublishedPeerReviewFailsOpenOnAPIError(t *testing.T) {
	// Rate limits and outages must not block a review the operator is waiting
	// for. A duplicate is recoverable; a permanently withheld review is not.
	f := &fakeReviewLister{err: errors.New("403 rate limited")}
	if _, _, found := pipeline.PublishedPeerReview(f, "o/r", 1, "bbb", nil); found {
		t.Error("PublishedPeerReview() found = true on API error, want false")
	}
	if f.calls != 1 {
		t.Errorf("GetPRReviews calls = %d, want 1", f.calls)
	}
}

func TestPublishedPeerReviewSkipsTheAPICallWithoutACommit(t *testing.T) {
	f := &fakeReviewLister{}
	if _, _, found := pipeline.PublishedPeerReview(f, "o/r", 1, "", nil); found {
		t.Error("PublishedPeerReview() found = true for an empty commit, want false")
	}
	if f.calls != 0 {
		t.Errorf("GetPRReviews calls = %d, want 0 — no commit means nothing to anchor on", f.calls)
	}
}

func TestPublishedPeerReviewReportsAPeersReview(t *testing.T) {
	f := &fakeReviewLister{reviews: []github.PRReview{
		{ID: 44, CommitID: "bbb", State: "COMMENTED", Body: heimdallmBody(t, "peer")},
	}}
	id, state, found := pipeline.PublishedPeerReview(f, "o/r", 1, "bbb", nil)
	if !found || id != 44 || state != "COMMENTED" {
		t.Errorf("PublishedPeerReview() = (%d, %q, %v), want (44, COMMENTED, true)", id, state, found)
	}
}

// peerGH is a minimal gh dependency that also carries the optional
// GetPRReviews capability, so SkipIfPeerPublished's type assertion succeeds.
type peerGH struct {
	reviews    []github.PRReview
	reviewsErr error
	submitted  int
}

func (g *peerGH) FetchDiff(string, int) (string, error) { return "+x", nil }
func (g *peerGH) SubmitReview(string, int, string, string) (int64, string, error) {
	g.submitted++
	return 999, "COMMENTED", nil
}
func (g *peerGH) PostComment(string, int, string) (time.Time, error) { return time.Now(), nil }
func (g *peerGH) FetchComments(string, int) ([]github.Comment, error) {
	return nil, nil
}
func (g *peerGH) GetPRHeadSHA(string, int) (string, error) { return "", nil }
func (g *peerGH) GetPRReviews(string, int) ([]github.PRReview, error) {
	return g.reviews, g.reviewsErr
}

// storeWithPendingReview builds an in-memory store holding one PR and one
// unpublished review for headSHA, and returns the review.
func storeWithPendingReview(t *testing.T, headSHA string) (*store.Store, *store.Review) {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	prID, err := s.UpsertPR(&store.PR{
		GithubID: 7001, Repo: "acme/widgets", Number: 12, Title: "t", Author: "alice",
		State: "open", UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	revID, err := s.InsertReview(&store.Review{
		PRID: prID, CLIUsed: "claude", Summary: "ours", Issues: "[]", Suggestions: "[]",
		Severity: "medium", CreatedAt: time.Now().UTC(), HeadSHA: headSHA, Event: "COMMENT",
	})
	if err != nil {
		t.Fatalf("InsertReview: %v", err)
	}
	rev, err := s.GetReview(revID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	return s, rev
}

func TestSkipIfPeerPublishedRetiresTheLocalRowAgainstThePeersReview(t *testing.T) {
	s, rev := storeWithPendingReview(t, "deadbeef")
	gh := &peerGH{reviews: []github.PRReview{
		{ID: 4242, CommitID: "deadbeef", State: "CHANGES_REQUESTED", Body: heimdallmBody(t, "peer verdict")},
	}}
	p := pipeline.New(s, gh, &peerExec{}, &peerNotify{})

	skip, err := p.SkipIfPeerPublished(rev, "acme/widgets", 12, "deadbeef")
	if err != nil {
		t.Fatalf("SkipIfPeerPublished: %v", err)
	}
	if !skip {
		t.Fatal("SkipIfPeerPublished() = false, want true — a peer already reviewed this commit")
	}

	// Retiring the row is load-bearing: left at github_review_id == 0 it stays
	// in ListUnpublishedReviews and the publish worker posts the duplicate a
	// minute later.
	stored, err := s.GetReview(rev.ID)
	if err != nil {
		t.Fatalf("GetReview: %v", err)
	}
	if stored.GitHubReviewID != 4242 {
		t.Errorf("stored github_review_id = %d, want 4242 (the peer's review)", stored.GitHubReviewID)
	}
	if stored.GitHubReviewState != "CHANGES_REQUESTED" {
		t.Errorf("stored state = %q, want CHANGES_REQUESTED", stored.GitHubReviewState)
	}
	if stored.PublishedAt.IsZero() {
		t.Error("published_at not stamped; the dedup window has no anchor")
	}
	pending, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("ListUnpublishedReviews: %v", err)
	}
	for _, r := range pending {
		if r.ID == rev.ID {
			t.Error("review still listed as unpublished; the publish worker would post the duplicate")
		}
	}
}

func TestSkipIfPeerPublishedAllowsAForcedReReviewOfOurOwnCommit(t *testing.T) {
	// The daemon's own published review must not look like a peer's, or the
	// manual re-review button stops working on every unchanged HEAD.
	s, rev := storeWithPendingReview(t, "deadbeef")
	published, err := s.GetPRByGithubID(7001)
	if err != nil || published == nil {
		t.Fatalf("GetPRByGithubID: %v", err)
	}
	oursID, err := s.InsertReview(&store.Review{
		PRID: published.ID, CLIUsed: "claude", Summary: "earlier run", Issues: "[]",
		Suggestions: "[]", Severity: "low", CreatedAt: time.Now().UTC().Add(-time.Hour),
		HeadSHA: "deadbeef", Event: "COMMENT",
	})
	if err != nil {
		t.Fatalf("InsertReview: %v", err)
	}
	if err := s.MarkReviewPublished(oursID, 4242, "COMMENTED", time.Now().UTC().Add(-time.Hour)); err != nil {
		t.Fatalf("MarkReviewPublished: %v", err)
	}

	gh := &peerGH{reviews: []github.PRReview{
		{ID: 4242, CommitID: "deadbeef", State: "COMMENTED", Body: heimdallmBody(t, "our earlier run")},
	}}
	p := pipeline.New(s, gh, &peerExec{}, &peerNotify{})

	skip, err := p.SkipIfPeerPublished(rev, "acme/widgets", 12, "deadbeef")
	if err != nil {
		t.Fatalf("SkipIfPeerPublished: %v", err)
	}
	if skip {
		t.Error("SkipIfPeerPublished() = true for the daemon's own review — forced re-reviews would be blocked")
	}
}

func TestSkipIfPeerPublishedFailsOpenOnAPIError(t *testing.T) {
	s, rev := storeWithPendingReview(t, "deadbeef")
	gh := &peerGH{reviewsErr: errors.New("502 bad gateway")}
	p := pipeline.New(s, gh, &peerExec{}, &peerNotify{})

	skip, err := p.SkipIfPeerPublished(rev, "acme/widgets", 12, "deadbeef")
	if err != nil || skip {
		t.Errorf("SkipIfPeerPublished() = (%v, %v), want (false, nil) — a rate limit must not withhold a review", skip, err)
	}
}

func TestSkipIfPeerPublishedNoOpForANonHeimdallmReview(t *testing.T) {
	s, rev := storeWithPendingReview(t, "deadbeef")
	gh := &peerGH{reviews: []github.PRReview{
		{ID: 4242, CommitID: "deadbeef", State: "CHANGES_REQUESTED", Body: "human feedback"},
	}}
	p := pipeline.New(s, gh, &peerExec{}, &peerNotify{})

	if skip, _ := p.SkipIfPeerPublished(rev, "acme/widgets", 12, "deadbeef"); skip {
		t.Error("SkipIfPeerPublished() = true for a human review, want false")
	}
}

type peerExec struct{}

func (peerExec) Detect(string, string) (string, error) { return "claude", nil }
func (peerExec) Execute(string, string, executor.ExecOptions) (*executor.ReviewResult, error) {
	return &executor.ReviewResult{Summary: "s", Severity: "low"}, nil
}

type peerNotify struct{}

func (peerNotify) Notify(string, string) {}
