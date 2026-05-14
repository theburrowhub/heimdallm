package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// GetCanonicalFullName returns the current canonical `owner/name` for
// the repository at `repo`. GitHub transparently 301s the API when a
// repo has been renamed, and the JSON response carries the fresh
// `full_name` regardless of which slug the request used, so this is a
// one-shot rename detector with no special redirect handling needed.
//
// Callers compare the result to the input: when they differ, dispatch
// the rename reconciler. A 404 (returned as *APIError with
// StatusCode=404) means the repo was deleted, not renamed, and is
// handled separately by the caller (typically a "repo unreachable"
// SSE event, not a rename).
func (c *Client) GetCanonicalFullName(repo string) (string, error) {
	resp, err := c.do("GET", "/repos/"+repo, "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("github: get canonical full_name: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		return "", &APIError{
			StatusCode: resp.StatusCode,
			Body:       safeTruncate(string(body), maxErrBodyLen),
		}
	}
	var out struct {
		FullName string `json:"full_name"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("github: decode canonical full_name: %w", err)
	}
	if out.FullName == "" {
		return "", fmt.Errorf("github: canonical full_name empty for %q", repo)
	}
	return out.FullName, nil
}
