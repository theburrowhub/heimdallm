package github

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// mergeInfoPreviewAccept is the schema preview required for
// PullRequest.mergeStateStatus. Without it GitHub rejects the field.
const mergeInfoPreviewAccept = "application/vnd.github.merge-info-preview+json"

// Page sizes and caps for the merge-readiness query. The caps exist so a
// pathological PR cannot spin the poller: when a connection is still truncated
// after the cap, the corresponding Truncated flag is set and the evaluator
// treats the unknown remainder as a blocker rather than as "nothing there".
const (
	mergeReadinessThreadPageSize = 100
	mergeReadinessMaxThreadPages = 5
	mergeReadinessCheckPageSize  = 100
	mergeReadinessMaxCheckPages  = 5
	mergeReadinessReviewPageSize = 50
)

// MergeStateStatus values returned by GitHub. Kept as typed constants because
// the evaluator branches on every one of them and a typo would silently fall
// through to the default branch.
const (
	MergeStateBehind   = "BEHIND"
	MergeStateBlocked  = "BLOCKED"
	MergeStateClean    = "CLEAN"
	MergeStateDirty    = "DIRTY"
	MergeStateDraft    = "DRAFT"
	MergeStateHasHooks = "HAS_HOOKS"
	MergeStateUnknown  = "UNKNOWN"
	MergeStateUnstable = "UNSTABLE"
)

// Mergeable values.
const (
	MergeableYes     = "MERGEABLE"
	MergeableNo      = "CONFLICTING"
	MergeableUnknown = "UNKNOWN"
)

// ReviewDecision values. GitHub returns null when the repo requires no
// reviews, which decodes to the empty string.
const (
	ReviewDecisionApproved         = "APPROVED"
	ReviewDecisionChangesRequested = "CHANGES_REQUESTED"
	ReviewDecisionReviewRequired   = "REVIEW_REQUIRED"
)

// CheckState is the normalised state of a single check or commit status.
// GitHub reports check runs via (status, conclusion) and statuses via a single
// state; normalising both into one vocabulary keeps the evaluator honest.
type CheckState string

const (
	CheckStateSuccess CheckState = "success"
	CheckStatePending CheckState = "pending"
	CheckStateFailure CheckState = "failure"
	CheckStateNeutral CheckState = "neutral" // skipped/neutral: green for merge purposes
)

