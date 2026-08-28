package mergetrack

import (
	"fmt"
	"sort"
	"strings"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

// unknownRecheck is how long to wait when GitHub has not finished computing
// mergeability. Short, because the answer usually lands within seconds.
const unknownRecheck = 45 * time.Second

// maxUnknownWaits bounds the mergeability polling so a PR GitHub can never
// resolve does not consume a query every cycle forever.
const maxUnknownWaits = 5

// terminalRetention is how long a merged or abandoned row stays in the table
// before it is pruned. Long enough that the Merge tab still shows what happened
// yesterday, short enough that the table does not grow without bound.
const terminalRetention = 7 * 24 * time.Hour

// stableBlockRecheck is the cooldown for blocks that will not change without a
// human doing something.
const stableBlockRecheck = time.Hour

// Evaluate turns a GitHub snapshot into an explainable readiness decision.
// Pure: no I/O, no clock beyond in.Now, no mutation of its arguments.
//
// The order of the checks below is the specification. Earlier rules win, so a
// draft is reported as a draft rather than as "checks pending", and a PR
// GitHub has already merged is never reported as anything else.
func Evaluate(st *gh.MergeStatus, in Input) Decision {
	d := Decision{
		Action:      ActionNone,
		HeadSHA:     st.HeadOID,
		MergeMethod: in.Cfg.MergeMethod,
		Evidence: Evidence{
			HeadSHA:              st.HeadOID,
			BaseRef:              st.BaseRef,
			MergeStateStatus:     st.MergeStateStatus,
			Mergeable:            st.Mergeable,
			ReviewDecision:       st.ReviewDecision,
			BehindBase:           st.MergeStateStatus == gh.MergeStateBehind || st.BehindBase(),
			InMergeQueue:         st.IsInMergeQueue,
			ProtectionUnreadable: st.ProtectionUnreadable,
		},
	}
	if st.AutoMerge != nil {
		d.Evidence.AutoMergeArmedAt = st.AutoMerge.EnabledAt
	}

	// The check breakdown is computed unconditionally, even for a draft or an
	// already-merged PR: the UI shows it either way, and a reader looking at a
	// draft still wants to know whether CI is green.
	d.Checks, d.ChecksSummary = summariseChecks(st)

	// 1. Terminal PR states. Nothing below can override "it is already merged".
	if st.Merged || st.State == "MERGED" {
		d.Action = ActionMarkMerged
		d.Blocks = []Block{{Reason: ReasonAlreadyMerged}}
		return d
	}
	if st.State == "CLOSED" {
		d.Action = ActionAbandon
		d.Blocks = []Block{{Reason: ReasonClosed}}
		return d
	}

	// 2. Scope. A PR that is neither ours nor assigned to us must not be
	// touched, whatever the config says.
	isAuthor, isAssignee := st.IsTrackedFor(in.ViewerLogin)
	if !isAuthor && !(isAssignee && in.Cfg.IncludeAssigned) {
		d.Action = ActionAbandon
		d.Blocks = []Block{{
			Reason: ReasonNotTracked,
			Detail: fmt.Sprintf("%s is neither the author nor a tracked assignee", in.ViewerLogin),
		}}
		return d
	}

	// 3. Operator opt-outs.
	if in.State.Excluded {
		d.Blocks = []Block{{Reason: ReasonExcluded}}
		return d
	}
	if !in.Cfg.Enabled {
		d.Blocks = []Block{{Reason: ReasonDisabled, Detail: "merge_tracking.enabled = false"}}
		return d
	}

	// 4. Permission. Without write access every action below would 403.
	switch st.ViewerPermission {
	case "ADMIN", "MAINTAIN", "WRITE":
	default:
		d.Blocks = []Block{{
			Reason: ReasonInsufficientPermission,
			Detail: fmt.Sprintf("viewer permission is %s", strings.ToLower(orUnknown(st.ViewerPermission))),
		}}
		return d
	}

	// 5. Drafts are tracked and displayed but never acted on.
	if st.IsDraft || st.MergeStateStatus == gh.MergeStateDraft {
		d.Blocks = []Block{{Reason: ReasonDraft}}
		d.CooldownHint = stableBlockRecheck
		return d
	}

	// 6. A head branch on someone else's fork is readable but not writable, so
	// no update, rebase or conflict resolution can land. Evaluate and show it;
	// never try to write.
	if st.HeadIsFork && !sameLogin(st.HeadRepoOwner, in.ViewerLogin) {
		d.Blocks = []Block{{
			Reason: ReasonCrossFork,
			Detail: fmt.Sprintf("head branch lives in %s", orUnknown(st.HeadRepo)),
		}}
		d.CooldownHint = stableBlockRecheck
		return d
	}

	// 7. Merge queue: GitHub owns the ordering. A direct merge would jump the
	// queue, so we stay out entirely.
	if st.IsInMergeQueue {
		d.Blocks = []Block{{
			Reason: ReasonInMergeQueue,
			Detail: strings.ToLower(orUnknown(st.MergeQueueEntryState)),
		}}
		return d
	}

	// 8. GitHub computes mergeable/mergeStateStatus lazily: the first read
	// returns UNKNOWN and kicks off the computation. Treating UNKNOWN as
	// anything but "ask again" is how an unmergeable PR gets merged.
	if st.Mergeable == gh.MergeableUnknown || st.MergeStateStatus == gh.MergeStateUnknown {
		d.Blocks = []Block{{Reason: ReasonMergeabilityUnknown}}
		if in.State.UnknownWaits >= maxUnknownWaits {
			d.CooldownHint = stableBlockRecheck
			d.Blocks[0].Detail = fmt.Sprintf("GitHub still reports an unknown merge state after %d checks", in.State.UnknownWaits)
			return d
		}
		d.Action = ActionWait
		d.CooldownHint = unknownRecheck
		return d
	}

	// 9. Pre-receive hooks still running: transient, same treatment as unknown.
	if st.MergeStateStatus == gh.MergeStateHasHooks {
		d.Action = ActionWait
		d.Blocks = []Block{{Reason: ReasonHooksPending}}
		d.CooldownHint = unknownRecheck
		return d
	}

	// 10. Conflicts and staleness are branch-level problems with their own
	// remedies, and both preclude a meaningful read of the gating signals.
	if st.Mergeable == gh.MergeableNo || st.MergeStateStatus == gh.MergeStateDirty {
		d.Blocks = []Block{{Reason: ReasonConflicts, Detail: "the head branch conflicts with " + orUnknown(st.BaseRef)}}
		return d
	}
	// When branch updates are enabled, out of date comes before every gating
	// signal below: checks that pass against a stale base prove nothing, and
	// bringing the branch up to date first is the move that unblocks the rest.
	//
	// The condition cannot be mergeStateStatus alone. GitHub collapses its whole
	// verdict into one value and ranks BLOCKED above BEHIND, so a PR that is
	// both behind and waiting on a review or a check never reports BEHIND —
	// verified against two open PRs in this repository, hundreds of commits
	// behind, both reporting DIRTY. BehindBase() compares the commit the PR is
	// based on with the current tip of the base branch, which is exactly the
	// question, and costs no extra request. That comparison is only a blocking
	// condition when the operator opted into update_branch: non-strict repos can
	// legitimately merge a CLEAN PR after the base tip advances. GitHub's
	// explicit BEHIND verdict remains a block because it means the repository
	// itself requires the branch to be brought up to date.
	if st.MergeStateStatus == gh.MergeStateBehind || (in.Cfg.UpdateBranch && d.Evidence.BehindBase) {
		d.Blocks = []Block{{Reason: ReasonBehindBase, Detail: "the head branch is behind " + orUnknown(st.BaseRef)}}
		return d
	}

	// 11. From here on we accumulate every blocker rather than returning on the
	// first, because the UI shows them all and a reader fixing one wants to
	// know what else is waiting.
	blocks := make([]Block, 0, 4)

	// Truncated data is not absence of data. Reporting ready on a partial read
	// is the one failure mode this whole package exists to prevent.
	if st.ChecksTruncated {
		blocks = append(blocks, Block{
			Reason: ReasonChecksUnknown,
			Detail: "GitHub reported more checks than could be read in one pass",
		})
	}
	if st.ThreadsTruncated {
		blocks = append(blocks, Block{
			Reason: ReasonThreadsUnknown,
			Detail: "GitHub reported more review threads than could be read in one pass",
		})
	}

	blocks = append(blocks, evaluateReviews(st, in, &d.Evidence)...)
	blocks = append(blocks, evaluateThreads(st, &d.Evidence)...)
	blocks = append(blocks, evaluateChecks(d.Checks, d.ChecksSummary)...)

	// 12. The repo has to allow the method we would use, or every merge attempt
	// is a guaranteed 422.
	if !st.AllowedMergeMethods.Allows(in.Cfg.MergeMethod) {
		blocks = append(blocks, Block{
			Reason: ReasonMergeMethodNotAllowed,
			Detail: fmt.Sprintf("%s merges are disabled for %s", in.Cfg.MergeMethod, st.Repo),
		})
	}

	// 13. GitHub says blocked and nothing above explains it — usually a rule we
	// cannot read, such as CODEOWNERS or a required deployment. Report it
	// honestly rather than inventing a cause.
	if st.MergeStateStatus == gh.MergeStateBlocked && len(blocks) == 0 {
		detail := "GitHub reports the merge as blocked by branch protection"
		if st.ProtectionUnreadable {
			detail += " (the rule itself is not readable with this token)"
		}
		blocks = append(blocks, Block{Reason: ReasonBlockedByProtection, Detail: detail})
	}

	d.Blocks = orderBlocks(blocks)
	d.Ready = len(d.Blocks) == 0
	if !d.Ready && d.CooldownHint == 0 {
		d.CooldownHint = cooldownFor(d.PrimaryReason())
	}
	return d
}

// evaluateReviews applies the review rules, which are deliberately stricter
// than GitHub's own in one respect: a standing CHANGES_REQUESTED blocks the
// merge even when GitHub would let it through because the reviewer is not a
// required one. See theburrowhub/heimdallm#674.
//
// Approvals and change requests are treated asymmetrically with respect to the
// head SHA, and that asymmetry is the point:
//   - An APPROVED anchored to an older commit does NOT count. A push after the
//     approval means nobody approved what would actually be merged.
//   - A CHANGES_REQUESTED anchored to an older commit DOES still count. GitHub
//     keeps it active until the reviewer resolves it, and so do we.
//
// Both choices fail in the same direction: towards not merging.
func evaluateReviews(st *gh.MergeStatus, in Input, ev *Evidence) []Block {
	var blocks []Block
	var changesRequestedBy []string
	approvalsAtHead := 0
	staleApprovals := 0

	for _, r := range st.Reviews {
		// Our own review never gates our own PR.
		if sameLogin(r.Login, in.ViewerLogin) {
			continue
		}
		// A reviewer without push access cannot satisfy branch protection, so
		// their approval must not be counted as one.
		switch r.State {
		case "APPROVED":
			if !r.CanPush {
				continue
			}
			if r.CommitOID != "" && st.HeadOID != "" && r.CommitOID != st.HeadOID {
				staleApprovals++
				continue
			}
			approvalsAtHead++
		case "CHANGES_REQUESTED":
			changesRequestedBy = append(changesRequestedBy, r.Login)
		}
	}

	ev.ApprovalsAtHead = approvalsAtHead
	ev.StaleApprovals = staleApprovals
	ev.ChangesRequestedBy = changesRequestedBy
	ev.PendingReviewers = st.ReviewRequests

	requiredApprovals := 0
	if st.Protection != nil && st.Protection.RequiresApprovingReviews {
		requiredApprovals = st.Protection.RequiredApprovingReviewCount
	}
	ev.RequiredApprovals = requiredApprovals

	if len(changesRequestedBy) > 0 {
		blocks = append(blocks, Block{
			Reason: ReasonChangesRequested,
			Detail: fmt.Sprintf("%s requested changes", joinNames(changesRequestedBy)),
		})
	}

	switch st.ReviewDecision {
	case gh.ReviewDecisionChangesRequested:
		if len(changesRequestedBy) == 0 {
			blocks = append(blocks, Block{
				Reason: ReasonChangesRequested,
				Detail: "GitHub reports changes requested",
			})
		}
	case gh.ReviewDecisionReviewRequired:
		detail := "an approving review is required"
		if requiredApprovals > 0 {
			detail = fmt.Sprintf("%d approving %s required, %d at the current commit",
				requiredApprovals, pluralise(requiredApprovals, "review is", "reviews are"), approvalsAtHead)
		}
		blocks = append(blocks, Block{Reason: ReasonReviewRequired, Detail: detail})
	}

	// Our own stricter gate: require a live approval even where the repository
	// does not. Opt-in, because plenty of personal repos legitimately have no
	// review requirement at all.
	if in.Cfg.RequireApproval && approvalsAtHead == 0 && st.ReviewDecision != gh.ReviewDecisionReviewRequired {
		detail := "merge_tracking.require_approval is on and no approval covers the current commit"
		if staleApprovals > 0 {
			detail = fmt.Sprintf("%d %s were invalidated by a later push",
				staleApprovals, pluralise(staleApprovals, "approval", "approvals"))
		}
		blocks = append(blocks, Block{Reason: ReasonInsufficientApprovals, Detail: detail})
	}

	// Branch protection asks for more approvals than we can see.
	if requiredApprovals > 0 && approvalsAtHead < requiredApprovals &&
		st.ReviewDecision != gh.ReviewDecisionReviewRequired {
		blocks = append(blocks, Block{
			Reason: ReasonInsufficientApprovals,
			Detail: fmt.Sprintf("%d of %d required approvals present at the current commit",
				approvalsAtHead, requiredApprovals),
		})
	}

	if len(st.ReviewRequests) > 0 {
		blocks = append(blocks, Block{
			Reason: ReasonPendingReviewers,
			Detail: fmt.Sprintf("waiting on %s", joinNames(st.ReviewRequests)),
		})
	}

	return blocks
}

// evaluateThreads blocks on any unresolved, non-outdated review thread.
//
// This is stricter than GitHub, which only enforces it when the branch has
// requiresConversationResolution set. #674 asks for the strict behaviour
// explicitly: an open conversation on a PR about to be merged automatically is
// a question nobody answered.
//
// Outdated threads are excluded: they hang off lines that no longer exist, and
// GitHub itself stops counting them.
func evaluateThreads(st *gh.MergeStatus, ev *Evidence) []Block {
	unresolved := 0
	for _, t := range st.ReviewThreads {
		if !t.IsResolved && !t.IsOutdated {
			unresolved++
		}
	}
	ev.UnresolvedThreads = unresolved
	if unresolved == 0 {
		return nil
	}
	return []Block{{
		Reason: ReasonUnresolvedThreads,
		Detail: fmt.Sprintf("%d unresolved review %s", unresolved, pluralise(unresolved, "thread", "threads")),
	}}
}

// evaluateChecks turns the check summary into blocks. Only required checks
// gate: an optional red check is reported in the breakdown and counted in the
// summary, but blocking on it would stop PRs that GitHub would happily merge.
func evaluateChecks(checks []gh.CheckContext, s ChecksSummary) []Block {
	var blocks []Block
	if len(s.MissingRequired) > 0 {
		blocks = append(blocks, Block{
			Reason: ReasonRequiredCheckMissing,
			Detail: fmt.Sprintf("%s %s not reported",
				pluralise(len(s.MissingRequired), "required check", "required checks"),
				joinNames(s.MissingRequired)),
		})
	}
	if s.RequiredFailing > 0 {
		blocks = append(blocks, Block{
			Reason: ReasonChecksFailing,
			Detail: fmt.Sprintf("%d required %s failing: %s",
				s.RequiredFailing,
				pluralise(s.RequiredFailing, "check is", "checks are"),
				joinNames(requiredCheckNames(checks, gh.CheckStateFailure))),
		})
	}
	if s.RequiredPending > 0 {
		blocks = append(blocks, Block{
			Reason: ReasonChecksPending,
			Detail: fmt.Sprintf("%d required %s still running: %s",
				s.RequiredPending,
				pluralise(s.RequiredPending, "check is", "checks are"),
				joinNames(requiredCheckNames(checks, gh.CheckStatePending))),
		})
	}
	return blocks
}

// blockPriority orders blocks so the first one is the most useful thing to
// tell a human. Failing checks and requested changes outrank "still waiting".
var blockPriority = map[Reason]int{
	ReasonChecksFailing:         0,
	ReasonRequiredCheckMissing:  1,
	ReasonChangesRequested:      2,
	ReasonUnresolvedThreads:     3,
	ReasonMergeMethodNotAllowed: 4,
	ReasonInsufficientApprovals: 5,
	ReasonReviewRequired:        6,
	ReasonPendingReviewers:      7,
	ReasonChecksPending:         8,
	ReasonBlockedByProtection:   9,
	ReasonChecksUnknown:         10,
	ReasonThreadsUnknown:        11,
}

func orderBlocks(blocks []Block) []Block {
	if len(blocks) < 2 {
		return blocks
	}
	sort.SliceStable(blocks, func(i, j int) bool {
		pi, oki := blockPriority[blocks[i].Reason]
		pj, okj := blockPriority[blocks[j].Reason]
		if !oki {
			pi = 100
		}
		if !okj {
			pj = 100
		}
		return pi < pj
	})
	return blocks
}

func cooldownFor(r Reason) time.Duration {
	switch r {
	case ReasonChecksPending, ReasonMergeabilityUnknown, ReasonHooksPending:
		return unknownRecheck
	case ReasonDraft, ReasonCrossFork, ReasonInsufficientPermission,
		ReasonMergeMethodNotAllowed, ReasonAutoMergeUnavailable:
		return stableBlockRecheck
	default:
		return 0 // caller's default poll cadence
	}
}

func sameLogin(a, b string) bool {
	a = strings.TrimSpace(strings.TrimLeft(a, "@"))
	b = strings.TrimSpace(strings.TrimLeft(b, "@"))
	return a != "" && b != "" && strings.EqualFold(a, b)
}

func orUnknown(s string) string {
	if strings.TrimSpace(s) == "" {
		return "unknown"
	}
	return s
}
