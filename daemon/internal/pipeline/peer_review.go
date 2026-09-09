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
// The marker says "a Heimdallm wrote this"; it does not say which one. The
// claim is therefore scoped by the review's author login in
// PeerPublishedReviewID: only a footered review published under this daemon's
// own GitHub login counts as a peer's. #767 originally made the claim
// login-independent on the grounds that cluster instances "authenticate as
// themselves" (docs §18.4), but every duplicate that motivated #765 and #770
// was two daemons publishing under one login — and several operators each
// running a standalone daemon as themselves are several reviewers GitHub asked
// for separately, not copies of one (theburrowhub/heimdallm#778).
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
// already published for any of commitIDs, returning its GitHub id and state
// so the caller can point its own local row at the review that actually
// exists.
//
// "Another instance" means another daemon publishing under ownLogin — the
// GitHub login this daemon authenticates as. A footered review by a different
// login is a colleague's daemon answering its own review request, not a
// duplicate of ours, and is never a claim against us (#778). The comparison
// is case-insensitive because GitHub logins are. An empty ownLogin means the
// daemon could not resolve who it is, which leaves no way to tell a peer from
// a colleague: fail open and publish, like every other ambiguous case below.
//
// Accepting several anchors — rather than one — matters because GitHub
// retargets an existing review's commit_id onto the merge commit an "Update
// branch" click produces, while the caller's own bookkeeping (the stored
// row's HeadSHA, or the PR's live HEAD) is not updated in lockstep. Anchoring
// on only one of the two loses the peer whose review just got moved out from
// under it. See theburrowhub/heimdallm#772.
//
// ours is the set of GitHub review ids this daemon published itself. Excluding
// them is load-bearing, not tidiness: a forced re-review runs against an
// unchanged HEAD on purpose, and without the exclusion the daemon would find
// its own earlier review anchored to that commit and refuse to publish the new
// one — breaking the manual re-review button rather than fixing #765.
//
// Every ambiguous case returns false (publish anyway), matching Router.Owns'
// bias: a duplicate review is recoverable, a permanently withheld one is not.
//   - No non-empty commitIDs means nothing to anchor on and would otherwise
//     match every unanchored legacy review on the PR.
//   - PENDING is a draft no one can see.
//   - DISMISSED was explicitly retired by a human, who is entitled to a fresh
//     verdict on the same commit.
//
// The last match wins: GitHub returns reviews chronologically, so the newest
// review for the commit is the one still standing.
func PeerPublishedReviewID(reviews []github.PRReview, ours map[int64]bool, ownLogin string, commitIDs ...string) (id int64, state string, found bool) {
	anchors := map[string]bool{}
	for _, c := range commitIDs {
		if c = strings.TrimSpace(c); c != "" {
			anchors[c] = true
		}
	}
	if len(anchors) == 0 {
		return 0, "", false
	}
	ownLogin = strings.TrimSpace(ownLogin)
	if ownLogin == "" {
		return 0, "", false
	}
	for _, rev := range reviews {
		if !anchors[rev.CommitID] || ours[rev.ID] {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(rev.User.Login), ownLogin) {
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
// Fails open on a nil fetcher, no anchor at all, no own login and any API
// error: the guard exists to stop a second review, never to withhold the
// first one because the lookup was rate-limited.
func PublishedPeerReview(f PublishedReviewFetcher, repo string, number int, ours map[int64]bool, ownLogin string, commitIDs ...string) (id int64, state string, found bool) {
	if f == nil {
		return 0, "", false
	}
	// Duplicates PeerPublishedReviewID's own no-anchor check below — the
	// duplication is deliberate, not drift: PeerPublishedReviewID's check
	// alone still lets f.GetPRReviews run first and its return value get
	// discarded, spending an API call this guard exists to avoid spending
	// when there is nothing to anchor on. Keep both in sync if the anchor
	// validation rule ever changes.
	hasAnchor := false
	for _, c := range commitIDs {
		if strings.TrimSpace(c) != "" {
			hasAnchor = true
			break
		}
	}
	if !hasAnchor || strings.TrimSpace(ownLogin) == "" {
		return 0, "", false
	}
	reviews, err := f.GetPRReviews(repo, number)
	if err != nil {
		slog.Warn("pipeline: could not list published reviews, cannot check for a peer instance's review",
			"repo", repo, "pr", number, "commits", commitIDs, "err", err)
		return 0, "", false
	}
	return PeerPublishedReviewID(reviews, ours, ownLogin, commitIDs...)
}

// ownPublishedReviewIDs is the set of GitHub review ids this daemon published
// for prID. Anything on the PR outside this set, carrying the Heimdallm
// footer and signed by this daemon's own login came from another instance
// running as the same account.
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
// "Another instance" is one publishing under this daemon's own login, read
// through Pipeline.ownLogin on every call (see PeerPublishedReviewID for the
// scope and SetBotLoginFunc for why it is not a startup copy). A colleague's
// daemon reviewing as a different account never triggers this skip.
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
//
// commitIDs takes every anchor the caller has for the review being retired
// (typically the stored row's HeadSHA plus the PR's live HEAD): see
// PeerPublishedReviewID for why a single anchor is not enough after GitHub
// retargets a review's commit_id (#772).
func (p *Pipeline) SkipIfPeerPublished(rev *store.Review, repo string, number int, commitIDs ...string) (bool, error) {
	// No anchor at all has nothing to key a claim on, and a nil store cannot
	// retire the row the skip would otherwise leave pending for the publish
	// worker to post anyway. Both short-circuit before the store read below.
	if rev == nil || p.store == nil {
		return false, nil
	}
	fetcher, ok := p.gh.(PublishedReviewFetcher)
	if !ok {
		return false, nil
	}
	peerID, peerState, found := PublishedPeerReview(fetcher, repo, number, p.ownPublishedReviewIDs(rev.PRID), p.ownLogin(), commitIDs...)
	if !found {
		return false, nil
	}

	slog.Warn("pipeline: another Heimdallm instance already published a review for this commit, not publishing a second one",
		"repo", repo, "pr", number, "commits", commitIDs,
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
