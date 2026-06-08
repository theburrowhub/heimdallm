package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// PRReview models a single entry returned by
// GET /repos/{owner}/{repo}/pulls/{n}/reviews. Only the fields the
// review-state vigilance flow needs (#482) are unmarshalled — the API
// payload also includes commit_id, html_url, etc., which we ignore.
type PRReview struct {
	ID          int64     `json:"id"`
	User        User      `json:"user"`
	State       string    `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	Body        string    `json:"body"`
	SubmittedAt time.Time `json:"submitted_at"`
}

// GetPRReviews returns the chronological list of reviews for a PR.
// Tier 3 calls this only for PRs that auto_implement created (i.e. the
// PR row carries `auto_implement_issue_id != 0`); the cost is bounded
// by the number of such PRs in the watch bucket, not the total PR
// count. The list is small in practice (one entry per reviewer per
// round), so a single page is sufficient — pagination would be a
// follow-up if a very chatty PR ever overflows.
func (c *Client) GetPRReviews(repo string, number int) ([]PRReview, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews?per_page=100", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: get PR reviews: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, &APIError{StatusCode: resp.StatusCode, Body: errBody}
	}
	var out []PRReview
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github: get PR reviews: unmarshal: %w", err)
	}
	return out, nil
}
