package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// graphqlURL returns the GraphQL endpoint URL. The REST base URL
// (e.g. https://api.github.com or an httptest server) is transformed to the
// /graphql path: api.github.com/graphql for production, <base>/graphql for
// tests. This allows httptest servers to serve both REST and GraphQL on the
// same mux.
func (c *Client) graphqlURL() string {
	base := strings.TrimRight(c.baseURL, "/")
	return base + "/graphql"
}

// graphQL executes a GraphQL request against the GitHub GraphQL endpoint.
// It marshals the query+variables into a JSON body, POSTs to <base>/graphql,
// and unmarshals the response envelope into out. Rate-limit headers are
// reported to the observer (mirrors doWithBody behaviour). If the response
// contains a top-level "errors" array, an error is returned containing all
// error messages joined with "; ". The body is limited to maxBodyBytes.
func (c *Client) graphQL(query string, variables map[string]any, out any) error {
	// Circuit breaker: while a rate-limit cooldown is active, fail fast without
	// touching GitHub. GraphQL needs this as much as do()/doWithBody — GitHub
	// penalises requests sent during a secondary/abuse block, so traffic on
	// this path would keep a block alive that going quiet lets clear.
	if until, paused := c.rateLimitPaused(); paused {
		return &RateLimitError{RetryAt: until}
	}

	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return fmt.Errorf("github: graphql: marshal request: %w", err)
	}

	gqlURL := c.graphqlURL()
	req, err := http.NewRequest("POST", gqlURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("github: graphql: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github: graphql: request: %w", err)
	}
	// Notify the rate-limit observer — mirrors doWithBody so GraphQL
	// responses also update the live budget.
	c.notifyRateObserver(resp)

	// Open the breaker when GitHub rejects this call for rate limiting, so the
	// rest of the cycle stops hammering. Mirrors do()/doWithBody; the response
	// is still handled below exactly as before.
	if wait, limited := rateLimitDelay(resp); limited {
		c.pauseRateLimit(wait)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if readErr != nil {
		return fmt.Errorf("github: graphql: read body: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return fmt.Errorf("github: graphql: status %d: %s", resp.StatusCode, errBody)
	}

	// Parse the GraphQL envelope: {"data":..., "errors":[...]}.
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("github: graphql: decode envelope: %w", err)
	}
	if len(envelope.Errors) > 0 {
		msgs := make([]string, len(envelope.Errors))
		for i, e := range envelope.Errors {
			msgs[i] = e.Message
		}
		return fmt.Errorf("github: graphql: errors: %s", strings.Join(msgs, "; "))
	}
	if out != nil && envelope.Data != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("github: graphql: decode data: %w", err)
		}
	}
	return nil
}

// graphQLSearchQuery is the GraphQL query used by SearchIssuesGraphQL.
// It paginates via the cursor/pageInfo mechanism and selects exactly the
// fields that SearchIssues (REST) populates on *Issue.
const graphQLSearchQuery = `
query($q: String!, $cursor: String) {
  search(query: $q, type: ISSUE, first: 100, after: $cursor) {
    pageInfo {
      hasNextPage
      endCursor
    }
    nodes {
      ... on Issue {
        databaseId
        number
        title
        body
        state
        url
        createdAt
        updatedAt
        author {
          login
        }
        repository {
          nameWithOwner
        }
        assignees(first: 20) {
          nodes {
            login
          }
        }
        labels(first: 50) {
          nodes {
            name
          }
        }
      }
    }
  }
}
`

// graphQLSearchResult is the JSON shape of the data.search node returned
// by the GraphQL search query.
type graphQLSearchResult struct {
	Search struct {
		PageInfo struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
		Nodes []graphQLIssueNode `json:"nodes"`
	} `json:"search"`
}

