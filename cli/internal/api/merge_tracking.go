package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"time"
)

// MergeCheck is one CI check or commit status on a tracked PR's head commit.
type MergeCheck struct {
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	State       string    `json:"state"` // success | pending | failure | neutral
	Required    bool      `json:"required"`
	Description string    `json:"description,omitempty"`
	App         string    `json:"app,omitempty"`
	URL         string    `json:"url,omitempty"`
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
