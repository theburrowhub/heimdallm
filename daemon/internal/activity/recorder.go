// Package activity records a row in the activity_log table for every
// significant Heimdallm action emitted on the SSE broker. The recorder
// subscribes once on Start and runs until its context is cancelled.
//
// Failure mode: log + drop. The activity log is observability, so a
// failed insert (disk full, locked DB) must never block the publisher.
package activity

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/sse"
)

// Store is the subset of *store.Store the recorder uses. Kept as a local
// interface so tests can inject fakes without importing the real store.
type Store interface {
	InsertActivity(ts time.Time, org, repo, itemType string, itemNumber int,
		itemTitle, action, outcome string, details map[string]any) (int64, error)
}

// Recorder consumes SSE events and writes activity rows.
type Recorder struct {
	store  Store
	events chan sse.Event
}

// New subscribes to the broker and returns a recorder ready to Start.
// Returns nil if the broker has reached its subscriber limit; the caller
// should log and continue without the recorder (activity is optional).
func New(s Store, broker *sse.Broker) *Recorder {
	ch := broker.Subscribe()
	if ch == nil {
		return nil
	}
	return &Recorder{store: s, events: ch}
}

// NewWithChannel is a test hook. Production code uses New.
func NewWithChannel(s Store, ch chan sse.Event) *Recorder {
	return &Recorder{store: s, events: ch}
}

// Start runs the event loop. Returns when ctx is cancelled or the event
// channel is closed.
func (r *Recorder) Start(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-r.events:
			if !ok {
				return
			}
			if err := r.handle(ev); err != nil {
				slog.Warn("activity: record failed", "err", err, "event", ev.Type)
			}
		}
	}
}

func (r *Recorder) handle(ev sse.Event) error {
	switch ev.Type {
	case sse.EventReviewCompleted:
		return r.recordReviewCompleted(ev)
	case sse.EventReviewError:
		return r.recordReviewError(ev)
	case sse.EventReviewSkipped:
		return r.recordReviewSkipped(ev)
	case sse.EventIssueReviewCompleted:
		return r.recordIssueTriage(ev)
	case sse.EventIssueImplemented:
		return r.recordIssueImplemented(ev)
	case sse.EventIssueRefinementDone:
		return r.recordIssueRefinementDone(ev)
	case sse.EventIssueReviewError:
		return r.recordIssueReviewError(ev)
	case sse.EventIssuePromoted:
		return r.recordIssuePromoted(ev)
	case sse.EventMergeTrackMerged:
		return r.recordMergeTrackMerged(ev)
	case sse.EventMergeTrackAutoMergeArmed:
		return r.recordMergeTrackAutoMergeArmed(ev)
	case sse.EventMergeTrackBranchUpdated:
		return r.recordMergeTrackBranchUpdated(ev)
	case sse.EventMergeTrackConflictResolved:
		return r.recordMergeTrackConflictResolved(ev)
	case sse.EventMergeTrackBlocked:
		return r.recordMergeTrackBlocked(ev)
	case sse.EventMergeTrackError:
		return r.recordMergeTrackError(ev)
	default:
		return nil
	}
}

// payload helpers ----------------------------------------------------------

func decode(data string, v any) error {
	return json.Unmarshal([]byte(data), v)
}

func orgOf(repo string) string {
	i := strings.IndexByte(repo, '/')
	if i < 0 {
		return repo
	}
	return repo[:i]
}

// event handlers -----------------------------------------------------------

func (r *Recorder) recordReviewCompleted(ev sse.Event) error {
	var p struct {
		Repo              string `json:"repo"`
		PRNumber          int    `json:"pr_number"`
		PRTitle           string `json:"pr_title"`
		CLIUsed           string `json:"cli_used"`
		Severity          string `json:"severity"`
		ReviewID          int64  `json:"review_id"`
		GitHubReviewState string `json:"github_review_state"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	details := map[string]any{
		"cli_used":            p.CLIUsed,
		"review_id":           p.ReviewID,
		"github_review_state": p.GitHubReviewState,
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.PRNumber, p.PRTitle, "review", p.Severity, details)
	return err
}

func (r *Recorder) recordReviewError(ev sse.Event) error {
	var p struct {
		Repo     string `json:"repo"`
		PRNumber int    `json:"pr_number"`
		PRTitle  string `json:"pr_title"`
		CLIUsed  string `json:"cli_used"`
		Error    string `json:"error"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.PRNumber, p.PRTitle, "error", p.Error, map[string]any{
			"item_type": "pr",
			"cli_used":  p.CLIUsed,
			"error":     p.Error,
		})
	return err
}

