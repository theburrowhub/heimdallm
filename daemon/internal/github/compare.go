package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// commitSHAPattern bounds what may be interpolated into a compare path: a
// stored or GitHub-reported commit id, never arbitrary text.
var commitSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// compareResponse is the subset of GET /repos/{repo}/compare/{base}...{head}
// that decides whether an incremental diff is meaningful.
type compareResponse struct {
	Status  string `json:"status"` // ahead | behind | diverged | identical
	Commits []struct {
		Parents []struct {
			SHA string `json:"sha"`
		} `json:"parents"`
	} `json:"commits"`
}

// FetchCompareDiff returns the diff between two commits of a PR's history,
// for re-reviewing only what changed since the last reviewed commit (the
// incremental_diff measure).
//
// ok is false — and the caller should review the full PR diff instead — when
// the range is not a plain linear extension: base is no longer an ancestor of
// head (force-push, rebase), or the range contains a merge commit (an "Update
// branch" would otherwise drag the base branch's changes into the diff), or
// the range is empty.
func (c *Client) FetchCompareDiff(repo, base, head string) (diff string, ok bool, err error) {
	if !commitSHAPattern.MatchString(base) || !commitSHAPattern.MatchString(head) {
		return "", false, nil
	}
	path := fmt.Sprintf("/repos/%s/compare/%s...%s", repo, base, head)

	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return "", false, fmt.Errorf("github: compare: %w", err)
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxFilesPageBytes))
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// A commit GitHub no longer has (garbage-collected after a force-push).
		return "", false, nil
	}
	if resp.StatusCode != http.StatusOK {
		return "", false, fmt.Errorf("github: compare: status %d: %s", resp.StatusCode, safeTruncate(string(body), maxErrBodyLen))
	}
	if readErr != nil {
		return "", false, fmt.Errorf("github: compare: read: %w", readErr)
	}
	var cmp compareResponse
	if err := json.Unmarshal(body, &cmp); err != nil {
		// Over the read cap the JSON is cut short; a full diff is the safe answer.
		return "", false, nil
	}
	if cmp.Status != "ahead" || len(cmp.Commits) == 0 {
		return "", false, nil
	}
	for _, commit := range cmp.Commits {
		if len(commit.Parents) > 1 {
			return "", false, nil
		}
	}

	resp, err = c.do("GET", path, "application/vnd.github.v3.diff")
	if err != nil {
		return "", false, fmt.Errorf("github: compare diff: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		return "", false, fmt.Errorf("github: compare diff: status %d: %s", resp.StatusCode, safeTruncate(string(errBody), maxErrBodyLen))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffBodyBytes))
	if err != nil {
		return "", false, fmt.Errorf("github: compare diff: read: %w", err)
	}
	if len(data) == 0 {
		return "", false, nil
	}
	return string(data), true, nil
}
