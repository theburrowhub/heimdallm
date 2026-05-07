package issues

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
)

type fakeStageClient struct {
	ops       []string
	comments  []string
	createErr error
	addErr    error
	removeErr error
}

func (f *fakeStageClient) CreateLabel(repo, name, color, description string) error {
	f.ops = append(f.ops, "create:"+name)
	return f.createErr
}

func (f *fakeStageClient) AddLabels(repo string, number int, labels []string) error {
	f.ops = append(f.ops, "add:"+strings.Join(labels, ","))
	return f.addErr
}

func (f *fakeStageClient) RemoveLabels(repo string, number int, labels []string) error {
	f.ops = append(f.ops, "remove:"+strings.Join(labels, ","))
	return f.removeErr
}

func (f *fakeStageClient) PostComment(repo string, number int, body string) (time.Time, error) {
	f.ops = append(f.ops, "comment")
	f.comments = append(f.comments, body)
	return time.Now().UTC(), nil
}

func stageCfg() config.IssueTrackingConfig {
	return config.IssueTrackingConfig{
		Enabled:          true,
		ReviewOnlyLabels: []string{"triage"},
		RefinementLabels: []string{"refine"},
		DevelopLabels:    []string{"develop"},
	}
}

func stageIssue() *github.Issue {
	return &github.Issue{
		ID:     99,
		Repo:   "org/repo",
		Number: 7,
		Title:  "Refine me",
	}
}

func TestNextStage(t *testing.T) {
	cfg := stageCfg()
	if got, err := NextStage(IssueStageTriage, cfg, false); err != nil || got != IssueStageRefinement {
		t.Fatalf("triage next = %q, %v; want refinement", got, err)
	}
	if got, err := NextStage(IssueStageRefinement, cfg, false); err != nil || got != IssueStageDevelopment {
		t.Fatalf("refinement next = %q, %v; want development", got, err)
	}
	if _, err := NextStage(IssueStageDevelopment, cfg, false); !errors.Is(err, ErrNoNextStage) {
		t.Fatalf("development next err = %v, want ErrNoNextStage", err)
	}

	cfg.RefinementLabels = nil
	if _, err := NextStage(IssueStageTriage, cfg, false); !errors.Is(err, ErrStageTargetLabelMissing) {
		t.Fatalf("triage without refinement err = %v, want ErrStageTargetLabelMissing", err)
	}
	if got, err := NextStage(IssueStageTriage, cfg, true); err != nil || got != IssueStageDevelopment {
		t.Fatalf("legacy triage next = %q, %v; want development", got, err)
	}
}

func TestTransitionIssueStage_AddsBeforeRemovingAndAudits(t *testing.T) {
	fake := &fakeStageClient{}
	err := TransitionIssueStage(context.Background(), fake, StageTransition{
		Issue:        stageIssue(),
		StoreIssueID: 12,
		Config:       stageCfg(),
		From:         IssueStageTriage,
		To:           IssueStageRefinement,
		Trigger:      StagePromotionManualAPI,
		Time:         time.Date(2026, 5, 7, 12, 43, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("TransitionIssueStage: %v", err)
	}
	wantOps := []string{"create:refine", "add:refine", "remove:triage,develop", "comment"}
	if strings.Join(fake.ops, "|") != strings.Join(wantOps, "|") {
		t.Fatalf("ops = %v, want %v", fake.ops, wantOps)
	}
	if len(fake.comments) != 1 || !strings.Contains(fake.comments[0], "- From: `triage`") || !strings.Contains(fake.comments[0], "- To: `refinement`") {
		t.Fatalf("audit comment not rendered as expected: %q", fake.comments)
	}
}

func TestTransitionIssueStage_MissingTargetLabelFailsBeforeMutating(t *testing.T) {
	cfg := stageCfg()
	cfg.RefinementLabels = nil
	fake := &fakeStageClient{}
	err := TransitionIssueStage(context.Background(), fake, StageTransition{
		Issue:   stageIssue(),
		Config:  cfg,
		From:    IssueStageTriage,
		To:      IssueStageRefinement,
		Trigger: StagePromotionAuto,
	})
	if !errors.Is(err, ErrStageTargetLabelMissing) {
		t.Fatalf("err = %v, want ErrStageTargetLabelMissing", err)
	}
	if len(fake.ops) != 0 {
		t.Fatalf("unexpected mutations before target validation: %v", fake.ops)
	}
}

func TestTransitionIssueStage_DeduplicatesRecentAuditComment(t *testing.T) {
	now := time.Date(2026, 5, 7, 12, 43, 0, 0, time.UTC)
	fake := &fakeStageClient{}
	err := TransitionIssueStage(context.Background(), fake, StageTransition{
		Issue:        stageIssue(),
		StoreIssueID: 12,
		Config:       stageCfg(),
		From:         IssueStageTriage,
		To:           IssueStageRefinement,
		Trigger:      StagePromotionAuto,
		Time:         now,
		RecentComments: []github.Comment{
			{
				CreatedAt: now.Add(-time.Minute),
				Body:      stagePromotionHeading + "\n\n- To: `refinement`\n",
			},
		},
	})
	if err != nil {
		t.Fatalf("TransitionIssueStage: %v", err)
	}
	for _, op := range fake.ops {
		if op == "comment" {
			t.Fatalf("dedup should skip audit comment, ops=%v", fake.ops)
		}
	}
}
