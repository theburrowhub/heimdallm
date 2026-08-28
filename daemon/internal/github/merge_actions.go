package github

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// MergeOutcome is the result of an accepted merge.
type MergeOutcome struct {
	Merged  bool
	SHA     string
	Message string
}

// UpdateBranchOutcome is the result of an accepted update-branch request.
// GitHub answers 202 Accepted and performs the merge asynchronously, so there
// is no post-state to report: the caller must re-read the PR on a later tick.
type UpdateBranchOutcome struct {
	Accepted bool
	Message  string
}

// Reasons attached to the typed errors below. They are persisted and surfaced
// in the UI, so they are stable identifiers rather than free text.
const (
	MergeReasonSHAMismatch   = "sha_mismatch"
	MergeReasonNotMergeable  = "not_mergeable"
	MergeReasonRequiredCheck = "required_checks"
	MergeReasonBlocked       = "blocked"
	MergeReasonForbidden     = "forbidden"
	MergeReasonNotFound      = "not_found"
	MergeReasonUnknown       = "unknown"

	UpdateBranchReasonUnprocessable = "unprocessable"
	UpdateBranchReasonSHAMismatch   = "sha_mismatch"
	UpdateBranchReasonForbidden     = "forbidden"
	UpdateBranchReasonNotFound      = "not_found"
	UpdateBranchReasonUnknown       = "unknown"

	AutoMergeReasonNotAllowedForRepo = "not_allowed_for_repo"
	AutoMergeReasonCleanStatus       = "clean_status"
	AutoMergeReasonSHAMismatch       = "sha_mismatch"
	AutoMergeReasonAlreadyEnabled    = "already_enabled"
	AutoMergeReasonForbidden         = "forbidden"
	AutoMergeReasonUnknown           = "unknown"
)

// MergeRejectedError means GitHub declined the merge. Reason classifies why so
// the caller can distinguish "try later" from "never retry": in particular a
// sha_mismatch means someone pushed after we evaluated, and blindly retrying
// would merge a commit that was never reviewed.
type MergeRejectedError struct {
	StatusCode int
	Reason     string
	Body       string
}

func (e *MergeRejectedError) Error() string {
	return fmt.Sprintf("github: merge rejected (status %d, reason %s): %s", e.StatusCode, e.Reason, e.Body)
}

// Terminal reports whether no future retry can succeed without a human.
func (e *MergeRejectedError) Terminal() bool {
	return e.Reason == MergeReasonForbidden || e.Reason == MergeReasonNotFound
}

// UpdateBranchRejectedError means GitHub declined to update the branch. A 422
// is the expected signal that the branch cannot be updated with a merge commit
// — typically because the base requires linear history — and is the trigger
// for the local-rebase fallback.
type UpdateBranchRejectedError struct {
	StatusCode int
	Reason     string
	Body       string
}

func (e *UpdateBranchRejectedError) Error() string {
	return fmt.Sprintf("github: update-branch rejected (status %d, reason %s): %s", e.StatusCode, e.Reason, e.Body)
}

// AutoMergeUnavailableError means the auto-merge mutation failed on PR or repo
// state rather than on transport.
type AutoMergeUnavailableError struct {
	Reason string
	Body   string
}

func (e *AutoMergeUnavailableError) Error() string {
	return fmt.Sprintf("github: auto-merge unavailable (reason %s): %s", e.Reason, e.Body)
}

// ErrPRAlreadyMerged is returned when a merge target turns out to be merged
// already. Callers treat it as success: the desired end state holds.
type PRAlreadyMergedError struct {
	Repo   string
	Number int
}

func (e *PRAlreadyMergedError) Error() string {
	return fmt.Sprintf("github: pull request %s#%d is already merged", e.Repo, e.Number)
}

