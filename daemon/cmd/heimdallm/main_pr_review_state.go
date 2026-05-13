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
// an auto_implement-created PR (#482, phase 1) and updates the store
// when the aggregated state changes. Called from Tier 3's CheckItem
// branch keyed on `stored.AutoImplementIssueID != 0`.
//
// Always returns nil after a successful fetch — even when the state
// did not change, because the caller (CheckItem) already short-
// circuits the standard review codepath for these PRs. An error from
// the GitHub fetch is surfaced so the caller can log it; the store
// stays untouched in that case.
//
// Side effects:
//   - Calls UpdatePRReviewState when the aggregate differs from the
//     stored value.
//   - Publishes sse.EventPRReviewStateChanged with the new and prior
//     state when a change is detected (no event on no-op refreshes).
func (a *tier2Adapter) refreshAutoImplementPRReviewState(
	item *scheduler.WatchItem, stored *store.PR,
) error {
	reviews, err := a.ghClient.GetPRReviews(item.Repo, item.Number)
	if err != nil {
		return fmt.Errorf("get pr reviews: %w", err)
	}
	botLogin := a.cachedAuthenticatedUser()
	state, reviewer, at := issuepipeline.LatestExternalReviewState(reviews, botLogin)
	if state == stored.ExternalReviewState {
		return nil
	}
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

	// Dispatch to the phase-2/phase-3 modules. Both Run methods are
	// safe to call when their owning config flag is off (they return
	// nil immediately) so the gating logic stays inside the modules
	// rather than smearing across the adapter.
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

