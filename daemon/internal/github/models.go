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
	// Populated client-side
	Repo string `json:"-"`
}
