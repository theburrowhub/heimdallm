package github

import (
	"strings"
	"time"
)

type User struct {
	Login string `json:"login"`
}

type Repo struct {
	FullName string `json:"full_name"`
}

type Branch struct {
	Repo Repo   `json:"repo"`
	SHA  string `json:"sha"`
	// Ref is the branch name. Returned by the Pulls API but not the
	// Search Issues API; merge tracking reads it to check out and
	// force-push the PR's head branch when resolving conflicts.
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