// CheckContext is one check run or commit status on the PR's head commit.
// The full list is carried through to the UI: a merge blocked by CI has to say
// which check, run by which app, with a link to the log.
type CheckContext struct {
	Name        string     `json:"name"`
	Kind        string     `json:"kind"` // "check_run" | "status"
	State       CheckState `json:"state"`
	Raw         string     `json:"raw"` // original conclusion/state, for diagnostics
	Required    bool       `json:"required"`
	Description string     `json:"description,omitempty"`
	App         string     `json:"app,omitempty"`
	URL         string     `json:"url,omitempty"`
	// Pointers rather than values: `omitempty` does nothing for a struct, so a
	// time.Time here would serialise a queued check's unset ends as
	// "0001-01-01T00:00:00Z" and every client would read two equal timestamps
	// as a check that finished instantly.
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

// Duration reports how long the check took, or 0 when GitHub did not report
// both ends.
func (c CheckContext) Duration() time.Duration {
	if c.StartedAt == nil || c.CompletedAt == nil || c.CompletedAt.Before(*c.StartedAt) {
		return 0
	}
	return c.CompletedAt.Sub(*c.StartedAt)
}

// BehindBase reports whether the base branch has moved since the PR was last
// brought up to date. Unknown (false) when either side is missing, because
// guessing "behind" would push a branch nobody asked to move.
func (s *MergeStatus) BehindBase() bool {
	if s == nil || s.BaseOID == "" || s.BaseTipOID == "" {
		return false
	}
	return s.BaseOID != s.BaseTipOID
}

// OpinionatedReview is the latest opinionated review state per reviewer.
type OpinionatedReview struct {
	Login       string    `json:"login"`
	State       string    `json:"state"` // APPROVED | CHANGES_REQUESTED | DISMISSED
	CommitOID   string    `json:"commit_oid"`
	CanPush     bool      `json:"can_push"`
	SubmittedAt time.Time `json:"submitted_at,omitempty"`
}

// ReviewThread is one inline review conversation.
type ReviewThread struct {
	ID          string `json:"id"`
	IsResolved  bool   `json:"is_resolved"`
	IsOutdated  bool   `json:"is_outdated"`
	IsCollapsed bool   `json:"is_collapsed"`
	ResolvedBy  string `json:"resolved_by,omitempty"`
}

// BranchProtection is the subset of the base branch's protection rule that
// affects merge readiness. Nil on MergeStatus when GitHub would not tell us —
// which is the common case, since reading it needs admin on the repository.
type BranchProtection struct {
	RequiresApprovingReviews       bool     `json:"requires_approving_reviews"`
	RequiredApprovingReviewCount   int      `json:"required_approving_review_count"`
	RequiresCodeOwnerReviews       bool     `json:"requires_code_owner_reviews"`
	RequiresStatusChecks           bool     `json:"requires_status_checks"`
	RequiresStrictStatusChecks     bool     `json:"requires_strict_status_checks"`
	RequiredStatusCheckContexts    []string `json:"required_status_check_contexts,omitempty"`
	RequiresConversationResolution bool     `json:"requires_conversation_resolution"`
	RequiresLinearHistory          bool     `json:"requires_linear_history"`
	AllowsForcePushes              bool     `json:"allows_force_pushes"`
}

// AutoMergeRequest mirrors GitHub's native auto-merge state on a PR.
type AutoMergeRequest struct {
	EnabledAt   time.Time `json:"enabled_at"`
	EnabledBy   string    `json:"enabled_by,omitempty"`
	MergeMethod string    `json:"merge_method"` // SQUASH | MERGE | REBASE
}

// MergeMethodSet reports which merge methods the repository allows.
type MergeMethodSet struct {
	Merge  bool `json:"merge"`
	Squash bool `json:"squash"`
	Rebase bool `json:"rebase"`
}

// Allows reports whether the repo permits the given config-level method name
// ("squash"|"merge"|"rebase").
func (s MergeMethodSet) Allows(method string) bool {
	switch strings.ToLower(strings.TrimSpace(method)) {
	case "squash":
		return s.Squash
	case "merge":
		return s.Merge
	case "rebase":
		return s.Rebase
	default:
		return false
	}
}

// MergeStatus is everything the merge-readiness evaluator needs about one PR,
// gathered in a single GraphQL round trip (plus pagination when a PR has an
// unusual number of checks or review threads).
type MergeStatus struct {
	// Viewer / repository scope.
	ViewerLogin         string         `json:"viewer_login"`
	ViewerPermission    string         `json:"viewer_permission"` // ADMIN|MAINTAIN|WRITE|TRIAGE|READ|NONE
	Repo                string         `json:"repo"`
	AllowedMergeMethods MergeMethodSet `json:"allowed_merge_methods"`
	MergeQueueEnabled   bool           `json:"merge_queue_enabled"`

	// PR identity.
	NodeID   string    `json:"node_id"` // GraphQL global id — required by the auto-merge mutations
	Number   int       `json:"number"`
	Title    string    `json:"title"`
	URL      string    `json:"url"`
	State    string    `json:"state"` // OPEN | CLOSED | MERGED
	IsDraft  bool      `json:"is_draft"`
	Merged   bool      `json:"merged"`
	MergedAt time.Time `json:"merged_at,omitempty"`

	// Mergeability.
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"merge_state_status"`
	ReviewDecision   string `json:"review_decision"`

	AutoMerge            *AutoMergeRequest `json:"auto_merge,omitempty"`
	IsInMergeQueue       bool              `json:"is_in_merge_queue"`
	MergeQueueEntryState string            `json:"merge_queue_entry_state,omitempty"`

	// Refs and ownership.
	BaseRef string `json:"base_ref"`
	HeadRef string `json:"head_ref"`
	HeadOID string `json:"head_oid"`
	// BaseOID is the commit the PR is currently based on, and BaseTipOID the
	// current tip of the base branch. They differ exactly when the PR is out of
	// date.
	//
	// This is a signal of our own because mergeStateStatus cannot be one:
	// GitHub collapses everything into a single value with BLOCKED ranked above
	// BEHIND, so a PR that is both behind and waiting on a review or a check
	// never reports BEHIND at all. Verified against this repository: two open
	// PRs whose base was hundreds of commits back both reported DIRTY.
	BaseOID       string   `json:"base_oid"`
	BaseTipOID    string   `json:"base_tip_oid"`
	HeadRepo      string   `json:"head_repo"`
	HeadIsFork    bool     `json:"head_is_fork"`
	HeadRepoOwner string   `json:"head_repo_owner"`
	Author        string   `json:"author"`
	Assignees     []string `json:"assignees,omitempty"`

	// Gating signals.
	Checks          []CheckContext `json:"checks,omitempty"`
	RollupState     string         `json:"rollup_state,omitempty"`
	ChecksTruncated bool           `json:"checks_truncated"`

	Reviews          []OpinionatedReview `json:"reviews,omitempty"`
	ReviewRequests   []string            `json:"review_requests,omitempty"`
	ReviewThreads    []ReviewThread      `json:"review_threads,omitempty"`
	ThreadsTruncated bool                `json:"threads_truncated"`

	Protection *BranchProtection `json:"protection,omitempty"`
	// ProtectionUnreadable is true when GitHub answered with data but refused
	// the branchProtectionRule field (no admin on the repo). The evaluator must
	// then assume the strictest plausible rules, never "no rules".
	ProtectionUnreadable bool `json:"protection_unreadable"`

	FetchedAt time.Time `json:"fetched_at"`
}

// IsTrackedFor reports whether login authored the PR or is one of its
// assignees. Login comparison is normalised the same way as everywhere else in
// this package (case-insensitive, leading "@" tolerated).
func (m *MergeStatus) IsTrackedFor(login string) (isAuthor, isAssignee bool) {
	if m == nil {
		return false, false
	}
	isAuthor = githubLoginsEqual(m.Author, login)
	for _, a := range m.Assignees {
		if githubLoginsEqual(a, login) {
			isAssignee = true
			break
		}
	}
	return isAuthor, isAssignee
}

// mergeStatusQuery gathers merge readiness in one round trip.
//
// Notes on specific selections:
//   - `mergeStateStatus` needs the merge-info preview Accept header.
//   - `latestOpinionatedReviews(writersOnly:true)` gives the current APPROVED /
//     CHANGES_REQUESTED per reviewer, restricted to people who can actually
//     push — a drive-by approval from someone without write access does not
//     satisfy branch protection, so counting it would be a real defect.
//   - `commit.oid` on each review is what anchors an approval to a SHA.
//   - `isRequired(pullRequestNumber:)` marks the checks that actually gate.
//   - `baseRef.branchProtectionRule` needs admin; a FORBIDDEN field error here
//     is expected and tolerated (see graphQLOptions.TolerateFieldErrors).
const mergeStatusQueryTemplate = `
query($owner:String!, $name:String!, $number:Int!, $threadCursor:String, $checkCursor:String) {
  viewer { login }
  repository(owner:$owner, name:$name) {
    nameWithOwner
    viewerPermission
    mergeCommitAllowed
    squashMergeAllowed
    rebaseMergeAllowed
    mergeQueue { id }
    pullRequest(number:$number) {
      id
      number
      title
      url
      state
      isDraft
      merged
      mergedAt
      mergeable
      mergeStateStatus
      reviewDecision
      isInMergeQueue
      mergeQueueEntry { state position }
      autoMergeRequest {
        enabledAt
        mergeMethod
        enabledBy { login }
      }
      baseRefName
      baseRefOid
      headRefName
      headRefOid
      headRepository { nameWithOwner isFork }
      headRepositoryOwner { login }
      author { login }
      assignees(first: 20) { nodes { login } }
      latestOpinionatedReviews(first: $$REVIEWS$$, writersOnly: true) {
        nodes {
          state
          submittedAt
          authorCanPushToRepository
          author { login }
          commit { oid }
        }
      }
      reviewRequests(first: 20) {
        totalCount
        nodes {
          requestedReviewer {
            __typename
            ... on User { login }
            ... on Team { slug }
          }
        }
      }
      reviewThreads(first: $$THREADS$$, after: $threadCursor) {
        pageInfo { hasNextPage endCursor }
        nodes {
          id
          isResolved
          isOutdated
          isCollapsed
          resolvedBy { login }
        }
      }
      commits(last: 1) {
        nodes {
          commit {
            oid
            statusCheckRollup {
              state
              contexts(first: $$CHECKS$$, after: $checkCursor) {
                totalCount
                pageInfo { hasNextPage endCursor }
                nodes {
                  __typename
                  ... on CheckRun {
                    name
                    status
                    conclusion
                    isRequired(pullRequestNumber: $number)
                    detailsUrl
                    startedAt
                    completedAt
                    checkSuite { app { name } }
                  }
                  ... on StatusContext {
                    context
                    state
                    isRequired(pullRequestNumber: $number)
                    description
                    targetUrl
                    createdAt
                  }
                }
              }
            }
          }
        }
      }
      baseRef {
        name
        target { oid }
        branchProtectionRule {
          requiresApprovingReviews
          requiredApprovingReviewCount
          requiresCodeOwnerReviews
          requiresStatusChecks
          requiresStrictStatusChecks
          requiredStatusCheckContexts
          requiresConversationResolution
          requiresLinearHistory
          allowsForcePushes
        }
      }
    }
  }
}`

// mergeStatusQuery is the template with the page sizes substituted, so the
// constants above remain the single source of truth for both the query and the
// pagination caps.
var mergeStatusQuery = strings.NewReplacer(
	"$$REVIEWS$$", strconv.Itoa(mergeReadinessReviewPageSize),
	"$$THREADS$$", strconv.Itoa(mergeReadinessThreadPageSize),
	"$$CHECKS$$", strconv.Itoa(mergeReadinessCheckPageSize),
).Replace(mergeStatusQueryTemplate)

// The GraphQL response is modelled with named types rather than nested
// anonymous structs: the decoders below take these as parameters, and an
// anonymous type in a function signature has to be repeated character for
// character at every use site.

type gqlLogin struct {
	Login string `json:"login"`
}

type gqlPageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

// gqlContextNode is one node of statusCheckRollup.contexts. It is a union of
// CheckRun and StatusContext, so both field sets live here and __typename
// selects which half is meaningful.
type gqlContextNode struct {
	TypeName string `json:"__typename"`

	// CheckRun
	Name        string `json:"name"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	DetailsURL  string `json:"detailsUrl"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
	CheckSuite  *struct {
		App *struct {
			Name string `json:"name"`
		} `json:"app"`
	} `json:"checkSuite"`

	// StatusContext
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"targetUrl"`
	CreatedAt   string `json:"createdAt"`

	// Both
	IsRequired bool `json:"isRequired"`
}

type gqlContexts struct {
	TotalCount int              `json:"totalCount"`
	PageInfo   gqlPageInfo      `json:"pageInfo"`
	Nodes      []gqlContextNode `json:"nodes"`
}

type gqlRollup struct {
	State    string      `json:"state"`
	Contexts gqlContexts `json:"contexts"`
}

type gqlThreadNode struct {
	ID          string    `json:"id"`
	IsResolved  bool      `json:"isResolved"`
	IsOutdated  bool      `json:"isOutdated"`
	IsCollapsed bool      `json:"isCollapsed"`
	ResolvedBy  *gqlLogin `json:"resolvedBy"`
}

type gqlReviewNode struct {
	State                     string    `json:"state"`
	SubmittedAt               string    `json:"submittedAt"`
	AuthorCanPushToRepository bool      `json:"authorCanPushToRepository"`
	Author                    *gqlLogin `json:"author"`
	Commit                    *struct {
		OID string `json:"oid"`
	} `json:"commit"`
}

type gqlBranchProtectionRule struct {
	RequiresApprovingReviews       bool     `json:"requiresApprovingReviews"`
	RequiredApprovingReviewCount   int      `json:"requiredApprovingReviewCount"`
	RequiresCodeOwnerReviews       bool     `json:"requiresCodeOwnerReviews"`
	RequiresStatusChecks           bool     `json:"requiresStatusChecks"`
	RequiresStrictStatusChecks     bool     `json:"requiresStrictStatusChecks"`
	RequiredStatusCheckContexts    []string `json:"requiredStatusCheckContexts"`
	RequiresConversationResolution bool     `json:"requiresConversationResolution"`
	RequiresLinearHistory          bool     `json:"requiresLinearHistory"`
	AllowsForcePushes              bool     `json:"allowsForcePushes"`
}

type gqlPullRequest struct {
	ID               string `json:"id"`
	Number           int    `json:"number"`
	Title            string `json:"title"`
	URL              string `json:"url"`
	State            string `json:"state"`
	IsDraft          bool   `json:"isDraft"`
	Merged           bool   `json:"merged"`
	MergedAt         string `json:"mergedAt"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	IsInMergeQueue   bool   `json:"isInMergeQueue"`

	MergeQueueEntry *struct {
		State    string `json:"state"`
		Position int    `json:"position"`
	} `json:"mergeQueueEntry"`

	AutoMergeRequest *struct {
		EnabledAt   string    `json:"enabledAt"`
		MergeMethod string    `json:"mergeMethod"`
		EnabledBy   *gqlLogin `json:"enabledBy"`
	} `json:"autoMergeRequest"`

	BaseRefName    string `json:"baseRefName"`
	BaseRefOid     string `json:"baseRefOid"`
	HeadRefName    string `json:"headRefName"`
	HeadRefOid     string `json:"headRefOid"`
	HeadRepository *struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsFork        bool   `json:"isFork"`
	} `json:"headRepository"`
	HeadRepositoryOwner *gqlLogin `json:"headRepositoryOwner"`
	Author              *gqlLogin `json:"author"`

	Assignees struct {
		Nodes []gqlLogin `json:"nodes"`
	} `json:"assignees"`

	LatestOpinionatedReviews struct {
		Nodes []gqlReviewNode `json:"nodes"`
	} `json:"latestOpinionatedReviews"`

	ReviewRequests struct {
		TotalCount int `json:"totalCount"`
		Nodes      []struct {
			RequestedReviewer *struct {
				TypeName string `json:"__typename"`
				Login    string `json:"login"`
				Slug     string `json:"slug"`
			} `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`

	ReviewThreads struct {
		PageInfo gqlPageInfo     `json:"pageInfo"`
		Nodes    []gqlThreadNode `json:"nodes"`
	} `json:"reviewThreads"`

	Commits struct {
		Nodes []struct {
			Commit struct {
				OID               string     `json:"oid"`
				StatusCheckRollup *gqlRollup `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`

	BaseRef *struct {
		Name   string `json:"name"`
		Target *struct {
			OID string `json:"oid"`
		} `json:"target"`
		BranchProtectionRule *gqlBranchProtectionRule `json:"branchProtectionRule"`
	} `json:"baseRef"`
}

type gqlRepository struct {
	NameWithOwner      string `json:"nameWithOwner"`
	ViewerPermission   string `json:"viewerPermission"`
	MergeCommitAllowed bool   `json:"mergeCommitAllowed"`
	SquashMergeAllowed bool   `json:"squashMergeAllowed"`
	RebaseMergeAllowed bool   `json:"rebaseMergeAllowed"`
	MergeQueue         *struct {
		ID string `json:"id"`
	} `json:"mergeQueue"`
	PullRequest *gqlPullRequest `json:"pullRequest"`
}

// mergeStatusResponse mirrors the query's shape.
type mergeStatusResponse struct {
	Viewer     gqlLogin       `json:"viewer"`
	Repository *gqlRepository `json:"repository"`
}

// ErrPRNotFound means the repo/number pair resolved to nothing — the PR was
// deleted, or the token lost access to the repository.
var ErrPRNotFound = errors.New("github: pull request not found")

// GetMergeStatus fetches everything needed to decide whether a PR can be
// merged, in one GraphQL round trip plus pagination for oversized check and
// thread connections.
//
// A FORBIDDEN field error on branchProtectionRule is expected for tokens
// without admin on the repository: the data is returned with
// ProtectionUnreadable set so the evaluator can fail closed instead of
// assuming the branch has no rules.
func (c *Client) GetMergeStatus(repo string, number int) (*MergeStatus, error) {
	owner, name, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	if err := c.acquireGraphQL(); err != nil {
		return nil, fmt.Errorf("github: merge status budget: %w", err)
	}

	vars := map[string]any{"owner": owner, "name": name, "number": number}
	var resp mergeStatusResponse
	protectionUnreadable := false

	gqlErr := c.graphQLWith(graphQLOptions{
		Accept:              mergeInfoPreviewAccept,
		TolerateFieldErrors: true,
	}, mergeStatusQuery, vars, &resp)
	if gqlErr != nil {
		var partial *PartialGraphQLError
		if !errors.As(gqlErr, &partial) {
			return nil, fmt.Errorf("github: merge status %s#%d: %w", repo, number, gqlErr)
		}
		// Only branch protection is allowed to be missing. Anything else means
		// we would be evaluating on incomplete gating data.
		if !onlyProtectionErrors(partial) {
			return nil, fmt.Errorf("github: merge status %s#%d: unusable partial response: %w", repo, number, gqlErr)
		}
		protectionUnreadable = true
	}

	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return nil, fmt.Errorf("%w: %s#%d", ErrPRNotFound, repo, number)
	}

	st := buildMergeStatus(&resp, repo)
	st.ProtectionUnreadable = protectionUnreadable
	if protectionUnreadable {
		st.Protection = nil
	}

	// Paginate the connections that can exceed one page. Both are capped; an
	// exhausted cap sets the Truncated flag rather than pretending the tail
	// does not exist.
	if err := c.paginateReviewThreads(owner, name, number, &resp, st); err != nil {
		return nil, err
	}
	if err := c.paginateCheckContexts(owner, name, number, &resp, st); err != nil {
		return nil, err
	}

	st.FetchedAt = time.Now().UTC()
	return st, nil
}

func (c *Client) paginateReviewThreads(owner, name string, number int, first *mergeStatusResponse, st *MergeStatus) error {
	pi := first.Repository.PullRequest.ReviewThreads.PageInfo
	page := 1
	for pi.HasNextPage {
		if page >= mergeReadinessMaxThreadPages {
			st.ThreadsTruncated = true
			return nil
		}
		if err := c.acquireGraphQL(); err != nil {
			return fmt.Errorf("github: merge status thread page budget: %w", err)
		}
		var resp mergeStatusResponse
		vars := map[string]any{
			"owner": owner, "name": name, "number": number,
			"threadCursor": pi.EndCursor,
		}
		err := c.graphQLWith(graphQLOptions{
			Accept:              mergeInfoPreviewAccept,
			TolerateFieldErrors: true,
		}, mergeStatusQuery, vars, &resp)
		if err != nil {
			var partial *PartialGraphQLError
			if !errors.As(err, &partial) {
				return fmt.Errorf("github: merge status threads page %d: %w", page+1, err)
			}
			if !onlyProtectionErrors(partial) {
				// A tolerated field error on the connection itself decodes to
				// an empty page with hasNextPage false, which would end the
				// loop as if the tail did not exist. Say so instead: the
				// evaluator refuses to call a truncated PR ready.
				st.ThreadsTruncated = true
				return nil
			}
		}
		if resp.Repository == nil || resp.Repository.PullRequest == nil {
			st.ThreadsTruncated = true
			return nil
		}
		st.ReviewThreads = append(st.ReviewThreads, decodeReviewThreads(&resp)...)
		pi = resp.Repository.PullRequest.ReviewThreads.PageInfo
		page++
	}
	return nil
}

func (c *Client) paginateCheckContexts(owner, name string, number int, first *mergeStatusResponse, st *MergeStatus) error {
	rollup := firstRollup(first)
	if rollup == nil {
		return nil
	}
	pi := rollup.Contexts.PageInfo
	page := 1
	for pi.HasNextPage {
		if page >= mergeReadinessMaxCheckPages {
			st.ChecksTruncated = true
			return nil
		}
		if err := c.acquireGraphQL(); err != nil {
			return fmt.Errorf("github: merge status check page budget: %w", err)
		}
		var resp mergeStatusResponse
		vars := map[string]any{
			"owner": owner, "name": name, "number": number,
			"checkCursor": pi.EndCursor,
		}
		err := c.graphQLWith(graphQLOptions{
			Accept:              mergeInfoPreviewAccept,
			TolerateFieldErrors: true,
		}, mergeStatusQuery, vars, &resp)
		if err != nil {
			var partial *PartialGraphQLError
			if !errors.As(err, &partial) {
				return fmt.Errorf("github: merge status checks page %d: %w", page+1, err)
			}
			if !onlyProtectionErrors(partial) {
				// Same fail-safe as the thread loop: an unexpected field error
				// on this page means we cannot see every check, and a check we
				// cannot see is not a check we may declare green.
				st.ChecksTruncated = true
				return nil
			}
		}
		next := firstRollup(&resp)
		if next == nil {
			st.ChecksTruncated = true
			return nil
		}
		st.Checks = append(st.Checks, decodeCheckContexts(next)...)
		pi = next.Contexts.PageInfo
		page++
	}
	return nil
}

// onlyProtectionErrors reports whether every path in a partial response points
// at branchProtectionRule — the one field a non-admin token is expected to be
// refused. Any other missing field means the snapshot is incomplete somewhere
// that matters for the merge decision.
func onlyProtectionErrors(partial *PartialGraphQLError) bool {
	if partial == nil {
		return false
	}
	for _, p := range partial.Paths {
		if !strings.Contains(p, "branchProtectionRule") {
			return false
		}
	}
	return len(partial.Paths) > 0
}
