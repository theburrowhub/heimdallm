package github

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const defaultBaseURL = "https://api.github.com"

// maxBodyBytes limits the size of API response bodies read into memory to
// prevent out-of-memory conditions caused by unexpectedly large responses.
const maxBodyBytes = 1 * 1024 * 1024 // 1 MB for most API responses

// maxDiffBodyBytes allows a larger limit for PR diffs, which can be legitimately large.
const maxDiffBodyBytes = 10 * 1024 * 1024 // 10 MB for diffs

// maxErrBodyLen limits the number of bytes included in error messages to avoid
// leaking sensitive GitHub diagnostic information (e.g. token details).
const maxErrBodyLen = 200

// safeTruncate shortens s to at most max bytes, snapping on a rune boundary
// so we never emit a split multi-byte UTF-8 character in an error message.
// Returns s unchanged when it already fits.
func safeTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	// Walk back from `max` until we hit the start of a rune.
	i := max
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return "", fmt.Errorf("github: get user: status %d: %s", resp.StatusCode, errBody)
	}
	var u struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&u); err != nil {
		return "", fmt.Errorf("github: decode user: %w", err)
	}
	return u.Login, nil
}

func (c *Client) do(method, path string, accept string) (*http.Response, error) {
	return c.doWithBody(method, path, accept, "", nil)
}

// doWithBody is the POST/PUT/PATCH counterpart to do(). It accepts an
// optional body (nil for GET-like calls) and a content-type that is only
// set when a body is present. Every authenticated call should go through
// this helper so auth, Accept headers, and the pinned API version stay in
// one place.
//
// TODO: migrate SubmitReview and PostComment to doWithBody as well — they
// still build their request inline, duplicating the header setup.
func (c *Client) doWithBody(method, path, accept, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return c.http.Do(req)
}

// FetchPRsToReview returns all open PRs where the authenticated user is explicitly
// requested as reviewer, across ALL repos (no repo filter in the query).
//
// The repo filter is intentionally omitted: adding many `repo:` terms to the
// GitHub Search API query can exceed its length limit and silently return zero
// results. Filtering by monitored repos is done in the caller instead.
func (c *Client) FetchPRsToReview() ([]*PullRequest, error) {
	username, err := c.AuthenticatedUser()
	if err != nil {
		return nil, fmt.Errorf("github: resolve user: %w", err)
	}
	prs, err := c.fetchByQualifier(username, "review-requested", nil) // no repo filter
	if err != nil {
		return nil, err
	}
	slog.Info("github: PRs to review (all repos)", "count", len(prs))
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, fmt.Errorf("github: search PRs (%s): status %d: %s", qualifier, resp.StatusCode, errBody)
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
// Returns the GitHub review ID and the review state reported by the API —
// typically "APPROVED", "CHANGES_REQUESTED", or "COMMENTED" depending on the
// event and on GitHub's server-side rules. We pass the state through to the
// store so the web UI can show a review-decision badge sourced from GitHub
// rather than derived locally from severity.
func (c *Client) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number)

	payload := map[string]any{
		"body":  body,
		"event": event,
	}

	data, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", c.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return 0, "", fmt.Errorf("github: submit review: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.http.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("github: submit review: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != 200 {
		errBody := safeTruncate(string(respBody), maxErrBodyLen)
		return 0, "", fmt.Errorf("github: submit review: status %d: %s", resp.StatusCode, errBody)
	}

	var result struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return 0, "", fmt.Errorf("github: submit review: decode: %w", err)
	}
	return result.ID, result.State, nil
}