func (r *Recorder) recordIssueTriage(ev sse.Event) error {
	var p struct {
		Repo         string `json:"repo"`
		IssueNumber  int    `json:"issue_number"`
		IssueTitle   string `json:"issue_title"`
		CLIUsed      string `json:"cli_used"`
		Severity     string `json:"severity"`
		Category     string `json:"category"`
		ChosenAction string `json:"chosen_action"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	outcome := p.Severity
	if outcome == "" {
		outcome = "ignored"
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "triage", outcome, map[string]any{
			"cli_used":      p.CLIUsed,
			"category":      p.Category,
			"chosen_action": p.ChosenAction,
		})
	return err
}

func (r *Recorder) recordIssueImplemented(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		Number      int    `json:"number"`
		IssueTitle  string `json:"issue_title"`
		CLIUsed     string `json:"cli_used"`
		PRNumber    int    `json:"pr_number"`
		PRCreated   int    `json:"pr_created"`
		PRURL       string `json:"pr_url"`
		Branch      string `json:"branch"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	issueNumber := firstNonZero(p.IssueNumber, p.Number)
	prNumber := firstNonZero(p.PRNumber, p.PRCreated)
	outcome := "pr_opened"
	if prNumber == 0 {
		outcome = "pr_failed"
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		issueNumber, p.IssueTitle, "implement", outcome, map[string]any{
			"cli_used":  p.CLIUsed,
			"pr_number": prNumber,
			"pr_url":    p.PRURL,
			"branch":    p.Branch,
		})
	return err
}

func (r *Recorder) recordIssueRefinementDone(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		Number      int    `json:"number"`
		IssueTitle  string `json:"issue_title"`
		CLIUsed     string `json:"cli_used"`
		ReviewID    int64  `json:"review_id"`
		PostOK      *bool  `json:"post_ok"`
		Truncated   bool   `json:"truncated"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	issueNumber := firstNonZero(p.IssueNumber, p.Number)
	postOK := true
	if p.PostOK != nil {
		postOK = *p.PostOK
	}
	outcome := "completed"
	if !postOK {
		outcome = "stored_locally"
	}
	details := map[string]any{
		"cli_used":  p.CLIUsed,
		"review_id": p.ReviewID,
		"post_ok":   postOK,
		"truncated": p.Truncated,
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		issueNumber, p.IssueTitle, "refinement", outcome, details)
	return err
}

func firstNonZero(primary, fallback int) int {
	if primary != 0 {
		return primary
	}
	return fallback
}

func (r *Recorder) recordIssueReviewError(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		IssueTitle  string `json:"issue_title"`
		CLIUsed     string `json:"cli_used"`
		Error       string `json:"error"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "error", p.Error, map[string]any{
			"item_type": "issue",
			"cli_used":  p.CLIUsed,
			"error":     p.Error,
		})
	return err
}

// dedupSkipReasons are review_skipped reasons that fire on routine
// poll cycles rather than user-visible policy decisions, and therefore
// should NOT produce activity_log rows. Recording them would spam the
// activity feed with one row per poll for every stable PR — exactly the
// regression theburrowhub/heimdallm#322 Bug 4 was meant to close (the
// pre-fix path emitted EventReviewCompleted on those skips, which was
// then routed here). Keep policy skips (not_open / draft) recorded —
// those reflect the bot deciding not to review and are useful in the
// audit trail.
//
// self_authored is deduped WITH the routine skips, not with the policy
// ones, because it does not mean what its name suggests any more.
// Heimdallm authenticates as the operator's own account, so the
// "self-authored" guard fires on every pull request the operator opens
// — on every poll cycle, for as long as the PR is open. That is not an
// audit trail, it is one row per PR per cycle burying everything else.
// The operator's own PRs have their own surface now (the Merge tab), so
// the skip is expected rather than notable. The SSE still goes out, so
// a spinner watching that PR clears either way.
var dedupSkipReasons = map[string]bool{
	"sha_unchanged":    true,
	"legacy_backfill":  true,
	"retry_cooldown":   true,
	"retry_repo_limit": true,
	"self_authored":    true,
}

