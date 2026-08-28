// Package mergetrack decides whether a pull request the authenticated user
// owns can be merged, and which automation step (if any) should run next.
//
// Everything in Evaluate and Decide is pure: they take a GitHub snapshot, the
// resolved config and the persisted state, and return an explainable decision.
// No I/O happens here, which is what makes the merge rules exhaustively
// testable — and merge rules are exactly the kind of thing that must be.
package mergetrack

// Reason identifies why a PR is not being merged, or why an action was not
// taken. Values are persisted, sent over SSE and rendered in the UI, so they
// are stable identifiers rather than prose.
type Reason string

const (
	ReasonNone Reason = ""

	// Configuration and scope.
	ReasonDisabled   Reason = "disabled"
	ReasonExcluded   Reason = "excluded"
	ReasonNotTracked Reason = "not_tracked"

	// Terminal PR states.
	ReasonAlreadyMerged Reason = "already_merged"
	ReasonClosed        Reason = "closed"

	// States we deliberately never act on.
	ReasonDraft     Reason = "draft"
	ReasonCrossFork Reason = "cross_fork"

	// GitHub has not finished computing, or would not tell us.
	ReasonMergeabilityUnknown Reason = "mergeability_unknown"
	ReasonHooksPending        Reason = "hooks_pending"
	ReasonChecksUnknown       Reason = "checks_unknown"
	ReasonThreadsUnknown      Reason = "threads_unknown"

	// Branch state.
	ReasonConflicts  Reason = "conflicts"
	ReasonBehindBase Reason = "behind_base"

	// Reviews.
	ReasonChangesRequested      Reason = "changes_requested"
	ReasonReviewRequired        Reason = "review_required"
	ReasonInsufficientApprovals Reason = "insufficient_approvals"
	ReasonPendingReviewers      Reason = "pending_reviewers"
	ReasonUnresolvedThreads     Reason = "unresolved_threads"

	// Checks.
	ReasonChecksPending        Reason = "checks_pending"
	ReasonChecksFailing        Reason = "checks_failing"
	ReasonRequiredCheckMissing Reason = "required_check_missing"

	// Branch protection and merge queue.
	ReasonBlockedByProtection  Reason = "blocked_by_protection"
	ReasonInMergeQueue         Reason = "in_merge_queue"
	ReasonMergeQueueConfigured Reason = "merge_queue_configured"

	// Permissions and repository settings.
	ReasonInsufficientPermission Reason = "insufficient_permission"
	ReasonMergeMethodNotAllowed  Reason = "merge_method_not_allowed"
	ReasonAutoMergeUnavailable   Reason = "automerge_unavailable"

	// Our own guards.
	ReasonHeadSHAMoved     Reason = "head_sha_moved"
	ReasonCooldown         Reason = "cooldown"
	ReasonAttemptCap       Reason = "attempt_cap_reached"
	ReasonAutoMergeWaiting Reason = "automerge_waiting"
)

// checkRelated is the set of reasons whose cause is CI, and which therefore
// drive the prominent check warnings in the listing and the per-check
// breakdown in the PR detail view.
var checkRelated = map[Reason]struct{}{
	ReasonChecksFailing:        {},
	ReasonChecksPending:        {},
	ReasonRequiredCheckMissing: {},
	ReasonChecksUnknown:        {},
}

// IsCheckRelated reports whether this reason is about CI checks. The UI uses it
// to decide when to show the check warning banner and the check table.
func (r Reason) IsCheckRelated() bool {
	_, ok := checkRelated[r]
	return ok
}

// terminalReasons will not resolve themselves: retrying costs API budget and
// changes nothing until a human intervenes.
var terminalReasons = map[Reason]struct{}{
	ReasonAlreadyMerged:          {},
	ReasonClosed:                 {},
	ReasonNotTracked:             {},
	ReasonCrossFork:              {},
	ReasonInsufficientPermission: {},
	ReasonMergeMethodNotAllowed:  {},
}

// IsTerminal reports whether the reason means "stop tracking for automation".
func (r Reason) IsTerminal() bool {
	_, ok := terminalReasons[r]
	return ok
}

// String makes Reason printable in logs and SSE payloads.
func (r Reason) String() string { return string(r) }
