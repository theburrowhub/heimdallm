package github

import (
	"encoding/json"
	"fmt"
	"io"
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

// FetchPRs fetches open PRs where the token owner is reviewer, assignee, or author.
// If repos is non-empty the search is scoped to those repos; otherwise all repos are searched.
func (c *Client) FetchPRs(repos []string) ([]*PullRequest, error) {
	var query string
	if len(repos) == 0 {
		query = "is:pr is:open (review-requested:@me OR assignee:@me OR author:@me)"
	} else {
		repoQuery := "repo:" + strings.Join(repos, " repo:")
		query = fmt.Sprintf("is:pr is:open (%s) (review-requested:@me OR assignee:@me OR author:@me)", repoQuery)
	}

	params := url.Values{}
	params.Set("q", query)
	params.Set("per_page", "100")

	resp, err := c.do("GET", "/search/issues?"+params.Encode(), "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: search PRs: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github: search PRs: status %d: %s", resp.StatusCode, body)
	}

	var result struct {
		Items []*PullRequest `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("github: decode search: %w", err)
	}
	return result.Items, nil
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
