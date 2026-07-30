package github

import "testing"

func TestPullRequestResolveRepoUsesBaseRepositoryForFork(t *testing.T) {
	pr := &PullRequest{
		Head: Branch{Repo: Repo{FullName: "contributor/fork"}},
		Base: Branch{Repo: Repo{FullName: "org/project"}},
	}

	pr.ResolveRepo()

	if pr.Repo != "org/project" {
		t.Fatalf("Repo = %q, want base repository org/project", pr.Repo)
	}
}

func TestPullRequestResolveRepoFallsBackToSearchRepositoryURL(t *testing.T) {
	pr := &PullRequest{
		Head:          Branch{Repo: Repo{FullName: "contributor/fork"}},
		RepositoryURL: "https://api.github.com/repos/org/project",
	}

	pr.ResolveRepo()

	if pr.Repo != "org/project" {
		t.Fatalf("Repo = %q, want search repository org/project", pr.Repo)
	}
}