// graphQLIssueNode is a single node in the search result. Non-Issue nodes
// (e.g. PRs returned in a search) will have a zero-value databaseId and are
// silently dropped by SearchIssuesGraphQL.
type graphQLIssueNode struct {
	DatabaseID int64  `json:"databaseId"`
	Number     int    `json:"number"`
	Title      string `json:"title"`
	Body       string `json:"body"`
	State      string `json:"state"`
	URL        string `json:"url"` // HTMLURL
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
	Author     struct {
		Login string `json:"login"`
	} `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
	} `json:"repository"`
	Assignees struct {
		Nodes []struct {
			Login string `json:"login"`
		} `json:"nodes"`
	} `json:"assignees"`
	Labels struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
}

// SearchIssuesGraphQL fetches open issues matching searchQuery via the GitHub
// GraphQL API, using the search(type:ISSUE) connection. It paginates via
// cursor/pageInfo up to the same 1000-item cap as SearchIssues (REST).
//
// Returns []*Issue with the same field layout as SearchIssues so it is a
// drop-in for the dispatch layer. Non-Issue nodes (PRs) are silently dropped.
//
// On any error, returns (nil, err) — the caller should fall back to REST.
func (c *Client) SearchIssuesGraphQL(searchQuery string) ([]*Issue, error) {
	var all []*Issue
	var cursor *string // nil on first page

	for page := 1; page <= maxSearchIssuePages; page++ {
		// Same metering as the REST path — GraphQL search spends its own
		// budget per request too.
		if err := c.acquireSearch(); err != nil {
			return nil, fmt.Errorf("github: search budget: %w", err)
		}
		vars := map[string]any{"q": searchQuery}
		if cursor != nil {
			vars["cursor"] = *cursor
		}

		var result graphQLSearchResult
		if err := c.graphQL(graphQLSearchQuery, vars, &result); err != nil {
			return nil, fmt.Errorf("github: SearchIssuesGraphQL (page %d): %w", page, err)
		}

		for _, node := range result.Search.Nodes {
			// Non-Issue nodes have a zero databaseId (and no number/title). Drop them.
			if node.DatabaseID == 0 {
				continue
			}

			issue := &Issue{
				ID:      node.DatabaseID,
				Number:  node.Number,
				Title:   node.Title,
				Body:    node.Body,
				State:   strings.ToLower(node.State),
				HTMLURL: node.URL,
				User:    User{Login: node.Author.Login},
				Repo:    node.Repository.NameWithOwner,
			}

			// Parse timestamps.
			if t, err := time.Parse(time.RFC3339, node.CreatedAt); err == nil {
				issue.CreatedAt = t
			}
			if t, err := time.Parse(time.RFC3339, node.UpdatedAt); err == nil {
				issue.UpdatedAt = t
			}

			// Assignees.
			issue.Assignees = make([]User, 0, len(node.Assignees.Nodes))
			for _, a := range node.Assignees.Nodes {
				if a.Login != "" {
					issue.Assignees = append(issue.Assignees, User{Login: a.Login})
				}
			}

			// Labels.
			issue.Labels = make([]Label, 0, len(node.Labels.Nodes))
			for _, l := range node.Labels.Nodes {
				if l.Name != "" {
					issue.Labels = append(issue.Labels, Label{Name: l.Name})
				}
			}

			all = append(all, issue)
		}

		if !result.Search.PageInfo.HasNextPage {
			break
		}
		if page == maxSearchIssuePages {
			// hasNextPage is still true, so GitHub had more to give. Report
			// the truncation with the partial results — see ErrSearchTruncated.
			slog.Warn("github: SearchIssuesGraphQL reached result cap", "cap", maxSearchIssuePages*100)
			return all, ErrSearchTruncated
		}
		end := result.Search.PageInfo.EndCursor
		cursor = &end
	}
	return all, nil
}

// SetGraphQLEnabled enables or disables GraphQL-based issue search. When
// enabled, SearchIssues dispatches to SearchIssuesGraphQL first and falls back
// to the REST /search/issues path on any error. Thread-safe; takes effect on
// the next call. Defaults to disabled (false = zero-value atomic.Bool).
func (c *Client) SetGraphQLEnabled(enabled bool) {
	c.useGraphQL.Store(enabled)
}