// MergePRAtSHA merges a pull request, refusing to proceed if the head has moved
// since expectedHeadSHA was observed.
//
// This is the merge path merge tracking uses, and the `sha` field is what makes
// it safe: GitHub compares it against the current head and answers 409 if they
// differ, so a push landing between evaluation and merge can never result in
// merging an unreviewed commit. MergePR (without a sha) is kept for the
// autonomous gate that predates this.
func (c *Client) MergePRAtSHA(repo string, number int, method, expectedHeadSHA string) (MergeOutcome, error) {
	if strings.TrimSpace(expectedHeadSHA) == "" {
		return MergeOutcome{}, fmt.Errorf("github: merge %s#%d: expected head sha is required", repo, number)
	}
	return c.mergePR(repo, number, method, expectedHeadSHA)
}

// mergePR is the shared implementation behind MergePR (no sha) and
// MergePRAtSHA (sha enforced).
func (c *Client) mergePR(repo string, number int, method, expectedHeadSHA string) (MergeOutcome, error) {
	if method == "" {
		method = "squash"
	}
	payload := map[string]any{"merge_method": method}
	if expectedHeadSHA != "" {
		payload["sha"] = expectedHeadSHA
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("github: marshal merge: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/merge", repo, number)
	resp, err := c.doWithBody("PUT", path, "application/vnd.github+json", "application/json", strings.NewReader(string(data)))
	if err != nil {
		return MergeOutcome{}, fmt.Errorf("github: merge PR %s#%d: %w", repo, number, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		var out struct {
			Merged  bool   `json:"merged"`
			SHA     string `json:"sha"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			// A 200 is authoritative even if the body surprises us.
			return MergeOutcome{Merged: true}, nil
		}
		return MergeOutcome{Merged: out.Merged, SHA: out.SHA, Message: out.Message}, nil
	}

	trimmed := safeTruncate(string(body), maxErrBodyLen)
	return MergeOutcome{}, &MergeRejectedError{
		StatusCode: resp.StatusCode,
		Reason:     classifyMergeRejection(resp.StatusCode, trimmed),
		Body:       trimmed,
	}
}

// classifyMergeRejection maps GitHub's merge failures onto stable reasons.
//
// The 409 case matters most: GitHub uses it both for "head branch was
// modified" (our sha guard fired) and for a merge conflict. Both mean the same
// thing for the caller — stop, re-evaluate, do not retry this SHA — so they
// share the sha_mismatch reason, and the body is preserved for diagnosis.
func classifyMergeRejection(status int, body string) string {
	lower := strings.ToLower(body)
	switch status {
	case http.StatusConflict:
		return MergeReasonSHAMismatch
	case http.StatusForbidden:
		return MergeReasonForbidden
	case http.StatusNotFound:
		return MergeReasonNotFound
	case http.StatusMethodNotAllowed:
		switch {
		case strings.Contains(lower, "required status check"):
			return MergeReasonRequiredCheck
		case strings.Contains(lower, "base branch policy"),
			strings.Contains(lower, "not authorized to push"),
			strings.Contains(lower, "protected branch"):
			return MergeReasonBlocked
		default:
			return MergeReasonNotMergeable
		}
	case http.StatusUnprocessableEntity:
		if strings.Contains(lower, "sha") {
			return MergeReasonSHAMismatch
		}
		return MergeReasonNotMergeable
	default:
		return MergeReasonUnknown
	}
}

// UpdatePRBranch asks GitHub to merge the base branch into the PR's head
// branch, bringing an out-of-date PR up to date.
//
// expectedHeadSHA is required: without it GitHub would happily update whatever
// the head happens to be now, which may be a commit we never evaluated.
//
// GitHub answers 202 Accepted and does the work asynchronously. There is no
// synchronous result to inspect — the caller must observe the new head SHA on a
// later poll rather than waiting in-line.
func (c *Client) UpdatePRBranch(repo string, number int, expectedHeadSHA string) (UpdateBranchOutcome, error) {
	if strings.TrimSpace(expectedHeadSHA) == "" {
		return UpdateBranchOutcome{}, fmt.Errorf("github: update-branch %s#%d: expected head sha is required", repo, number)
	}
	data, err := json.Marshal(map[string]any{"expected_head_sha": expectedHeadSHA})
	if err != nil {
		return UpdateBranchOutcome{}, fmt.Errorf("github: marshal update-branch: %w", err)
	}
	path := fmt.Sprintf("/repos/%s/pulls/%d/update-branch", repo, number)
	resp, err := c.doWithBody("PUT", path, "application/vnd.github+json", "application/json", strings.NewReader(string(data)))
	if err != nil {
		return UpdateBranchOutcome{}, fmt.Errorf("github: update-branch %s#%d: %w", repo, number, err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()

	if resp.StatusCode == http.StatusAccepted {
		var out struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &out)
		return UpdateBranchOutcome{Accepted: true, Message: out.Message}, nil
	}

	trimmed := safeTruncate(string(body), maxErrBodyLen)
	return UpdateBranchOutcome{}, &UpdateBranchRejectedError{
		StatusCode: resp.StatusCode,
		Reason:     classifyUpdateBranchRejection(resp.StatusCode),
		Body:       trimmed,
	}
}

func classifyUpdateBranchRejection(status int) string {
	switch status {
	case http.StatusUnprocessableEntity:
		return UpdateBranchReasonUnprocessable
	case http.StatusConflict:
		return UpdateBranchReasonSHAMismatch
	case http.StatusForbidden:
		return UpdateBranchReasonForbidden
	case http.StatusNotFound:
		return UpdateBranchReasonNotFound
	default:
		return UpdateBranchReasonUnknown
	}
}

const enableAutoMergeMutation = `
mutation($pullRequestId:ID!, $mergeMethod:PullRequestMergeMethod!, $expectedHeadOid:GitObjectID!) {
  enablePullRequestAutoMerge(input:{
    pullRequestId: $pullRequestId,
    mergeMethod: $mergeMethod,
    expectedHeadOid: $expectedHeadOid
  }) {
    pullRequest {
      id
      autoMergeRequest {
        enabledAt
        mergeMethod
        enabledBy { login }
      }
    }
  }
}`

const disableAutoMergeMutation = `
mutation($pullRequestId:ID!) {
  disablePullRequestAutoMerge(input:{pullRequestId: $pullRequestId}) {
    pullRequest { id autoMergeRequest { enabledAt } }
  }
}`

// EnableAutoMerge arms GitHub's native auto-merge on a PR, so GitHub performs
// the merge itself once every requirement is satisfied.
//
// method takes the config-level spelling ("squash"|"merge"|"rebase"); the
// GraphQL enum needs upper case. expectedHeadOid pins the arming to the commit
// we evaluated, the same guard MergePRAtSHA applies.
func (c *Client) EnableAutoMerge(nodeID, method, expectedHeadOid string) (*AutoMergeRequest, error) {
	if strings.TrimSpace(nodeID) == "" {
		return nil, fmt.Errorf("github: enable auto-merge: pull request node id is required")
	}
	if strings.TrimSpace(expectedHeadOid) == "" {
		return nil, fmt.Errorf("github: enable auto-merge: expected head oid is required")
	}
	if err := c.acquireGraphQL(); err != nil {
		return nil, fmt.Errorf("github: enable auto-merge budget: %w", err)
	}

	var resp struct {
		EnablePullRequestAutoMerge *struct {
			PullRequest *struct {
				ID               string `json:"id"`
				AutoMergeRequest *struct {
					EnabledAt   string    `json:"enabledAt"`
					MergeMethod string    `json:"mergeMethod"`
					EnabledBy   *gqlLogin `json:"enabledBy"`
				} `json:"autoMergeRequest"`
			} `json:"pullRequest"`
		} `json:"enablePullRequestAutoMerge"`
	}
	vars := map[string]any{
		"pullRequestId":   nodeID,
		"mergeMethod":     strings.ToUpper(strings.TrimSpace(method)),
		"expectedHeadOid": expectedHeadOid,
	}
	if err := c.graphQL(enableAutoMergeMutation, vars, &resp); err != nil {
		if reason, ok := classifyAutoMergeFailure(err.Error()); ok {
			return nil, &AutoMergeUnavailableError{Reason: reason, Body: err.Error()}
		}
		return nil, fmt.Errorf("github: enable auto-merge: %w", err)
	}
	if resp.EnablePullRequestAutoMerge == nil ||
		resp.EnablePullRequestAutoMerge.PullRequest == nil ||
		resp.EnablePullRequestAutoMerge.PullRequest.AutoMergeRequest == nil {
		// The mutation reported success but no auto-merge request came back.
		// Report it rather than claiming an armed state we cannot see.
		return nil, &AutoMergeUnavailableError{
			Reason: AutoMergeReasonUnknown,
			Body:   "mutation returned no autoMergeRequest",
		}
	}
	am := resp.EnablePullRequestAutoMerge.PullRequest.AutoMergeRequest
	out := &AutoMergeRequest{
		EnabledAt:   parseGraphQLTime(am.EnabledAt),
		MergeMethod: strings.ToUpper(strings.TrimSpace(am.MergeMethod)),
	}
	if out.EnabledAt.IsZero() {
		out.EnabledAt = time.Now().UTC()
	}
	if am.EnabledBy != nil {
		out.EnabledBy = am.EnabledBy.Login
	}
	return out, nil
}

// DisableAutoMerge disarms GitHub's native auto-merge.
//
// Merge tracking calls this immediately before a direct merge: with auto-merge
// still armed, GitHub could fire its own merge concurrently with our PUT, and
// two merge attempts on one PR produce confusing 405/409 noise at best.
func (c *Client) DisableAutoMerge(nodeID string) error {
	if strings.TrimSpace(nodeID) == "" {
		return fmt.Errorf("github: disable auto-merge: pull request node id is required")
	}
	if err := c.acquireGraphQL(); err != nil {
		return fmt.Errorf("github: disable auto-merge budget: %w", err)
	}
	vars := map[string]any{"pullRequestId": nodeID}
	if err := c.graphQL(disableAutoMergeMutation, vars, nil); err != nil {
		lower := strings.ToLower(err.Error())
		// Disabling something already disabled is the state we wanted.
		if strings.Contains(lower, "not enabled") || strings.Contains(lower, "auto merge is not") {
			return nil
		}
		return fmt.Errorf("github: disable auto-merge: %w", err)
	}
	return nil
}

// classifyAutoMergeFailure maps the mutation's error text onto a stable reason.
// GitHub reports these as GraphQL errors with human-readable messages, so text
// matching is the only option; each case is paired with a test asserting the
// exact message GitHub sends today.
func classifyAutoMergeFailure(msg string) (string, bool) {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "auto merge is not allowed"),
		strings.Contains(lower, "auto-merge is not allowed"),
		strings.Contains(lower, "allow auto-merge"):
		return AutoMergeReasonNotAllowedForRepo, true
	case strings.Contains(lower, "clean status"):
		// The PR is already mergeable, so GitHub refuses to queue an auto-merge.
		// The caller should merge directly instead.
		return AutoMergeReasonCleanStatus, true
	case strings.Contains(lower, "expectedheadoid"), strings.Contains(lower, "head oid"):
		return AutoMergeReasonSHAMismatch, true
	case strings.Contains(lower, "already enabled"), strings.Contains(lower, "already set"):
		return AutoMergeReasonAlreadyEnabled, true
	case strings.Contains(lower, "forbidden"), strings.Contains(lower, "not have permission"):
		return AutoMergeReasonForbidden, true
	default:
		return "", false
	}
}
