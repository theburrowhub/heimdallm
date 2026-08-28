package github

import (
	"errors"
	"fmt"
	"log/slog"
)

// TrackedPR is a PR the authenticated user is on the hook for, tagged with the
// search qualifier that found it.
//
// FetchPRs deduplicates by pr.ID and throws that distinction away; merge
// tracking needs it, because "I wrote this" and "someone assigned this to me"
// are different levels of licence to act on a branch.
type TrackedPR struct {
	*PullRequest
	IsAuthor   bool
	IsAssignee bool
}

// FetchMergeTrackingPRs returns the open PRs where the authenticated user is
// the author or (when includeAssigned) an assignee.
//
// Unlike FetchPRsToReview this does NOT filter out self-authored PRs — those
// are precisely the target. And like FetchPRsToReview it omits any repo:
// qualifier: a long list of repo: terms can exceed the Search API's query
// length limit and silently return zero results. Callers intersect with their
// monitored set afterwards.
func (c *Client) FetchMergeTrackingPRs(includeAssigned bool) ([]*TrackedPR, error) {
	username, err := c.AuthenticatedUser()
	if err != nil {
		return nil, fmt.Errorf("github: resolve user: %w", err)
	}

	qualifiers := []string{"author"}
	if includeAssigned {
		qualifiers = append(qualifiers, "assignee")
	}

	byID := make(map[int64]*TrackedPR)
	order := make([]int64, 0, 64)
	var errs []error

	for _, q := range qualifiers {
		prs, err := c.fetchByQualifier(username, q, nil)
		if err != nil {
			// One failing qualifier must not wipe the other: an author-only
			// result is still useful, and the caller sees the joined error.
			slog.Warn("github: merge tracking fetch partial error", "qualifier", q, "err", err)
			errs = append(errs, fmt.Errorf("%s: %w", q, err))
			continue
		}
		for _, pr := range prs {
			if pr == nil {
				continue
			}
			pr.ResolveRepo()
			existing, ok := byID[pr.ID]
			if !ok {
				existing = &TrackedPR{PullRequest: pr}
				byID[pr.ID] = existing
				order = append(order, pr.ID)
			}
			switch q {
			case "author":
				existing.IsAuthor = true
			case "assignee":
				existing.IsAssignee = true
			}
		}
	}

	out := make([]*TrackedPR, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	slog.Info("github: merge tracking PRs", "count", len(out), "user", username, "include_assigned", includeAssigned)

	if len(errs) > 0 {
		return out, errors.Join(errs...)
	}
	return out, nil
}
