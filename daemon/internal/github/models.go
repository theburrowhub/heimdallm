package github

import "time"

type User struct {
	Login string `json:"login"`
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

// ResolveRepo sets the Repo field from available data.
func (pr *PullRequest) ResolveRepo() {
	if pr.Head.Repo.FullName != "" {
		pr.Repo = pr.Head.Repo.FullName
		return
	}
	// Extract "org/repo" from "https://api.github.com/repos/org/repo"
	const prefix = "https://api.github.com/repos/"
	if len(pr.RepositoryURL) > len(prefix) {
		pr.Repo = pr.RepositoryURL[len(prefix):]
	}
}
