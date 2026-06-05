package pipeline

import (
	"regexp"
	"strings"

	"github.com/heimdallm/daemon/internal/github"
)

// CommentSignals represents extracted review-relevant signals from PR discussion.
// These signals complement the AI's severity assessment by detecting explicit
// blocking intent or unresolved concerns that the AI might overlook.
type CommentSignals struct {
	// HasBlockerKeywords indicates that UNRESOLVED reviewer comments contain
	// explicit blocking language ("blocker", "must fix", "do not merge", etc.).
	// Resolved concerns (followed by an author reply) clear this flag.
	HasBlockerKeywords bool
	// UnresolvedConcerns counts comments by non-author reviewers that appear
	// to raise issues without a subsequent acknowledgement by the PR author.
	UnresolvedConcerns int
	// Urgency is a computed 0-3 score summarizing signal strength:
	//   0 = no concerning signals
	//   1 = minor unresolved discussion (1-2 reviewer comments)
	//   2 = moderate unresolved discussion (3+ reviewer comments)
	//   3 = explicit blocking language in unresolved comments
	// Note: Urgency 1-2 currently only enriches prompt context (re-review
	// NOTE injection). Only Urgency >= 3 escalates the stored severity via
	// ApplySignalEscalation.
	Urgency int
}

// blockerPatterns are word-boundary-aware regexes that indicate a reviewer
// explicitly wants to block the PR from merging.
//
// Design:
//   - Each pattern uses \b (word boundary) to avoid substring false positives
//     (e.g. "non-blocking" won't match the "blocking" pattern).
//   - Negation prefixes ("not a", "non-", "no ") are excluded via negative
//     lookbehind approximation (checked separately in containsBlockerKeyword).
//
// Precondition: input to matching is already lowercased.
var blockerPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bblocker\b`),
	regexp.MustCompile(`\bmust fix\b`),
	regexp.MustCompile(`\bdo not merge\b`),
	regexp.MustCompile(`\bdon'?t merge\b`),
	regexp.MustCompile(`\bsecurity issue\b`),
	regexp.MustCompile(`\bcritical bug\b`),
	regexp.MustCompile(`\bblocking\b`),
	regexp.MustCompile(`\bnack\b`),
	regexp.MustCompile(`\bmust be fixed\b`),
	regexp.MustCompile(`\bcannot merge\b`),
	regexp.MustCompile(`\bshould not merge\b`),
	regexp.MustCompile(`\bneeds to be fixed before\b`),
}

// negationPrefixes are phrases that negate a blocker keyword when they
// immediately precede the match (e.g. "not a blocker", "non-blocking").
var negationPrefixes = []string{
	"not a ",
	"not ",
	"non-",
	"non ",
	"no ",
	"isn't a ",
	"isnt a ",
}

// containsBlockerKeyword checks whether a lowercased comment body contains
// a blocker keyword that is NOT negated by a preceding phrase.
func containsBlockerKeyword(body string) bool {
	for _, pat := range blockerPatterns {
		loc := pat.FindStringIndex(body)
		if loc == nil {
			continue
		}
		// Check if the match is preceded by a negation prefix.
		matchStart := loc[0]
		negated := false
		for _, neg := range negationPrefixes {
			prefixStart := matchStart - len(neg)
			if prefixStart >= 0 && body[prefixStart:matchStart] == neg {
				negated = true
				break
			}
		}
		if !negated {
			return true
		}
	}
	return false
}

// ExtractCommentSignals analyzes PR comments for signals that should
// influence the review decision beyond what the AI alone determines.
// Only comments from non-author reviewers are considered — the PR author's
// own comments are excluded since they represent responses, not concerns.
//
// Precondition: comments must be in chronological order. The acknowledgement
// heuristic relies on temporal ordering to determine which concerns have been
// addressed by the author.
//
// The function is designed to be conservative: it only elevates the decision,
// never lowers it. A PR with blocker keywords in UNRESOLVED reviewer comments
// will trigger Urgency 3, which can escalate a "medium" severity via
// ApplySignalEscalation.
func ExtractCommentSignals(comments []github.Comment, prAuthor string) CommentSignals {
	if len(comments) == 0 {
		return CommentSignals{}
	}

	// Track reviewer concerns and whether they contain blocker keywords.
	// Both are cleared when the PR author replies, ensuring that resolved
	// blockers don't persist in the final signal.
	type concern struct {
		hasBlocker bool
	}
	var unresolvedConcerns []concern

	for _, c := range comments {
		if strings.EqualFold(c.Author, prAuthor) {
			// Author comment: marks ALL prior reviewer concerns as acknowledged.
			// This is intentionally broad — any author reply signals engagement.
			unresolvedConcerns = nil
			continue
		}

		// Reviewer comment: check for blocker keywords with word-boundary
		// matching and negation handling.
		body := strings.ToLower(c.Body)
		hasBlocker := containsBlockerKeyword(body)

		unresolvedConcerns = append(unresolvedConcerns, concern{hasBlocker: hasBlocker})
	}

	// Build final signals from unresolved concerns only.
	var signals CommentSignals
	signals.UnresolvedConcerns = len(unresolvedConcerns)
	for _, c := range unresolvedConcerns {
		if c.hasBlocker {
			signals.HasBlockerKeywords = true
			break
		}
	}

	// Compute urgency score.
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
