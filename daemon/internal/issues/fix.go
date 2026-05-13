package issues

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// FixRunner drives phase 3 of the PR review-state vigilance feature
// (#482): when an external reviewer leaves a CHANGES_REQUESTED review
// on a PR auto_implement created, the FixRunner optionally invokes
// the agent on the PR's head branch and pushes back a fix.
//
// This first cut of the runner targets the lower-blast-radius
// advisory path: it generates a structured comment explaining how the
// agent interprets the requested changes and what fix it would
// produce. The actual agentic git checkout + commit + push flow is
// kept behind the FixExecutor interface so a follow-up can swap in
// real branch operations without touching the cap/cooldown story
// already covered here.
//
// Even in this first cut every guard the issue called out is
// enforced:
//
//   - Enabled defaults false (opt-in).
//   - Per-PR lifetime cap. After the cap an issue_review_error event
//     fires with reason=review_fix_cap_exceeded and
//     ErrFixCapExceeded is returned.
//   - Cooldown blocks back-to-back runs on the same PR.
//   - Reviews authored by the bot are filtered out before the trigger
//     check so a self-submitted CR can never start the loop.
//   - The CHANGES_REQUESTED review's body is sanitised through the
//     same untrusted-text fence the issue triage pipeline uses.
type FixRunner struct {
	store    fixStore
	gh       fixGH
	exec     FixExecutor
	broker   eventPublisher
	cfgFn    func() config.ReviewFixConfig
	loginFn  func() string
	nowFn    func() time.Time
}

type fixStore interface {
	IncrementPRReviewFixCount(prID int64) (int, error)
	SetPRLastRespondedAt(prID int64, at time.Time) error
	UpdatePRReviewState(prID int64, state, reviewer string, at time.Time) error
	// GetIssue lets the FixRunner embed the originating issue's body
	// in the prompt — #482 explicitly asks for the issue context so
	// the agent can judge whether the reviewer's requested changes
	// are in-scope for the original work item.
	GetIssue(id int64) (*store.Issue, error)
}

// fixReviewsFetcher mirrors the slice of *github.Client the runner
// reads. Tests inject a stub that returns canned reviews.
type fixReviewsFetcher interface {
	GetPRReviews(repo string, number int) ([]github.PRReview, error)
}

type fixGH interface {
	IssueCommenter
	fixReviewsFetcher
}

// FixExecutor is the dependency that addresses the reviewer feedback
// — production wires this to Pipeline.RunFix, which actually checks
// out the PR's head branch, runs the agent with write permissions,
// and pushes back. The runner stays in charge of cap/cooldown/state
// flipping so the executor can focus on the agent + git glue.
type FixExecutor interface {
	RunFix(ctx context.Context, req FixRequest) (FixResult, error)
}

func NewFixRunner(
	st fixStore,
	gh fixGH,
	exec FixExecutor,
	broker eventPublisher,
	cfgFn func() config.ReviewFixConfig,
	loginFn func() string,
) *FixRunner {
	return &FixRunner{
		store: st, gh: gh, exec: exec, broker: broker,
		cfgFn: cfgFn, loginFn: loginFn,
		nowFn: time.Now,
	}
}

// ErrFixCapExceeded is the typed error the cap-exceeded branch
// returns. Tier 3 logs it; the SSE event surface carries the same
// signal for the UI.
var ErrFixCapExceeded = errors.New("issues fix runner: per-PR lifetime cap exceeded")

