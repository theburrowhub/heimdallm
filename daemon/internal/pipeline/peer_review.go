package pipeline

import (
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// ReviewFooterMarker is the substring every review body Heimdallm publishes
// carries, and therefore the only cross-instance claim this project has.
//
// Two daemons in a cluster share nothing: their SQLite stores, their
// reviews_in_flight claims and their circuit breakers are all local, and a
// network partition takes the hub's health probe away too. GitHub is the one
// store both of them can still reach, so the PR itself is where the claim has
// to live — and the footer is already on it. See theburrowhub/heimdallm#765.
//
// Deliberately login-independent: docs §18.4 states github.token never
// propagates ("each instance authenticates as itself"), so two instances may
// publish as two different bot accounts. Keying the claim on the author would
// miss exactly the case it exists to catch.
const ReviewFooterMarker = "Reviewed by [Heimdallm]"

// BodyIsHeimdallm reports whether a review body was written by a Heimdallm
// instance.
func BodyIsHeimdallm(body string) bool {
	return strings.Contains(body, ReviewFooterMarker)
}

// PublishedReviewFetcher lists the reviews already on a PR.
//
// Optional capability, discovered by type assertion on the pipeline's gh
// dependency exactly like PRSnapshotFetcher and CommitAnchoredReviewer:
// *github.Client implements it, the package's test doubles do not, and the
// duplicate guard degrades to "publish anyway" without it.
type PublishedReviewFetcher interface {
	GetPRReviews(repo string, number int) ([]github.PRReview, error)
}

// PeerPublishedReviewID reports a review another Heimdallm instance has
// already published for commitID, returning its GitHub id and state so the
// caller can point its own local row at the review that actually exists.
//
// ours is the set of GitHub review ids this daemon published itself. Excluding
// them is load-bearing, not tidiness: a forced re-review runs against an
// unchanged HEAD on purpose, and without the exclusion the daemon would find
// its own earlier review anchored to that commit and refuse to publish the new
// one — breaking the manual re-review button rather than fixing #765.
//
// Every ambiguous case returns false (publish anyway), matching Router.Owns'
// bias: a duplicate review is recoverable, a permanently withheld one is not.
//   - An empty commitID has nothing to anchor on and would otherwise match
//     every unanchored legacy review on the PR.
//   - PENDING is a draft no one can see.
//   - DISMISSED was explicitly retired by a human, who is entitled to a fresh
//     verdict on the same commit.
//
// The last match wins: GitHub returns reviews chronologically, so the newest
// review for the commit is the one still standing.
func PeerPublishedReviewID(reviews []github.PRReview, commitID string, ours map[int64]bool) (id int64, state string, found bool) {
	if strings.TrimSpace(commitID) == "" {
		return 0, "", false
	}
	for _, rev := range reviews {
		if rev.CommitID != commitID || ours[rev.ID] {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(rev.State)) {
		case "PENDING", "DISMISSED":
			continue
		}
		if !BodyIsHeimdallm(rev.Body) {
			continue
		}
		id, state, found = rev.ID, rev.State, true
	}
	return id, state, found
}

// PublishedPeerReview is PeerPublishedReviewID with the GitHub lookup in
// front of it, shared by the three sites that submit a review (Run,
// PublishPending and the NATS publish worker in cmd/heimdallm) so a future
// change to the claim cannot drift between them.
//
// Fails open on a nil fetcher, an empty commit and any API error: the guard
// exists to stop a second review, never to withhold the first one because the
// lookup was rate-limited.
func PublishedPeerReview(f PublishedReviewFetcher, repo string, number int, commitID string, ours map[int64]bool) (id int64, state string, found bool) {
	if f == nil || strings.TrimSpace(commitID) == "" {
		return 0, "", false
	}
	reviews, err := f.GetPRReviews(repo, number)
	if err != nil {
		slog.Warn("pipeline: could not list published reviews, cannot check for a peer instance's review",
			"repo", repo, "pr", number, "commit", commitID, "err", err)
		return 0, "", false
	}
	return PeerPublishedReviewID(reviews, commitID, ours)
}

// ownPublishedReviewIDs is the set of GitHub review ids this daemon published
// for prID. Anything on the PR outside this set and carrying the Heimdallm
// footer came from another instance.
//
// A store error yields an empty set, which is the cautious direction for this
// particular lookup: with no way to recognise our own reviews the guard treats
// them as a peer's and declines to publish again. It never publishes twice on
// a failed read.
func (p *Pipeline) ownPublishedReviewIDs(prID int64) map[int64]bool {
	out := map[int64]bool{}
	if p.store == nil {
		return out
	}
	reviews, err := p.store.ListReviewsForPR(prID)
	if err != nil {
		slog.Warn("pipeline: could not list local reviews for the peer-review guard",
			"pr_id", prID, "err", err)
		return out
	}
	for _, rev := range reviews {
		if rev.GitHubReviewID > 0 {
			out[rev.GitHubReviewID] = true
		}
	}
	return out
}

// SkipIfPeerPublished is the publish-boundary half of the #765 fix: the guard
// that runs immediately before a review is submitted and stops it when another
// Heimdallm instance has already published one for the same commit.
//
// It is the only defence in this project that works across a network
// partition. Everything upstream — reviews_in_flight, PRAlreadyReviewed, the
// SHA guard in Run, the circuit breaker — reads a SQLite database local to one
// daemon, so two partitioned instances each conclude the PR is unreviewed.
// GitHub is the one store both of them can still reach, and the review footer
// is already on the PR, so that is where the claim lives.
//
// When a peer's review is found the local row is retired against it rather than
// deleted or left pending: the row was stored with GitHubReviewID == 0, which
// is exactly what ListUnpublishedReviews selects on, so skipping the submit
// without retiring it would just hand the duplicate to the publish worker to
// post a minute later. Pointing it at the peer's id also gives the UI a review
// that genuinely exists, and stamps the published_at the dedup window anchors
// on.
//
// Returns (true, …) when the caller must stop before submitting. Exported for
// the NATS publish worker in cmd/heimdallm, which submits outside Run and must
// apply the identical check — the same reason markOrphanIfPermanent is shared.
func (p *Pipeline) SkipIfPeerPublished(rev *store.Review, repo string, number int, commitID string) (bool, error) {
	// An empty commit has nothing to anchor a claim on, and a nil store cannot
	// retire the row the skip would otherwise leave pending for the publish
	// worker to post anyway. Both short-circuit before the store read below.
	if rev == nil || p.store == nil || strings.TrimSpace(commitID) == "" {
		return false, nil
	}
	fetcher, ok := p.gh.(PublishedReviewFetcher)
	if !ok {
		return false, nil
	}
	peerID, peerState, found := PublishedPeerReview(fetcher, repo, number, commitID, p.ownPublishedReviewIDs(rev.PRID))
	if !found {
		return false, nil
	}

	slog.Warn("pipeline: another Heimdallm instance already published a review for this commit, not publishing a second one",
		"repo", repo, "pr", number, "commit", commitID,
		"review_id", rev.ID, "peer_github_review_id", peerID, "peer_state", peerState)
	if err := p.store.MarkReviewPublished(rev.ID, peerID, peerState, time.Now().UTC()); err != nil {
		// Report rather than swallow, but still stop: publishing anyway is the
		// duplicate this guard exists to prevent, and the row stays pending so
		// the publish worker re-checks it on the next tick.
		return true, err
	}
	p.publish(sse.EventReviewSkipped, map[string]any{
		"repo":      repo,
		"pr_number": number,
		"reason":    string(SkipReasonPeerPublished),
	})
	return true, nil
}
