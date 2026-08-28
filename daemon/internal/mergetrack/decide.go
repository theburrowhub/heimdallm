package mergetrack

import (
	"fmt"
	"strings"

	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// Decide chooses the next action from a readiness decision, the resolved config
// and the persisted state. Pure, like Evaluate.
//
// The returned Decision is the one passed in, with Action and possibly an extra
// leading block set — Decide never re-derives readiness.
//
// The two-phase merge is implemented here. When enable_auto_merge is on we arm
// GitHub's native auto-merge first and record that we did; only on a LATER
// reconciliation pass, if GitHub still has not merged and everything is still
// green, do we merge directly. Two guards make that safe: the arming is
// compared against TickStart so it can never be promoted within the same pass,
// and the caller disables auto-merge immediately before its direct merge so
// GitHub cannot fire concurrently.
func Decide(d Decision, st *gh.MergeStatus, in Input) Decision {
	// Terminal and waiting verdicts from Evaluate are already final.
	switch d.Action {
	case ActionMarkMerged, ActionAbandon, ActionWait:
		return d
	}

	primary := d.PrimaryReason()

	// A reason that cannot change on its own gets no action, whatever the
	// toggles say. Retrying a draft or a cross-fork PR is pure waste.
	if primary.IsTerminal() || primary == ReasonDraft ||
		primary == ReasonExcluded || primary == ReasonDisabled ||
		primary == ReasonInMergeQueue {
		d.Action = ActionNone
		return d
	}

	// Respect the cooldown even when the caller did not filter on it (the
	// manual "evaluate now" endpoint deliberately does not).
	if !in.State.CooldownUntil.IsZero() && in.State.CooldownUntil.After(in.Now) {
		d.Action = ActionNone
		d.Blocks = prependBlock(d.Blocks, Block{
			Reason: ReasonCooldown,
			Detail: fmt.Sprintf("next attempt after %s", in.State.CooldownUntil.UTC().Format("15:04:05Z")),
		})
		return d
	}

	// Conflicts: hand to the agent when allowed, otherwise just report.
	if primary == ReasonConflicts {
		switch {
		case !in.Cfg.ResolveConflicts:
			d.Action = ActionNone
		case in.State.ConflictAttempts >= in.Cfg.MaxResolveAttempts:
			d.Action = ActionNone
			d.Blocks = prependBlock(d.Blocks, Block{
				Reason: ReasonAttemptCap,
				Detail: fmt.Sprintf("%d of %d conflict-resolution attempts used for this commit",
					in.State.ConflictAttempts, in.Cfg.MaxResolveAttempts),
			})
		default:
			d.Action = ActionResolveConflicts
		}
		return d
	}

	// Behind the base: update the branch when allowed.
	if primary == ReasonBehindBase {
		switch {
		case !in.Cfg.UpdateBranch:
			d.Action = ActionNone
		case in.State.UpdateAttempts >= in.Cfg.MaxUpdateAttempts:
			d.Action = ActionNone
			d.Blocks = prependBlock(d.Blocks, Block{
				Reason: ReasonAttemptCap,
				Detail: fmt.Sprintf("%d of %d branch-update attempts used for this commit",
					in.State.UpdateAttempts, in.Cfg.MaxUpdateAttempts),
			})
		default:
			d.Action = ActionUpdateBranchRemote
		}
		return d
	}

	// Two independent facts, and they can disagree:
	//
	//   githubArmed — GitHub currently has native auto-merge enabled. This is
	//     the authority on whether it is on at all: someone can turn it off in
	//     the web UI, and GitHub keeps it on across pushes.
	//   rowArmed    — we recorded arming it for THIS head SHA. This is what
	//     licences the second phase, the direct merge.
	//
	// When GitHub is armed but our record does not cover the current commit —
	// after a push, typically — we deliberately do nothing this pass. The
	// reconciler re-anchors the row from the snapshot, and the next pass can
	// promote. One extra cycle in a rare case, in exchange for never merging a
	// commit on the strength of a licence granted for a different one.
	githubArmed := st.AutoMerge != nil
	rowArmed := in.State.AutoMergeArmedFor(st.HeadOID)
	armed := githubArmed && rowArmed

	// Not ready yet: arming native auto-merge is exactly the right move, since
	// GitHub will merge on its own the moment the last requirement goes green.
	if !d.Ready {
		if canArmAutoMerge(d, st, in, armed) {
			d.Action = ActionArmAutoMerge
			return d
		}
		d.Action = ActionNone
		return d
	}

	// Ready. A configured merge queue means GitHub owns the merge; a direct
	// PUT would jump the queue, so auto-merge is the only permitted path.
	if st.MergeQueueEnabled {
		if canArmAutoMerge(d, st, in, armed) {
			d.Action = ActionArmAutoMerge
			return d
		}
		d.Action = ActionNone
		d.Blocks = prependBlock(d.Blocks, Block{
			Reason: ReasonMergeQueueConfigured,
			Detail: "the base branch uses a merge queue, so Heimdallm never merges directly",
		})
		return d
	}

	if !in.Cfg.Merge {
		// Still worth arming: the operator asked for auto-merge but not for a
		// direct merge, which is a coherent, more conservative setup.
		if canArmAutoMerge(d, st, in, armed) {
			d.Action = ActionArmAutoMerge
			return d
		}
		d.Action = ActionNone
		d.Blocks = prependBlock(d.Blocks, Block{
			Reason: ReasonDisabled,
			Detail: "merge_tracking.merge = false",
		})
		return d
	}

	if in.State.MergeAttempts >= in.Cfg.MaxMergeAttempts {
		d.Action = ActionNone
		d.Blocks = prependBlock(d.Blocks, Block{
			Reason: ReasonAttemptCap,
			Detail: fmt.Sprintf("%d of %d merge attempts used for this commit",
				in.State.MergeAttempts, in.Cfg.MaxMergeAttempts),
		})
		return d
	}

	if in.Cfg.EnableAutoMerge {
		if !armed {
			if canArmAutoMerge(d, st, in, armed) {
				d.Action = ActionArmAutoMerge
				return d
			}
			if githubArmed {
				// Armed on GitHub, but not for the commit we evaluated. Wait a
				// pass while the row is re-anchored.
				d.Action = ActionNone
				d.Blocks = prependBlock(d.Blocks, Block{
					Reason: ReasonAutoMergeWaiting,
					Detail: "auto-merge is enabled on GitHub for a different commit; re-checking next cycle",
				})
				return d
			}
			// Auto-merge is wanted but unavailable for this repo — no node id,
			// or the method is disabled. Falling back to a direct merge is
			// correct: the PR is ready and the operator asked for it to merge.
			d.Action = ActionMerge
			return d
		}
		// Armed for this commit. Give GitHub its turn: promote to a direct
		// merge only on a pass that started after the arming.
		if in.State.AutoMergeArmedAt.IsZero() || !in.State.AutoMergeArmedAt.Before(in.TickStart) {
			d.Action = ActionNone
			d.Blocks = prependBlock(d.Blocks, Block{
				Reason: ReasonAutoMergeWaiting,
				Detail: "auto-merge was just armed; waiting for GitHub to merge before trying directly",
			})
			return d
		}
		d.Action = ActionMerge
		return d
	}

	d.Action = ActionMerge
	return d
}

// canArmAutoMerge reports whether arming GitHub's native auto-merge is possible
// and useful right now.
func canArmAutoMerge(d Decision, st *gh.MergeStatus, in Input, armed bool) bool {
	if !in.Cfg.EnableAutoMerge || armed {
		return false
	}
	if st.AutoMerge != nil {
		// Already on, just not recorded by us — the caller reconciles the row
		// rather than issuing a redundant mutation.
		return false
	}
	if st.NodeID == "" {
		return false
	}
	if !st.AllowedMergeMethods.Allows(in.Cfg.MergeMethod) {
		return false
	}
	// Arming a PR whose blocker will never clear on its own only creates a
	// standing instruction to merge something nobody is going to fix.
	switch d.PrimaryReason() {
	case ReasonConflicts, ReasonDraft, ReasonCrossFork, ReasonChecksUnknown, ReasonThreadsUnknown:
		return false
	}
	return true
}

// PhaseFor maps an action to the persisted phase it claims while running.
func PhaseFor(a Action) string {
	switch a {
	case ActionUpdateBranchRemote, ActionUpdateBranchLocal:
		return store.MergePhaseUpdating
	case ActionResolveConflicts:
		return store.MergePhaseResolving
	case ActionMerge:
		return store.MergePhaseMerging
	case ActionArmAutoMerge:
		return store.MergePhaseAutoMergeArmed
	default:
		return store.MergePhaseIdle
	}
}

// RestPhaseFor maps a non-mutating decision to the phase the row settles in.
func RestPhaseFor(d Decision) string {
	switch d.Action {
	case ActionMarkMerged:
		return store.MergePhaseMerged
	case ActionAbandon:
		return store.MergePhaseAbandoned
	}
	if d.Ready && len(d.Blocks) == 0 {
		return store.MergePhaseIdle
	}
	if len(d.Blocks) == 0 {
		return store.MergePhaseIdle
	}
	return store.MergePhaseBlocked
}

// AttemptKindFor maps an action to the counter it increments on failure.
func AttemptKindFor(a Action) string {
	switch a {
	case ActionUpdateBranchRemote, ActionUpdateBranchLocal:
		return store.MergeAttemptUpdate
	case ActionResolveConflicts:
		return store.MergeAttemptConflict
	case ActionMerge:
		return store.MergeAttemptMerge
	default:
		return ""
	}
}

func prependBlock(blocks []Block, b Block) []Block {
	out := make([]Block, 0, len(blocks)+1)
	out = append(out, b)
	return append(out, blocks...)
}

// MergeMethodForGitHub renders the config-level method for GitHub's APIs. REST
// wants lower case, the GraphQL enum wants upper case; the mutation helper
// upper-cases it, so this just normalises.
func MergeMethodForGitHub(method string) string {
	return strings.ToLower(strings.TrimSpace(method))
}
