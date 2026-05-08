package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
)

type IssueStage string

const (
	IssueStageTriage      IssueStage = "triage"
	IssueStageRefinement  IssueStage = "refinement"
	IssueStageDevelopment IssueStage = "development"
)

const ActionAutoImplementNoChanges = "auto_implement_no_changes"

type StagePromotionTrigger string

const (
	StagePromotionManualAPI    StagePromotionTrigger = "manual API"
	StagePromotionManualGitHub StagePromotionTrigger = "manual GitHub label change"
	StagePromotionAuto         StagePromotionTrigger = "auto-promote"
)

const (
	stagePromotionHeading = "🤖 **Heimdallm stage promotion**"
	stagePromotionWindow  = 2 * time.Minute
)

var (
	ErrStageTargetLabelMissing = errors.New("stage target label missing")
	ErrNoNextStage             = errors.New("issue has no next stage")
)

// StageTransitionClient is the GitHub surface needed to move an issue between
// issue pipeline stages. It is intentionally separate from the dependency
// promoter's client: stage promotion has a different contract and label order.
type StageTransitionClient interface {
	AddLabels(repo string, number int, labels []string) error
	RemoveLabels(repo string, number int, labels []string) error
	CreateLabel(repo, name, color, description string) error
	PostComment(repo string, number int, body string) (time.Time, error)
}

type StageTransition struct {
	Issue          *github.Issue
	StoreIssueID   int64
	Config         config.IssueTrackingConfig
	From           IssueStage
	To             IssueStage
	Trigger        StagePromotionTrigger
	Time           time.Time
	RecentComments []github.Comment
	SuppressAudit  bool
	Broker         Publisher
}

func StageFromMode(mode config.IssueMode) (IssueStage, bool) {
	switch mode {
	case config.IssueModeReviewOnly:
		return IssueStageTriage, true
	case config.IssueModeRefinement:
		return IssueStageRefinement, true
	case config.IssueModeDevelop:
		return IssueStageDevelopment, true
	default:
		return "", false
	}
}

func StageFromAction(action string) (IssueStage, bool) {
	switch strings.ToLower(strings.TrimSpace(action)) {
	case string(config.IssueModeReviewOnly), "triage":
		return IssueStageTriage, true
	case string(config.IssueModeRefinement):
		return IssueStageRefinement, true
	case string(config.IssueModeDevelop), "auto_implement", ActionAutoImplementNoChanges:
		return IssueStageDevelopment, true
	default:
		return "", false
	}
}

func NextStage(stage IssueStage, cfg config.IssueTrackingConfig, allowLegacyDevelopment bool) (IssueStage, error) {
	switch stage {
	case IssueStageTriage:
		if firstNonBlank(cfg.RefinementLabels) != "" {
			return IssueStageRefinement, nil
		}
		if allowLegacyDevelopment && firstNonBlank(cfg.DevelopLabels) != "" {
			return IssueStageDevelopment, nil
		}
		return "", fmt.Errorf("%w: no refinement_labels configured", ErrStageTargetLabelMissing)
	case IssueStageRefinement:
		if firstNonBlank(cfg.DevelopLabels) != "" {
			return IssueStageDevelopment, nil
		}
		return "", fmt.Errorf("%w: no develop_labels configured", ErrStageTargetLabelMissing)
	case IssueStageDevelopment:
		return "", fmt.Errorf("%w: already in development stage", ErrNoNextStage)
	default:
		return "", fmt.Errorf("%w: unknown issue stage %q", ErrNoNextStage, stage)
	}
}

