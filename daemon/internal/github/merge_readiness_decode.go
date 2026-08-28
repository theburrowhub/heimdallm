package github

import (
	"fmt"
	"strings"
	"time"
)

// splitRepo splits an "owner/name" slug into its two segments. GraphQL takes
// owner and name as separate variables, unlike the REST paths that interpolate
// the slug whole.
func splitRepo(repo string) (owner, name string, err error) {
	repo = strings.TrimSpace(repo)
	i := strings.Index(repo, "/")
	if i <= 0 || i == len(repo)-1 || strings.Contains(repo[i+1:], "/") {
		return "", "", fmt.Errorf("github: invalid repo slug %q, want owner/name", repo)
	}
	return repo[:i], repo[i+1:], nil
}

// parseGraphQLTime parses an ISO-8601 timestamp, returning the zero time for
// empty or malformed input. GitHub omits timestamps for states that have not
// happened yet (a queued check has no completedAt), so a parse miss is normal
// and must not fail the whole fetch.
func parseGraphQLTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

// normalizeCheckState collapses GitHub's two reporting shapes into one
// vocabulary.
//
// The single most important rule here: a check run whose status is not
// COMPLETED is pending regardless of what conclusion says. GitHub leaves a
// stale conclusion in place while a check re-runs, so reading conclusion first
// would report a green check that is currently running — and that is exactly
// the mistake that merges an unverified commit.
//
// SKIPPED and NEUTRAL count as green: GitHub's own merge gate treats them as
// satisfied, so treating them as failures would block PRs GitHub would merge.
func normalizeCheckState(kind, status, conclusion, state string) (CheckState, string) {
	status = strings.ToUpper(strings.TrimSpace(status))
	conclusion = strings.ToUpper(strings.TrimSpace(conclusion))
	state = strings.ToUpper(strings.TrimSpace(state))

	if kind == "check_run" {
		if status != "" && status != "COMPLETED" {
			// QUEUED, IN_PROGRESS, WAITING, PENDING, REQUESTED
			return CheckStatePending, status
		}
		switch conclusion {
		case "SUCCESS":
			return CheckStateSuccess, conclusion
		case "SKIPPED", "NEUTRAL":
			return CheckStateNeutral, conclusion
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE", "STALE":
			return CheckStateFailure, conclusion
		case "":
			// COMPLETED with no conclusion should not happen; treat the unknown
			// as pending rather than as green.
			return CheckStatePending, status
		default:
			return CheckStateFailure, conclusion
		}
	}

	// StatusContext
	switch state {
	case "SUCCESS":
		return CheckStateSuccess, state
	case "PENDING", "EXPECTED":
		return CheckStatePending, state
	case "FAILURE", "ERROR":
		return CheckStateFailure, state
	default:
		return CheckStatePending, state
	}
}

// firstRollup returns the status check rollup of the PR's head commit, or nil
// when GitHub reported no rollup (a PR whose head has no checks at all).
func firstRollup(resp *mergeStatusResponse) *gqlRollup {
	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return nil
	}
	nodes := resp.Repository.PullRequest.Commits.Nodes
	if len(nodes) == 0 {
		return nil
	}
	return nodes[0].Commit.StatusCheckRollup
}

// decodeCheckContexts maps one page of rollup contexts to CheckContext values.
func decodeCheckContexts(rollup *gqlRollup) []CheckContext {
	if rollup == nil {
		return nil
	}
	out := make([]CheckContext, 0, len(rollup.Contexts.Nodes))
	for _, n := range rollup.Contexts.Nodes {
		cc := CheckContext{Required: n.IsRequired}
		switch n.TypeName {
		case "CheckRun":
			cc.Kind = "check_run"
			cc.Name = n.Name
			cc.URL = n.DetailsURL
			cc.StartedAt = parseGraphQLTime(n.StartedAt)
			cc.CompletedAt = parseGraphQLTime(n.CompletedAt)
			if n.CheckSuite != nil && n.CheckSuite.App != nil {
				cc.App = n.CheckSuite.App.Name
			}
			cc.State, cc.Raw = normalizeCheckState("check_run", n.Status, n.Conclusion, "")
		case "StatusContext":
			cc.Kind = "status"
			cc.Name = n.Context
			cc.URL = n.TargetURL
			cc.Description = n.Description
			cc.StartedAt = parseGraphQLTime(n.CreatedAt)
			cc.State, cc.Raw = normalizeCheckState("status", "", "", n.State)
		default:
			// An unknown context type is not something to guess about: a check
			// we cannot classify must not read as green.
			cc.Kind = strings.ToLower(n.TypeName)
			cc.Name = firstNonEmpty(n.Name, n.Context, n.TypeName)
			cc.State = CheckStatePending
			cc.Raw = n.TypeName
		}
		if strings.TrimSpace(cc.Name) == "" {
			continue
		}
		out = append(out, cc)
	}
	return out
}

// decodeReviewThreads maps one page of review threads.
func decodeReviewThreads(resp *mergeStatusResponse) []ReviewThread {
	if resp.Repository == nil || resp.Repository.PullRequest == nil {
		return nil
	}
	nodes := resp.Repository.PullRequest.ReviewThreads.Nodes
	out := make([]ReviewThread, 0, len(nodes))
	for _, n := range nodes {
		t := ReviewThread{
			ID:          n.ID,
			IsResolved:  n.IsResolved,
			IsOutdated:  n.IsOutdated,
			IsCollapsed: n.IsCollapsed,
		}
		if n.ResolvedBy != nil {
			t.ResolvedBy = n.ResolvedBy.Login
		}
		out = append(out, t)
	}
	return out
}

