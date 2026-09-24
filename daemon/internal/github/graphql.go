package github

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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

// graphQLOptions tunes a single GraphQL request.
type graphQLOptions struct {
	// Accept overrides the Accept header. Empty uses the default
	// application/vnd.github+json. Needed for schema previews:
	// mergeStateStatus requires application/vnd.github.merge-info-preview+json.
	Accept string

	// TolerateFieldErrors keeps a response whose "data" is populated even
	// though "errors" carries per-field failures, returning the decoded data
	// plus a *PartialGraphQLError.
	//
	// This is not a convenience. GitHub answers a query that selects
	// baseRef.branchProtectionRule with data AND a FORBIDDEN error carrying a
	// path whenever the token lacks admin on the repository — which is the
	// normal case. Without this, every merge-readiness query would fail for
	// ordinary tokens. Only errors that name a path and are of a known
	// field-scoped type are tolerated; anything else stays fatal.
	TolerateFieldErrors bool
}

// PartialGraphQLError reports that GitHub answered with usable data but could
// not resolve some fields. The caller decides whether the missing fields are
// acceptable — for merge readiness, an unreadable branchProtectionRule means
// "assume the strictest rules", never "assume no rules".
type PartialGraphQLError struct {
	Paths    []string // dotted field paths GitHub could not resolve
	Types    []string // error types, e.g. FORBIDDEN, NOT_FOUND
	Messages []string
}

func (e *PartialGraphQLError) Error() string {
	return fmt.Sprintf("github: graphql: partial response (paths %s): %s",
		strings.Join(e.Paths, ", "), strings.Join(e.Messages, "; "))
}

// HasPath reports whether any unresolved path contains the given segment.
func (e *PartialGraphQLError) HasPath(segment string) bool {
	if e == nil {
		return false
	}
	for _, p := range e.Paths {
		if strings.Contains(p, segment) {
			return true
		}
	}
	return false
}

// tolerableGraphQLErrorTypes are the field-scoped failures that can accompany
// a usable data payload. Deliberately narrow: a type outside this set (or an
// error with no path, which is a whole-query failure) must stay fatal.
var tolerableGraphQLErrorTypes = map[string]struct{}{
	"FORBIDDEN": {},
	"NOT_FOUND": {},
}

// graphQL executes a GraphQL request against the GitHub GraphQL endpoint with
// the default options: any top-level "errors" array is a hard failure.
func (c *Client) graphQL(query string, variables map[string]any, out any) error {
	return c.graphQLWith(graphQLOptions{}, query, variables, out)
}

// graphQLWith executes a GraphQL request against the GitHub GraphQL endpoint.
// It marshals the query+variables into a JSON body, POSTs to <base>/graphql,
// and unmarshals the response envelope into out. Rate-limit headers are
// reported to the observer (mirrors doWithBody behaviour). If the response
// contains a top-level "errors" array, an error is returned containing all
// error messages joined with "; " — unless opts.TolerateFieldErrors applies.
// The body is limited to maxBodyBytes.
func (c *Client) graphQLWith(opts graphQLOptions, query string, variables map[string]any, out any) error {
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
	accept := opts.Accept
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
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
	// `path` entries are a mix of field names and list indices, so the raw
	// element type is any; only the string segments are meaningful here.
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Path    []any  `json:"path"`
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
		partial := opts.TolerateFieldErrors && envelope.Data != nil
		var paths, types []string
		for _, e := range envelope.Errors {
			p := graphQLErrorPath(e.Path)
			if p == "" {
				// No path means the whole query failed, not one field.
				partial = false
				break
			}
			if _, ok := tolerableGraphQLErrorTypes[strings.ToUpper(e.Type)]; !ok {
				partial = false
				break
			}
			paths = append(paths, p)
			types = append(types, strings.ToUpper(e.Type))
		}
		if !partial {
			return fmt.Errorf("github: graphql: errors: %s", strings.Join(msgs, "; "))
		}
		if out != nil {
			if err := json.Unmarshal(envelope.Data, out); err != nil {
				return fmt.Errorf("github: graphql: decode data: %w", err)
			}
		}
		return &PartialGraphQLError{Paths: paths, Types: types, Messages: msgs}
	}
	if out != nil && envelope.Data != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("github: graphql: decode data: %w", err)
		}
	}
	return nil
}

// graphQLErrorPath renders a GraphQL error path as a dotted string. List
// indices are rendered as their numeric form so a path stays comparable.
func graphQLErrorPath(path []any) string {
	if len(path) == 0 {
		return ""
	}
	parts := make([]string, 0, len(path))
	for _, seg := range path {
		switch v := seg.(type) {
		case string:
			parts = append(parts, v)
		case float64:
			parts = append(parts, strconv.Itoa(int(v)))
		}
	}
	return strings.Join(parts, ".")
}
