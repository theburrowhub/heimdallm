package mergetrack

import (
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/store"
)

// Attention says who a tracked PR is waiting on. It is what turns a list of
// blockers into "what should I do next", and it is what the badge, the tray and
// the notifications count. Values are persisted and sent over the API.
type Attention string

const (
	// AttentionNone: nothing to report — terminal, excluded, or nothing known yet.
	AttentionNone Attention = "none"
	// AttentionAction: the operator has to do something (fix CI, answer a
	// review, resolve conflicts, update the branch).
	AttentionAction Attention = "action"
	// AttentionReady: every requirement is met and nothing will merge it on its
	// own. The PR is one click from done.
	AttentionReady Attention = "ready"
	// AttentionWaiting: the ball is in someone else's court (reviewers, CI still
	// running, GitHub computing, auto-merge armed).
	AttentionWaiting Attention = "waiting"
)

// actionReasons are the blockers only the operator can clear.
var actionReasons = map[Reason]struct{}{
	ReasonChecksFailing:        {},
	ReasonRequiredCheckMissing: {},
	ReasonChangesRequested:     {},
	ReasonUnresolvedThreads:    {},
	ReasonConflicts:            {},
	ReasonBehindBase:           {},
	ReasonDraft:                {},
	ReasonAttemptCap:           {},
}

// ComputeAttention classifies a decision. Pure.
//
// Conflicts and an out-of-date branch are the operator's job unless the
// matching automation is on, in which case Heimdallm is already handling them
// and the PR is merely waiting. A ready PR that Heimdallm is about to merge, or
// that GitHub will merge through an armed auto-merge, is waiting as well: only
// a ready PR nobody is going to merge needs the operator's click.
//
// An armed auto-merge is NOT a reason to wait on its own: GitHub merges only
// once every requirement is met, so an armed PR that is behind its base or has
// changes requested sits there forever — the classic "Auto-merge on" PR left
// in limbo. It waits only when nothing on the operator's side blocks it.
func ComputeAttention(d Decision, cfg config.MergeTrackingConfig, phase string) Attention {
	switch phase {
	case store.MergePhaseMerged, store.MergePhaseAbandoned:
		return AttentionNone
	case store.MergePhaseUpdating, store.MergePhaseResolving,
		store.MergePhaseMerging, store.MergePhaseUpdatePending:
		return AttentionWaiting
	}
	autoMergeArmed := phase == store.MergePhaseAutoMergeArmed
	switch d.Action {
	case ActionMarkMerged, ActionAbandon:
		return AttentionNone
	case ActionWait:
		return AttentionWaiting
	}

	if d.Ready {
		if cfg.Merge || cfg.EnableAutoMerge || autoMergeArmed {
			return AttentionWaiting
		}
		return AttentionReady
	}

	primary := d.PrimaryReason()
	// Decide prepends bookkeeping blocks (attempt caps, "merge = false") in
	// front of the real blocker. A spent attempt cap means the automation gave
	// up and the operator must step in; the others say nothing about the PR.
	for _, b := range d.Blocks {
		switch b.Reason {
		case ReasonCooldown, ReasonAutoMergeWaiting, ReasonMergeQueueConfigured:
			continue
		case ReasonDisabled:
			if primary == ReasonDisabled && len(d.Blocks) == 1 {
				return AttentionNone
			}
			continue
		}
		primary = b.Reason
		break
	}

	switch primary {
	case ReasonNone:
		if autoMergeArmed {
			return AttentionWaiting
		}
		return AttentionNone
	case ReasonExcluded:
		return AttentionNone
	case ReasonConflicts:
		if cfg.ResolveConflicts {
			return AttentionWaiting
		}
		return AttentionAction
	case ReasonBehindBase:
		if cfg.UpdateBranch {
			return AttentionWaiting
		}
		return AttentionAction
	}
	if _, ok := actionReasons[primary]; ok {
		return AttentionAction
	}
	if primary.IsTerminal() {
		// Cross-fork, missing permission, merge method disabled: automation
		// cannot proceed, a human has to decide.
		return AttentionAction
	}
	return AttentionWaiting
}
