package pipeline

import (
	"strings"

	"github.com/heimdallm/daemon/internal/github"
)

// CommentSignals represents extracted review-relevant signals from PR discussion.
// These signals complement the AI's severity assessment by detecting explicit
// blocking intent or unresolved concerns that the AI might overlook.
type CommentSignals struct {
	// HasBlockerKeywords indicates comments contain explicit blocking language
	// ("blocker", "must fix", "do not merge", etc.).
	HasBlockerKeywords bool
	// UnresolvedConcerns counts comments by non-author reviewers that appear
	// to raise issues without a subsequent acknowledgement by the PR author.
	UnresolvedConcerns int
	// Urgency is a computed 0-3 score summarizing signal strength:
	//   0 = no concerning signals
	//   1 = minor unresolved discussion (1-2 reviewer comments)
	//   2 = moderate unresolved discussion (3+ reviewer comments)
	//   3 = explicit blocking language detected
	Urgency int
}

// blockerKeywords are phrases that indicate a reviewer explicitly wants to
// block the PR from merging. These are intentionally conservative to avoid
// false positives from casual language.
var blockerKeywords = []string{
	"blocker",
	"must fix",
	"do not merge",
	"don't merge",
	"security issue",
	"critical bug",
	"blocking",
	"nack",
	"must be fixed",
	"cannot merge",
	"should not merge",
	"needs to be fixed before",
}

// ExtractCommentSignals analyzes PR comments for signals that should
// influence the review decision beyond what the AI alone determines.
// Only comments from non-author reviewers are considered — the PR author's
// own comments are excluded since they represent responses, not concerns.
//
// The function is designed to be conservative: it only elevates the decision,
// never lowers it. A PR with blocker keywords in reviewer comments will
// trigger Urgency 3, which can escalate a "medium" severity to REQUEST_CHANGES.
func ExtractCommentSignals(comments []github.Comment, prAuthor string) CommentSignals {
	if len(comments) == 0 {
		return CommentSignals{}
	}

	var signals CommentSignals

	// Track which reviewer concerns have been acknowledged by the author.
	// A reviewer comment followed by an author reply (later in time) is
	// considered acknowledged.
	type concern struct {
		index  int
		author string
	}
	var reviewerConcerns []concern

	for i, c := range comments {
		if strings.EqualFold(c.Author, prAuthor) {
			// Author comment: marks prior reviewer concerns as acknowledged
			// (simple heuristic — any author reply after a concern counts).
			reviewerConcerns = nil
			continue
		}

		// Reviewer comment: check for blocker keywords
		body := strings.ToLower(c.Body)
		for _, kw := range blockerKeywords {
			if strings.Contains(body, kw) {
				signals.HasBlockerKeywords = true
				break
			}
		}

		reviewerConcerns = append(reviewerConcerns, concern{index: i, author: c.Author})
	}

	// Unresolved concerns are reviewer comments that were never followed by
	// an author acknowledgement (i.e., remain at the end of the thread).
	signals.UnresolvedConcerns = len(reviewerConcerns)

	// Compute urgency score
	switch {
	case signals.HasBlockerKeywords:
		signals.Urgency = 3
	case signals.UnresolvedConcerns >= 3:
		signals.Urgency = 2
	case signals.UnresolvedConcerns >= 1:
		signals.Urgency = 1
	default:
		signals.Urgency = 0
	}

	return signals
}