// buildMergeStatus maps the first (and usually only) response page into the
// MergeStatus the evaluator consumes.
func buildMergeStatus(resp *mergeStatusResponse, repo string) *MergeStatus {
	r := resp.Repository
	pr := r.PullRequest

	st := &MergeStatus{
		ViewerLogin:      resp.Viewer.Login,
		ViewerPermission: strings.ToUpper(strings.TrimSpace(r.ViewerPermission)),
		Repo:             firstNonEmpty(r.NameWithOwner, repo),
		AllowedMergeMethods: MergeMethodSet{
			Merge:  r.MergeCommitAllowed,
			Squash: r.SquashMergeAllowed,
			Rebase: r.RebaseMergeAllowed,
		},
		MergeQueueEnabled: r.MergeQueue != nil && r.MergeQueue.ID != "",

		NodeID:   pr.ID,
		Number:   pr.Number,
		Title:    pr.Title,
		URL:      pr.URL,
		State:    strings.ToUpper(strings.TrimSpace(pr.State)),
		IsDraft:  pr.IsDraft,
		Merged:   pr.Merged,
		MergedAt: parseGraphQLTime(pr.MergedAt),

		Mergeable:        strings.ToUpper(strings.TrimSpace(pr.Mergeable)),
		MergeStateStatus: strings.ToUpper(strings.TrimSpace(pr.MergeStateStatus)),
		ReviewDecision:   strings.ToUpper(strings.TrimSpace(pr.ReviewDecision)),

		IsInMergeQueue: pr.IsInMergeQueue,

		BaseRef: pr.BaseRefName,
		HeadRef: pr.HeadRefName,
		HeadOID: pr.HeadRefOid,

		RollupState: rollupState(resp),
	}

	if pr.MergeQueueEntry != nil {
		st.MergeQueueEntryState = strings.ToUpper(strings.TrimSpace(pr.MergeQueueEntry.State))
	}
	if pr.AutoMergeRequest != nil {
		am := &AutoMergeRequest{
			EnabledAt:   parseGraphQLTime(pr.AutoMergeRequest.EnabledAt),
			MergeMethod: strings.ToUpper(strings.TrimSpace(pr.AutoMergeRequest.MergeMethod)),
		}
		if pr.AutoMergeRequest.EnabledBy != nil {
			am.EnabledBy = pr.AutoMergeRequest.EnabledBy.Login
		}
		st.AutoMerge = am
	}
	if pr.HeadRepository != nil {
		st.HeadRepo = pr.HeadRepository.NameWithOwner
		st.HeadIsFork = pr.HeadRepository.IsFork
	}
	if pr.HeadRepositoryOwner != nil {
		st.HeadRepoOwner = pr.HeadRepositoryOwner.Login
	}
	if pr.Author != nil {
		st.Author = pr.Author.Login
	}
	for _, a := range pr.Assignees.Nodes {
		if a.Login != "" {
			st.Assignees = append(st.Assignees, a.Login)
		}
	}

	for _, rv := range pr.LatestOpinionatedReviews.Nodes {
		review := OpinionatedReview{
			State:       strings.ToUpper(strings.TrimSpace(rv.State)),
			CanPush:     rv.AuthorCanPushToRepository,
			SubmittedAt: parseGraphQLTime(rv.SubmittedAt),
		}
		if rv.Author != nil {
			review.Login = rv.Author.Login
		}
		if rv.Commit != nil {
			review.CommitOID = rv.Commit.OID
		}
		st.Reviews = append(st.Reviews, review)
	}

	for _, rr := range pr.ReviewRequests.Nodes {
		if rr.RequestedReviewer == nil {
			continue
		}
		if name := firstNonEmpty(rr.RequestedReviewer.Login, rr.RequestedReviewer.Slug); name != "" {
			st.ReviewRequests = append(st.ReviewRequests, name)
		}
	}
	// A requested reviewer we could not name still gates the PR, so record the
	// shortfall rather than silently dropping it.
	if missing := pr.ReviewRequests.TotalCount - len(st.ReviewRequests); missing > 0 {
		for i := 0; i < missing; i++ {
			st.ReviewRequests = append(st.ReviewRequests, "(undisclosed reviewer)")
		}
	}

	st.ReviewThreads = decodeReviewThreads(resp)
	st.Checks = decodeCheckContexts(firstRollup(resp))

	if pr.BaseRef != nil && pr.BaseRef.BranchProtectionRule != nil {
		bp := pr.BaseRef.BranchProtectionRule
		st.Protection = &BranchProtection{
			RequiresApprovingReviews:       bp.RequiresApprovingReviews,
			RequiredApprovingReviewCount:   bp.RequiredApprovingReviewCount,
			RequiresCodeOwnerReviews:       bp.RequiresCodeOwnerReviews,
			RequiresStatusChecks:           bp.RequiresStatusChecks,
			RequiresStrictStatusChecks:     bp.RequiresStrictStatusChecks,
			RequiredStatusCheckContexts:    bp.RequiredStatusCheckContexts,
			RequiresConversationResolution: bp.RequiresConversationResolution,
			RequiresLinearHistory:          bp.RequiresLinearHistory,
			AllowsForcePushes:              bp.AllowsForcePushes,
		}
	}

	return st
}

func rollupState(resp *mergeStatusResponse) string {
	if r := firstRollup(resp); r != nil {
		return strings.ToUpper(strings.TrimSpace(r.State))
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
