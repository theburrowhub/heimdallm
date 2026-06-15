package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const defaultBaseURL = "https://api.github.com"

// maxBodyBytes limits the size of API response bodies read into memory to
// prevent out-of-memory conditions caused by unexpectedly large responses.
const maxBodyBytes = 1 * 1024 * 1024 // 1 MB for most API responses

// maxDiffBodyBytes allows a larger limit for PR diffs, which can be legitimately large.
const maxDiffBodyBytes = 10 * 1024 * 1024 // 10 MB for diffs

// maxPaginatedPageBytes is the body ceiling for the paginated endpoints
// (fetchReviewComments, fetchIssueComments, GetPRTimelineEventsForReviewer).
// These request per_page=100 items per call, and a single comment can
// legitimately carry 30 KB+ (long stack traces, big code blocks,
// multi-paragraph review bodies), so a full page can exceed the generic
// 1 MiB maxBodyBytes — truncating mid-JSON and aborting pagination with a
// decode error, which drops the newest comments (#518, the failure mode
// #512 exists to prevent). 5 MiB gives ~50 KB per item at a full page,
// comfortably above anything observed, while still bounding exposure to a
// misbehaving server.
const maxPaginatedPageBytes = 5 * 1024 * 1024 // 5 MB for per_page=100 endpoints

// maxCacheBodyBytes is the maximum body size that will be stored in the ETag
// cache. Bodies larger than this limit are still served in full to the caller
// (read up to maxDiffBodyBytes) but are not kept in memory across requests.
// Set to maxBodyBytes (1 MiB) so the cached-entry memory budget is bounded
// regardless of which Accept type was used to fetch the resource.
const maxCacheBodyBytes = maxBodyBytes // 1 MB

// maxErrBodyLen limits the number of bytes included in error messages to avoid
// leaking sensitive GitHub diagnostic information (e.g. token details).
const maxErrBodyLen = 200