func (r *Recorder) recordReviewSkipped(ev sse.Event) error {
	var p struct {
		Repo     string `json:"repo"`
		PRNumber int    `json:"pr_number"`
		PRTitle  string `json:"pr_title"`
		Reason   string `json:"reason"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	if dedupSkipReasons[p.Reason] {
		// Routine dedup skip — UI still gets the SSE so the spinner can
		// clear, but the activity log stays free of poll-cycle noise.
		return nil
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.PRNumber, p.PRTitle, "review_skipped", p.Reason, map[string]any{
			"reason": p.Reason,
		})
	return err
}

func (r *Recorder) recordIssuePromoted(ev sse.Event) error {
	var p struct {
		Repo        string `json:"repo"`
		IssueNumber int    `json:"issue_number"`
		IssueTitle  string `json:"issue_title"`
		FromLabel   string `json:"from_label"`
		ToLabel     string `json:"to_label"`
		FromStage   string `json:"from_stage"`
		ToStage     string `json:"to_stage"`
		Trigger     string `json:"trigger"`
		Reason      string `json:"reason"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	from := p.FromLabel
	to := p.ToLabel
	if p.FromStage != "" || p.ToStage != "" {
		from = p.FromStage
		to = p.ToStage
	}
	details := map[string]any{}
	if p.FromStage != "" || p.ToStage != "" {
		details["from_stage"] = p.FromStage
		details["to_stage"] = p.ToStage
	} else {
		details["from_label"] = p.FromLabel
		details["to_label"] = p.ToLabel
	}
	if p.Trigger != "" {
		details["trigger"] = p.Trigger
	}
	if p.Reason != "" {
		details["reason"] = p.Reason
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "promote",
		from+" → "+to, details)
	return err
}

// merge-tracking handlers --------------------------------------------------
//
// merge_track_evaluated is deliberately NOT recorded: it fires on every PR on
// every cycle, and a per-cycle row per PR would drown the activity log. The
// events below are the ones that represent something having happened —
// including merge_track_blocked, which the reconciler only emits when the
// blocking reason changes.

// mergeTrackPayload is the common shape of the merge_track_* events.
type mergeTrackPayload struct {
	Repo         string   `json:"repo"`
	Number       int      `json:"number"`
	Reason       string   `json:"reason"`
	Detail       string   `json:"detail"`
	Method       string   `json:"method"`
	SHA          string   `json:"sha"`
	Mode         string   `json:"mode"`
	Action       string   `json:"action"`
	Err          string   `json:"err"`
	Pushed       bool     `json:"pushed"`
	Files        []string `json:"files"`
	PreRebaseSHA string   `json:"pre_rebase_sha"`
}

func (r *Recorder) recordMergeTrackMerged(ev sse.Event) error {
	var p mergeTrackPayload
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.Number, "", "merge_track_merged", p.Method, map[string]any{
			"item_type": "pr",
			"method":    p.Method,
			"sha":       p.SHA,
		})
	return err
}

func (r *Recorder) recordMergeTrackAutoMergeArmed(ev sse.Event) error {
	var p mergeTrackPayload
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.Number, "", "merge_track_auto_merge_armed", p.Method, map[string]any{
			"item_type": "pr",
			"method":    p.Method,
		})
	return err
}

func (r *Recorder) recordMergeTrackBranchUpdated(ev sse.Event) error {
	var p mergeTrackPayload
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.Number, "", "merge_track_branch_updated", p.Mode, map[string]any{
			"item_type": "pr",
			"mode":      p.Mode,
		})
	return err
}

// recordMergeTrackConflictResolved records both outcomes. A resolution that was
// NOT pushed is at least as interesting as one that was: it means the agent
// looked at the conflicts and declined, which the author needs to know.
func (r *Recorder) recordMergeTrackConflictResolved(ev sse.Event) error {
	var p mergeTrackPayload
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	summary := "not pushed"
	if p.Pushed {
		summary = "pushed"
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.Number, "", "merge_track_conflict_resolved", summary, map[string]any{
			"item_type":      "pr",
			"pushed":         p.Pushed,
			"files":          p.Files,
			"pre_rebase_sha": p.PreRebaseSHA,
		})
	return err
}

func (r *Recorder) recordMergeTrackBlocked(ev sse.Event) error {
	var p mergeTrackPayload
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.Number, "", "merge_track_blocked", p.Reason, map[string]any{
			"item_type": "pr",
			"reason":    p.Reason,
			"detail":    p.Detail,
		})
	return err
}

func (r *Recorder) recordMergeTrackError(ev sse.Event) error {
	var p mergeTrackPayload
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "pr",
		p.Number, "", "error", p.Err, map[string]any{
			"item_type": "pr",
			"action":    p.Action,
			"error":     p.Err,
		})
	return err
}
