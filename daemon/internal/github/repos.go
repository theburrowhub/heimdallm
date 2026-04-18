package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GetDefaultBranch returns the `default_branch` field from the GitHub
// repository metadata. Used by the auto_implement pipeline (#27) to base
// the work branch on the right trunk — main, master, whatever the repo
// defaults to — without assuming.
func (c *Client) GetDefaultBranch(repo string) (string, error) {
	if repo == "" {
		return "", fmt.Errorf("github: GetDefaultBranch: empty repo")
	}
	resp, err := c.do("GET", "/repos/"+repo, "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("github: get repo %s: %w", repo, err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if readErr != nil {
		return "", fmt.Errorf("github: read repo %s: %w", repo, readErr)
	}
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return "", fmt.Errorf("github: get repo %s: status %d: %s", repo, resp.StatusCode, errBody)
	}

	var out struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("github: decode repo %s: %w", repo, err)
	}
	if out.DefaultBranch == "" {
		return "", fmt.Errorf("github: repo %s has empty default_branch", repo)
	}
	return out.DefaultBranch, nil
}

// CreatePR opens a pull request in the given repo and returns the PR number.
// head may be either "branch" (same repo) or "owner:branch" (cross-repo);
// the auto_implement pipeline always pushes to the monitored repo, so "branch"
// is the normal case.
//
// Uses the shared doWithBody helper so auth, Accept, and API-version headers
// are set in one place. That also means any retry/rate-limit logic added to
// the helper in the future applies uniformly to PR creation.
func (c *Client) CreatePR(repo, title, body, head, base string, draft bool) (int, error) {
	if repo == "" || title == "" || head == "" || base == "" {
		return 0, fmt.Errorf("github: CreatePR: repo/title/head/base are all required")
	}
	payload, err := json.Marshal(map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
		"draft": draft,
	})
	if err != nil {
		return 0, fmt.Errorf("github: marshal pr payload: %w", err)
	}

	resp, err := c.doWithBody("POST", "/repos/"+repo+"/pulls",
		"application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, fmt.Errorf("github: create pr: %w", err)
	}
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if readErr != nil {
		return 0, fmt.Errorf("github: read pr response: %w", readErr)
	}
	// GitHub returns 201 Created on success; anything else is an error to surface.
	if resp.StatusCode != http.StatusCreated {
		errBody := safeTruncate(string(respBody), maxErrBodyLen)
		return 0, fmt.Errorf("github: create pr %s: status %d: %s", repo, resp.StatusCode, errBody)
	}

	var out struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return 0, fmt.Errorf("github: decode pr response: %w", err)
	}
	if out.Number == 0 {
		return 0, fmt.Errorf("github: create pr: response missing number (raw: %.200s)", respBody)
	}
	return out.Number, nil
}

// SetPRReviewers requests reviewers on a pull request.
func (c *Client) SetPRReviewers(repo string, prNumber int, reviewers []string) error {
	if repo == "" || prNumber == 0 || len(reviewers) == 0 {
		return nil // nothing to do
	}
	payload, err := json.Marshal(map[string]any{
		"reviewers": reviewers,
	})
	if err != nil {
		return fmt.Errorf("github: marshal reviewers: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/requested_reviewers", repo, prNumber)
	resp, err := c.doWithBody("POST", path, "application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github: set pr reviewers: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github: set pr reviewers %s#%d: status %d: %s", repo, prNumber, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
}

// AddLabels adds labels to an issue or pull request.
func (c *Client) AddLabels(repo string, number int, labels []string) error {
	if repo == "" || number == 0 || len(labels) == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"labels": labels,
	})
	if err != nil {
		return fmt.Errorf("github: marshal labels: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/labels", repo, number)
	resp, err := c.doWithBody("POST", path, "application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github: add labels: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: add labels %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
}

// SetAssignees sets assignees on an issue or pull request.
func (c *Client) SetAssignees(repo string, number int, assignees []string) error {
	if repo == "" || number == 0 || len(assignees) == 0 {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"assignees": assignees,
	})
	if err != nil {
		return fmt.Errorf("github: marshal assignees: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/assignees", repo, number)
	resp, err := c.doWithBody("POST", path, "application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github: set assignees: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("github: set assignees %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
}