func TransitionIssueStage(ctx context.Context, client StageTransitionClient, tr StageTransition) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("stage promotion: nil GitHub client")
	}
	if tr.Issue == nil {
		return fmt.Errorf("stage promotion: nil issue")
	}
	if tr.From == "" || tr.To == "" {
		return fmt.Errorf("stage promotion: from/to stages are required")
	}
	if tr.From == tr.To {
		return nil
	}
	now := tr.Time
	if now.IsZero() {
		now = time.Now().UTC()
	}

	targetLabel := stageTargetLabel(tr.Config, tr.To)
	if targetLabel == "" {
		return fmt.Errorf("%w: no label configured for %s", ErrStageTargetLabelMissing, tr.To)
	}

	ensureStageLabel(client, tr.Issue.Repo, targetLabel, tr.To)

	// ADD before REMOVE. If add succeeds and remove fails, the issue is still in
	// a coherent, recoverable multi-label state. The classifier's "earliest stage
	// wins" rule prevents accidental forward progress until cleanup succeeds.
	if err := client.AddLabels(tr.Issue.Repo, tr.Issue.Number, []string{targetLabel}); err != nil {
		return fmt.Errorf("stage promotion: add target label %q: %w", targetLabel, err)
	}

	remove := stageLabelsExcept(tr.Config, tr.To, targetLabel)
	if err := client.RemoveLabels(tr.Issue.Repo, tr.Issue.Number, remove); err != nil {
		return fmt.Errorf("stage promotion: remove previous stage labels: %w", err)
	}

	commented := false
	if !tr.SuppressAudit && !hasRecentStagePromotionComment(tr.RecentComments, tr.To, now) {
		body := StagePromotionAuditComment(tr.From, tr.To, tr.Trigger, now)
		if _, err := client.PostComment(tr.Issue.Repo, tr.Issue.Number, body); err != nil {
			slog.Warn("stage promotion: audit comment failed",
				"repo", tr.Issue.Repo, "issue", tr.Issue.Number, "from", tr.From, "to", tr.To, "err", err)
		} else {
			commented = true
		}
	}

	if tr.Broker != nil {
		publishStagePromotionEvent(tr.Broker, tr.Issue, tr.StoreIssueID, tr.From, tr.To, tr.Trigger, now, commented || tr.SuppressAudit)
	}
	return nil
}

func StagePromotionAuditComment(from, to IssueStage, trigger StagePromotionTrigger, ts time.Time) string {
	return fmt.Sprintf("%s\n\n- From: `%s`\n- To: `%s`\n- Trigger: `%s`\n- Time: `%s`\n",
		stagePromotionHeading, from, to, trigger, ts.UTC().Format(time.RFC3339))
}

func stageTargetLabel(cfg config.IssueTrackingConfig, stage IssueStage) string {
	switch stage {
	case IssueStageTriage:
		return firstNonBlank(cfg.ReviewOnlyLabels)
	case IssueStageRefinement:
		return firstNonBlank(cfg.RefinementLabels)
	case IssueStageDevelopment:
		return firstNonBlank(cfg.DevelopLabels)
	default:
		return ""
	}
}

func stageLabelsExcept(cfg config.IssueTrackingConfig, keep IssueStage, targetLabel string) []string {
	keepSet := lowerSet(stageLabels(cfg, keep))
	targetKey := strings.ToLower(strings.TrimSpace(targetLabel))
	outSet := make(map[string]struct{})
	var out []string
	for _, stage := range []IssueStage{IssueStageTriage, IssueStageRefinement, IssueStageDevelopment} {
		if stage == keep {
			continue
		}
		for _, label := range stageLabels(cfg, stage) {
			clean := strings.TrimSpace(label)
			if clean == "" {
				continue
			}
			key := strings.ToLower(clean)
			if key == targetKey {
				continue
			}
			if _, keep := keepSet[key]; keep {
				continue
			}
			if _, seen := outSet[key]; seen {
				continue
			}
			outSet[key] = struct{}{}
			out = append(out, clean)
		}
	}
	return out
}

func stageLabels(cfg config.IssueTrackingConfig, stage IssueStage) []string {
	switch stage {
	case IssueStageTriage:
		return cfg.ReviewOnlyLabels
	case IssueStageRefinement:
		return cfg.RefinementLabels
	case IssueStageDevelopment:
		return cfg.DevelopLabels
	default:
		return nil
	}
}

