package github

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strconv"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
)

// maxSearchIssuePages is the GitHub Search API's hard cap on paginated results:
// at most 1000 items (10 pages × per_page=100). Requesting page 11 or beyond
// returns an error. We stop here to avoid burning the search budget on an
// empty or error response.
const maxSearchIssuePages = 10

// SearchIssues fetches open issues matching query. When GraphQL is enabled via
// SetGraphQLEnabled(true), it dispatches to SearchIssuesGraphQL first; on any
// error it falls back to the REST /search/issues path automatically (the caller
// never sees the GraphQL error — only the REST result or error).
//
// When GraphQL is disabled (the default), only the REST path is used.
//
// On any error SearchIssues returns (nil, err) so callers can decide whether
// to fall back to per-repo FetchIssues.
func (c *Client) SearchIssues(query string) ([]*Issue, error) {
	if c.useGraphQL.Load() {
		issues, err := c.SearchIssuesGraphQL(query)
		if err != nil {
			slog.Warn("github: SearchIssuesGraphQL failed, falling back to REST",
				"err", err)
			return c.searchIssuesREST(query)
		}
		return issues, nil
	}
	return c.searchIssuesREST(query)
}

// searchIssuesREST fetches open issues matching query from the GitHub Search
// API (GET /search/issues?q=...) and returns them as a flat []*Issue slice.
//
// Each page is capped at 100 items; pagination stops when GitHub returns a
// partial page, the result set hits the 1000-item cap (maxSearchIssuePages ×
// 100), or an error occurs. Because this is a GET the existing ETag cache layer
// and rate-limit observer are applied automatically.
//
// The Repo field of each returned issue is derived from the issue's
// repository_url field ("https://api.github.com/repos/org/repo") using the
// same extraction logic as PullRequest.ResolveRepo. The PullRequest probe
// field is checked so PRs that appear in the search results are silently
// dropped (GitHub's search endpoint returns both issues and PRs when the query
// targets issues unless `is:issue` is present — this is defense-in-depth for
// callers that pass a query without that qualifier).
//
// On any error searchIssuesREST returns (nil, err).
func (c *Client) searchIssuesREST(query string) ([]*Issue, error) {
	var all []*Issue
	for page := 1; page <= maxSearchIssuePages; page++ {
		params := url.Values{}
		params.Set("q", query)
		params.Set("per_page", "100")
		params.Set("page", strconv.Itoa(page))

		path := "/search/issues?" + params.Encode()
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: search issues (page %d): %w", page, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("github: search issues: read body (page %d): %w", page, readErr)
		}
		if resp.StatusCode != 200 {
			errBody := safeTruncate(string(body), maxErrBodyLen)
			return nil, fmt.Errorf("github: search issues (page %d): status %d: %s", page, resp.StatusCode, errBody)
		}

		var result struct {
			TotalCount int      `json:"total_count"`
			Items      []*Issue `json:"items"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("github: search issues: decode (page %d): %w", page, err)
		}

		for _, issue := range result.Items {
			if issue == nil {
				continue
			}
			if issue.IsPullRequest() {
				continue // search returns PRs too; drop them
			}
			// Derive Repo from repository_url when Repository is absent.
			if issue.Repository != nil && issue.Repository.FullName != "" {
				issue.Repo = issue.Repository.FullName
			} else {
				issue.Repo = repoFromURL(issue.HTMLURL)
			}
			all = append(all, issue)
		}

		if len(result.Items) < 100 {
			break // last (partial) page
		}
		if page == maxSearchIssuePages {
			slog.Warn("github: SearchIssues reached result cap", "cap", maxSearchIssuePages*100)
		}
	}
	return all, nil
}

// repoFromURL derives "org/repo" from a GitHub html_url
// ("https://github.com/org/repo/issues/42"). Returns "" on failure.
//
// Note: issue.HTMLURL is always a github.com URL; the api.github.com/repos/
// form only appears in the repository_url field, which is handled by the
// Repository.FullName path in SearchIssues before repoFromURL is called.
func repoFromURL(rawURL string) string {
	// html_url form: https://github.com/org/repo/issues/42
	const ghPrefix = "https://github.com/"
	if strings.HasPrefix(rawURL, ghPrefix) {
		rest := rawURL[len(ghPrefix):]
		// rest = "org/repo/issues/42"
		parts := strings.SplitN(rest, "/", 3)
		if len(parts) >= 2 && parts[0] != "" && parts[1] != "" {
			candidate := parts[0] + "/" + parts[1]
			if isValidRepo(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// isValidRepo checks that s has the form "owner/name" with no path traversal.
func isValidRepo(s string) bool {
	return strings.Count(s, "/") == 1 &&
		!strings.Contains(s, "..") &&
		!strings.Contains(s, "//")
}

// BuildAggregatedSearchQuery constructs a minimal `q=` value for the
// aggregated prefetch path. Unlike BuildIssueSearchQuery it intentionally
// omits label qualifiers so the search returns a SUPERSET of all issues that
// the per-repo client-side ClassifyAndFilterIssues would keep. This is correct
// because:
//
//  1. Per-repo label configs differ, so no single label set can scope all repos.
//  2. ClassifyAndFilterIssues (called in ProcessRepo for prefetched results)
//     already applies classification + label/filter_mode filtering precisely,
//     ensuring no unwanted issue survives into the pipeline.
//
// The query includes `is:issue is:open`, one `assignee:<login>` qualifier per
// assignee, and one `repo:<org/repo>` qualifier per repo.
//
// assignees must be the resolved set for the group (empty falls back to authUser
// if provided via the caller; pass the already-resolved list here).
// repos must be non-empty.
func BuildAggregatedSearchQuery(assignees []string, repos []string) string {
	var parts []string
	parts = append(parts, "is:issue", "is:open")
	for _, a := range assignees {
		a = strings.TrimSpace(a)
		if a != "" {
			parts = append(parts, "assignee:"+a)
		}
	}
	for _, r := range repos {
		r = strings.TrimSpace(r)
		if r != "" {
			parts = append(parts, "repo:"+r)
		}
	}
	return strings.Join(parts, " ")
}

// BuildIssueSearchQuery constructs the `q=` value for GET /search/issues that
// mirrors the semantics of FetchIssues:
//
//   - always adds `is:issue is:open`
//   - adds `assignee:<login>` for each configured assignee (or the
//     authenticatedUser as fallback when Assignees is empty)
//   - adds `org:<org>` for each configured organization when no explicit repos
//     are given; otherwise adds `repo:<org/repo>` for each repo in the list
//   - adds `label:<name>` for each label found in DevelopLabels,
//     RefinementLabels, or ReviewOnlyLabels (the positive action-mapped labels)
//
// The resulting query is intended for SearchIssues, which drops PRs
// and further applies cfg.Classify + issueMatchesFilters — so the query is a
// broad net, not an exact replacement for those filters.
//
// repos must be pre-filtered to only include repos that have issue tracking
// enabled (and that are not in autonomous mode) — buildIssueSearchQuery does
// not perform that filtering.
func BuildIssueSearchQuery(it config.IssueTrackingConfig, authenticatedUser string, repos []string) string {
	var parts []string
	parts = append(parts, "is:issue", "is:open")

	// Assignee scope.
	assignees := it.Assignees
	if len(assignees) == 0 && authenticatedUser != "" {
		assignees = []string{authenticatedUser}
	}
	for _, a := range assignees {
		a = strings.TrimSpace(a)
		if a != "" {
			parts = append(parts, "assignee:"+a)
		}
	}

	// Repo / org scope.
	if len(repos) > 0 {
		for _, r := range repos {
			r = strings.TrimSpace(r)
			if r != "" {
				parts = append(parts, "repo:"+r)
			}
		}
	} else if len(it.Organizations) > 0 {
		for _, o := range it.Organizations {
			o = strings.TrimSpace(o)
			if o != "" {
				parts = append(parts, "org:"+o)
			}
		}
	}

	// Label filters — include only the positive-action labels (develop,
	// refinement, review_only). Skip and blocked labels are not included:
	// the search net is wide; per-issue filtering in FetchIssues / SearchIssues
	// downstream removes skip/blocked items.
	allActionLabels := make([]string, 0,
		len(it.DevelopLabels)+len(it.RefinementLabels)+len(it.ReviewOnlyLabels))
	allActionLabels = append(allActionLabels, it.DevelopLabels...)
	allActionLabels = append(allActionLabels, it.RefinementLabels...)
	allActionLabels = append(allActionLabels, it.ReviewOnlyLabels...)

	// Only add label qualifiers when every action dimension has labels.
	// If DefaultAction != ignore, unlabelled issues also qualify and a
	// label filter would incorrectly exclude them.
	defaultActionIsIgnore := it.DefaultAction == "" || it.DefaultAction == string(config.IssueModeIgnore)
	if defaultActionIsIgnore && len(allActionLabels) > 0 {
		seen := make(map[string]struct{}, len(allActionLabels))
		for _, l := range allActionLabels {
			l = strings.TrimSpace(l)
			if l == "" {
				continue
			}
			if _, dup := seen[l]; dup {
				continue
			}
			seen[l] = struct{}{}
			// GitHub label qualifiers need quoting when the name has spaces.
			if strings.ContainsAny(l, " \t") {
				l = `"` + l + `"`
			}
			parts = append(parts, "label:"+l)
		}
	}

	return strings.Join(parts, " ")
}
