package github

import (
	"bytes"
	"encoding/json"
	"errors"
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
// graphQLExec runs a GraphQL query and returns the raw data node plus any
// GraphQL-level error messages. data and gqlErrs can coexist: a batched query
// where some aliases resolve and others fail (e.g. a deleted repo →
// "Could not resolve to a Repository") returns partial data alongside errors.
// transportErr is non-nil only for HTTP/transport/decode failures, never for
// GraphQL-level errors — callers decide which gqlErrs are tolerable.
func (c *Client) graphQLExec(query string, variables map[string]any) (data json.RawMessage, gqlErrs []string, transportErr error) {
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": variables,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("github: graphql: marshal request: %w", err)
	}

	gqlURL := c.graphqlURL()
	req, err := http.NewRequest("POST", gqlURL, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("github: graphql: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("github: graphql: request: %w", err)
	}
	// Notify the rate-limit observer — mirrors doWithBody so GraphQL
	// responses also update the live budget.
	c.notifyRateObserver(resp)
	defer resp.Body.Close()

	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if readErr != nil {
		return nil, nil, fmt.Errorf("github: graphql: read body: %w", readErr)
	}
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, nil, fmt.Errorf("github: graphql: status %d: %s", resp.StatusCode, errBody)
	}

	// Parse the GraphQL envelope: {"data":..., "errors":[...]}.
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, nil, fmt.Errorf("github: graphql: decode envelope: %w", err)
	}
	for _, e := range envelope.Errors {
		gqlErrs = append(gqlErrs, e.Message)
	}
	return envelope.Data, gqlErrs, nil
}

func (c *Client) graphQL(query string, variables map[string]any, out any) error {
	data, gqlErrs, err := c.graphQLExec(query, variables)
	if err != nil {
		return err
	}
	if len(gqlErrs) > 0 {
		return fmt.Errorf("github: graphql: errors: %s", strings.Join(gqlErrs, "; "))
	}
	if out != nil && data != nil {
		if err := json.Unmarshal(data, out); err != nil {
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
			slog.Warn("github: SearchIssuesGraphQL reached result cap", "cap", maxSearchIssuePages*100)
			break
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

// maxBatchArchivedRepos caps how many repos go into a single batched archive
// query. GraphQL bills one point per query regardless of alias count, but the
// node/complexity limits and request size make unbounded batches unwise; the
// caller chunks larger sets across multiple queries.
const maxBatchArchivedRepos = 100

// BatchReposArchived resolves isArchived for many repos in as few API calls as
// possible. With GraphQL enabled it batches via BatchReposArchivedGraphQL (one
// call per up-to-100 repos); otherwise it returns errGraphQLDisabled so the
// caller falls back to per-repo REST IsRepoArchived.
func (c *Client) BatchReposArchived(repos []string) (map[string]bool, error) {
	if !c.useGraphQL.Load() {
		return nil, errGraphQLDisabled
	}
	out := make(map[string]bool, len(repos))
	for start := 0; start < len(repos); start += maxBatchArchivedRepos {
		end := start + maxBatchArchivedRepos
		if end > len(repos) {
			end = len(repos)
		}
		chunk, err := c.BatchReposArchivedGraphQL(repos[start:end])
		if err != nil {
			return nil, err
		}
		for k, v := range chunk {
			out[k] = v
		}
	}
	return out, nil
}

// errGraphQLDisabled signals that GraphQL batching is off so callers fall back
// to the per-repo REST path.
var errGraphQLDisabled = errors.New("github: graphql disabled")

// BatchReposArchivedGraphQL checks isArchived for many repos in a single
// GraphQL query using per-repo aliases (r0, r1, ...). A repo GraphQL cannot
// resolve (deleted/transferred → "Could not resolve to a Repository") is
// reported as archived=true, mirroring IsRepoArchived's 404 handling. Returns a
// map keyed by the input "owner/name". On transport errors or unexpected
// GraphQL errors returns (nil, err) so the caller can fall back to REST.
func (c *Client) BatchReposArchivedGraphQL(repos []string) (map[string]bool, error) {
	if len(repos) == 0 {
		return map[string]bool{}, nil
	}
	var decls, fields []string
	vars := make(map[string]any, len(repos)*2)
	aliasToRepo := make(map[string]string, len(repos))
	i := 0
	for _, repo := range repos {
		owner, name, ok := strings.Cut(repo, "/")
		if !ok || owner == "" || name == "" {
			continue
		}
		alias := fmt.Sprintf("r%d", i)
		ov, nv := fmt.Sprintf("o%d", i), fmt.Sprintf("n%d", i)
		decls = append(decls, fmt.Sprintf("$%s: String!, $%s: String!", ov, nv))
		fields = append(fields, fmt.Sprintf("  %s: repository(owner: $%s, name: $%s) { isArchived }", alias, ov, nv))
		vars[ov] = owner
		vars[nv] = name
		aliasToRepo[alias] = repo
		i++
	}
	if i == 0 {
		return map[string]bool{}, nil
	}
	query := fmt.Sprintf("query(%s) {\n%s\n}", strings.Join(decls, ", "), strings.Join(fields, "\n"))

	data, gqlErrs, err := c.graphQLExec(query, vars)
	if err != nil {
		return nil, err
	}
	// Only NOT_FOUND-style errors are expected (deleted/transferred repos →
	// node resolves to null). Any other GraphQL error is unexpected → bubble up
	// so the caller falls back to REST rather than silently mis-classifying.
	for _, m := range gqlErrs {
		if !strings.Contains(m, "Could not resolve to a Repository") {
			return nil, fmt.Errorf("github: graphql: batch archived: %s", m)
		}
	}

	var raw map[string]*struct {
		IsArchived bool `json:"isArchived"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("github: graphql: decode batch archived: %w", err)
	}
	out := make(map[string]bool, len(aliasToRepo))
	for alias, repo := range aliasToRepo {
		// A null node (unresolved repo) means deleted/transferred → treat as
		// archived, exactly like IsRepoArchived's 404 → true.
		if node := raw[alias]; node != nil {
			out[repo] = node.IsArchived
		} else {
			out[repo] = true
		}
	}
	return out, nil
}