func stageTransitionApplied(issue *github.Issue, cfg config.IssueTrackingConfig, to IssueStage) bool {
	if issue == nil {
		return false
	}
	targetLabel := stageTargetLabel(cfg, to)
	if targetLabel == "" {
		return false
	}
	have := lowerSet(issue.LabelNames())
	if _, ok := have[strings.ToLower(strings.TrimSpace(targetLabel))]; !ok {
		return false
	}
	for _, label := range stageLabelsExcept(cfg, to, targetLabel) {
		if _, ok := have[strings.ToLower(strings.TrimSpace(label))]; ok {
			return false
		}
	}
	return true
}

func firstNonBlank(labels []string) string {
	for _, label := range labels {
		if clean := strings.TrimSpace(label); clean != "" {
			return clean
		}
	}
	return ""
}

func ensureStageLabel(client StageTransitionClient, repo, label string, stage IssueStage) {
	if label == "" {
		return
	}
	color, description := stageLabelMetadata(stage)
	if err := client.CreateLabel(repo, label, color, description); err != nil {
		slog.Warn("stage promotion: ensure label failed, continuing with add",
			"repo", repo, "label", label, "stage", stage, "err", err)
	}
}

func stageLabelMetadata(stage IssueStage) (string, string) {
	switch stage {
	case IssueStageTriage:
		return "1d76db", "Heimdallm: triage issue"
	case IssueStageRefinement:
		return "5319e7", "Heimdallm: refine implementation plan"
	case IssueStageDevelopment:
		return "0e8a16", "Heimdallm: auto-implement issue"
	default:
		return "ededed", "Heimdallm issue stage"
	}
}

func hasRecentStagePromotionComment(comments []github.Comment, to IssueStage, now time.Time) bool {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for _, c := range comments {
		if c.CreatedAt.IsZero() || now.Sub(c.CreatedAt) > stagePromotionWindow || c.CreatedAt.After(now.Add(stagePromotionWindow)) {
			continue
		}
		if stagePromotionCommentMatches(c.Body, "", to) {
			return true
		}
	}
	return false
}

func hasStagePromotionCommentSince(comments []github.Comment, from, to IssueStage, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	for _, c := range comments {
		if c.CreatedAt.IsZero() || c.CreatedAt.Before(since) {
			continue
		}
		if stagePromotionCommentMatches(c.Body, from, to) {
			return true
		}
	}
	return false
}

func hasStagePromotionTargetCommentSince(comments []github.Comment, to IssueStage, since time.Time) bool {
	if since.IsZero() {
		return false
	}
	for _, c := range comments {
		if c.CreatedAt.IsZero() || c.CreatedAt.Before(since) {
			continue
		}
		if stagePromotionCommentMatches(c.Body, "", to) {
			return true
		}
	}
	return false
}

// stagePromotionCommentMatches treats an empty from/to stage as "do not filter
// on that field". This keeps recent-comment dedup compatible with older audit
// comments that only need to prove the target stage was already recorded.
func stagePromotionCommentMatches(body string, from, to IssueStage) bool {
	if !strings.Contains(body, stagePromotionHeading) {
		return false
	}
	if from != "" && !strings.Contains(body, fmt.Sprintf("- From: `%s`", from)) {
		return false
	}
	if to != "" && !strings.Contains(body, fmt.Sprintf("- To: `%s`", to)) {
		return false
	}
	return true
}

func publishStagePromotionEvent(broker Publisher, issue *github.Issue, storeIssueID int64, from, to IssueStage, trigger StagePromotionTrigger, ts time.Time, commented bool) {
	payload := map[string]any{
		"repo":            issue.Repo,
		"number":          issue.Number,
		"issue_number":    issue.Number,
		"issue_title":     issue.Title,
		"github_issue_id": issue.ID,
		"from_stage":      string(from),
		"to_stage":        string(to),
		"trigger":         string(trigger),
		"time":            ts.UTC().Format(time.RFC3339),
		"audit_comment":   commented,
	}
	if storeIssueID > 0 {
		payload["issue_id"] = storeIssueID
	}
	if b, err := json.Marshal(payload); err == nil {
		broker.Publish(sse.Event{Type: sse.EventIssuePromoted, Data: string(b)})
	}
}
