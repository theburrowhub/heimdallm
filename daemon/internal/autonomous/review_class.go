package autonomous

import "strings"

// ReviewInput is the minimal projection of a PR review the classifier needs.
type ReviewInput struct {
	State              string // APPROVED | CHANGES_REQUESTED | COMMENTED
	Body               string
	UnresolvedComments int // count of unresolved inline review comment threads
}

// ReviewDecision is what the autonomous review-loop should do next.
type ReviewDecision int

const (
	DecisionWait      ReviewDecision = iota // no human review yet — keep watching
	DecisionFix                             // run FixRunner
	DecisionMergeGate                       // approved clean — hand to merge gate
)

func (d ReviewDecision) String() string {
	switch d {
	case DecisionFix:
		return "fix"
	case DecisionMergeGate:
		return "merge_gate"
	default:
		return "wait"
	}
}

// actionableHints are phrases that indicate an "approved" review still asks
// for changes ("approve with issues"). Conservative: matching any one routes
// to a fix rather than merge.
var actionableHints = []string{
	"please ", "before merge", "should change", "needs ", "fix ", "rename ",
	"remove ", "address ", "todo", "must ",
}

// ClassifyReview reduces the review list to a single decision. The latest
// review dominates; an APPROVED with unresolved inline comments or an
// actionable body is treated as "approved with issues" and routed to a fix
// instead of the merge gate.
func ClassifyReview(reviews []ReviewInput) ReviewDecision {
	if len(reviews) == 0 {
		return DecisionWait
	}
	latest := reviews[len(reviews)-1]
	switch strings.ToUpper(latest.State) {
	case "CHANGES_REQUESTED", "COMMENTED":
		return DecisionFix
	case "APPROVED":
		if latest.UnresolvedComments > 0 || hasActionableBody(latest.Body) {
			return DecisionFix
		}
		return DecisionMergeGate
	default:
		return DecisionWait
	}
}

func hasActionableBody(body string) bool {
	b := strings.ToLower(body)
	for _, h := range actionableHints {
		if strings.Contains(b, h) {
			return true
		}
	}
	return false
}
