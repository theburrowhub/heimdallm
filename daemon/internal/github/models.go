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
	ID          int64            `json:"id"`
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	HTMLURL     string           `json:"html_url"`
	User        User             `json:"user"`
	Assignees   []User           `json:"assignees"`
	Labels      []Label          `json:"labels"`
	State       string           `json:"state"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	PullRequest *struct{}        `json:"pull_request,omitempty"`
	Repo        string           `json:"-"`
	Mode        config.IssueMode `json:"-"`
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
	Repo Repo `json:"repo"`
}

type PullRequest struct {
	ID        int64     `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	HTMLURL   string    `json:"html_url"`
	User      User      `json:"user"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updated_at"`
	Head      Branch    `json:"head"`
	// repository_url is returned by the Search Issues API: "https://api.github.com/repos/org/repo"
	RepositoryURL string `json:"repository_url"`
	// Populated client-side from RepositoryURL or Head.Repo.FullName
	Repo string `json:"-"`
}

// Comment represents a single comment on a PR — either an inline review comment
// (File and Line are set) or a general issue comment (File and Line are zero values).
type Comment struct {
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
