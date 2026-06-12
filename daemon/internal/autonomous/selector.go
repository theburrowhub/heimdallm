// Package autonomous implements Heimdallm's fully-unattended end-to-end mode:
// it selects an issue (assigned-to-bot > unassigned > others), drives it
// through the existing triage/refinement/development pipeline single-flight,
// and lets the existing Tier 3 review loop react to reviews.
package autonomous

import (
	"context"
	"fmt"
	"strings"
)

// SelectorStore is the persistence surface the selector needs.
type SelectorStore interface {
	HasOpenAutoImplementPR(issueGithubID int64) (bool, error)
	SetIssueClaimedByAutonomous(issueID int64, claimed bool) error
	IsIssueClaimedByAutonomous(issueID int64) (bool, error)
}

// SelectorGH is the GitHub surface the selector needs.
type SelectorGH interface {
	BranchExists(repo, branch string) (bool, error)
	AddAssignees(repo string, number int, assignees []string) error
	PostComment(repo string, number int, body string) error
}

// CommentGenerator produces the coordination comment via the agent. The
// production implementation wraps the executor (review-only mode, no workdir)
// and MUST fence the untrusted issue body before prompting.
type CommentGenerator interface {
	GenerateCoordinationComment(ctx context.Context, c Candidate) (string, error)
}

// Candidate is one issue the selector may pick.
type Candidate struct {
	Repo      string
	Number    int
	GithubID  int64
	StoreID   int64
	Assignees []string
	Labels    []string
	Title     string
	Body      string
}

// Bucket identifies which cascade tier a candidate was selected from.
type Bucket int

const (
	BucketNone Bucket = iota
	BucketBotAssigned
	BucketUnassigned
	BucketOthers
)

func (b Bucket) String() string {
	switch b {
	case BucketBotAssigned:
		return "bot_assigned"
	case BucketUnassigned:
		return "unassigned"
	case BucketOthers:
		return "others"
	default:
		return "none"
	}
}

// Selector picks the next issue to drive autonomously.
type Selector struct {
	store        SelectorStore
	gh           SelectorGH
	branchPrefix string // prefix gitops uses for issue branches
	botLogin     string
	skipLabels   []string // skip_labels + blocked_labels (compared case-insensitively)
	takeOthers   bool
	reassign     bool
	commentGen   CommentGenerator
}

// NewSelector builds a Selector. skipLabels should already include both
// skip_labels and blocked_labels for the repos in scope.
//
// Configure must be called before Pick to set takeOthers/reassign/skipLabels;
// NewSelector defaults to permissive (takeOthers=true, reassign=true, no skip labels).
func NewSelector(store SelectorStore, gh SelectorGH, botLogin, branchPrefix string, commentGen CommentGenerator) *Selector {
	return &Selector{
		store: store, gh: gh, botLogin: botLogin, branchPrefix: branchPrefix,
		commentGen: commentGen, takeOthers: true, reassign: true,
	}
}

// Configure applies resolved autonomous settings for the current tick/scope.
func (s *Selector) Configure(takeOthers, reassign bool, skipLabels []string) {
	s.takeOthers = takeOthers
	s.reassign = reassign
	s.skipLabels = skipLabels
}

// Pick scans candidates in cascade order (bot-assigned > unassigned > others),
// skipping anything with a skip/blocked label or already started, and returns
// the first eligible candidate with the bucket it came from. Returns (nil,
// BucketNone, nil) when nothing is eligible.
func (s *Selector) Pick(ctx context.Context, cands []Candidate) (*Candidate, Bucket, error) {
	for _, bucket := range []Bucket{BucketBotAssigned, BucketUnassigned, BucketOthers} {
		if bucket == BucketOthers && !s.takeOthers {
			continue
		}
		for i := range cands {
			c := cands[i]
			if s.hasSkipLabel(c) || s.bucketOf(c) != bucket {
				continue
			}
			eligible, err := s.isEligible(ctx, c)
			if err != nil {
				return nil, BucketNone, err
			}
			if !eligible {
				continue
			}
			picked := c
			return &picked, bucket, nil
		}
	}
	return nil, BucketNone, nil
}

// isEligible reports whether the candidate is unstarted: not already claimed by
// a previous (possibly crashed) autonomous Drive, no open linked PR, and no
// remote branch referencing it. Conservative — on doubt it returns false
// (treated as started, so the selector skips it).
func (s *Selector) isEligible(ctx context.Context, c Candidate) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	hasPR, err := s.store.HasOpenAutoImplementPR(c.GithubID)
	if err != nil {
		return false, fmt.Errorf("autonomous: eligibility PR check: %w", err)
	}
	if hasPR {
		return false, nil
	}
	// Claimed flag: survives a daemon restart mid-Drive (the poller clears it
	// only on normal Drive completion). Without this check a crash during
	// triage/refinement — before any PR or branch exists — would let the next
	// tick re-pick the same issue and start a duplicate Drive.
	if c.StoreID != 0 {
		claimed, err := s.store.IsIssueClaimedByAutonomous(c.StoreID)
		if err != nil {
			return false, fmt.Errorf("autonomous: eligibility claimed check: %w", err)
		}
		if claimed {
			return false, nil
		}
	}
	branch := fmt.Sprintf("%s%d", s.branchPrefix, c.Number)
	hasBranch, err := s.gh.BranchExists(c.Repo, branch)
	if err != nil {
		return false, fmt.Errorf("autonomous: eligibility branch check: %w", err)
	}
	return !hasBranch, nil
}

func (s *Selector) bucketOf(c Candidate) Bucket {
	for _, a := range c.Assignees {
		if a == s.botLogin {
			return BucketBotAssigned
		}
	}
	if len(c.Assignees) == 0 {
		return BucketUnassigned
	}
	return BucketOthers
}

func (s *Selector) hasSkipLabel(c Candidate) bool {
	for _, l := range c.Labels {
		for _, skip := range s.skipLabels {
			if strings.EqualFold(l, skip) {
				return true
			}
		}
	}
	return false
}

// Claim marks the candidate as taken. For the "others" bucket it performs the
// courtesy step: add the bot as an assignee (keeping the original) and post an
// agent-generated coordination comment. For bot-assigned / unassigned buckets
// it only flags the store.
func (s *Selector) Claim(ctx context.Context, c Candidate, bucket Bucket) error {
	if c.StoreID != 0 {
		if err := s.store.SetIssueClaimedByAutonomous(c.StoreID, true); err != nil {
			return fmt.Errorf("autonomous: flag claimed: %w", err)
		}
	}
	if bucket != BucketOthers {
		return nil
	}
	if s.reassign {
		if err := s.gh.AddAssignees(c.Repo, c.Number, []string{s.botLogin}); err != nil {
			return fmt.Errorf("autonomous: reassign: %w", err)
		}
	}
	if s.commentGen != nil {
		body, err := s.commentGen.GenerateCoordinationComment(ctx, c)
		if err != nil {
			return fmt.Errorf("autonomous: generate coordination comment: %w", err)
		}
		if body != "" {
			if err := s.gh.PostComment(c.Repo, c.Number, body); err != nil {
				return fmt.Errorf("autonomous: post coordination comment: %w", err)
			}
		}
	}
	return nil
}