// Run drives one fix cycle for the given PR. Like Responder.Run, a
// nil return covers every no-op branch (disabled, no current CR
// review, bot-only CR, cooldown). Errors surface unexpected failures
// (executor, post).
func (r *FixRunner) Run(ctx context.Context, pr *store.PR, originIssueID int64) error {
	cfg := r.cfgFn()
	if !cfg.Enabled {
		return nil
	}

	bot := r.loginFn()
	reviews, err := r.gh.GetPRReviews(pr.Repo, pr.Number)
	if err != nil {
		return fmt.Errorf("fix runner: get reviews: %w", err)
	}
	cr := latestChangesRequestedByExternal(reviews, bot)
	if cr == nil {
		// The trigger state was CHANGES_REQUESTED but the only CR is
		// from the bot itself (or already DISMISSED). No-op.
		return nil
	}

	now := r.nowFn()
	cooldown := time.Duration(cfg.CooldownSecs) * time.Second
	if !pr.LastRespondedAt.IsZero() && now.Sub(pr.LastRespondedAt) < cooldown {
		return nil
	}

	n, err := r.store.IncrementPRReviewFixCount(pr.ID)
	if err != nil {
		return fmt.Errorf("fix runner: increment counter: %w", err)
	}
	if n > cfg.PerPRLifetime {
		if r.broker != nil {
			r.broker.Publish(sse.Event{
				Type: sse.EventIssueReviewError,
				Data: marshalEvent(map[string]any{
					"issue_id": originIssueID,
					"repo":     pr.Repo,
					"number":   pr.Number,
					"pr_id":    pr.ID,
					"reason":   "review_fix_cap_exceeded",
					"error":    fmt.Sprintf("Per-PR lifetime cap of %d fix runs reached.", cfg.PerPRLifetime),
				}),
			})
		}
		return ErrFixCapExceeded
	}

	originIssue, _ := r.store.GetIssue(originIssueID)
	req := FixRequest{
		Repo:          pr.Repo,
		PRNumber:      pr.Number,
		PRTitle:       pr.Title,
		OriginIssue:   originIssue,
		ReviewerLogin: cr.User.Login,
		ReviewBody:    cr.Body,
	}
	result, err := r.exec.RunFix(ctx, req)
	if err != nil {
		return fmt.Errorf("fix runner: execute: %w", err)
	}

	// Post the comment first so the reviewer sees what happened even
	// if the post-push state updates fail.
	if strings.TrimSpace(result.CommentBody) != "" {
		if _, err := r.gh.PostComment(pr.Repo, pr.Number, result.CommentBody); err != nil {
			return fmt.Errorf("fix runner: post comment: %w", err)
		}
	}

	// Always update last_responded_at — we just acted on this CR
	// regardless of whether the act was a push or an advisory
	// comment. The cooldown gate then keeps a fresh tick out for the
	// configured window.
	if err := r.store.SetPRLastRespondedAt(pr.ID, now); err != nil {
		slog.Warn("fix runner: failed to update last_responded_at",
			"pr_id", pr.ID, "err", err)
	}

	// Re-arm only when we actually pushed. If the executor returned
	// Pushed=false (no-changes fallback) we deliberately keep the
	// stored state at CHANGES_REQUESTED so a reviewer who supplies
	// more context — and we get to retry within the cooldown +
	// lifetime cap — can re-trigger the runner. Without this the
	// no-changes case would silently terminate the loop after a
	// single advisory.
	if result.Pushed {
		if err := r.store.UpdatePRReviewState(pr.ID, ReviewStateFixPushed, cr.User.Login, cr.SubmittedAt); err != nil {
			slog.Warn("fix runner: failed to flip state to FIX_PUSHED",
				"pr_id", pr.ID, "err", err)
		}
	}

	if r.broker != nil {
		r.broker.Publish(sse.Event{
			Type: sse.EventIssueReviewCompleted,
			Data: marshalEvent(map[string]any{
				"mode":     "review_fix",
				"pushed":   result.Pushed,
				"issue_id": originIssueID,
				"repo":     pr.Repo,
				"number":   pr.Number,
				"pr_id":    pr.ID,
			}),
		})
	}
	slog.Info("fix runner: completed",
		"repo", pr.Repo, "pr", pr.Number, "count", n, "cap", cfg.PerPRLifetime,
		"pushed", result.Pushed)
	return nil
}

// latestChangesRequestedByExternal returns the review that drives the
// CHANGES_REQUESTED aggregate per the same per-reviewer collapse used
// by LatestExternalReviewState — that's what guarantees we act on the
// CR the aggregator surfaced and never on a stale CR that was
// superseded by an APPROVED or DISMISSED from the same reviewer.
//
// Without this collapse the runner would pick the most recent raw CR
// in the list, which could be a CR Alice posted minutes before
// approving — and "fixing" a CR the reviewer already withdrew is the
// wrong action.
func latestChangesRequestedByExternal(reviews []github.PRReview, botLogin string) *github.PRReview {
	dec := currentDecisionsByReviewer(reviews, botLogin)
	var best *github.PRReview
	for _, r := range dec {
		if r.State != ReviewStateChangesRequested {
			continue
		}
		cur := r
		if best == nil || cur.SubmittedAt.After(best.SubmittedAt) {
			best = &cur
		}
	}
	return best
}

// currentDecisionsByReviewer applies the per-reviewer collapse the
// aggregator uses: bot reviews are filtered out, DISMISSED drops the
// reviewer's prior decision, and only the latest non-COMMENTED state
// from a reviewer counts as their "decision". Returns the map of
// driving reviews keyed by lowercased reviewer login. Shared between
// the FixRunner trigger selector and the Responder's COMMENTED
// selector so the trigger picker and the aggregator can never
// disagree about who the active reviewer is.
func currentDecisionsByReviewer(reviews []github.PRReview, botLogin string) map[string]github.PRReview {
	out := map[string]github.PRReview{}
	for _, r := range reviews {
		if botLogin != "" && strings.EqualFold(r.User.Login, botLogin) {
			continue
		}
		key := strings.ToLower(r.User.Login)
		switch r.State {
		case "DISMISSED":
			delete(out, key)
		case ReviewStateApproved, ReviewStateChangesRequested:
			out[key] = r
		case ReviewStateCommented:
			if _, has := out[key]; !has {
				out[key] = r
			}
		}
	}
	return out
}

