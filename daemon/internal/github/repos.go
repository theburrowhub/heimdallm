package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CreatedPR holds the essential fields returned by GitHub when a PR is created.
type CreatedPR struct {
	Number  int
	ID      int64
	HTMLURL string
}

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
func (c *Client) CreatePR(repo, title, body, head, base string, draft bool) (*CreatedPR, error) {
	if repo == "" || title == "" || head == "" || base == "" {
		return nil, fmt.Errorf("github: CreatePR: repo/title/head/base are all required")
	}
	payload, err := json.Marshal(map[string]any{
		"title": title,
		"body":  body,
		"head":  head,
		"base":  base,
		"draft": draft,
	})
	if err != nil {
		return nil, fmt.Errorf("github: marshal pr payload: %w", err)
	}

	resp, err := c.doWithBody("POST", "/repos/"+repo+"/pulls",
		"application/vnd.github+json", "application/json", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("github: create pr: %w", err)
	}
	respBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if readErr != nil {
		return nil, fmt.Errorf("github: read pr response: %w", readErr)
	}
	// GitHub returns 201 Created on success; anything else is an error to surface.
	if resp.StatusCode != http.StatusCreated {
		errBody := safeTruncate(string(respBody), maxErrBodyLen)
		return nil, fmt.Errorf("github: create pr %s: status %d: %s", repo, resp.StatusCode, errBody)
	}

	var out struct {
		Number  int    `json:"number"`
		ID      int64  `json:"id"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("github: decode pr response: %w", err)
	}
	if out.Number == 0 {
		return nil, fmt.Errorf("github: create pr: response missing number (raw: %.200s)", respBody)
	}
	return &CreatedPR{Number: out.Number, ID: out.ID, HTMLURL: out.HTMLURL}, nil
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

// RemoveLabels removes one or more labels from an issue. GitHub has no bulk
// delete endpoint, so we issue one DELETE per label. A 404 (label not on the
// issue) is tolerated — promotion code that removes "blocked, queued" on an
// issue that only carries "blocked" should not fail just because "queued"
// was missing: the desired end state is already reached.
func (c *Client) RemoveLabels(repo string, number int, labels []string) error {
	if repo == "" || number == 0 || len(labels) == 0 {
		return nil
	}
	for _, label := range labels {
		path := fmt.Sprintf("/repos/%s/issues/%d/labels/%s", repo, number, url.PathEscape(label))
		resp, err := c.do("DELETE", path, "application/vnd.github+json")
		if err != nil {
			return fmt.Errorf("github: remove label %q: %w", label, err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			continue // already absent — desired state
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("github: remove label %s#%d %q: status %d: %s", repo, number, label, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
		}
	}
	return nil
}

// GetIssue fetches a single issue by repo + number. Used by the dependency
// promotion pass to check whether a referenced issue has been closed. Works
// across repos (same client credentials, REST `GET /repos/{owner}/{name}/
// issues/{number}`).
func (c *Client) GetIssue(repo string, number int) (*Issue, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: get issue %s#%d: %w", repo, number, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: get issue %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	var issue Issue
	if err := json.Unmarshal(body, &issue); err != nil {
		return nil, fmt.Errorf("github: decode issue %s#%d: %w", repo, number, err)
	}
	issue.Repo = repo
	return &issue, nil
}

// ListSubIssues returns the native GitHub sub-issues declared under a
// parent issue. GitHub's sub-issues are same-owner only (the REST endpoint
// refuses cross-owner children) but can span repos inside that owner, so
// each returned *Issue has its Repo resolved from the embedded repository
// object when present, falling back to the parent repo.
//
// Used by the dependency promotion pass to pick up deps declared via the
// native UI/API instead of (or alongside) a `## Depends on` Markdown
// section in the parent body. A 200 with an empty array is returned as a
// nil slice and nil error — GitHub's semantics for "no sub-issues".
func (c *Client) ListSubIssues(repo string, number int) ([]*Issue, error) {
	if repo == "" {
		return nil, fmt.Errorf("github: ListSubIssues: empty repo")
	}
	path := fmt.Sprintf("/repos/%s/issues/%d/sub_issues", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: list sub-issues %s#%d: %w", repo, number, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: list sub-issues %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	var out []*Issue
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("github: decode sub-issues %s#%d: %w", repo, number, err)
	}
	for _, issue := range out {
		if issue.Repository != nil && issue.Repository.FullName != "" {
			issue.Repo = issue.Repository.FullName
		} else {
			issue.Repo = repo
		}
	}
	return out, nil
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
