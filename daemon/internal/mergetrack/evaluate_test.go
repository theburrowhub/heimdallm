package mergetrack_test

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/mergetrack"
	"github.com/heimdallm/daemon/internal/store"
)

const (
	viewer  = "octocat"
	headSHA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldSHA  = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// cleanStatus is a PR that every rule should agree is mergeable: authored by
// the viewer, open, CLEAN, approved by someone with push access at the current
// head, no threads, all required checks green.
func cleanStatus() *gh.MergeStatus {
	return &gh.MergeStatus{
		ViewerLogin:         viewer,
		ViewerPermission:    "WRITE",
		Repo:                "acme/widgets",
		AllowedMergeMethods: gh.MergeMethodSet{Squash: true, Merge: true, Rebase: true},
		NodeID:              "PR_node",
		Number:              7,
		State:               "OPEN",
		Mergeable:           gh.MergeableYes,
		MergeStateStatus:    gh.MergeStateClean,
		ReviewDecision:      gh.ReviewDecisionApproved,
		BaseRef:             "main",
		HeadRef:             "feature",
		HeadOID:             headSHA,
		HeadRepo:            "acme/widgets",
		Author:              viewer,
		Reviews: []gh.OpinionatedReview{
			{Login: "reviewer", State: "APPROVED", CommitOID: headSHA, CanPush: true},
		},
		Checks: []gh.CheckContext{
			{Name: "build", Kind: "check_run", State: gh.CheckStateSuccess, Required: true, App: "GitHub Actions"},
		},
	}
}

// enabledCfg turns on tracking with every automation off — the shape a cautious
// operator starts from.
func enabledCfg() config.MergeTrackingConfig {
	return config.MergeTrackingConfig{
		Enabled:            true,
		MergeMethod:        config.MergeMethodSquash,
		IncludeAssigned:    true,
		MaxUpdateAttempts:  3,
		MaxResolveAttempts: 2,
		MaxMergeAttempts:   3,
	}
}

func baseInput(cfg config.MergeTrackingConfig) mergetrack.Input {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	return mergetrack.Input{
		Cfg:         cfg,
		ViewerLogin: viewer,
		State:       store.MergeTracking{HeadSHA: headSHA, Phase: store.MergePhaseIdle},
		Now:         now,
		TickStart:   now,
	}
}

func TestEvaluate_CleanPRIsReady(t *testing.T) {
	d := mergetrack.Evaluate(cleanStatus(), baseInput(enabledCfg()))
	if !d.Ready {
		t.Fatalf("clean PR not ready, blocks = %v", d.Blocks)
	}
	if d.PrimaryReason() != mergetrack.ReasonNone {
		t.Errorf("reason = %q, want empty", d.PrimaryReason())
	}
	if d.HeadSHA != headSHA {
		t.Errorf("head sha = %q, want %q", d.HeadSHA, headSHA)
	}
}

// A merged PR must report already_merged and nothing else, no matter what the
// rest of the snapshot says. Merging twice is the failure this prevents.
func TestEvaluate_MergedIsIdempotent(t *testing.T) {
	st := cleanStatus()
	st.Merged = true
	st.State = "MERGED"
	st.MergeStateStatus = gh.MergeStateUnknown // deliberately hostile
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Action != mergetrack.ActionMarkMerged {
		t.Errorf("action = %q, want %q", d.Action, mergetrack.ActionMarkMerged)
	}
	if d.PrimaryReason() != mergetrack.ReasonAlreadyMerged {
		t.Errorf("reason = %q, want already_merged", d.PrimaryReason())
	}
	if d.Ready {
		t.Error("a merged PR must not report ready")
	}
}

// theburrowhub/heimdallm#674: "Alice solicita cambios y Bob aprueba después: no
// hay merge hasta resolver/dismiss correctamente lo de Alice."
func TestEvaluate_ChangesRequestedDominatesLaterApproval(t *testing.T) {
	st := cleanStatus()
	st.ReviewDecision = gh.ReviewDecisionApproved // GitHub itself would allow it
	st.Reviews = []gh.OpinionatedReview{
		{Login: "alice", State: "CHANGES_REQUESTED", CommitOID: headSHA, CanPush: true},
		{Login: "bob", State: "APPROVED", CommitOID: headSHA, CanPush: true},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a standing CHANGES_REQUESTED must block even when GitHub reports APPROVED")
	}
	if d.PrimaryReason() != mergetrack.ReasonChangesRequested {
		t.Errorf("reason = %q, want changes_requested", d.PrimaryReason())
	}
	if !strings.Contains(d.PrimaryDetail(), "alice") {
		t.Errorf("detail %q should name alice", d.PrimaryDetail())
	}
}

// A change request stays active across pushes (GitHub keeps it until the
// reviewer resolves it), so an older commit OID must not make it stale.
func TestEvaluate_ChangesRequestedOnOlderCommitStillBlocks(t *testing.T) {
	st := cleanStatus()
	st.Reviews = []gh.OpinionatedReview{
		{Login: "alice", State: "CHANGES_REQUESTED", CommitOID: oldSHA, CanPush: true},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a change request from an earlier commit is still active and must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonChangesRequested {
		t.Errorf("reason = %q, want changes_requested", d.PrimaryReason())
	}
}

// theburrowhub/heimdallm#674: "Un push posterior invalida aprobaciones y
// evidencia del SHA anterior." An approval anchored to an older commit is not
// an approval of what would actually be merged.
func TestEvaluate_ApprovalOnOlderCommitIsStale(t *testing.T) {
	st := cleanStatus()
	st.ReviewDecision = gh.ReviewDecisionApproved
	st.Reviews = []gh.OpinionatedReview{
		{Login: "reviewer", State: "APPROVED", CommitOID: oldSHA, CanPush: true},
	}
	cfg := enabledCfg()
	cfg.RequireApproval = true
	d := mergetrack.Evaluate(st, baseInput(cfg))
	if d.Ready {
		t.Fatal("a stale approval must not satisfy require_approval")
	}
	if d.PrimaryReason() != mergetrack.ReasonInsufficientApprovals {
		t.Errorf("reason = %q, want insufficient_approvals", d.PrimaryReason())
	}
	if d.Evidence.StaleApprovals != 1 {
		t.Errorf("stale approvals = %d, want 1", d.Evidence.StaleApprovals)
	}
	if d.Evidence.ApprovalsAtHead != 0 {
		t.Errorf("approvals at head = %d, want 0", d.Evidence.ApprovalsAtHead)
	}
}

// Our own approval on our own PR is not an approval.
func TestEvaluate_SelfApprovalIsIgnored(t *testing.T) {
	st := cleanStatus()
	st.Reviews = []gh.OpinionatedReview{
		{Login: viewer, State: "APPROVED", CommitOID: headSHA, CanPush: true},
	}
	cfg := enabledCfg()
	cfg.RequireApproval = true
	d := mergetrack.Evaluate(st, baseInput(cfg))
	if d.Ready {
		t.Fatal("self-approval must not satisfy require_approval")
	}
	if d.Evidence.ApprovalsAtHead != 0 {
		t.Errorf("approvals at head = %d, want 0", d.Evidence.ApprovalsAtHead)
	}
}

// An approval from someone without push access cannot satisfy branch
// protection, so counting it would be a real defect.
func TestEvaluate_ApprovalWithoutPushAccessDoesNotCount(t *testing.T) {
	st := cleanStatus()
	st.Reviews = []gh.OpinionatedReview{
		{Login: "drive-by", State: "APPROVED", CommitOID: headSHA, CanPush: false},
	}
	cfg := enabledCfg()
	cfg.RequireApproval = true
	d := mergetrack.Evaluate(st, baseInput(cfg))
	if d.Ready {
		t.Fatal("an approval from a user without push access must not count")
	}
}

func TestEvaluate_DraftIsNeverActionable(t *testing.T) {
	st := cleanStatus()
	st.IsDraft = true
	cfg := enabledCfg()
	// Every automation on: a draft must still be untouched.
	cfg.EnableAutoMerge, cfg.UpdateBranch, cfg.ResolveConflicts, cfg.Merge = true, true, true, true

	d := mergetrack.Decide(mergetrack.Evaluate(st, baseInput(cfg)), st, baseInput(cfg))
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none for a draft", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonDraft {
		t.Errorf("reason = %q, want draft", d.PrimaryReason())
	}
}

// UNKNOWN means GitHub has not finished computing. Reading it as anything but
// "ask again" is how an unmergeable PR gets merged.
func TestEvaluate_UnknownMergeabilityWaits(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableUnknown
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("unknown mergeability must never be ready")
	}
	if d.Action != mergetrack.ActionWait {
		t.Errorf("action = %q, want wait", d.Action)
	}
	if d.CooldownHint <= 0 || d.CooldownHint > time.Minute {
		t.Errorf("cooldown hint = %v, want a short recheck", d.CooldownHint)
	}
}

