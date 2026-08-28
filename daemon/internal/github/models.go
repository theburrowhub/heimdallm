package github

import (
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

type User struct {
	Login string `json:"login"`
}

// Label is a GitHub label stripped down to the field the pipeline needs.
type Label struct {
	Name string `json:"name"`
}

// Issue mirrors a GitHub issue filtered and classified by FetchIssues.
//
// The JSON field `pull_request` on the wire distinguishes issues from PRs
// when using the `GET /repos/{owner}/{repo}/issues` endpoint (which returns
// both). `PullRequest` is a probe field — when non-nil the record is a PR
// and FetchIssues drops it. We do not unmarshal its contents.
//
// `Mode` is populated client-side by FetchIssues after running the
// config-driven label classifier so downstream consumers (the pipeline in
// #26 / #27) don't need to re-apply the precedence rules.
type Issue struct {
	ID          int64     `json:"id"`
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	HTMLURL     string    `json:"html_url"`
	User        User      `json:"user"`
	Assignees   []User    `json:"assignees"`
	Labels      []Label   `json:"labels"`
	State       string    `json:"state"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
	// Repository is populated by GitHub on endpoints that can return issues
	// from more than one repo in a single response (e.g. the sub-issues
	// endpoint — same-owner, possibly cross-repo children). Kept as a
	// pointer so the extra field is zero-cost when the endpoint doesn't
	// include it. Consumers normally read Repo (below) — that's set by
	// the client from this field when present, else from the parent
	// context.
	Repository *Repo            `json:"repository,omitempty"`
	Repo       string           `json:"-"`
	Mode       config.IssueMode `json:"-"`
}

// IsPullRequest reports whether the record returned by the issues endpoint is
// actually a pull request. The issues API returns both; the pipeline only
// wants plain issues.
func (i *Issue) IsPullRequest() bool {
	return i.PullRequest != nil
}

// LabelNames extracts label names as a plain string slice for use with
// IssueTrackingConfig.Classify and for logging / storage.
func (i *Issue) LabelNames() []string {
	out := make([]string, len(i.Labels))
	for idx, l := range i.Labels {
		out[idx] = l.Name
	}
	return out
}

// AssigneeLogins returns the logins assigned to the issue (may be empty).
func (i *Issue) AssigneeLogins() []string {
	out := make([]string, len(i.Assignees))
	for idx, a := range i.Assignees {
		out[idx] = a.Login
	}
	return out
}

type Repo struct {
	FullName string `json:"full_name"`
}

type Branch struct {
	Repo Repo   `json:"repo"`
	SHA  string `json:"sha"`
	// Ref is the branch name (e.g. "heimdallm/issue-42"). Returned by
	// the Pulls API but not the Search Issues API; the review-state
	// fix flow (#482 phase 3) reads it to push back to the same
	// branch the PR was opened from.
	Ref string `json:"ref"`
}

type PullRequest struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	HTMLURL   string    `json:"html_url"`
	User      User      `json:"user"`
	State     string    `json:"state"`
	Draft     bool      `json:"draft"`
	UpdatedAt time.Time `json:"updated_at"`
	Head      Branch    `json:"head"`
	// RequestedReviewers is populated by the Pulls API (GET /repos/{o}/{r}/pulls/{n})
	// but NOT by the Search Issues API. Used by the tier-2 loop to confirm the
	// bot is still a pending reviewer before enqueuing a review — the search
	// index can lag behind the actual requested_reviewers list.
	RequestedReviewers []User `json:"requested_reviewers"`
	// Assignees is populated by both the Search Issues API and the Pulls API.
	// Merge tracking needs it to decide whether the authenticated user is on
	// the hook for a PR they did not author.
	Assignees []User `json:"assignees"`
	// Base is the target branch. Populated by the Pulls API only — the Search
	// Issues API omits it.
	Base Branch `json:"base"`
	// repository_url is returned by the Search Issues API: "https://api.github.com/repos/org/repo"
	RepositoryURL string `json:"repository_url"`
	// Populated client-side from RepositoryURL or Head.Repo.FullName
	Repo string `json:"-"`
}

// ReviewRequestedFor reports whether the current Pulls API representation
// still lists login as a pending reviewer. Search can lag behind this source
// of truth, so workers call it after their fresh hydration.
func (pr *PullRequest) ReviewRequestedFor(login string) bool {
	want := strings.TrimSpace(strings.TrimLeft(login, "@"))
	if pr == nil || want == "" {
		return false
	}
	for _, reviewer := range pr.RequestedReviewers {
		got := strings.TrimSpace(strings.TrimLeft(reviewer.Login, "@"))
		if strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}

// AssignedTo reports whether login is one of the PR's assignees. Login
// comparison matches ReviewRequestedFor: case-insensitive, leading "@"
// tolerated.
func (pr *PullRequest) AssignedTo(login string) bool {
	want := strings.TrimSpace(strings.TrimLeft(login, "@"))
	if pr == nil || want == "" {
		return false
	}
	for _, a := range pr.Assignees {
		got := strings.TrimSpace(strings.TrimLeft(a.Login, "@"))
		if strings.EqualFold(got, want) {
			return true
		}
	}
	return false
}

// Comment represents a single comment on a PR — either an inline review comment
// (File and Line are set) or a general issue comment (File and Line are zero values).
type Comment struct {
	ID        int64 // GitHub comment id; 0 if unknown
	Author    string
	Body      string
	CreatedAt time.Time
	File      string // non-empty for inline review comments
	Line      int    // non-zero for inline review comments
}

// ResolveRepo sets the Repo field from available data.
func (pr *PullRequest) ResolveRepo() {
	if pr.Head.Repo.FullName != "" {
		pr.Repo = pr.Head.Repo.FullName
		return
	}
	// Extract "org/repo" from "https://api.github.com/repos/org/repo".
	// Validate the extracted segment has exactly the format "org/repo" —
	// one slash, no path traversal sequences, no special path characters —
	// to prevent a manipulated RepositoryURL from injecting path traversal.
	const prefix = "https://api.github.com/repos/"
	if len(pr.RepositoryURL) > len(prefix) {
		extracted := pr.RepositoryURL[len(prefix):]
		if strings.Count(extracted, "/") == 1 &&
			!strings.Contains(extracted, "..") &&
			!strings.Contains(extracted, "//") {
			pr.Repo = extracted
		}
	}
}
