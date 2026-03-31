package github

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultBaseURL = "https://api.github.com"

type Client struct {
	token   string
	baseURL string
	http    *http.Client
}

type Option func(*Client)

func WithBaseURL(u string) Option {
	return func(c *Client) { c.baseURL = u }
}

func NewClient(token string, opts ...Option) *Client {
	c := &Client{
		token:   token,
		baseURL: defaultBaseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// AuthenticatedUser returns the GitHub login of the token owner.
// Used to resolve the actual username instead of @me (which some token types reject).
func (c *Client) AuthenticatedUser() (string, error) {
	resp, err := c.do("GET", "/user", "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("github: get user: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github: get user: status %d: %s", resp.StatusCode, body)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", fmt.Errorf("github: decode user: %w", err)
	}
	return u.Login, nil
}

func (c *Client) do(method, path string, accept string) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	return c.http.Do(req)
}

// FetchPRsToReview returns only PRs where the authenticated user is explicitly
// requested as reviewer. ONLY these should be auto-reviewed by the AI pipeline.
// This prevents posting reviews on the user's own PRs or PRs they're just assigned to.
func (c *Client) FetchPRsToReview(repos []string) ([]*PullRequest, error) {
	username, err := c.AuthenticatedUser()
	if err != nil {
		return nil, fmt.Errorf("github: resolve user: %w", err)
	}
	prs, err := c.fetchByQualifier(username, "review-requested", repos)
	if err != nil {
		return nil, err
	}
	slog.Info("github: PRs to review", "count", len(prs))
	return prs, nil
}

// FetchPRs fetches all open PRs where the user is reviewer, assignee, or author.
// Used for the dashboard display only — NOT for triggering AI reviews.
func (c *Client) FetchPRs(repos []string) ([]*PullRequest, error) {
	username, err := c.AuthenticatedUser()
	if err != nil {
		return nil, fmt.Errorf("github: resolve user: %w", err)
	}

	qualifiers := []string{"review-requested", "assignee", "author"}
	seen := make(map[int64]struct{})
	var all []*PullRequest

	for _, q := range qualifiers {
		prs, err := c.fetchByQualifier(username, q, repos)
		if err != nil {
			slog.Warn("github: fetch PRs partial error", "qualifier", q, "err", err)
			continue
		}
		for _, pr := range prs {
			if _, dup := seen[pr.ID]; !dup {
				seen[pr.ID] = struct{}{}
				all = append(all, pr)
			}
		}
	}
	return all, nil
}

func (c *Client) fetchByQualifier(username, qualifier string, repos []string) ([]*PullRequest, error) {
	repoFilter := ""
	if len(repos) > 0 {
		repoFilter = " repo:" + strings.Join(repos, " repo:")
	}
	query := fmt.Sprintf("is:pr is:open %s:%s%s", qualifier, username, repoFilter)
	params := url.Values{}
	params.Set("q", query)
	params.Set("per_page", "100")

	resp, err := c.do("GET", "/search/issues?"+params.Encode(), "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: search PRs (%s): %w", qualifier, err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: search PRs (%s): status %d: %s", qualifier, resp.StatusCode, body)
	}
	var result struct {
		Items []*PullRequest `json:"items"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("github: decode search (%s): %w", qualifier, err)
	}
	return result.Items, nil
}

// SubmitReview posts an AI-generated review to GitHub as a PR review.
// event should be "REQUEST_CHANGES", "COMMENT", or "APPROVE".
// Returns the GitHub review ID.
func (c *Client) SubmitReview(repo string, number int, body, event string) (int64, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number)

	payload := map[string]any{
		"body":  body,
		"event": event,
	}

	data, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", c.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return 0, fmt.Errorf("github: submit review: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("github: submit review: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK && resp.StatusCode != 200 {
		return 0, fmt.Errorf("github: submit review: status %d: %s", resp.StatusCode, respBody)
	}

	var result struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, fmt.Errorf("github: submit review: decode: %w", err)
	}
	return result.ID, nil
}

// FetchDiff returns the unified diff for a PR.
func (c *Client) FetchDiff(repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github.v3.diff")
	if err != nil {
		return "", fmt.Errorf("github: fetch diff: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github: fetch diff: status %d: %s", resp.StatusCode, body)
	}
	data, err := io.ReadAll(resp.Body)
	return string(data), err
}
