package mergetrack_test

import (
	"testing"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/store"
)

func blocked(reasons ...mergetrack.Reason) mergetrack.Decision {
	d := mergetrack.Decision{Action: mergetrack.ActionNone}
	for _, r := range reasons {
		d.Blocks = append(d.Blocks, mergetrack.Block{Reason: r})
	}
	return d
}

func TestComputeAttention(t *testing.T) {
	watch := config.MergeTrackingConfig{Enabled: true, WatchOnly: true}
	auto := config.MergeTrackingConfig{Enabled: true, UpdateBranch: true, ResolveConflicts: true, Merge: true}

	cases := []struct {
		name  string
		d     mergetrack.Decision
		cfg   config.MergeTrackingConfig
		phase string
		want  mergetrack.Attention
	}{
		{"merged is none", blocked(mergetrack.ReasonAlreadyMerged), watch, store.MergePhaseMerged, mergetrack.AttentionNone},
		{"abandoned is none", blocked(mergetrack.ReasonClosed), watch, store.MergePhaseAbandoned, mergetrack.AttentionNone},
		{"mark merged action is none", mergetrack.Decision{Action: mergetrack.ActionMarkMerged}, watch, store.MergePhaseIdle, mergetrack.AttentionNone},
		{"github still computing waits", mergetrack.Decision{Action: mergetrack.ActionWait}, watch, store.MergePhaseIdle, mergetrack.AttentionWaiting},
		{"auto-merge armed waits on CI", blocked(mergetrack.ReasonChecksPending), auto, store.MergePhaseAutoMergeArmed, mergetrack.AttentionWaiting},
		{"auto-merge armed and ready waits for GitHub", mergetrack.Decision{Ready: true}, watch, store.MergePhaseAutoMergeArmed, mergetrack.AttentionWaiting},
		{"auto-merge armed but behind base needs action", blocked(mergetrack.ReasonBehindBase), watch, store.MergePhaseAutoMergeArmed, mergetrack.AttentionAction},
		{"auto-merge armed with changes requested needs action", blocked(mergetrack.ReasonChangesRequested), watch, store.MergePhaseAutoMergeArmed, mergetrack.AttentionAction},
		{"auto-merge armed with nothing known waits", mergetrack.Decision{Action: mergetrack.ActionNone}, watch, store.MergePhaseAutoMergeArmed, mergetrack.AttentionWaiting},
		{"in-flight update waits", blocked(), auto, store.MergePhaseUpdating, mergetrack.AttentionWaiting},

		{"ready with nobody merging is ready", mergetrack.Decision{Ready: true}, watch, store.MergePhaseIdle, mergetrack.AttentionReady},
		{"ready with merge automation waits", mergetrack.Decision{Ready: true}, auto, store.MergePhaseIdle, mergetrack.AttentionWaiting},
		{"ready with auto-merge waits", mergetrack.Decision{Ready: true}, config.MergeTrackingConfig{Enabled: true, EnableAutoMerge: true}, store.MergePhaseIdle, mergetrack.AttentionWaiting},

		{"failing checks need action", blocked(mergetrack.ReasonChecksFailing, mergetrack.ReasonPendingReviewers), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"missing required check needs action", blocked(mergetrack.ReasonRequiredCheckMissing), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"changes requested need action", blocked(mergetrack.ReasonChangesRequested), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"unresolved threads need action", blocked(mergetrack.ReasonUnresolvedThreads), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"draft needs action", blocked(mergetrack.ReasonDraft), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"conflicts need action when nobody resolves them", blocked(mergetrack.ReasonConflicts), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"conflicts wait when the agent resolves them", blocked(mergetrack.ReasonConflicts), auto, store.MergePhaseBlocked, mergetrack.AttentionWaiting},
		{"behind base needs action without update_branch", blocked(mergetrack.ReasonBehindBase), watch, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"behind base waits with update_branch", blocked(mergetrack.ReasonBehindBase), auto, store.MergePhaseBlocked, mergetrack.AttentionWaiting},
		{"spent attempt cap needs action", blocked(mergetrack.ReasonAttemptCap, mergetrack.ReasonConflicts), auto, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"cross fork needs a human", blocked(mergetrack.ReasonCrossFork), auto, store.MergePhaseBlocked, mergetrack.AttentionAction},

		{"review required waits", blocked(mergetrack.ReasonReviewRequired), watch, store.MergePhaseBlocked, mergetrack.AttentionWaiting},
		{"pending reviewers wait", blocked(mergetrack.ReasonPendingReviewers), watch, store.MergePhaseBlocked, mergetrack.AttentionWaiting},
		{"pending checks wait", blocked(mergetrack.ReasonChecksPending), watch, store.MergePhaseBlocked, mergetrack.AttentionWaiting},
		{"cooldown in front of the real blocker is skipped", blocked(mergetrack.ReasonCooldown, mergetrack.ReasonChecksFailing), auto, store.MergePhaseBlocked, mergetrack.AttentionAction},
		{"merge=false bookkeeping is skipped", blocked(mergetrack.ReasonDisabled, mergetrack.ReasonChangesRequested), auto, store.MergePhaseBlocked, mergetrack.AttentionAction},

		{"disabled repo is none", blocked(mergetrack.ReasonDisabled), config.MergeTrackingConfig{}, store.MergePhaseBlocked, mergetrack.AttentionNone},
		{"excluded is none", blocked(mergetrack.ReasonExcluded), watch, store.MergePhaseBlocked, mergetrack.AttentionNone},
		{"nothing known is none", mergetrack.Decision{Action: mergetrack.ActionNone}, watch, store.MergePhaseIdle, mergetrack.AttentionNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergetrack.ComputeAttention(tc.d, tc.cfg, tc.phase); got != tc.want {
				t.Errorf("ComputeAttention = %q, want %q", got, tc.want)
			}
		})
	}
}
