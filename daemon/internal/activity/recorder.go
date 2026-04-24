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
	case sse.EventIssueReviewError:
		return r.recordIssueReviewError(ev)
	case sse.EventIssuePromoted:
		return r.recordIssuePromoted(ev)
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
		IssueTitle  string `json:"issue_title"`
		CLIUsed     string `json:"cli_used"`
		PRNumber    int    `json:"pr_number"`
		PRURL       string `json:"pr_url"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	outcome := "pr_opened"
	if p.PRNumber == 0 {
		outcome = "pr_failed"
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "implement", outcome, map[string]any{
			"cli_used":  p.CLIUsed,
			"pr_number": p.PRNumber,
			"pr_url":    p.PRURL,
		})
	return err
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
// then routed here). Keep policy skips (not_open / draft /
// self_authored) recorded — those reflect the bot deciding not to
// review and are useful in the audit trail.
var dedupSkipReasons = map[string]bool{
	"sha_unchanged":   true,
	"legacy_backfill": true,
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
		Reason      string `json:"reason"`
	}
	if err := decode(ev.Data, &p); err != nil {
		return err
	}
	_, err := r.store.InsertActivity(time.Now(), orgOf(p.Repo), p.Repo, "issue",
		p.IssueNumber, p.IssueTitle, "promote",
		p.FromLabel+" → "+p.ToLabel, map[string]any{
			"from_label": p.FromLabel,
			"to_label":   p.ToLabel,
			"reason":     p.Reason,
		})
	return err
}
