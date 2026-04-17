package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
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
		errBody := string(body)
		if len(errBody) > maxErrBodyLen {
			errBody = errBody[:maxErrBodyLen]
		}
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
func (c *Client) CreatePR(repo, title, body, head, base string) (int, error) {
	if repo == "" || title == "" || head == "" || base == "" {
		return 0, fmt.Errorf("github: CreatePR: repo/title/head/base are all required")
	}
	payload, err := json.Marshal(map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
	})
	if err != nil {
		return 0, fmt.Errorf("github: marshal pr payload: %w", err)
	}

	req, err := http.NewRequest("POST", c.baseURL+"/repos/"+repo+"/pulls", strings.NewReader(string(payload)))
	if err != nil {
		return 0, fmt.Errorf("github: create pr: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.http.Do(req)
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
		errBody := string(respBody)
		if len(errBody) > maxErrBodyLen {
			errBody = errBody[:maxErrBodyLen]
		}
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
