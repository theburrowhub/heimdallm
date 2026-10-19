package github

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// maxTimelineGraphQLPages bounds the backward walk for the latest relevant
// event (100 filtered events per page).
const maxTimelineGraphQLPages = 10

const timelineQuery = `
query($owner:String!, $name:String!, $number:Int!, $before:String) {
  repository(owner:$owner, name:$name) {
    pullRequest(number:$number) {
      timelineItems(last: 100, before: $before, itemTypes: [REVIEW_REQUESTED_EVENT, REVIEW_DISMISSED_EVENT]) {
        pageInfo { hasPreviousPage startCursor }
        nodes {
          __typename
          ... on ReviewRequestedEvent {
            createdAt
            actor { login }
            requestedReviewer {
              __typename
              ... on User { login }
              ... on Bot { login }
              ... on Mannequin { login }
              ... on Team { slug organization { login } }
            }
          }
          ... on ReviewDismissedEvent {
            createdAt
            actor { login }
            review { author { login } }
          }
        }
      }
    }
  }
}`

type timelineGraphQLResponse struct {
	Repository *struct {
		PullRequest *struct {
			TimelineItems struct {
				PageInfo struct {
					HasPreviousPage bool   `json:"hasPreviousPage"`
					StartCursor     string `json:"startCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					Typename  string    `json:"__typename"`
					CreatedAt time.Time `json:"createdAt"`
					Actor     *gqlLogin `json:"actor"`
					Requested *struct {
						Typename     string    `json:"__typename"`
						Login        string    `json:"login"`
						Slug         string    `json:"slug"`
						Organization *gqlLogin `json:"organization"`
					} `json:"requestedReviewer"`
					Review *struct {
						Author *gqlLogin `json:"author"`
					} `json:"review"`
				} `json:"nodes"`
			} `json:"timelineItems"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

// timelineEventsGraphQL returns login's review_requested / review_dismissed
// events from the most recent page that has any, sorted ascending. Paging
// stops there: the caller only decides on the newest relevant event.
func (c *Client) timelineEventsGraphQL(repo string, number int, login string) ([]TimelineEvent, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	id := c.ReviewerIdentityFor(login, repo)
	out := []TimelineEvent{}
	var before any
	for page := 0; page < maxTimelineGraphQLPages; page++ {
		if err := c.acquireGraphQL(); err != nil {
			return nil, fmt.Errorf("github: timeline budget: %w", err)
		}
		var resp timelineGraphQLResponse
		vars := map[string]any{"owner": owner, "name": name, "number": number, "before": before}
		if err := c.graphQL(timelineQuery, vars, &resp); err != nil {
			return nil, fmt.Errorf("github: timeline: %w", err)
		}
		if resp.Repository == nil || resp.Repository.PullRequest == nil {
			return nil, fmt.Errorf("github: timeline: %w: %s#%d", ErrPRNotFound, repo, number)
		}
		items := resp.Repository.PullRequest.TimelineItems
		for _, n := range items.Nodes {
			actor := ""
			if n.Actor != nil {
				actor = n.Actor.Login
			}
			switch n.Typename {
			case "ReviewRequestedEvent":
				r := n.Requested
				if r == nil {
					continue
				}
				match := false
				if r.Typename == "Team" {
					org := ""
					if r.Organization != nil {
						org = r.Organization.Login
					}
					match = teamRequested([]Team{{Slug: r.Slug, Org: org}}, id)
				} else {
					match = strings.EqualFold(r.Login, login)
				}
				if match {
					out = append(out, TimelineEvent{Event: "review_requested", Actor: actor, CreatedAt: n.CreatedAt})
				}
			case "ReviewDismissedEvent":
				if n.Review != nil && n.Review.Author != nil && strings.EqualFold(n.Review.Author.Login, login) {
					out = append(out, TimelineEvent{Event: "review_dismissed", Actor: actor, CreatedAt: n.CreatedAt})
				}
			}
		}
		if len(out) > 0 || !items.PageInfo.HasPreviousPage || items.PageInfo.StartCursor == "" {
			sortTimelineEvents(out)
			return out, nil
		}
		before = items.PageInfo.StartCursor
	}
	return nil, fmt.Errorf("github: timeline: no relevant event in the last %d pages of %s#%d", maxTimelineGraphQLPages, repo, number)
}

// sortTimelineEvents orders events ascending by time. Stable, with an explicit
// same-second tiebreak (#602): GitHub's created_at and the stored review
// anchor are second-granular, so a dismiss and a re-request done back to back
// collapse to one timestamp; on a tie review_dismissed sorts BEFORE
// review_requested so the explicit re-request ends up last and wins. Two
// same-second events of the same type compare equal, keeping input order.
func sortTimelineEvents(out []TimelineEvent) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].Event == "review_dismissed" && out[j].Event == "review_requested"
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
}
