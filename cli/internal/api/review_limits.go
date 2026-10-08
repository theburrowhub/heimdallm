package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// ReviewLimitWindow is one window of a review budget from GET /review-limits.
type ReviewLimitWindow struct {
	Window  string    `json:"window"`
	Used    int       `json:"used"`
	Limit   int       `json:"limit"`
	ResetAt time.Time `json:"reset_at"`
}

// Exhausted reports whether the window has no free slot.
func (w ReviewLimitWindow) Exhausted() bool { return w.Limit > 0 && w.Used >= w.Limit }

// ReviewLimitStatus is the live usage of one budget (global, org, repo, agent).
type ReviewLimitStatus struct {
	Kind    string              `json:"kind"`
	Key     string              `json:"key"`
	Windows []ReviewLimitWindow `json:"windows"`
}

// reviewLimitLabelMax bounds a budget label printed to the terminal.
const reviewLimitLabelMax = 120

// Label names the budget for display, stripped of control bytes like every
// other daemon-supplied string the CLI prints.
func (s ReviewLimitStatus) Label() string {
	key := DisplayText(s.Key, reviewLimitLabelMax)
	switch s.Kind {
	case "global":
		return "all reviews"
	case "org":
		return "org " + key
	case "repo":
		return key
	case "agent":
		return "agent " + key
	}
	kind := DisplayText(s.Kind, reviewLimitLabelMax)
	if key == "" {
		return kind
	}
	return kind + " " + key
}

// GetReviewLimits returns live usage of every configured review budget.
func (c *Client) GetReviewLimits() ([]ReviewLimitStatus, error) {
	data, err := c.do("GET", "/review-limits")
	if err != nil {
		return nil, err
	}
	var out []ReviewLimitStatus
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parsing review limits: %w", err)
	}
	return out, nil
}