// PostComment posts a general comment on a PR (issue comment).
// Used in multi-feedback mode to post one comment per issue before the formal review.
// Returns the comment's creation timestamp as reported by GitHub so callers
// can record when the comment actually landed (see issue #222).
func (c *Client) PostComment(repo string, number int, body string) (time.Time, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number)
	payload := map[string]any{"body": body}
	data, _ := json.Marshal(payload)
	req, err := http.NewRequest("POST", c.baseURL+path, strings.NewReader(string(data)))
	if err != nil {
		return time.Time{}, fmt.Errorf("github: post comment: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := c.http.Do(req)
	if err != nil {
		return time.Time{}, fmt.Errorf("github: post comment: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusCreated {
		errBody := safeTruncate(string(respBody), maxErrBodyLen)
		return time.Time{}, fmt.Errorf("github: post comment: status %d: %s", resp.StatusCode, errBody)
	}
	var result struct {
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return time.Now().UTC(), nil
	}
	return result.CreatedAt, nil
}

// maxDiscoveryPages bounds the number of Search API pages consumed per org.
// GitHub caps search results at 1000 entries (10 pages × 100 per_page); we stop
// there to avoid endless pagination in the unlikely event of a malformed response.
const maxDiscoveryPages = 10

// FetchReposByTopic returns the full_names of non-archived, non-disabled repos
// that carry the given topic in any of the provided orgs. Empty topic or orgs
// return an empty slice without calling the API.
//
// Each org is queried independently so one failing org does not wipe the
// others — a partial result is returned alongside a joined error (see
// errors.Join) describing which orgs failed. Results are deduplicated across
// orgs.
func (c *Client) FetchReposByTopic(topic string, orgs []string) ([]string, error) {
	if topic == "" || len(orgs) == 0 {
		return nil, nil
	}

	seen := make(map[string]struct{})
	var repos []string
	var errs []error

	for _, org := range orgs {
		found, err := c.fetchReposForOrg(topic, org)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", org, err))
			continue
		}
		for _, r := range found {
			if _, dup := seen[r]; dup {
				continue
			}
			seen[r] = struct{}{}
			repos = append(repos, r)
		}
	}

	// Deterministic order makes the result easy to compare in tests and logs.
	sort.Strings(repos)

	if len(errs) > 0 {
		return repos, fmt.Errorf("github: discovery errors: %w", errors.Join(errs...))
	}
	return repos, nil
}

func (c *Client) fetchReposForOrg(topic, org string) ([]string, error) {
	// archived:false and fork filtering is handled post-fetch because Search API
	// does not honour the `fork:` qualifier reliably alongside `topic:`.
	query := fmt.Sprintf("topic:%s org:%s archived:false", topic, org)

	var repos []string
	totalFetched := 0
	for page := 1; page <= maxDiscoveryPages; page++ {
		params := url.Values{}
		params.Set("q", query)
		params.Set("per_page", "100")
		params.Set("page", strconv.Itoa(page))

		resp, err := c.do("GET", "/search/repositories?"+params.Encode(), "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("search repositories: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			errBody := safeTruncate(string(body), maxErrBodyLen)
			return nil, fmt.Errorf("search repositories: status %d: %s", resp.StatusCode, errBody)
		}

		var result struct {
			TotalCount int `json:"total_count"`
			Items      []struct {
				FullName string `json:"full_name"`
				Archived bool   `json:"archived"`
				Disabled bool   `json:"disabled"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode repositories: %w", err)
		}

		for _, it := range result.Items {
			if it.Archived || it.Disabled || it.FullName == "" {
				continue
			}
			repos = append(repos, it.FullName)
		}
		totalFetched += len(result.Items)

		// Stop when the page is partial (no more items upstream) OR when we
		// have already consumed the advertised total. The second check avoids
		// an extra empty request when total_count is an exact multiple of
		// per_page.
		if len(result.Items) < 100 || totalFetched >= result.TotalCount {
			break
		}
	}
	return repos, nil
}

// GetPRHeadSHA returns the PR's current HEAD commit SHA via the Pulls API.
// The Search Issues API used by FetchPRsToReview does not populate head.sha,
// so the pipeline needs this lookup to deduplicate reviews by commit rather
// than by the PR's updated_at (which any peer reviewer bumps on every review).
func (c *Client) GetPRHeadSHA(repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("github: get PR head sha: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return "", fmt.Errorf("github: get PR head sha (%s #%d): status %d: %s", repo, number, resp.StatusCode, errBody)
	}
	var pr PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return "", fmt.Errorf("github: get PR head sha: unmarshal: %w", err)
	}
	return pr.Head.SHA, nil
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
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return "", fmt.Errorf("github: fetch diff: status %d: %s", resp.StatusCode, errBody)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffBodyBytes))
	if err != nil {
		return "", fmt.Errorf("github: fetch diff (%s #%d): read: %w", repo, number, err)
	}
	if int64(len(data)) >= maxDiffBodyBytes {
		slog.Warn("github: diff truncated at size limit", "repo", repo, "pr", number, "limit_bytes", maxDiffBodyBytes)
	}
	return string(data), nil
}

// FetchComments retrieves both inline review comments and general issue comments
// for a PR, merged and sorted chronologically.
// Both endpoint calls run concurrently. An error from either is returned immediately.
func (c *Client) FetchComments(repo string, number int) ([]Comment, error) {
	type result struct {
		comments []Comment
		err      error
	}
	reviewCh := make(chan result, 1)
	issueCh := make(chan result, 1)

	go func() {
		comments, err := c.fetchReviewComments(repo, number)
		reviewCh <- result{comments, err}
	}()
	go func() {
		comments, err := c.fetchIssueComments(repo, number)
		issueCh <- result{comments, err}
	}()

	r1 := <-reviewCh
	r2 := <-issueCh
	if r1.err != nil {
		return nil, r1.err
	}
	if r2.err != nil {
		return nil, r2.err
	}

	all := append(r1.comments, r2.comments...)
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	return all, nil
}

func (c *Client) fetchReviewComments(repo string, number int) ([]Comment, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d/comments", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch review comments: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, fmt.Errorf("github: fetch review comments: status %d: %s", resp.StatusCode, errBody)
	}
	var raw []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body         string    `json:"body"`
		CreatedAt    time.Time `json:"created_at"`
		Path         string    `json:"path"`
		Line         *int      `json:"line"`
		OriginalLine int       `json:"original_line"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode review comments: %w", err)
	}
	comments := make([]Comment, len(raw))
	for i, r := range raw {
		line := r.OriginalLine
		if r.Line != nil {
			line = *r.Line
		}
		comments[i] = Comment{
			Author:    r.User.Login,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
			File:      r.Path,
			Line:      line,
		}
	}
	return comments, nil
}

func (c *Client) fetchIssueComments(repo string, number int) ([]Comment, error) {
	path := fmt.Sprintf("/repos/%s/issues/%d/comments", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch issue comments: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, fmt.Errorf("github: fetch issue comments: status %d: %s", resp.StatusCode, errBody)
	}
	var raw []struct {
		User struct {
			Login string `json:"login"`
		} `json:"user"`
		Body      string    `json:"body"`
		CreatedAt time.Time `json:"created_at"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode issue comments: %w", err)
	}
	comments := make([]Comment, len(raw))
	for i, r := range raw {
		comments[i] = Comment{
			Author:    r.User.Login,
			Body:      r.Body,
			CreatedAt: r.CreatedAt,
		}
	}
	return comments, nil
}

// FetchLabels returns the label names for a repository.
func (c *Client) FetchLabels(repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	resp, err := c.do("GET", fmt.Sprintf("/repos/%s/labels?per_page=100", repo), "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch labels: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: fetch labels %s: status %d", repo, resp.StatusCode)
	}
	var raw []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode labels: %w", err)
	}
	names := make([]string, len(raw))
	for i, l := range raw {
		names[i] = l.Name
	}
	return names, nil
}

// AddIssueLabel adds a label to an issue. No-op if the label is already present.
func (c *Client) AddIssueLabel(repo string, number int, label string) error {
	body := fmt.Sprintf(`{"labels":[%q]}`, label)
	resp, err := c.doWithBody("POST",
		fmt.Sprintf("/repos/%s/issues/%d/labels", repo, number),
		"application/vnd.github+json", "application/json",
		strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: add label: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: add label %q to %s#%d: status %d", label, repo, number, resp.StatusCode)
	}
	return nil
}

// RemoveIssueLabel removes a label from an issue. No-op if the label is not present (404 ignored).
func (c *Client) RemoveIssueLabel(repo string, number int, label string) error {
	resp, err := c.do("DELETE",
		fmt.Sprintf("/repos/%s/issues/%d/labels/%s", repo, number, url.PathEscape(label)),
		"application/vnd.github+json")
	if err != nil {
		return fmt.Errorf("github: remove label: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // label not present — no-op
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: remove label %q from %s#%d: status %d", label, repo, number, resp.StatusCode)
	}
	return nil
}

// FetchCollaborators returns the login names of repository collaborators.
func (c *Client) FetchCollaborators(repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	resp, err := c.do("GET", fmt.Sprintf("/repos/%s/collaborators?per_page=100", repo), "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch collaborators: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: fetch collaborators %s: status %d", repo, resp.StatusCode)
	}
	var raw []struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode collaborators: %w", err)
	}
	logins := make([]string, len(raw))
	for i, u := range raw {
		logins[i] = u.Login
	}
	return logins, nil
}