func TestEvaluate_UnknownMergeabilityGivesUpAfterCap(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateUnknown
	in := baseInput(enabledCfg())
	in.State.UnknownWaits = 5
	d := mergetrack.Evaluate(st, in)
	if d.Action == mergetrack.ActionWait {
		t.Error("after the wait cap the evaluator must stop rechecking every cycle")
	}
	if d.CooldownHint < time.Minute {
		t.Errorf("cooldown hint = %v, want a long backoff", d.CooldownHint)
	}
}

// UNSTABLE means a non-required check is red. GitHub merges those, so blocking
// would stop PRs the platform itself considers mergeable.
func TestEvaluate_OptionalFailingCheckDoesNotBlock(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateUnstable
	st.Checks = []gh.CheckContext{
		{Name: "build", State: gh.CheckStateSuccess, Required: true},
		{Name: "coverage", State: gh.CheckStateFailure, Required: false},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if !d.Ready {
		t.Fatalf("an optional failing check must not block, blocks = %v", d.Blocks)
	}
	if d.ChecksSummary.OptionalFailing != 1 {
		t.Errorf("optional failing = %d, want 1", d.ChecksSummary.OptionalFailing)
	}
	if !strings.Contains(d.Headline(), "does not block") {
		t.Errorf("headline %q should explain that the optional failure is not blocking", d.Headline())
	}
}

func TestEvaluate_RequiredFailingCheckBlocksAndNamesIt(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBlocked
	st.Checks = []gh.CheckContext{
		{Name: "lint", State: gh.CheckStateSuccess, Required: true},
		{Name: "build", State: gh.CheckStateFailure, Required: true, App: "GitHub Actions"},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a failing required check must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonChecksFailing {
		t.Errorf("reason = %q, want checks_failing", d.PrimaryReason())
	}
	// The detail is what the listing shows; a bare count is not actionable.
	if !strings.Contains(d.PrimaryDetail(), "build") {
		t.Errorf("detail %q must name the failing check", d.PrimaryDetail())
	}
	if !strings.Contains(d.PrimaryDetail(), "GitHub Actions") {
		t.Errorf("detail %q must name the app running the check", d.PrimaryDetail())
	}
	if d.ChecksSummary.RequiredFailing != 1 || d.ChecksSummary.RequiredTotal != 2 {
		t.Errorf("summary = %+v, want 1 failing of 2 required", d.ChecksSummary)
	}
	// Failing checks sort first so the blocking row is the first one rendered.
	if d.Checks[0].Name != "build" {
		t.Errorf("checks[0] = %q, want the failing check first", d.Checks[0].Name)
	}
	if !strings.Contains(d.Headline(), "cannot be merged") {
		t.Errorf("headline %q should say the PR cannot be merged", d.Headline())
	}
}

func TestEvaluate_PendingRequiredCheckBlocks(t *testing.T) {
	st := cleanStatus()
	st.Checks = []gh.CheckContext{
		{Name: "build", State: gh.CheckStatePending, Required: true},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a pending required check must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonChecksPending {
		t.Errorf("reason = %q, want checks_pending", d.PrimaryReason())
	}
	if !strings.Contains(d.Headline(), "merges on its own") {
		t.Errorf("headline %q should tell the reader it will merge once checks pass", d.Headline())
	}
}

// A context branch protection requires but which never reported does not appear
// in the rollup at all: nothing red, nothing to notice. It must still block.
func TestEvaluate_MissingRequiredContextBlocks(t *testing.T) {
	st := cleanStatus()
	st.Protection = &gh.BranchProtection{
		RequiresStatusChecks:        true,
		RequiredStatusCheckContexts: []string{"build", "e2e"},
	}
	st.Checks = []gh.CheckContext{
		{Name: "build", State: gh.CheckStateSuccess, Required: true},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a required context that never reported must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonRequiredCheckMissing {
		t.Errorf("reason = %q, want required_check_missing", d.PrimaryReason())
	}
	if got := d.ChecksSummary.MissingRequired; len(got) != 1 || got[0] != "e2e" {
		t.Errorf("missing required = %v, want [e2e]", got)
	}
}

// Branch protection can mark a context required even when GitHub's isRequired
// flag lags behind the rule change.
func TestEvaluate_ProtectionContextOverridesIsRequired(t *testing.T) {
	st := cleanStatus()
	st.Protection = &gh.BranchProtection{
		RequiresStatusChecks:        true,
		RequiredStatusCheckContexts: []string{"build"},
	}
	st.Checks = []gh.CheckContext{
		{Name: "build", State: gh.CheckStateFailure, Required: false},
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a context named by branch protection is required regardless of isRequired")
	}
	if d.ChecksSummary.RequiredFailing != 1 {
		t.Errorf("required failing = %d, want 1", d.ChecksSummary.RequiredFailing)
	}
}

// theburrowhub/heimdallm#674 asks for this explicitly, and it is stricter than
// GitHub: an open conversation blocks even without
// requiresConversationResolution.
func TestEvaluate_UnresolvedThreadBlocksEvenWithoutProtectionRule(t *testing.T) {
	st := cleanStatus()
	st.Protection = &gh.BranchProtection{RequiresConversationResolution: false}
	st.ReviewThreads = []gh.ReviewThread{{ID: "t1", IsResolved: false, IsOutdated: false}}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("an unresolved review thread must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonUnresolvedThreads {
		t.Errorf("reason = %q, want unresolved_threads", d.PrimaryReason())
	}
}

// Outdated threads hang off lines that no longer exist; GitHub stops counting
// them and so do we.
func TestEvaluate_OutdatedThreadDoesNotBlock(t *testing.T) {
	st := cleanStatus()
	st.ReviewThreads = []gh.ReviewThread{{ID: "t1", IsResolved: false, IsOutdated: true}}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if !d.Ready {
		t.Fatalf("an outdated thread must not block, blocks = %v", d.Blocks)
	}
	if d.Evidence.UnresolvedThreads != 0 {
		t.Errorf("unresolved threads = %d, want 0", d.Evidence.UnresolvedThreads)
	}
}

// Truncated data is not absence of data: reporting ready on a partial read is
// the failure this package exists to prevent.
func TestEvaluate_TruncatedChecksNeverReady(t *testing.T) {
	st := cleanStatus()
	st.ChecksTruncated = true
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a truncated check list must never report ready")
	}
	if !d.ChecksSummary.Truncated {
		t.Error("summary must carry the truncation forward for the UI")
	}
}

func TestEvaluate_TruncatedThreadsNeverReady(t *testing.T) {
	st := cleanStatus()
	st.ThreadsTruncated = true
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a truncated thread list must never report ready")
	}
}

func TestEvaluate_MergeQueueIsNeverTouched(t *testing.T) {
	st := cleanStatus()
	st.IsInMergeQueue = true
	st.MergeQueueEntryState = "QUEUED"
	cfg := enabledCfg()
	cfg.Merge, cfg.EnableAutoMerge = true, true
	in := baseInput(cfg)

	d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none for a PR in the merge queue", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonInMergeQueue {
		t.Errorf("reason = %q, want in_merge_queue", d.PrimaryReason())
	}
}

// A configured merge queue means GitHub owns the merge order: a direct PUT
// would jump the queue.
func TestEvaluate_MergeQueueConfiguredForbidsDirectMerge(t *testing.T) {
	st := cleanStatus()
	st.MergeQueueEnabled = true
	cfg := enabledCfg()
	cfg.Merge = true // auto-merge deliberately off
	in := baseInput(cfg)

	d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
	if d.Action == mergetrack.ActionMerge {
		t.Fatal("a direct merge must never run against a merge queue")
	}
	if d.PrimaryReason() != mergetrack.ReasonMergeQueueConfigured {
		t.Errorf("reason = %q, want merge_queue_configured", d.PrimaryReason())
	}
}

func TestEvaluate_ReadOnlyPermissionIsTerminal(t *testing.T) {
	st := cleanStatus()
	st.ViewerPermission = "READ"
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.PrimaryReason() != mergetrack.ReasonInsufficientPermission {
		t.Errorf("reason = %q, want insufficient_permission", d.PrimaryReason())
	}
	if !d.PrimaryReason().IsTerminal() {
		t.Error("insufficient_permission should be terminal")
	}
}

// A head branch on someone else's fork cannot be pushed to, so no write action
// is possible; it is still tracked and displayed.
func TestEvaluate_CrossForkIsReportedNotActed(t *testing.T) {
	st := cleanStatus()
	st.HeadIsFork = true
	st.HeadRepo = "someone/widgets"
	st.HeadRepoOwner = "someone"
	cfg := enabledCfg()
	cfg.UpdateBranch, cfg.ResolveConflicts, cfg.Merge = true, true, true
	in := baseInput(cfg)

	d := mergetrack.Decide(mergetrack.Evaluate(st, in), st, in)
	if d.Action != mergetrack.ActionNone {
		t.Errorf("action = %q, want none for a cross-fork head", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonCrossFork {
		t.Errorf("reason = %q, want cross_fork", d.PrimaryReason())
	}
}

// A fork owned by the viewer is pushable, so it must not be excluded.
func TestEvaluate_OwnForkIsActionable(t *testing.T) {
	st := cleanStatus()
	st.HeadIsFork = true
	st.HeadRepo = viewer + "/widgets"
	st.HeadRepoOwner = viewer
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if !d.Ready {
		t.Fatalf("a fork the viewer owns is pushable, blocks = %v", d.Blocks)
	}
}

func TestEvaluate_UntrackedPRIsAbandoned(t *testing.T) {
	st := cleanStatus()
	st.Author = "someone-else"
	st.Assignees = nil
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Action != mergetrack.ActionAbandon {
		t.Errorf("action = %q, want abandon", d.Action)
	}
	if d.PrimaryReason() != mergetrack.ReasonNotTracked {
		t.Errorf("reason = %q, want not_tracked", d.PrimaryReason())
	}
}

func TestEvaluate_AssigneeIsTrackedWhenIncluded(t *testing.T) {
	st := cleanStatus()
	st.Author = "someone-else"
	st.Assignees = []string{"other", viewer}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if !d.Ready {
		t.Fatalf("an assignee's PR should be evaluated, blocks = %v", d.Blocks)
	}
}

func TestEvaluate_AssigneeIsSkippedWhenExcludedByConfig(t *testing.T) {
	st := cleanStatus()
	st.Author = "someone-else"
	st.Assignees = []string{viewer}
	cfg := enabledCfg()
	cfg.IncludeAssigned = false
	d := mergetrack.Evaluate(st, baseInput(cfg))
	if d.PrimaryReason() != mergetrack.ReasonNotTracked {
		t.Errorf("reason = %q, want not_tracked with include_assigned=false", d.PrimaryReason())
	}
}

func TestEvaluate_MergeMethodNotAllowedByRepo(t *testing.T) {
	st := cleanStatus()
	st.AllowedMergeMethods = gh.MergeMethodSet{Merge: true} // squash disabled
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("a merge method the repo forbids must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonMergeMethodNotAllowed {
		t.Errorf("reason = %q, want merge_method_not_allowed", d.PrimaryReason())
	}
}

// Branch protection is unreadable without admin, which is the normal case. The
// evaluator must fall back to GitHub's own verdict rather than assuming there
// are no rules.
func TestEvaluate_UnreadableProtectionUsesGitHubVerdict(t *testing.T) {
	st := cleanStatus()
	st.Protection = nil
	st.ProtectionUnreadable = true
	st.MergeStateStatus = gh.MergeStateBlocked
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("BLOCKED with unreadable protection must not be ready")
	}
	if d.PrimaryReason() != mergetrack.ReasonBlockedByProtection {
		t.Errorf("reason = %q, want blocked_by_protection", d.PrimaryReason())
	}
	if !strings.Contains(d.PrimaryDetail(), "not readable") {
		t.Errorf("detail %q should admit the rule could not be read", d.PrimaryDetail())
	}
	if !d.Evidence.ProtectionUnreadable {
		t.Error("evidence must record that protection was unreadable")
	}
}

func TestEvaluate_InsufficientApprovalsAgainstProtectionCount(t *testing.T) {
	st := cleanStatus()
	st.ReviewDecision = gh.ReviewDecisionApproved
	st.Protection = &gh.BranchProtection{
		RequiresApprovingReviews:     true,
		RequiredApprovingReviewCount: 2,
	}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("one approval against a required count of two must block")
	}
	if d.PrimaryReason() != mergetrack.ReasonInsufficientApprovals {
		t.Errorf("reason = %q, want insufficient_approvals", d.PrimaryReason())
	}
	if d.Evidence.RequiredApprovals != 2 || d.Evidence.ApprovalsAtHead != 1 {
		t.Errorf("evidence = %+v, want 1 of 2 approvals", d.Evidence)
	}
}

func TestEvaluate_PendingReviewersBlock(t *testing.T) {
	st := cleanStatus()
	st.ReviewRequests = []string{"team-platform"}
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.Ready {
		t.Fatal("an outstanding review request must block")
	}
	if !strings.Contains(d.Explain(), "team-platform") {
		t.Errorf("explain %q should name the pending reviewer", d.Explain())
	}
}

func TestEvaluate_ConflictsReported(t *testing.T) {
	st := cleanStatus()
	st.Mergeable = gh.MergeableNo
	st.MergeStateStatus = gh.MergeStateDirty
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.PrimaryReason() != mergetrack.ReasonConflicts {
		t.Errorf("reason = %q, want conflicts", d.PrimaryReason())
	}
}

func TestEvaluate_BehindBaseReported(t *testing.T) {
	st := cleanStatus()
	st.MergeStateStatus = gh.MergeStateBehind
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if d.PrimaryReason() != mergetrack.ReasonBehindBase {
		t.Errorf("reason = %q, want behind_base", d.PrimaryReason())
	}
	if d.Evidence.BehindBase {
		t.Error("OID evidence must not be inferred from mergeStateStatus")
	}
}

// Every mergeStateStatus value must map to a defined outcome. A new value
// GitHub adds should surface as a block, never as ready-by-default.
func TestEvaluate_MergeStateStatusMatrix(t *testing.T) {
	cases := []struct {
		status     string
		wantReady  bool
		wantReason mergetrack.Reason
	}{
		{gh.MergeStateClean, true, mergetrack.ReasonNone},
		{gh.MergeStateUnstable, true, mergetrack.ReasonNone},
		{gh.MergeStateHasHooks, false, mergetrack.ReasonHooksPending},
		{gh.MergeStateBehind, false, mergetrack.ReasonBehindBase},
		{gh.MergeStateDirty, false, mergetrack.ReasonConflicts},
		{gh.MergeStateDraft, false, mergetrack.ReasonDraft},
		{gh.MergeStateUnknown, false, mergetrack.ReasonMergeabilityUnknown},
		{gh.MergeStateBlocked, false, mergetrack.ReasonBlockedByProtection},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			st := cleanStatus()
			st.MergeStateStatus = tc.status
			d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
			if d.Ready != tc.wantReady {
				t.Errorf("ready = %v, want %v (blocks %v)", d.Ready, tc.wantReady, d.Blocks)
			}
			if d.PrimaryReason() != tc.wantReason {
				t.Errorf("reason = %q, want %q", d.PrimaryReason(), tc.wantReason)
			}
		})
	}
}

func TestEvaluate_HeadlineDescribesNoChecks(t *testing.T) {
	st := cleanStatus()
	st.Checks = nil
	d := mergetrack.Evaluate(st, baseInput(enabledCfg()))
	if !strings.Contains(d.Headline(), "no checks") {
		t.Errorf("headline = %q, want it to say there are no checks", d.Headline())
	}
}
