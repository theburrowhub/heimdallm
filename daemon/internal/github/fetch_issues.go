package github

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
)

// maxIssuePages bounds how many pages we consume per repo on a single
// FetchIssues call — a hard safety net against runaway pagination. At
// per_page=100 this caps the result at 1000 open issues per repo, which
// is plenty for the review pipeline and prevents a malformed response
// from looping forever.
const maxIssuePages = 10

// FetchIssues returns the open issues for a repo after applying the filters
// and classification rules in cfg, sorted by processing priority.
//
// Behaviour:
//
//  1. Fetches `GET /repos/{repo}/issues?state=open` (REST, not Search API —
//     5000 req/h authenticated vs. 30 req/min). The endpoint returns both
//     issues and PRs; we drop PRs by inspecting the `pull_request` field.
//
//  2. Assigns each remaining issue an IssueMode via cfg.Classify. Issues
//     that classify as ignore are dropped unconditionally — this covers
//     both explicit skip_labels and "no labels matched + default_action =
//     ignore". The label dimension is therefore filtered before filter_mode
//     is applied to the remaining dimensions (org + assignee).
//
//  3. Applies organizations and assignees filters. filter_mode decides how
//     they combine: "exclusive" = all active dimensions must pass (AND);
//     "inclusive" = at least one active dimension must pass (OR). A
//     dimension is "active" only when its configured list is non-empty.
//
//  4. Sorts: issues assigned to authenticatedUser first, then the rest;
//     within each group review_only before develop (review is cheap, develop
//     is expensive, so it clears the queue faster); within each mode oldest
//     first so long-pending issues move forward.
//
// The store-level "skip already processed without new activity" check is
// intentionally not here — it needs the local store and belongs to the
// pipeline caller (#26 onward).
func (c *Client) FetchIssues(repo string, cfg config.IssueTrackingConfig, authenticatedUser string) ([]*Issue, error) {
	if repo == "" {
		return nil, fmt.Errorf("github: FetchIssues: empty repo")
	}

	var kept []*Issue
	for page := 1; page <= maxIssuePages; page++ {
		batch, err := c.fetchIssuesPage(repo, page)
		if err != nil {
			return nil, err
		}

		for _, issue := range batch {
			issue.Repo = repo
			if issue.IsPullRequest() {
				continue
			}
			mode := cfg.Classify(issue.LabelNames())
			if mode == config.IssueModeIgnore {
				continue
			}
			if !issueMatchesFilters(issue, repo, cfg) {
				continue
			}
			issue.Mode = mode
			kept = append(kept, issue)
		}

		if len(batch) < 100 {
			break
		}
		if page == maxIssuePages {
			slog.Warn("github: FetchIssues page cap reached", "repo", repo, "cap", maxIssuePages)
		}
	}

	sortIssuesByPriority(kept, authenticatedUser)
	return kept, nil
}

func (c *Client) fetchIssuesPage(repo string, page int) ([]*Issue, error) {
	params := url.Values{}
	params.Set("state", "open")
	params.Set("per_page", "100")
	params.Set("page", strconv.Itoa(page))

	path := fmt.Sprintf("/repos/%s/issues?%s", repo, params.Encode())
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch issues (%s page %d): %w", repo, page, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errBody := string(body)
		if len(errBody) > maxErrBodyLen {
			errBody = errBody[:maxErrBodyLen]
		}
		return nil, fmt.Errorf("github: fetch issues (%s page %d): status %d: %s", repo, page, resp.StatusCode, errBody)
	}

	var batch []*Issue
	if err := json.Unmarshal(body, &batch); err != nil {
		return nil, fmt.Errorf("github: decode issues (%s page %d): %w", repo, page, err)
	}
	return batch, nil
}

// issueMatchesFilters applies organizations + assignees + filter_mode.
// The label dimension is handled up-stream (cfg.Classify + ignore short-
// circuit), so by the time we get here the issue is known to be
// review_only or develop.
func issueMatchesFilters(issue *Issue, repo string, cfg config.IssueTrackingConfig) bool {
	orgActive := len(cfg.Organizations) > 0
	assigneeActive := len(cfg.Assignees) > 0

	orgPass := !orgActive || repoBelongsToOrg(repo, cfg.Organizations)
	assigneePass := !assigneeActive || hasAnyAssignee(issue.AssigneeLogins(), cfg.Assignees)

	// No active filters → include.
	if !orgActive && !assigneeActive {
		return true
	}

	if cfg.FilterMode == config.FilterModeInclusive {
		// OR: at least one active dimension passes.
		if orgActive && orgPass {
			return true
		}
		if assigneeActive && assigneePass {
			return true
		}
		return false
	}

	// Default / exclusive: AND across active dimensions.
	if orgActive && !orgPass {
		return false
	}
	if assigneeActive && !assigneePass {
		return false
	}
	return true
}

// repoBelongsToOrg reports whether "org/name" has an owner in the org list.
// Comparison is case-insensitive to match how GitHub treats org slugs in the
// UI; the API preserves case on the way out.
func repoBelongsToOrg(repo string, orgs []string) bool {
	slash := strings.IndexByte(repo, '/')
	if slash <= 0 {
		return false
	}
	owner := strings.ToLower(repo[:slash])
	for _, o := range orgs {
		if strings.EqualFold(strings.TrimSpace(o), owner) {
			return true
		}
	}
	return false
}

// hasAnyAssignee reports whether any entry in got is also in want. Empty
// got never matches — an issue with no assignees never satisfies an active
// assignee filter.
func hasAnyAssignee(got, want []string) bool {
	if len(got) == 0 || len(want) == 0 {
		return false
	}
	wantSet := make(map[string]struct{}, len(want))
	for _, w := range want {
		wantSet[strings.ToLower(strings.TrimSpace(w))] = struct{}{}
	}
	for _, g := range got {
		if _, ok := wantSet[strings.ToLower(strings.TrimSpace(g))]; ok {
			return true
		}
	}
	return false
}

// sortIssuesByPriority applies the ordering described in FetchIssues:
//
//	1) assigned-to-authenticatedUser before everyone else
//	2) review_only before develop (review is cheap, clears the queue faster)
//	3) oldest first
//
// sort.SliceStable keeps the GitHub response order within otherwise-equal
// issues (GitHub returns newest-updated first), making the tertiary
// CreatedAt tiebreaker deterministic.
func sortIssuesByPriority(issues []*Issue, authenticatedUser string) {
	authLower := strings.ToLower(strings.TrimSpace(authenticatedUser))
	isMine := func(i *Issue) bool {
		if authLower == "" {
			return false
		}
		for _, a := range i.Assignees {
			if strings.ToLower(a.Login) == authLower {
				return true
			}
		}
		return false
	}
	sort.SliceStable(issues, func(i, j int) bool {
		a, b := issues[i], issues[j]
		am, bm := isMine(a), isMine(b)
		if am != bm {
			return am // mine first
		}
		aDev := a.Mode == config.IssueModeDevelop
		bDev := b.Mode == config.IssueModeDevelop
		if aDev != bDev {
			return !aDev // review_only (develop=false) first
		}
		return a.CreatedAt.Before(b.CreatedAt) // older first
	})
}
