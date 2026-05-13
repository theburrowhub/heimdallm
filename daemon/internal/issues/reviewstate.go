package issues

import (
	"strings"
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
//   - For each remaining reviewer, only their latest non-COMMENTED
//     review counts as their "decision" — matches the GitHub UI's
//     per-reviewer collapse rule.
//   - COMMENTED contributes only when a reviewer has never left a
//     decision (no APPROVED / CHANGES_REQUESTED entry).
//   - Across reviewers, CHANGES_REQUESTED dominates APPROVED dominates
//     COMMENTED dominates empty.
//
// The returned `reviewer` and `at` correspond to the latest review
// that drives the aggregate (so the SSE payload can name the reviewer
// who triggered the state).
func LatestExternalReviewState(reviews []github.PRReview, botLogin string) (state, reviewer string, at time.Time) {
	type decision struct {
		state string
		at    time.Time
		login string
	}
	perReviewer := map[string]decision{}

	botLower := strings.ToLower(botLogin)
	for _, r := range reviews {
		if botLower != "" && strings.EqualFold(r.User.Login, botLogin) {
			continue
		}
		key := strings.ToLower(r.User.Login)
		switch r.State {
		case "DISMISSED":
			// Drop any prior decision from this reviewer — DISMISSED
			// resets them to "no current opinion".
			delete(perReviewer, key)
		case ReviewStateApproved, ReviewStateChangesRequested:
			perReviewer[key] = decision{state: r.State, at: r.SubmittedAt, login: r.User.Login}
		case ReviewStateCommented:
			// Only set if the reviewer has no decision yet.
			if _, ok := perReviewer[key]; !ok {
				perReviewer[key] = decision{state: r.State, at: r.SubmittedAt, login: r.User.Login}
			}
		default:
			// PENDING and any future state — ignore.
		}
	}

	// Pick the dominant aggregate.
	var bestApproved, bestCR, bestComment *decision
	for _, d := range perReviewer {
		d := d
		switch d.state {
		case ReviewStateChangesRequested:
			if bestCR == nil || d.at.After(bestCR.at) {
				bestCR = &d
			}
		case ReviewStateApproved:
			if bestApproved == nil || d.at.After(bestApproved.at) {
				bestApproved = &d
			}
		case ReviewStateCommented:
			if bestComment == nil || d.at.After(bestComment.at) {
				bestComment = &d
			}
		}
	}
	switch {
	case bestCR != nil:
		return ReviewStateChangesRequested, bestCR.login, bestCR.at
	case bestApproved != nil:
		return ReviewStateApproved, bestApproved.login, bestApproved.at
	case bestComment != nil:
		return ReviewStateCommented, bestComment.login, bestComment.at
	}
	return "", "", time.Time{}
}
