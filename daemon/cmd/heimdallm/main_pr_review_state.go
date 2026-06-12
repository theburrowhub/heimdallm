package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/heimdallm/daemon/internal/autonomous"
	gh "github.com/heimdallm/daemon/internal/github"
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
	ctx context.Context, item *scheduler.WatchItem, stored *store.PR,
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

	// Autonomous repos route review reactions through the classifier so an
	// approved-clean PR can advance to the merge gate without a human. When
	// autonomous is disabled for the repo the classifier is bypassed entirely
	// and the legacy COMMENTED→Responder / CHANGES_REQUESTED→FixRunner switch
	// below runs unchanged.
	if a.autonomousEnabledForRepo(item.Repo) {
		return a.dispatchAutonomousReview(ctx, item, stored, reviews)
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
	//
	// Critically: runner errors propagate. Without propagation the
	// caller (CheckItem) would return changed=true and the
	// StateWorker would ResetBackoff → advance LastSeen → the next
	// tick's early `!snap.UpdatedAt.After(LastSeen)` gate would
	// silently drop the still-unaddressed review. Surfacing the
	// error keeps LastSeen frozen so the next tick re-enters the
	// refresh and retries the dispatch. The state-handler in
	// main.go's stateHandler closure already routes 404 errors to
	// the watch-cleanup path; other errors fall through to the
	// StateWorker's IncreaseBackoff branch.
	switch state {
	case issuepipeline.ReviewStateCommented:
		if a.responder != nil {
			if err := a.responder.Run(ctx, stored, stored.AutoImplementIssueID); err != nil {
				slog.Warn("tier3: responder run failed (retrying next tick)",
					"repo", item.Repo, "number", item.Number, "err", err)
				return fmt.Errorf("responder: %w", err)
			}
		}
	case issuepipeline.ReviewStateChangesRequested:
		if a.fixRunner != nil {
			if err := a.fixRunner.Run(ctx, stored, stored.AutoImplementIssueID); err != nil {
				slog.Warn("tier3: fix runner failed (retrying next tick)",
					"repo", item.Repo, "number", item.Number, "err", err)
				return fmt.Errorf("fix runner: %w", err)
			}
		}
	}
	return nil
}

// autonomousEnabledForRepo reports whether autonomous mode is enabled for the
// repo, resolving config under the adapter's cfg lock.
func (a *tier2Adapter) autonomousEnabledForRepo(repo string) bool {
	if a == nil || a.cfg == nil {
		return false
	}
	if a.cfgMu != nil {
		a.cfgMu.Lock()
		defer a.cfgMu.Unlock()
	}
	return (*a.cfg).AutonomousForRepo(repo).Enabled
}

// dispatchAutonomousReview is the Tier 3 reaction for autonomous repos. It
// classifies the aggregate review set and routes:
//
//   - DecisionFix       → the existing FixRunner (same path as legacy
//     CHANGES_REQUESTED / COMMENTED handling), so reviewer feedback is
//     addressed automatically.
//   - DecisionMergeGate → the merge gate. With auto_merge disabled (the
//     default) this is a safe no-op that emits an audit SSE; with it enabled
//     the PR is merged via the configured method.
//   - DecisionWait      → nothing (no human review yet).
//
// Runner errors propagate exactly like the legacy path so the StateWorker keeps
// LastSeen frozen and retries the dispatch next tick.
func (a *tier2Adapter) dispatchAutonomousReview(
	ctx context.Context, item *scheduler.WatchItem, stored *store.PR, reviews []gh.PRReview,
) error {
	decision := autonomous.ClassifyReview(toReviewInputs(reviews))

	if a.broker != nil {
		a.broker.Publish(sse.Event{
			Type: sse.EventAutonomousReviewClass,
			Data: sseData(map[string]any{
				"repo": item.Repo, "number": item.Number, "decision": decision.String(),
			}),
		})
	}

	switch decision {
	case autonomous.DecisionFix:
		if a.fixRunner != nil {
			if err := a.fixRunner.Run(ctx, stored, stored.AutoImplementIssueID); err != nil {
				slog.Warn("tier3: autonomous fix runner failed (retrying next tick)",
					"repo", item.Repo, "number", item.Number, "err", err)
				return fmt.Errorf("autonomous fix runner: %w", err)
			}
		}
	case autonomous.DecisionMergeGate:
		auto := a.autonomousConfigForRepo(item.Repo)
		gate := autonomous.NewMergeGate(a.ghClient, auto.AutoMerge, auto.MergeMethod)
		res, err := gate.Run(ctx, item.Repo, item.Number)
		if err != nil {
			slog.Warn("tier3: autonomous merge gate failed (retrying next tick)",
				"repo", item.Repo, "number", item.Number, "err", err)
			return fmt.Errorf("autonomous merge gate: %w", err)
		}
		if res == autonomous.MergeSkippedDisabled && a.broker != nil {
			a.broker.Publish(sse.Event{
				Type: sse.EventAutonomousMergeSkipped,
				Data: sseData(map[string]any{
					"repo": item.Repo, "number": item.Number, "reason": "auto_merge_disabled",
				}),
			})
		}
		slog.Info("tier3: autonomous merge gate",
			"repo", item.Repo, "number", item.Number, "result", res.String())
	case autonomous.DecisionWait:
		// No human review yet — keep watching.
	}
	return nil
}

// autonomousConfigForRepo resolves the autonomous config for a repo under lock.
func (a *tier2Adapter) autonomousConfigForRepo(repo string) (cfg autonomousConfigView) {
	if a == nil || a.cfg == nil {
		return cfg
	}
	if a.cfgMu != nil {
		a.cfgMu.Lock()
		defer a.cfgMu.Unlock()
	}
	resolved := (*a.cfg).AutonomousForRepo(repo)
	return autonomousConfigView{AutoMerge: resolved.AutoMerge, MergeMethod: resolved.MergeMethod}
}

// autonomousConfigView is the minimal projection dispatchAutonomousReview needs
// from the resolved autonomous config; kept tiny so the lock is held briefly.
type autonomousConfigView struct {
	AutoMerge   bool
	MergeMethod string
}
