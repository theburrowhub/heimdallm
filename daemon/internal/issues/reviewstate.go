package issues

import (
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

// External review state constants surfaced by LatestExternalReviewState
// and persisted on store.PR.ExternalReviewState. Mirrors GitHub's
// review state strings (uppercase) so the SSE payload and the store
// stay aligned with the wire protocol.
const (
	ReviewStateApproved          = "APPROVED"
	ReviewStateChangesRequested  = "CHANGES_REQUESTED"
	ReviewStateCommented         = "COMMENTED"
	// ReviewStateFixPushed marks a PR where the FixRunner has pushed
	// changes in response to a CHANGES_REQUESTED. The state stays in
	// this terminal slot until a reviewer submits a new review; this
	// is how the fix-loop arms cleanly without re-triggering on the
	// stale CR.
	ReviewStateFixPushed = "FIX_PUSHED"
)

// LatestExternalReviewState collapses a chronological reviews list
// into the single aggregate state Tier 3 will react to. Rules:
//
//   - Reviews authored by `botLogin` (case-insensitive) are filtered
//     out so the daemon never reacts to its own posted reviews.
//   - DISMISSED reviews drop the reviewer's previous decision; they
//     do not themselves count as a state.
//   - For each remaining reviewer, only their LATEST non-COMMENTED
//     review counts as their "decision" — matches the GitHub UI's
//     per-reviewer collapse rule. A reviewer who CR'd then later
//     APPROVED contributes APPROVED.
//   - COMMENTED contributes only when a reviewer has never left a
//     decision (no APPROVED / CHANGES_REQUESTED entry); within that
//     branch, the LATEST COMMENTED from the same reviewer wins so
//     the aggregate's timestamp advances and the Tier 3 fresh-
//     same-state gate can re-dispatch on a follow-up comment.
//   - Across reviewers, CHANGES_REQUESTED dominates APPROVED dominates
//     COMMENTED dominates empty.
//
// The returned `reviewer` and `at` correspond to the latest review
// that drives the aggregate (so the SSE payload can name the reviewer
// who triggered the state).
//
// The per-reviewer collapse is shared with the Responder + FixRunner
// trigger selectors via currentDecisionsByReviewer, so the trigger
// picker and the aggregator can never disagree about who the active
// reviewer is.
func LatestExternalReviewState(reviews []github.PRReview, botLogin string) (state, reviewer string, at time.Time) {
	dec := currentDecisionsByReviewer(reviews, botLogin)
	var bestApproved, bestCR, bestComment *github.PRReview
	for _, r := range dec {
		cur := r
		switch cur.State {
		case ReviewStateChangesRequested:
			if bestCR == nil || cur.SubmittedAt.After(bestCR.SubmittedAt) {
				bestCR = &cur
			}
		case ReviewStateApproved:
			if bestApproved == nil || cur.SubmittedAt.After(bestApproved.SubmittedAt) {
				bestApproved = &cur
			}
		case ReviewStateCommented:
			if bestComment == nil || cur.SubmittedAt.After(bestComment.SubmittedAt) {
				bestComment = &cur
			}
		}
	}
	switch {
	case bestCR != nil:
		return ReviewStateChangesRequested, bestCR.User.Login, bestCR.SubmittedAt
	case bestApproved != nil:
		return ReviewStateApproved, bestApproved.User.Login, bestApproved.SubmittedAt
	case bestComment != nil:
		return ReviewStateCommented, bestComment.User.Login, bestComment.SubmittedAt
	}
	return "", "", time.Time{}
}
