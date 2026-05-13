package main

import (
	"context"
	"fmt"
	"log/slog"

	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// reviewResponderDispatcher is the abstraction Tier 3 uses to invoke
// the phase-2 Responder. *issuepipeline.Responder satisfies this
// directly; tests inject a stub that records calls without touching
// the executor.
type reviewResponderDispatcher interface {
	Run(ctx context.Context, pr *store.PR, originIssueID int64) error
}

// reviewFixDispatcher is the phase-3 surface; same shape so the two
// callsites in refreshAutoImplementPRReviewState stay symmetric.
type reviewFixDispatcher interface {
	Run(ctx context.Context, pr *store.PR, originIssueID int64) error
}

// refreshAutoImplementPRReviewState observes the external reviews on
// an auto_implement-created PR (#482) and reacts to them. Two
// concerns kept independent so a transient runner failure can be
// retried without re-emitting SSE noise:
//
//   - Persist + SSE only fire when the aggregate state actually
//     moves (different state OR newer timestamp). The FIX_PUSHED
//     re-arm guard suppresses the daemon-internal CHANGES_REQUESTED
//     flip-back for the same CR the FixRunner already addressed.
//
//   - Dispatch fires on every tick whose aggregate state requires
//     action (COMMENTED → Responder, CHANGES_REQUESTED → FixRunner).
//     The runners' own cap / cooldown / last_responded_at gates
//     decide whether to actually do work. Decoupling here is what
//     makes failed runs retriable: when the runner fails the state
//     was already persisted by an earlier tick (or never moved at
//     all if the failure happened on the first observation), so a
//     plain "state changed?" gate would silently swallow the retry.
//
// CheckItem (in main.go) routes errors from this function to the
// state-handler, which propagates them to the StateWorker so backoff
// increases on transient failures (no LastSeen advance until a tick
// succeeds).
func (a *tier2Adapter) refreshAutoImplementPRReviewState(
	item *scheduler.WatchItem, stored *store.PR,
) error {
	reviews, err := a.ghClient.GetPRReviews(item.Repo, item.Number)
	if err != nil {
		return fmt.Errorf("get pr reviews: %w", err)
	}
	botLogin := a.cachedAuthenticatedUser()
	state, reviewer, at := issuepipeline.LatestExternalReviewState(reviews, botLogin)

	// FIX_PUSHED re-arm guard: after the FixRunner addresses a CR it
	// flips the stored state to FIX_PUSHED with `external_review_at`
	// set to the CR's own SubmittedAt. The raw aggregate over the same
	// historical reviews list will still return CHANGES_REQUESTED for
	// that exact CR. Suppress BOTH the persist+emit and the dispatch
	// in that case — re-firing the FixRunner with stale feedback is
	// exactly the loop the guard exists to prevent. A fresh CR
	// (SubmittedAt strictly after the stored mark) reactivates the
	// cycle.
	fixPushedSameCR := stored.ExternalReviewState == issuepipeline.ReviewStateFixPushed &&
		state == issuepipeline.ReviewStateChangesRequested &&
		!at.After(stored.ExternalReviewAt)
	if fixPushedSameCR {
		return nil
	}

	// Persist + emit only on real movement. Same-state same-at means
	// the dashboard has no new chip to show; emitting another SSE
	// would just flap. The dispatch below still fires for retry.
	stateMoved := state != stored.ExternalReviewState || at.After(stored.ExternalReviewAt)
	if stateMoved {
		prevState := stored.ExternalReviewState
		if err := a.store.UpdatePRReviewState(stored.ID, state, reviewer, at); err != nil {
			return fmt.Errorf("update pr review state: %w", err)
		}
		if a.broker != nil {
			a.broker.Publish(sse.Event{
				Type: sse.EventPRReviewStateChanged,
				Data: sseData(map[string]any{
					"pr_id":      stored.ID,
					"repo":       item.Repo,
					"number":     item.Number,
					"state":      state,
					"reviewer":   reviewer,
					"prev_state": prevState,
				}),
			})
		}
		slog.Info("tier3: PR review state changed",
			"repo", item.Repo, "number", item.Number,
			"prev_state", prevState, "new_state", state, "reviewer", reviewer)
	}

	// Dispatch independently of persist/emit so a failed runner does
	// not poison the retry path. Both Run methods are safe to call
	// when their owning config flag is off (they return nil
	// immediately); when on, the runner's own cap / cooldown /
	// last_responded_at gates filter the no-op cases. A dispatch on
	// every tick whose state is COMMENTED or CHANGES_REQUESTED costs
	// at most one extra GetPRReviews call inside the runner while the
	// cooldown window is open — bounded by the runner's per-PR
	// lifetime cap.
	switch state {
	case issuepipeline.ReviewStateCommented:
		if a.responder != nil {
			if err := a.responder.Run(context.Background(), stored, stored.AutoImplementIssueID); err != nil {
				slog.Warn("tier3: responder run failed",
					"repo", item.Repo, "number", item.Number, "err", err)
			}
		}
	case issuepipeline.ReviewStateChangesRequested:
		if a.fixRunner != nil {
			if err := a.fixRunner.Run(context.Background(), stored, stored.AutoImplementIssueID); err != nil {
				slog.Warn("tier3: fix runner failed",
					"repo", item.Repo, "number", item.Number, "err", err)
			}
		}
	}
	return nil
}