var labelColorRE = regexp.MustCompile(`^[0-9A-Fa-f]{6}$`)

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
	token         string
	baseURL       string
	http          *http.Client
	cache         *ConditionalCache
	rateObs       RateLimitObserver
	cacheDisabled atomic.Bool // when true, ETag conditional-request layer is bypassed
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
		cache:   NewConditionalCache(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// CacheStats returns the number of ETag cache hits (304→200 transparently
// converted) and misses (200 responses stored) since this client was created.
// Intended for observability / metrics; callers must not depend on exact values.
func (c *Client) CacheStats() (hits, misses int64) {
	return c.cache.CacheHits(), c.cache.CacheMisses()
}

// SetCacheEnabled enables or disables the conditional ETag cache. When
// disabled every GET request is made without an If-None-Match header, forcing
// a full 200 response from GitHub. Thread-safe; takes effect on the next
// request. Defaults to enabled (true).
func (c *Client) SetCacheEnabled(enabled bool) {
	c.cacheDisabled.Store(!enabled)
}

// SetRateObserver registers an observer that will be called after every HTTP
// response (success or error status) made through do() or doWithBody().
// The observer receives the raw *http.Response to inspect headers.
// Replaces any previously registered observer. Pass nil to disable.
func (c *Client) SetRateObserver(o RateLimitObserver) {
	c.rateObs = o
}

// notifyRateObserver calls the registered observer if one is set.
// Must be called AFTER the response is ready but BEFORE the body is consumed
// by this layer (headers are what the observer needs).
func (c *Client) notifyRateObserver(resp *http.Response) {
	if c.rateObs != nil && resp != nil {
		c.rateObs.ObserveResponse(resp)
	}
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
	if method != "GET" {
		return c.doWithBody(method, path, accept, "", nil)
	}

	// ── ETag conditional-request layer (GET only) ────────────────────────────
	// When the cache is disabled via SetCacheEnabled(false), bypass the ETag
	// layer entirely and make a plain unconditional GET. doWithBody already
	// calls notifyRateObserver so all rate-limit accounting still applies.
	if c.cacheDisabled.Load() {
		return c.doWithBody(method, path, accept, "", nil)
	}
	// If we have a cached ETag for this path, send If-None-Match so GitHub can
	// return 304 Not Modified (which does NOT count against the rate limit).
	// On 304: swap the empty body for the cached bytes and re-report 200 so
	// every caller transparently gets the data it already decoded once.
	// On 200 with a new ETag: read+buffer the body, store it, hand the caller
	// a fresh reader so it can decode normally.
	// Any other status: passthrough without touching the cache.
	//
	// The key includes the Accept value because the same path can be requested
	// with different Accept headers (e.g. application/vnd.github+json vs
	// application/vnd.github.v3.diff for the pulls endpoint) and must produce
	// separate cache entries. See cacheKey for details.
	key := cacheKey(method, accept, path)
	cachedETag, cachedBody, hasCached := c.cache.Get(key)

	req, err := http.NewRequest(method, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if hasCached && cachedETag != "" {
		req.Header.Set("If-None-Match", cachedETag)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}

	// Notify the rate-limit observer with the raw response (headers only —
	// the observer must not consume the body). Called before the ETag cache
	// layer modifies the response so the observer sees the real status code
	// and headers GitHub sent (including 304, which does not count against
	// the budget and carries up-to-date X-RateLimit-* values).
	c.notifyRateObserver(resp)

	switch resp.StatusCode {
	case http.StatusNotModified: // 304
		// Must drain and close the (empty) server body.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()

		if !hasCached || cachedBody == nil {
			// Defensive: 304 with nothing cached — this shouldn't happen with a
			// well-behaved server, but we must not return a nil body to callers.
			// Treat it as a passthrough empty 304 that callers will handle as an
			// error (non-200 status).
			resp.Body = http.NoBody
			return resp, nil
		}

		c.cache.cacheHits.Add(1)
		resp.StatusCode = http.StatusOK
		resp.Body = io.NopCloser(bytes.NewReader(cachedBody))
		return resp, nil

	case http.StatusOK:
		newETag := resp.Header.Get("ETag")
		if newETag == "" {
			// No ETag — pass through without caching.
			return resp, nil
		}
		// Do not cache paginated responses: a 304 re-serve would lose the
		// Link header and break the caller's cursor-based pagination
		// (e.g. FetchCollaborators). Serve the body normally without storing.
		if resp.Header.Get("Link") != "" {
			return resp, nil
		}
		// Buffer the full body up to maxDiffBodyBytes so that large diffs
		// (up to 10 MiB) are never silently truncated by the cache layer.
		// We read with the larger limit here; callers that need only a
		// smaller bound (most JSON endpoints) apply their own LimitReader
		// after receiving the response. The body is served in full regardless
		// of size, but is only stored in the cache when it is small enough
		// not to blow the per-entry memory budget (maxCacheBodyBytes).
		data, readErr := io.ReadAll(io.LimitReader(resp.Body, maxDiffBodyBytes))
		resp.Body.Close()
		if readErr != nil {
			// Return an error-wrapping response with a body so callers that
			// do resp.Body.Close() don't panic.
			resp.Body = http.NoBody
			return resp, fmt.Errorf("github: etag cache: read body: %w", readErr)
		}
		if len(data) <= maxCacheBodyBytes {
			c.cache.Put(key, newETag, data)
			c.cache.cacheMisses.Add(1)
		}
		resp.Body = io.NopCloser(bytes.NewReader(data))
		return resp, nil

	default:
		// Non-200/304 status — pass through unchanged; do not cache.
		return resp, nil
	}
}

// doWithBody is the POST/PUT/PATCH counterpart to do(). It accepts an
// optional body (nil for GET-like calls) and a content-type that is only
// set when a body is present. Every authenticated call should go through
// this helper so auth, Accept headers, and the pinned API version stay in
// one place.
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
	resp, err := c.http.Do(req)
	if err != nil {
		return resp, err
	}
	// Notify the rate-limit observer so POST/PUT/PATCH responses also update
	// the live budget. The observer inspects headers only; the body is untouched.
	c.notifyRateObserver(resp)
	return resp, nil
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
	prs, skipped := filterSelfAuthoredPRs(prs, username)
	if skipped > 0 {
		slog.Info("github: self-authored PRs skipped by review scope", "count", skipped, "user", username)
	}
	slog.Info("github: PRs to review (all repos)", "count", len(prs))
	return prs, nil
}

func filterSelfAuthoredPRs(prs []*PullRequest, username string) ([]*PullRequest, int) {
	username = normalizeGitHubLoginForCompare(username)
	if username == "" || len(prs) == 0 {
		return prs, 0
	}
	filtered := make([]*PullRequest, 0, len(prs))
	skipped := 0
	for _, pr := range prs {
		if pr != nil && githubLoginsEqual(pr.User.Login, username) {
			skipped++
			continue
		}
		filtered = append(filtered, pr)
	}
	return filtered, skipped
}

func githubLoginsEqual(a, b string) bool {
	a = normalizeGitHubLoginForCompare(a)
	b = normalizeGitHubLoginForCompare(b)
	return a != "" && b != "" && strings.EqualFold(a, b)
}

func normalizeGitHubLoginForCompare(login string) string {
	return strings.TrimSpace(strings.TrimLeft(login, "@"))
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

// APIError is an HTTP error from the GitHub API with a typed StatusCode.
// Used by callers to detect specific error classes (e.g. 404 Not Found).
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("github: API error (status %d): %s", e.StatusCode, e.Body)
}

// PermanentSubmitError signals that SubmitReview hit a state from
// which no retry will recover — e.g. the PR's conversation is locked,
// the PR has been deleted, or the repo was archived. Callers should
// mark the local review row as orphaned (via the same sentinel as
// the no-repo path in pipeline.PublishPending) instead of retrying
// every poll cycle. See theburrowhub/heimdallm#325.
type PermanentSubmitError struct {
	StatusCode int    // HTTP status code returned by GitHub (always 422 today)
	Reason     string // short human-readable label, e.g. "pr_locked"
	Body       string // safe-truncated response body for diagnostics
}

func (e *PermanentSubmitError) Error() string {
	return fmt.Sprintf("github: submit review permanent failure (status %d, reason %s): %s",
		e.StatusCode, e.Reason, e.Body)
}

// classifyPermanentSubmit422 inspects an HTTP 422 response body and
// returns (reason, true) when the failure is known to be permanent for
// the daemon — i.e. no future retry can succeed without operator
// intervention. Returns ("", false) for transient or unknown 422s so
// the caller falls back to the existing retry path.
//
// Currently classified as permanent:
//   - "lock prevents review" — PR conversation is locked. Unlocking
//     requires the operator to use the GitHub UI; the daemon cannot
//     recover on its own.
//
// Extend with care: a false positive here marks a recoverable review
// as orphan and loses it permanently. When in doubt, leave the case
// out and let the retry loop run — it is rate-limited by the poll
// interval and only burns 1 GitHub API call per cycle.
func classifyPermanentSubmit422(status int, body []byte) (string, bool) {
	if status != http.StatusUnprocessableEntity {
		return "", false
	}
	lower := strings.ToLower(string(body))
	if strings.Contains(lower, "lock prevents review") || strings.Contains(lower, "pull request is locked") {
		return "pr_locked", true
	}
	return "", false
}

// SubmitReview posts an AI-generated review to GitHub as a PR review.
// event should be "REQUEST_CHANGES", "COMMENT", or "APPROVE".
// Returns the GitHub review ID and the review state reported by the API —
// typically "APPROVED", "CHANGES_REQUESTED", or "COMMENTED" depending on the
// event and on GitHub's server-side rules. We pass the state through to the
// store so the web UI can show a review-decision badge sourced from GitHub
// rather than derived locally from severity.
//
// On HTTP 422 with a body that indicates the PR is in a state from which
// no retry can recover (e.g. the conversation has been locked), returns
// a *PermanentSubmitError so the caller can mark the local review row
// as orphaned instead of retrying every poll cycle. See
// theburrowhub/heimdallm#325.
func (c *Client) SubmitReview(repo string, number int, body, event string) (int64, string, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d/reviews", repo, number)

	payload := map[string]any{
		"body":  body,
		"event": event,
	}

	data, _ := json.Marshal(payload)
	resp, err := c.doWithBody("POST", path, "application/vnd.github+json", "application/json", strings.NewReader(string(data)))
	if err != nil {
		return 0, "", fmt.Errorf("github: submit review: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != 200 {
		errBody := safeTruncate(string(respBody), maxErrBodyLen)
		if reason, ok := classifyPermanentSubmit422(resp.StatusCode, respBody); ok {
			// Body carries the FULL response (not safe-truncated) so an
			// operator inspecting *PermanentSubmitError can see the full
			// GitHub error payload — the reason substring may live past
			// the maxErrBodyLen cutoff that the generic-error path uses.
			return 0, "", &PermanentSubmitError{
				StatusCode: resp.StatusCode,
				Reason:     reason,
				Body:       string(respBody),
			}
		}
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
	resp, err := c.doWithBody("POST", path, "application/vnd.github+json", "application/json", strings.NewReader(string(data)))
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

// MergePR merges a pull request using the given method ("squash"|"merge"|
// "rebase"). Built for the autonomous merge gate, which is disabled by
// default — this only runs when AutoMerge is explicitly enabled.
func (c *Client) MergePR(repo string, number int, method string) error {
	if method == "" {
		method = "squash"
	}
	payload, err := json.Marshal(map[string]any{"merge_method": method})
	if err != nil {
		return fmt.Errorf("github: marshal merge: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number)
	resp, err := c.doWithBody("PUT", path, "application/vnd.github+json", "application/json", strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("github: merge PR %s#%d: %w", repo, number, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github: merge PR %s#%d: status %d: %s", repo, number, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
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

// IsRepoArchived checks whether a repository is archived or has been deleted.
// Returns true for archived repos and repos that return 404 (deleted/transferred).
// Non-200/404 responses are treated as errors and the caller should assume the
// repo is still active (fail-open, so transient API errors do not purge repos).
func (c *Client) IsRepoArchived(repo string) (bool, error) {
	path := fmt.Sprintf("/repos/%s", repo)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return false, fmt.Errorf("github: check repo archived: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))

	if resp.StatusCode == http.StatusNotFound {
		return true, nil
	}
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return false, fmt.Errorf("github: check repo archived: status %d: %s", resp.StatusCode, errBody)
	}

	var result struct {
		Archived bool `json:"archived"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return false, fmt.Errorf("github: decode repo: %w", err)
	}
	return result.Archived, nil
}

// PRHeadInfo bundles the HEAD SHA and the pending-reviewer logins returned by
// the Pulls API. Tier 2 uses ReviewRequestedFor to confirm the bot is still a
// pending reviewer (the Search API index can lag behind the actual
// requested_reviewers list).
type PRHeadInfo struct {
	HeadSHA            string
	RequestedReviewers []string // lowercased logins
}

// ReviewRequestedFor reports whether login (case-insensitive) is still in the
// PR's requested_reviewers list. Returns false when the list is empty (which
// happens after the bot submits a review).
func (info PRHeadInfo) ReviewRequestedFor(login string) bool {
	lower := strings.ToLower(login)
	for _, r := range info.RequestedReviewers {
		if r == lower {
			return true
		}
	}
	return false
}

// getPR fetches a single PR via the Pulls API and returns the unmarshalled
// struct. Shared by GetPRHeadSHA (pipeline interface — returns only the SHA)
// and GetPRHeadInfo (tier 2 — returns SHA + requested_reviewers).
func (c *Client) getPR(repo string, number int) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: get PR: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, fmt.Errorf("github: get PR (%s #%d): status %d: %s", repo, number, resp.StatusCode, errBody)
	}
	var pr PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("github: get PR: unmarshal: %w", err)
	}
	return &pr, nil
}

// GetPRHeadSHA returns the PR's current HEAD commit SHA via the Pulls API.
// The Search Issues API used by FetchPRsToReview does not populate head.sha,
// so the pipeline needs this lookup to deduplicate reviews by commit rather
// than by the PR's updated_at (which any peer reviewer bumps on every review).
func (c *Client) GetPRHeadSHA(repo string, number int) (string, error) {
	pr, err := c.getPR(repo, number)
	if err != nil {
		return "", err
	}
	return pr.Head.SHA, nil
}

// GetPRHeadInfo returns the HEAD SHA and pending reviewer logins for a PR.
// Used by the tier-2 adapter to (a) resolve the HEAD SHA and (b) confirm the
// bot is still in requested_reviewers before enqueuing a review — without an
// extra API call, since both fields come from the same GET /pulls/{n} response.
func (c *Client) GetPRHeadInfo(repo string, number int) (PRHeadInfo, error) {
	pr, err := c.getPR(repo, number)
	if err != nil {
		return PRHeadInfo{}, err
	}
	logins := make([]string, len(pr.RequestedReviewers))
	for i, u := range pr.RequestedReviewers {
		logins[i] = strings.ToLower(u.Login)
	}
	return PRHeadInfo{HeadSHA: pr.Head.SHA, RequestedReviewers: logins}, nil
}

// PRSnapshot is the subset of PR fields Tier 3's guard evaluator needs.
// Returned by GetPRSnapshot in one call so the watch tier doesn't have to
// combine GetIssue + GetPRHeadSHA. The Pulls API returns state="closed" for
// merged PRs already (merged_at is non-null but the top-level state is still
// normalised to "closed"), so Tier 3's not_open guard fires correctly without
// an explicit merged_at check.
type PRSnapshot struct {
	State     string
	Draft     bool
	Author    string
	UpdatedAt time.Time
	HeadSHA   string
}

// GetPRSnapshot returns the current state, draft flag, author, updated_at,
// and HEAD SHA of a PR via the Pulls API. Tier 3 uses this to refresh state
// before re-reviewing a watched PR so a merged/closed PR does not burn AI
// tokens on a stale open-PR record.
func (c *Client) GetPRSnapshot(repo string, number int) (*PRSnapshot, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: get PR snapshot: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, &APIError{StatusCode: resp.StatusCode, Body: errBody}
	}
	var pr PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("github: get PR snapshot: unmarshal: %w", err)
	}
	return &PRSnapshot{
		State:     pr.State,
		Draft:     pr.Draft,
		Author:    pr.User.Login,
		UpdatedAt: pr.UpdatedAt,
		HeadSHA:   pr.Head.SHA,
	}, nil
}

// GetPR returns the full PullRequest struct for a single PR via the Pulls API.
// Used by the NATS review worker to hydrate a PRReviewMsg into a full PR.
func (c *Client) GetPR(repo string, number int) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: get PR: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, fmt.Errorf("github: get PR (%s #%d): status %d: %s", repo, number, resp.StatusCode, errBody)
	}
	var pr PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("github: get PR: unmarshal: %w", err)
	}
	// Populate the Repo field the same way FetchPRs does.
	if pr.Head.Repo.FullName != "" {
		pr.Repo = pr.Head.Repo.FullName
	} else {
		pr.Repo = repo
	}
	return &pr, nil
}

// FetchDiff returns the unified diff for a PR. PRs over GitHub's 300-file
// diff limit make the diff endpoint return 406; those fall back to a
// reconstruction from the List Pull Request Files API (#506) instead of
// aborting the review.
func (c *Client) FetchDiff(repo string, number int) (string, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github.v3.diff")
	if err != nil {
		return "", fmt.Errorf("github: fetch diff: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotAcceptable {
		// GitHub's "diff exceeded the maximum number of files (300)"
		// response. Only 406 triggers the fallback; every other non-200
		// keeps the hard-error contract below.
		slog.Info("github: diff endpoint returned 406, reconstructing via files API",
			"repo", repo, "pr", number)
		return c.fetchDiffViaFilesAPI(repo, number)
	}
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

// maxFilesPageBytes is the body ceiling for List Pull Request Files pages.
// Deliberately NOT maxPaginatedPageBytes (5 MiB): files pages embed per-file
// `patch` strings, so a per_page=100 page can plausibly exceed 5 MiB even
// when the final reconstruction would truncate safely at maxDiffBodyBytes —
// a truncated JSON page fails to decode and would abort the review, the
// exact failure mode #506 removes. 2× maxDiffBodyBytes (20 MiB) is generous
// because GitHub omits the `patch` field entirely for very large file
// diffs, which bounds realistic page sizes.
const maxFilesPageBytes = 2 * maxDiffBodyBytes

// githubPRFilesCap is GitHub's documented hard cap on the List Pull Request
// Files endpoint: at most 3,000 files are ever returned, regardless of
// pagination.
const githubPRFilesCap = 3000

// prDiffFile is the subset of a List Pull Request Files item needed to
// reconstruct a unified-diff entry.
type prDiffFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Patch            string `json:"patch"`
}

// fetchDiffViaFilesAPI reconstructs a unified diff from the List Pull
// Request Files API, for PRs whose diff endpoint 406s (>300 files, #506).
//
// Unlike the comment/timeline fetchers (#516/#519), every degradation path
// here returns a PARTIAL diff with a note instead of an error: the diff is
// advisory review context that the prompt layer already truncates to 32 KB,
// whereas comments and timeline events feed dedup/re-review correctness
// decisions. Erroring on truncation would re-create the aborted-review
// failure mode this fallback exists to remove. The reconstructed diff makes
// no claim about a local checkout: the review path runs with or without one
// (repoctx is best-effort) and this client cannot tell which.
func (c *Client) fetchDiffViaFilesAPI(repo string, number int) (string, error) {
	var b strings.Builder
	b.WriteString("# NOTE: diff reconstructed via GitHub's List Pull Request Files API because this PR exceeds GitHub's 300-file diff limit; it may be incomplete.\n")

	listed := 0        // files received from the API, appended or not
	truncated := false // any lossy stop: size ceiling, page ceiling, pagination cap
	complete := false  // saw a short page (no more files upstream)
pages:
	for page := 1; page <= maxPaginationPages; page++ {
		path := fmt.Sprintf("/repos/%s/pulls/%d/files?per_page=%d&page=%d", repo, number, perPage, page)
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return "", fmt.Errorf("github: list pr files: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxFilesPageBytes))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errBody := safeTruncate(string(body), maxErrBodyLen)
			return "", fmt.Errorf("github: list pr files: status %d: %s", resp.StatusCode, errBody)
		}
		var raw []prDiffFile
		if err := json.Unmarshal(body, &raw); err != nil {
			if int64(len(body)) >= maxFilesPageBytes {
				// Self-inflicted truncation: the page ceiling cut the JSON.
				// Degrade to what we have rather than abort the review.
				slog.Warn("github: pr files page truncated at byte ceiling, returning partial reconstructed diff",
					"repo", repo, "pr", number, "page", page, "bytes_read", len(body))
				truncated = true
				break
			}
			// Below the ceiling this is genuine upstream corruption.
			return "", fmt.Errorf("github: decode pr files (%d bytes read): %w", len(body), err)
		}
		for i := range raw {
			// Render the entry separately and check the PROJECTED size, not
			// the accumulated size before append — a single oversized patch
			// (under maxFilesPageBytes, over maxDiffBodyBytes) on a short
			// page would otherwise be appended whole and returned without a
			// truncation note, breaking the "same ceiling as the direct
			// diff path" contract.
			var entry strings.Builder
			appendFileDiff(&entry, &raw[i])
			if b.Len()+entry.Len() > maxDiffBodyBytes {
				// Same ceiling as the direct diff path. Stop paginating too:
				// the remaining pages would only be fetched to be dropped,
				// which is why the truncation note is generic (no exact
				// omitted count).
				slog.Warn("github: reconstructed diff truncated at size limit",
					"repo", repo, "pr", number, "limit_bytes", maxDiffBodyBytes, "files_appended", listed)
				truncated = true
				break pages
			}
			b.WriteString(entry.String())
			listed++
		}
		if len(raw) < perPage {
			complete = true
			break
		}
	}
	if !complete && !truncated {
		// Pagination cap with every page full. Unreachable in practice
		// (GitHub caps the endpoint at 3,000 files = 30 pages, the cap is
		// 50) but handled for symmetry with the other fetchers.
		slog.Warn("github: pr files pagination cap hit, returning partial reconstructed diff",
			"repo", repo, "pr", number, "pages", maxPaginationPages)
		truncated = true
	}
	if truncated {
		b.WriteString("\n... (diff truncated: size limit reached, remaining files omitted)\n")
	}
	if listed >= githubPRFilesCap {
		// Conservative: a PR with exactly 3,000 files gets the note too —
		// distinguishing "at cap" from "capped" would need extra API calls
		// and a spurious note is harmless.
		slog.Warn("github: pr files listing reached GitHub's 3,000-file cap, diff may omit files",
			"repo", repo, "pr", number, "files", listed)
		b.WriteString("\n# NOTE: GitHub caps file listings at 3,000 files; some files may be missing from this diff.\n")
	}
	return b.String(), nil
}

// appendFileDiff writes one file's unified-diff-shaped entry: the
// `diff --git` header, `---`/`+++` markers (with /dev/null for added and
// removed files, and previous_filename on the a/ side for renames), then
// the patch body — or a stub when GitHub omitted the patch (binary or
// oversized per-file diff).
func appendFileDiff(b *strings.Builder, f *prDiffFile) {
	oldName := f.Filename
	if f.PreviousFilename != "" {
		oldName = f.PreviousFilename
	}
	fmt.Fprintf(b, "diff --git a/%s b/%s\n", oldName, f.Filename)
	switch f.Status {
	case "added":
		fmt.Fprintf(b, "--- /dev/null\n+++ b/%s\n", f.Filename)
	case "removed":
		fmt.Fprintf(b, "--- a/%s\n+++ /dev/null\n", oldName)
	default:
		fmt.Fprintf(b, "--- a/%s\n+++ b/%s\n", oldName, f.Filename)
	}
	if f.Patch == "" {
		fmt.Fprintf(b, "(patch unavailable — +%d/-%d)\n", f.Additions, f.Deletions)
		return
	}
	b.WriteString(f.Patch)
	if !strings.HasSuffix(f.Patch, "\n") {
		b.WriteByte('\n')
	}
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

// FetchIssueCommentsOnly retrieves the issue comments for an issue or PR
// number WITHOUT also calling /pulls/:n/comments. Callers that operate on
// issue numbers MUST use this method: /pulls/:n/comments always 404s on
// issues, and FetchComments treats that as a hard error. See
// theburrowhub/heimdallm#292 — the 404 cascade broke the marker-scan
// idempotency check and produced a re-triage loop.
func (c *Client) FetchIssueCommentsOnly(repo string, number int) ([]Comment, error) {
	return c.fetchIssueComments(repo, number)
}

// TimelineEvent is a slim view of a GitHub PR timeline entry. Only the
// two events the pipeline needs are surfaced: review_requested (someone
// asked the reviewer for a review) and review_dismissed (someone
// dismissed the reviewer's prior review). Other event types in the
// timeline (commits, labels, comments, assignments…) are filtered out
// at fetch time so callers don't have to reason about them.
type TimelineEvent struct {
	Event     string // "review_requested" or "review_dismissed"
	Actor     string // login of the user who triggered the event
	CreatedAt time.Time
}

// GetPRTimelineEventsForReviewer returns the review_requested and
// review_dismissed events on a PR that target the given reviewer login,
// sorted ascending by created_at. Used by the pipeline to detect
// explicit re-request review actions on PRs whose HEAD SHA hasn't
// changed (theburrowhub/heimdallm#322 Bug 5): a re-request bumps
// updated_at and re-adds the bot to requested_reviewers, but the SHA
// fail-closed dedup (#245) would otherwise silently skip the review
// regardless of the user's explicit intent.
//
// Returns an empty slice (not nil) when no relevant events exist —
// callers can range over the result without a nil guard. Returns an
// error (never a partial slice) when the pagination cap is hit, so
// callers can fail closed instead of acting on a truncated timeline
// (#519).
func (c *Client) GetPRTimelineEventsForReviewer(repo string, number int, login string) ([]TimelineEvent, error) {
	if login == "" {
		return nil, fmt.Errorf("github: GetPRTimelineEventsForReviewer: empty login")
	}
	out := []TimelineEvent{}
	for page := 1; page <= maxPaginationPages; page++ {
		path := fmt.Sprintf("/repos/%s/issues/%d/timeline?per_page=%d&page=%d", repo, number, perPage, page)
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: fetch timeline: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPaginatedPageBytes))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errBody := safeTruncate(string(body), maxErrBodyLen)
			return nil, fmt.Errorf("github: fetch timeline: status %d: %s", resp.StatusCode, errBody)
		}
		var raw []struct {
			Event     string    `json:"event"`
			CreatedAt time.Time `json:"created_at"`
			Actor     struct {
				Login string `json:"login"`
			} `json:"actor"`
			RequestedReviewer *struct {
				Login string `json:"login"`
			} `json:"requested_reviewer,omitempty"`
			DismissedReview *struct {
				User struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"dismissed_review,omitempty"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			// Include the payload size: a decode failure right at the byte
			// ceiling is the signature of a truncated page (#518).
			return nil, fmt.Errorf("github: decode timeline (%d bytes read): %w", len(body), err)
		}
		for _, ev := range raw {
			switch ev.Event {
			case "review_requested":
				if ev.RequestedReviewer != nil && strings.EqualFold(ev.RequestedReviewer.Login, login) {
					out = append(out, TimelineEvent{
						Event:     ev.Event,
						Actor:     ev.Actor.Login,
						CreatedAt: ev.CreatedAt,
					})
				}
			case "review_dismissed":
				if ev.DismissedReview != nil && strings.EqualFold(ev.DismissedReview.User.Login, login) {
					out = append(out, TimelineEvent{
						Event:     ev.Event,
						Actor:     ev.Actor.Login,
						CreatedAt: ev.CreatedAt,
					})
				}
			}
		}
		// Stop paging when we get a short page (under per_page).
		if len(raw) < perPage {
			// Defensive re-sort: GitHub's timeline API documents ascending
			// chronological order, but the contract is not load-bearing for us
			// — pipeline.shouldBypassSHASkipForReReview relies on the slice
			// being sorted to walk it backward in O(1) for the common case.
			// A single sort here is cheap (events is filtered down to just
			// review_requested / review_dismissed for one login) and immunises
			// the caller against an undocumented re-ordering on GitHub's side.
			//
			// Stable sort + an explicit same-second tiebreak (#602): both
			// GitHub's created_at and our stored review anchor are RFC3339
			// second-granularity, so a dismiss and a re-request done back to
			// back (or by automation) collapse to the same timestamp. The
			// caller decides on the single most-recent relevant event, so a
			// non-deterministic order silently dropped legitimate re-reviews
			// about half the time. On a tie we order review_dismissed BEFORE
			// review_requested — the explicit re-request wins and ends up last
			// — because when we cannot recover the true sub-second order we
			// honour the operator's intent to be re-reviewed. The tiebreak is
			// kept total (a strict weak ordering): two same-second events of
			// the same type compare as equal so the stable sort preserves
			// their input order, rather than reporting both i<j and j<i.
			sort.SliceStable(out, func(i, j int) bool {
				if out[i].CreatedAt.Equal(out[j].CreatedAt) {
					return out[i].Event == "review_dismissed" && out[j].Event == "review_requested"
				}
				return out[i].CreatedAt.Before(out[j].CreatedAt)
			})
			return out, nil
		}
	}
	// Hitting the cap with every page full is suspicious: either GitHub is
	// misbehaving or this PR has >5,000 timeline events. Silently returning
	// a truncated slice would drop exactly the tail events that
	// pipeline.shouldBypassSHASkipForReReview walks backward to detect
	// re-review intent (#322 Bug 5), so surface a hard error and let the
	// caller fail closed — matching the cap-hit contract of
	// fetchReviewComments / fetchIssueComments post-#516 (see #519).
	slog.Warn("github: timeline pagination cap hit",
		"repo", repo, "pr", number, "pages", maxPaginationPages)
	return nil, fmt.Errorf("github: timeline pagination exceeded %d pages for %s#%d (per_page=%d, possible runaway)",
		maxPaginationPages, repo, number, perPage)
}

// maxPaginationPages bounds the number of pages the paginated endpoints
// (fetchReviewComments, fetchIssueComments, GetPRTimelineEventsForReviewer)
// will walk. GitHub returns 30 items per page by default and 100 max; with
// per_page=100 this caps each fetcher at 5,000 items, well above the
// largest real PRs we've seen. The cap exists only to keep a misbehaving
// server from making us iterate forever.
const maxPaginationPages = 50

// perPage is the page size used for paginated endpoints. Kept alongside
// maxPaginationPages so changing either is an explicit decision, and so
// the per_page query parameter and the short-page termination check
// can't drift apart.
const perPage = 100

func (c *Client) fetchReviewComments(repo string, number int) ([]Comment, error) {
	// GitHub's default page size is 30 and the API sorts ascending by
	// created_at, so without explicit pagination any PR with >30 review
	// comments silently drops the newest entries — exactly the ones a
	// re-review depends on. Walk every page with per_page=100, matching
	// the pattern used by GetPRTimelineEventsForReviewer. Pin
	// sort=created&direction=asc explicitly as defense-in-depth against
	// server-side default changes; callers downstream rely on ascending
	// order.
	comments := []Comment{}
	for page := 1; page <= maxPaginationPages; page++ {
		path := fmt.Sprintf("/repos/%s/pulls/%d/comments?per_page=%d&page=%d&sort=created&direction=asc",
			repo, number, perPage, page)
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: fetch review comments: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPaginatedPageBytes))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errBody := safeTruncate(string(body), maxErrBodyLen)
			return nil, fmt.Errorf("github: fetch review comments: status %d: %s", resp.StatusCode, errBody)
		}
		var raw []struct {
			ID   int64 `json:"id"`
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
			// Include the payload size: a decode failure right at the byte
			// ceiling is the signature of a truncated page (#518).
			return nil, fmt.Errorf("github: decode review comments (%d bytes read): %w", len(body), err)
		}
		for _, r := range raw {
			line := r.OriginalLine
			if r.Line != nil {
				line = *r.Line
			}
			comments = append(comments, Comment{
				ID:        r.ID,
				Author:    r.User.Login,
				Body:      r.Body,
				CreatedAt: r.CreatedAt,
				File:      r.Path,
				Line:      line,
			})
		}
		// Short page means GitHub has no more entries upstream.
		if len(raw) < perPage {
			return comments, nil
		}
	}
	// Hitting the cap with every page full is suspicious: either GitHub
	// is misbehaving or this PR has >5,000 review comments. Either way,
	// silently returning a truncated slice would re-introduce the exact
	// bug this function exists to fix (losing the newest entries — the
	// ones a re-review depends on), so surface a hard error and match
	// the mid-pagination error contract the other tests already enforce.
	slog.Warn("github: fetch review comments pagination cap hit",
		"repo", repo, "pr", number, "pages", maxPaginationPages)
	return nil, fmt.Errorf("github: comment pagination exceeded %d pages for %s#%d (per_page=%d, possible runaway)",
		maxPaginationPages, repo, number, perPage)
}

func (c *Client) fetchIssueComments(repo string, number int) ([]Comment, error) {
	// Same rationale as fetchReviewComments: paginate with per_page=perPage
	// so we don't lose the newest issue comments on long-running PRs or
	// busy issues, and pin sort=created&direction=asc explicitly.
	comments := []Comment{}
	for page := 1; page <= maxPaginationPages; page++ {
		path := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=%d&page=%d&sort=created&direction=asc",
			repo, number, perPage, page)
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: fetch issue comments: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxPaginatedPageBytes))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			errBody := safeTruncate(string(body), maxErrBodyLen)
			return nil, fmt.Errorf("github: fetch issue comments: status %d: %s", resp.StatusCode, errBody)
		}
		var raw []struct {
			ID   int64 `json:"id"`
			User struct {
				Login string `json:"login"`
			} `json:"user"`
			Body      string    `json:"body"`
			CreatedAt time.Time `json:"created_at"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			// Include the payload size: a decode failure right at the byte
			// ceiling is the signature of a truncated page (#518).
			return nil, fmt.Errorf("github: decode issue comments (%d bytes read): %w", len(body), err)
		}
		for _, r := range raw {
			comments = append(comments, Comment{
				ID:        r.ID,
				Author:    r.User.Login,
				Body:      r.Body,
				CreatedAt: r.CreatedAt,
			})
		}
		if len(raw) < perPage {
			return comments, nil
		}
	}
	// See fetchReviewComments for the rationale: silent partial results
	// at the cap would re-introduce the bug #512 fixes.
	slog.Warn("github: fetch issue comments pagination cap hit",
		"repo", repo, "issue", number, "pages", maxPaginationPages)
	return nil, fmt.Errorf("github: comment pagination exceeded %d pages for %s#%d (per_page=%d, possible runaway)",
		maxPaginationPages, repo, number, perPage)
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

// CreateLabel creates a repository label. A 422 is tolerated only when GitHub
// reports an already-existing label, which can happen if another process wins
// the race after FetchLabels.
func (c *Client) CreateLabel(repo, name, color, description string) error {
	if repo == "" || name == "" {
		return nil
	}
	color = strings.TrimPrefix(color, "#")
	if !labelColorRE.MatchString(color) {
		return fmt.Errorf("github: invalid label color %q", color)
	}
	payload, err := json.Marshal(map[string]string{
		"name":        name,
		"color":       color,
		"description": description,
	})
	if err != nil {
		return fmt.Errorf("github: marshal label: %w", err)
	}
	resp, err := c.doWithBody("POST",
		fmt.Sprintf("/repos/%s/labels", repo),
		"application/vnd.github+json", "application/json",
		strings.NewReader(string(payload)))
	if err != nil {
		return fmt.Errorf("github: create label: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		if resp.StatusCode == http.StatusUnprocessableEntity && labelAlreadyExistsBody(body) {
			return nil
		}
		return fmt.Errorf("github: create label %q in %s: status %d: %s", name, repo, resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	return nil
}

func labelAlreadyExistsBody(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "already_exists") || strings.Contains(s, "already exists")
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

// parseNextLink extracts the URL whose rel parameter is "next" from a GitHub
// Link header. Returns "" when no such URL exists. The header format is:
//
//	<https://api.github.com/...&page=2>; rel="next", <...>; rel="last"
//
// We do not pull in a parser dependency; a small string scan is sufficient.
func parseNextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		linkURL := urlPart[1 : len(urlPart)-1]
		for _, s := range segs[1:] {
			s = strings.TrimSpace(s)
			if s == `rel="next"` || s == "rel=next" {
				return linkURL
			}
		}
	}
	return ""
}

// FetchCollaborators returns the login names of repository collaborators,
// following GitHub's Link: rel="next" header to walk every page.
func (c *Client) FetchCollaborators(repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	const maxPages = 100
	path := fmt.Sprintf("/repos/%s/collaborators?per_page=100", repo)
	var logins []string
	for page := 0; page < maxPages; page++ {
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: fetch collaborators: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		linkHeader := resp.Header.Get("Link")
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
		for _, u := range raw {
			logins = append(logins, u.Login)
		}
		next := parseNextLink(linkHeader)
		if next == "" {
			return logins, nil
		}
		nextURL, err := url.Parse(next)
		if err != nil {
			return nil, fmt.Errorf("github: parse next link %q: %w", next, err)
		}
		path = nextURL.RequestURI()
	}
	return nil, fmt.Errorf("github: fetch collaborators %s: pagination exceeded %d pages", repo, maxPages)
}
