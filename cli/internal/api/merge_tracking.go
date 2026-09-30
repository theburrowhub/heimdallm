package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"time"
)

// MergeCheck is one CI check or commit status on a tracked PR's head commit.
type MergeCheck struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	State       string `json:"state"` // success | pending | failure | neutral
	Required    bool   `json:"required"`
	Description string `json:"description,omitempty"`
	App         string `json:"app,omitempty"`
	URL         string `json:"url,omitempty"`
	// Pointers: `omitempty` is a no-op for a struct, so value timestamps would
	// decode a queued check's absent ends as the zero time and read back as a
	// run that took no time at all.
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// MergeChecksSummary carries the counts the listing renders from.
type MergeChecksSummary struct {
	Total           int      `json:"total"`
	RequiredTotal   int      `json:"required_total"`
	RequiredSuccess int      `json:"required_success"`
	RequiredPending int      `json:"required_pending"`
	RequiredFailing int      `json:"required_failing"`
	OptionalFailing int      `json:"optional_failing"`
	MissingRequired []string `json:"missing_required,omitempty"`
	Truncated       bool     `json:"truncated"`
}

// MergeBlock is one reason a PR is not being merged.
type MergeBlock struct {
	Reason string `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// MergeDecision is the explainable decision the daemon recorded.
type MergeDecision struct {
	Ready         bool               `json:"ready"`
	Blocks        []MergeBlock       `json:"blocks,omitempty"`
	Checks        []MergeCheck       `json:"checks,omitempty"`
	ChecksSummary MergeChecksSummary `json:"checks_summary"`
}

// MergeTrackingEntry is a PR the authenticated user authored or is assigned to,
// with its merge-readiness state.
type MergeTrackingEntry struct {
	PRID   int64  `json:"pr_id"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title,omitempty"`
	URL    string `json:"url,omitempty"`
	Author string `json:"author,omitempty"`

	Phase       string `json:"phase"`
	HeadSHA     string `json:"head_sha,omitempty"`
	BaseRef     string `json:"base_ref,omitempty"`
	HeadRef     string `json:"head_ref,omitempty"`
	BlockReason string `json:"block_reason,omitempty"`
	BlockDetail string `json:"block_detail,omitempty"`

	IsAuthor   bool `json:"is_author"`
	IsAssignee bool `json:"is_assignee"`
	Excluded   bool `json:"excluded"`

	ChecksRequiredFailing int `json:"checks_required_failing"`
	ChecksRequiredPending int `json:"checks_required_pending"`

	AutoMergeMethod string `json:"auto_merge_method,omitempty"`
	PreRebaseSHA    string `json:"pre_rebase_sha,omitempty"`
	LastError       string `json:"last_error,omitempty"`

	// My PRs. Attention is who the PR is waiting on: none | action (you) |
	// ready (one click from merged) | waiting (reviewers, CI, automation).
	// Stale means no activity on GitHub for longer than [my_prs].stale_after.
	Attention      string     `json:"attention,omitempty"`
	Stale          bool       `json:"stale"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	TerminalAt     *time.Time `json:"terminal_at,omitempty"`

	// Decision is only populated by the detail endpoint.
	Decision *MergeDecision `json:"decision,omitempty"`
}

// BlockedByChecks reports whether CI is what is holding this PR up.
func (e MergeTrackingEntry) BlockedByChecks() bool {
	switch e.BlockReason {
	case "checks_failing", "checks_pending", "required_check_missing", "checks_unknown":
		return true
	default:
		return false
	}
}

// Terminal reports whether the PR has reached a state it will not leave.
func (e MergeTrackingEntry) Terminal() bool {
	return e.Phase == "merged" || e.Phase == "abandoned"
}

// NeedsOperator reports whether the PR asks for its owner: something to fix,
// a merge to click, or it has gone quiet.
func (e MergeTrackingEntry) NeedsOperator() bool {
	if e.Terminal() || e.Excluded {
		return false
	}
	return e.Attention == "action" || e.Attention == "ready" || e.Stale
}

// AttentionLabel is a short phrase for who the PR is waiting on, with the stale
// flag appended. Empty for finished PRs, whose phase says it all.
func (e MergeTrackingEntry) AttentionLabel() string {
	if e.Terminal() {
		return ""
	}
	label := "waiting"
	switch e.Attention {
	case "action":
		label = "needs you"
	case "ready":
		label = "ready to merge"
	}
	if e.Stale {
		label += ", stale"
	}
	return label
}

// SortMyPRs orders entries the way the My PRs tab groups them: needs you,
// ready to merge, waiting, then merged or closed. Stable, so the daemon's own
// order (CI problems first) survives inside each group.
func SortMyPRs(entries []MergeTrackingEntry) {
	rank := func(e MergeTrackingEntry) int {
		switch {
		case e.Terminal():
			return 3
		case e.Excluded:
			return 2
		case e.Attention == "action":
			return 0
		case e.Attention == "ready":
			return 1
		default:
			return 2
		}
	}
	sort.SliceStable(entries, func(i, j int) bool { return rank(entries[i]) < rank(entries[j]) })
}

// ListMergeTracking fetches the tracked PRs, ordered by the daemon so the ones
// blocked by CI come first.
func (c *Client) ListMergeTracking() ([]MergeTrackingEntry, error) {
	data, err := c.do("GET", "/merge-tracking")
	if err != nil {
		return nil, err
	}
	var entries []MergeTrackingEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing merge tracking: %w", err)
	}
	return entries, nil
}

// GetMergeTracking fetches one tracked PR including the per-check breakdown.
func (c *Client) GetMergeTracking(prID int64) (*MergeTrackingEntry, error) {
	data, err := c.do("GET", fmt.Sprintf("/merge-tracking/%d", prID))
	if err != nil {
		return nil, err
	}
	var entry MergeTrackingEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("parsing merge tracking entry: %w", err)
	}
	return &entry, nil
}

// EvaluateMergeTracking re-evaluates one tracked PR against GitHub. dryRun
// records the decision without acting on it.
func (c *Client) EvaluateMergeTracking(prID int64, dryRun bool) (*MergeTrackingEntry, error) {
	path := fmt.Sprintf("/merge-tracking/%d/evaluate", prID)
	if dryRun {
		path += "?" + url.Values{"dry_run": {"true"}}.Encode()
	}
	data, err := c.do("POST", path)
	if err != nil {
		return nil, err
	}
	var entry MergeTrackingEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("parsing merge tracking entry: %w", err)
	}
	return &entry, nil
}
