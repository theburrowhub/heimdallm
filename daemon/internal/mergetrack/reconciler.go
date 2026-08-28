package mergetrack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/workgate"
)

// Gateway is the GitHub surface the reconciler uses. Declared in the consumer
// so tests can supply a fake that fails the test on any unexpected call —
// which is how "everything disabled means zero API calls" is actually proven
// rather than asserted.
type Gateway interface {
	FetchMergeTrackingPRs(includeAssigned bool) ([]*gh.TrackedPR, error)
	GetMergeStatus(repo string, number int) (*gh.MergeStatus, error)
	EnableAutoMerge(nodeID, method, expectedHeadOid string) (*gh.AutoMergeRequest, error)
	DisableAutoMerge(nodeID string) error
	UpdatePRBranch(repo string, number int, expectedHeadSHA string) (gh.UpdateBranchOutcome, error)
	MergePRAtSHA(repo string, number int, method, expectedHeadSHA string) (gh.MergeOutcome, error)
	PostComment(repo string, number int, body string) (time.Time, error)
}

// StateStore is the persistence the reconciler needs.
type StateStore interface {
	GetPRByGithubID(githubID int64) (*store.PR, error)
	GetPRByRepoNumber(repo string, number int) (*store.PR, error)
	UpsertPR(pr *store.PR) (int64, error)
	EnsureMergeTracking(prID int64, repo string, number int) (*store.MergeTracking, error)
	GetMergeTracking(prID int64) (*store.MergeTracking, error)
	ListMergeTrackingDue(now time.Time, limit int) ([]*store.MergeTracking, error)
	UpdateMergeTrackingIdentity(prID int64, nodeID, baseRef, headRef string, isAuthor, isAssignee bool) error
	ResetMergeTrackingForNewHead(prID int64, headSHA string, at time.Time) error
	ClaimMergeTrackingAction(prID int64, headSHA, phase string, now time.Time) (bool, error)
	ReleaseMergeTrackingAction(prID int64, phase string, cooldownUntil time.Time, lastErr string) error
	ArmNativeAutoMerge(prID int64, headSHA, method string, at time.Time) error
	ClearNativeAutoMerge(prID int64) error
	RecordMergeTrackingDecision(prID int64, d store.MergeDecisionRecord) error
	BumpMergeTrackingAttempt(prID int64, kind string, cooldownUntil time.Time, lastErr string) error
	SetMergeTrackingPreRebaseSHA(prID int64, sha string) error
	BlockMergeTracking(prID int64, reason, detail string, cooldownUntil time.Time) error
	MarkMergeTrackingMerged(prID int64, at time.Time) error
	MarkMergeTrackingAbandoned(prID int64, reason string, at time.Time) error
}

// Publisher emits SSE events.
type Publisher interface {
	Publish(ev sse.Event)
}

// Gate is the update-drain admission gate.
type Gate interface {
	AcquireContext(ctx context.Context, kind workgate.Kind) (context.Context, *workgate.Permit, bool, error)
}

// WorktreeRunner performs the two actions that need a local checkout. It lives
// behind an interface because worktree acquisition is wired in main, where
// repoctx and the per-repo AI config are available; the reconciler must stay
// free of both.
type WorktreeRunner interface {
	// ResolveConflicts rebases the head branch onto its base, has the agent
	// resolve the conflicts, and force-pushes.
	ResolveConflicts(ctx context.Context, req ConflictRequest) (ConflictResult, error)
	// RebaseAndForcePush is the fallback when GitHub refuses update-branch,
	// typically because the base requires linear history. Returns the new head.
	RebaseAndForcePush(ctx context.Context, req ConflictRequest) (string, error)
}

// Reconciler drives tracked PRs towards merge.
type Reconciler struct {
	gw       Gateway
	st       StateStore
	pub      Publisher
	gate     Gate
	worktree WorktreeRunner

	// cfgFor resolves the effective config for a repo (repo > org > global).
	cfgFor func(repo string) config.MergeTrackingConfig
	// globalCfg returns the settings that are not per-repo (batch size).
	globalCfg func() config.MergeTrackingConfig
	// viewer returns the authenticated GitHub login.
	viewer func() string
	// now is injectable so tests control time without sleeping.
	now func() time.Time
	// defaultCooldown is applied when a decision offers no hint.
	defaultCooldown time.Duration
}

// ReconcilerOptions bundles the Reconciler's dependencies. A struct rather than
// a ten-argument constructor, because most of these are optional in tests.
type ReconcilerOptions struct {
	Gateway         Gateway
	Store           StateStore
	Publisher       Publisher
	Gate            Gate
	Worktree        WorktreeRunner
	ConfigForRepo   func(repo string) config.MergeTrackingConfig
	GlobalConfig    func() config.MergeTrackingConfig
	Viewer          func() string
	Now             func() time.Time
	DefaultCooldown time.Duration
}

// NewReconciler builds a Reconciler, filling in safe defaults for the optional
// dependencies.
func NewReconciler(o ReconcilerOptions) *Reconciler {
	r := &Reconciler{
		gw:              o.Gateway,
		st:              o.Store,
		pub:             o.Publisher,
		gate:            o.Gate,
		worktree:        o.Worktree,
		cfgFor:          o.ConfigForRepo,
		globalCfg:       o.GlobalConfig,
		viewer:          o.Viewer,
		now:             o.Now,
		defaultCooldown: o.DefaultCooldown,
	}
	if r.now == nil {
		r.now = func() time.Time { return time.Now().UTC() }
	}
	if r.defaultCooldown <= 0 {
		r.defaultCooldown = 5 * time.Minute
	}
	return r
}

// TickStats summarises one reconciliation cycle, for logging.
type TickStats struct {
	Discovered int
	Evaluated  int
	Actions    int
	Errors     int
}

// AnyEnabled reports whether merge tracking is on for at least one of the given
// repos. The poller calls this first so a cycle with the feature off everywhere
// costs nothing at all — not one GitHub call.
func (r *Reconciler) AnyEnabled(repos []string) bool {
	if r == nil || r.cfgFor == nil {
		return false
	}
	for _, repo := range repos {
		if r.cfgFor(repo).Enabled {
			return true
		}
	}
	return false
}

// Tick runs one reconciliation cycle over the given monitored repos.
func (r *Reconciler) Tick(ctx context.Context, repos []string) TickStats {
	var stats TickStats
	if len(repos) == 0 || !r.AnyEnabled(repos) {
		return stats
	}
	tickStart := r.now()

	stats.Discovered = r.discover(ctx, repos)

	limit := 0
	if r.globalCfg != nil {
		limit = r.globalCfg().MaxPRsPerTick
	}
	if limit <= 0 {
		limit = config.DefaultMergeTrackingMaxPRsPerTick
	}

	due, err := r.st.ListMergeTrackingDue(tickStart, limit)
	if err != nil {
		slog.Error("mergetrack: list due rows", "err", err)
		stats.Errors++
		return stats
	}

	for _, row := range due {
		if err := ctx.Err(); err != nil {
			return stats
		}
		acted, err := r.ReconcilePR(ctx, row.PRID, tickStart, false)
		stats.Evaluated++
		if acted {
			stats.Actions++
		}
		if err != nil {
			stats.Errors++
			// A rate-limit rejection means every further call this cycle would
			// be wasted (and would keep a secondary block alive), so stop.
			var rl *gh.RateLimitError
			if errors.As(err, &rl) {
				slog.Warn("mergetrack: rate limited, ending cycle early", "retry_at", rl.RetryAt)
				return stats
			}
		}
	}
	return stats
}

// discover finds PRs the user authors or is assigned to and enrols them.
func (r *Reconciler) discover(ctx context.Context, repos []string) int {
	includeAssigned := false
	if r.globalCfg != nil {
		includeAssigned = r.globalCfg().IncludeAssigned
	}

	prs, err := r.gw.FetchMergeTrackingPRs(includeAssigned)
	if err != nil {
		// A partial result is still worth enrolling: FetchMergeTrackingPRs
		// returns what it got alongside the joined error.
		slog.Warn("mergetrack: discovery partial failure", "err", err)
	}

	monitored := make(map[string]struct{}, len(repos))
	for _, repo := range repos {
		monitored[repo] = struct{}{}
	}

	enrolled := 0
	for _, pr := range prs {
		if err := ctx.Err(); err != nil {
			return enrolled
		}
		if pr == nil || pr.PullRequest == nil {
			continue
		}
		if _, ok := monitored[pr.Repo]; !ok {
			continue
		}
		if !r.cfgFor(pr.Repo).Enabled {
			continue
		}
		prID, isNew, err := r.enrol(pr)
		if err != nil {
			slog.Warn("mergetrack: enrol PR", "repo", pr.Repo, "number", pr.Number, "err", err)
			continue
		}
		if isNew {
			enrolled++
			r.emit(sse.EventMergeTrackDetected, map[string]any{
				"pr_id":       prID,
				"repo":        pr.Repo,
				"number":      pr.Number,
				"is_author":   pr.IsAuthor,
				"is_assignee": pr.IsAssignee,
			})
		}
	}
	return enrolled
}

// enrol makes sure the PR exists locally and has a merge-tracking row.
// Reports whether the tracking row was created by this call.
func (r *Reconciler) enrol(pr *gh.TrackedPR) (int64, bool, error) {
	row, err := r.st.GetPRByGithubID(pr.ID)
	if err != nil || row == nil {
		// The search and pulls APIs can disagree on the global id, so fall back
		// to the stable repo/number identity before creating a duplicate.
		row, err = r.st.GetPRByRepoNumber(pr.Repo, pr.Number)
	}
	if err != nil || row == nil {
		id, upErr := r.st.UpsertPR(&store.PR{
			GithubID:  pr.ID,
			Repo:      pr.Repo,
			Number:    pr.Number,
			Title:     pr.Title,
			Author:    pr.User.Login,
			URL:       pr.HTMLURL,
			State:     pr.State,
			UpdatedAt: pr.UpdatedAt,
			FetchedAt: r.now(),
		})
		if upErr != nil {
			return 0, false, fmt.Errorf("upsert pr: %w", upErr)
		}
		row = &store.PR{ID: id, Repo: pr.Repo, Number: pr.Number}
	}

	existing, err := r.st.GetMergeTracking(row.ID)
	isNew := err != nil || existing == nil
	if _, err := r.st.EnsureMergeTracking(row.ID, pr.Repo, pr.Number); err != nil {
		return row.ID, false, fmt.Errorf("ensure merge tracking: %w", err)
	}
	return row.ID, isNew, nil
}

// ReconcilePR evaluates one tracked PR and performs the chosen action.
//
// dryRun stops after persisting the evaluation, which is what the manual
// "evaluate now" endpoint uses: an operator asking "why is this stuck?" should
// get an answer, not a merge.
//
// Reports whether a mutating action ran.
func (r *Reconciler) ReconcilePR(ctx context.Context, prID int64, tickStart time.Time, dryRun bool) (bool, error) {
	row, err := r.st.GetMergeTracking(prID)
	if err != nil {
		return false, fmt.Errorf("mergetrack: load row %d: %w", prID, err)
	}

	st, err := r.gw.GetMergeStatus(row.Repo, row.Number)
	if err != nil {
		if errors.Is(err, gh.ErrPRNotFound) {
			// Deleted, or we lost access. Stop tracking rather than retrying
			// a 404 every cycle forever.
			_ = r.st.MarkMergeTrackingAbandoned(prID, string(ReasonClosed), r.now())
			return false, nil
		}
		r.emitError(row, "fetch", err)
		return false, fmt.Errorf("mergetrack: merge status %s#%d: %w", row.Repo, row.Number, err)
	}

	row = r.syncRow(prID, row, st)

	cfg := r.cfgFor(row.Repo)
	in := Input{
		Cfg:         cfg,
		ViewerLogin: r.viewerLogin(st),
		State:       *row,
		Now:         r.now(),
		TickStart:   tickStart,
		// A dry run is an operator asking a question, so it reports the PR's
		// real blocker rather than our own attempt pacing.
		IgnoreCooldown: dryRun,
	}

	d := Decide(Evaluate(st, in), st, in)
	r.persistDecision(prID, row, d)

	if dryRun || !d.Action.Mutating() {
		r.handleNonMutating(prID, row, st, d)
		return false, nil
	}

	return r.performAction(ctx, prID, row, st, d, in)
}

// syncRow reconciles the persisted row against the live snapshot before any
// decision is made: a new head SHA resets the per-commit counters, and GitHub's
// auto-merge state overrides ours in both directions.
func (r *Reconciler) syncRow(prID int64, row *store.MergeTracking, st *gh.MergeStatus) *store.MergeTracking {
	isAuthor, isAssignee := st.IsTrackedFor(r.viewerLogin(st))
	if err := r.st.UpdateMergeTrackingIdentity(prID, st.NodeID, st.BaseRef, st.HeadRef, isAuthor, isAssignee); err != nil {
		slog.Warn("mergetrack: update identity", "pr_id", prID, "err", err)
	}

	if st.HeadOID != "" && row.HeadSHA != st.HeadOID {
		// A push invalidates every per-commit conclusion: approvals, attempt
		// counters, cooldowns, and the licence to merge.
		if err := r.st.ResetMergeTrackingForNewHead(prID, st.HeadOID, r.now()); err != nil {
			slog.Warn("mergetrack: reset for new head", "pr_id", prID, "err", err)
		}
	}

	switch {
	case st.AutoMerge == nil && row.AutoMergeHeadSHA != "":
		// Someone turned auto-merge off. Forget our record rather than acting
		// on a belief GitHub no longer shares.
		if err := r.st.ClearNativeAutoMerge(prID); err != nil {
			slog.Warn("mergetrack: clear auto-merge", "pr_id", prID, "err", err)
		}
	case st.AutoMerge != nil && row.AutoMergeHeadSHA != st.HeadOID && st.HeadOID != "":
		// Auto-merge is on but our record points at another commit — GitHub
		// keeps auto-merge across pushes. Re-anchor it, using GitHub's own
		// enabledAt so the "wait a pass" rule measures from when it was really
		// armed, not from now.
		at := st.AutoMerge.EnabledAt
		if at.IsZero() {
			at = r.now()
		}
		if err := r.st.ArmNativeAutoMerge(prID, st.HeadOID, st.AutoMerge.MergeMethod, at); err != nil {
			slog.Warn("mergetrack: re-anchor auto-merge", "pr_id", prID, "err", err)
		}
	}

	fresh, err := r.st.GetMergeTracking(prID)
	if err != nil || fresh == nil {
		return row
	}
	return fresh
}

// persistDecision writes the evaluation outcome, including the check
// breakdown, so the UI can explain a blocked merge without calling GitHub.
func (r *Reconciler) persistDecision(prID int64, row *store.MergeTracking, d Decision) {
	payload, err := json.Marshal(d)
	if err != nil {
		slog.Warn("mergetrack: marshal decision", "pr_id", prID, "err", err)
		payload = nil
	}
	cooldown := time.Time{}
	if d.CooldownHint > 0 {
		cooldown = r.now().Add(d.CooldownHint)
	} else if !d.Action.Mutating() {
		cooldown = r.now().Add(r.defaultCooldown)
	}

	rec := store.MergeDecisionRecord{
		Phase:                 RestPhaseFor(d),
		HeadSHA:               d.HeadSHA,
		BlockReason:           string(d.PrimaryReason()),
		BlockDetail:           d.PrimaryDetail(),
		DecisionJSON:          string(payload),
		ChecksRequiredFailing: d.ChecksSummary.RequiredFailing,
		ChecksRequiredPending: d.ChecksSummary.RequiredPending,
		CooldownUntil:         cooldown,
		At:                    r.now(),
	}
	// A mutating action claims its own phase moments from now; writing the
	// resting phase here would fight that claim.
	if d.Action.Mutating() {
		rec.Phase = row.Phase
		rec.CooldownUntil = row.CooldownUntil
	}
	if err := r.st.RecordMergeTrackingDecision(prID, rec); err != nil {
		slog.Warn("mergetrack: record decision", "pr_id", prID, "err", err)
	}

	r.emit(sse.EventMergeTrackEvaluated, map[string]any{
		"pr_id":    prID,
		"repo":     row.Repo,
		"number":   row.Number,
		"ready":    d.Ready,
		"phase":    rec.Phase,
		"reason":   string(d.PrimaryReason()),
		"detail":   d.PrimaryDetail(),
		"head_sha": d.HeadSHA,
		"checks":   d.ChecksSummary,
		"headline": d.Headline(),
	})

	// Only announce a block when the reason CHANGED. A PR waiting an hour on CI
	// would otherwise produce one activity-log row per cycle.
	if reason := string(d.PrimaryReason()); reason != "" && reason != row.BlockReason {
		r.emit(sse.EventMergeTrackBlocked, map[string]any{
			"pr_id":  prID,
			"repo":   row.Repo,
			"number": row.Number,
			"reason": reason,
			"detail": d.PrimaryDetail(),
		})
	}
}

// handleNonMutating applies the terminal transitions that need no GitHub call.
func (r *Reconciler) handleNonMutating(prID int64, row *store.MergeTracking, st *gh.MergeStatus, d Decision) {
	switch d.Action {
	case ActionMarkMerged:
		at := st.MergedAt
		if at.IsZero() {
			at = r.now()
		}
		if err := r.st.MarkMergeTrackingMerged(prID, at); err != nil {
			slog.Warn("mergetrack: mark merged", "pr_id", prID, "err", err)
			return
		}
		r.emit(sse.EventMergeTrackMerged, map[string]any{
			"pr_id": prID, "repo": row.Repo, "number": row.Number,
			"method": row.AutoMergeMethod, "sha": st.HeadOID,
		})
	case ActionAbandon:
		if err := r.st.MarkMergeTrackingAbandoned(prID, string(d.PrimaryReason()), r.now()); err != nil {
			slog.Warn("mergetrack: mark abandoned", "pr_id", prID, "err", err)
		}
	}
}

// performAction claims the row, takes the work gate, and runs the action.
func (r *Reconciler) performAction(ctx context.Context, prID int64, row *store.MergeTracking, st *gh.MergeStatus, d Decision, in Input) (bool, error) {
	phase := PhaseFor(d.Action)
	claimed, err := r.st.ClaimMergeTrackingAction(prID, st.HeadOID, phase, r.now())
	if err != nil {
		return false, fmt.Errorf("mergetrack: claim %s: %w", d.Action, err)
	}
	if !claimed {
		// Another cycle, or another daemon, already owns this PR.
		return false, nil
	}

	if r.gate != nil {
		gateCtx, permit, owned, gateErr := r.gate.AcquireContext(ctx, workgate.KindMergeTracking)
		if gateErr != nil {
			// An update is draining. Release the claim so the next cycle picks
			// it up rather than leaving the row parked in an in-flight phase.
			_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseIdle, time.Time{}, "")
			return false, nil
		}
		if owned {
			defer permit.Release()
		}
		ctx = gateCtx
	}

	actionErr := r.dispatch(ctx, prID, row, st, d, in)
	if actionErr != nil {
		r.recordFailure(prID, row, d, actionErr)
		return true, actionErr
	}
	return true, nil
}

func (r *Reconciler) dispatch(ctx context.Context, prID int64, row *store.MergeTracking, st *gh.MergeStatus, d Decision, in Input) error {
	switch d.Action {
	case ActionArmAutoMerge:
		return r.armAutoMerge(prID, row, st, d)
	case ActionUpdateBranchRemote:
		return r.updateBranch(ctx, prID, row, st, in)
	case ActionUpdateBranchLocal:
		return r.rebaseLocally(ctx, prID, row, st, in)
	case ActionResolveConflicts:
		return r.resolveConflicts(ctx, prID, row, st, in)
	case ActionMerge:
		return r.merge(ctx, prID, row, st, d, in)
	default:
		return nil
	}
}

func (r *Reconciler) armAutoMerge(prID int64, row *store.MergeTracking, st *gh.MergeStatus, d Decision) error {
	method := MergeMethodForGitHub(d.MergeMethod)
	am, err := r.gw.EnableAutoMerge(st.NodeID, method, st.HeadOID)
	if err != nil {
		var unavailable *gh.AutoMergeUnavailableError
		if errors.As(err, &unavailable) {
			switch unavailable.Reason {
			case gh.AutoMergeReasonAlreadyEnabled:
				// The desired state already holds.
				return r.st.ArmNativeAutoMerge(prID, st.HeadOID, method, r.now())
			case gh.AutoMergeReasonNotAllowedForRepo:
				// Retrying every cycle would burn budget forever. Park it: the
				// next evaluation reports the reason, and a config or repo
				// change clears the cooldown naturally.
				_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseBlocked,
					r.now().Add(stableBlockRecheck), unavailable.Error())
				return nil
			}
		}
		return err
	}

	at := am.EnabledAt
	if at.IsZero() {
		at = r.now()
	}
	if err := r.st.ArmNativeAutoMerge(prID, st.HeadOID, method, at); err != nil {
		return err
	}
	r.emit(sse.EventMergeTrackAutoMergeArmed, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number,
		"method": method, "head_sha": st.HeadOID,
	})
	return nil
}

func (r *Reconciler) updateBranch(ctx context.Context, prID int64, row *store.MergeTracking, st *gh.MergeStatus, in Input) error {
	_, err := r.gw.UpdatePRBranch(row.Repo, row.Number, st.HeadOID)
	if err != nil {
		var rejected *gh.UpdateBranchRejectedError
		if errors.As(err, &rejected) && rejected.Reason == gh.UpdateBranchReasonUnprocessable {
			// GitHub will not merge the base in — almost always because the
			// branch requires linear history. Fall back to a local rebase.
			slog.Info("mergetrack: update-branch refused, falling back to local rebase",
				"repo", row.Repo, "pr", row.Number)
			return r.rebaseLocally(ctx, prID, row, st, in)
		}
		return err
	}

	// GitHub answers 202 and does the work asynchronously, so there is nothing
	// to verify here. A short cooldown lets the next cycle observe the new head.
	_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseIdle, r.now().Add(unknownRecheck), "")
	r.emit(sse.EventMergeTrackBranchUpdated, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number, "mode": "github",
	})
	return nil
}

func (r *Reconciler) rebaseLocally(ctx context.Context, prID int64, row *store.MergeTracking, st *gh.MergeStatus, in Input) error {
	if r.worktree == nil {
		return fmt.Errorf("mergetrack: local rebase requested but no worktree runner is wired")
	}
	if err := r.st.SetMergeTrackingPreRebaseSHA(prID, st.HeadOID); err != nil {
		slog.Warn("mergetrack: record pre-rebase sha", "pr_id", prID, "err", err)
	}
	newHead, err := r.worktree.RebaseAndForcePush(ctx, r.conflictRequest(row, st, in))
	if err != nil {
		return err
	}
	_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseIdle, r.now().Add(unknownRecheck), "")
	r.emit(sse.EventMergeTrackBranchUpdated, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number,
		"mode": "local_rebase", "head_sha": newHead,
	})
	return nil
}

func (r *Reconciler) resolveConflicts(ctx context.Context, prID int64, row *store.MergeTracking, st *gh.MergeStatus, in Input) error {
	if r.worktree == nil {
		return fmt.Errorf("mergetrack: conflict resolution requested but no worktree runner is wired")
	}
	if err := r.st.SetMergeTrackingPreRebaseSHA(prID, st.HeadOID); err != nil {
		slog.Warn("mergetrack: record pre-rebase sha", "pr_id", prID, "err", err)
	}

	res, err := r.worktree.ResolveConflicts(ctx, r.conflictRequest(row, st, in))
	// The audit comment is posted on both paths: a resolution the author should
	// review, or an explanation of why nothing was pushed.
	if res.CommentBody != "" {
		if _, postErr := r.gw.PostComment(row.Repo, row.Number, res.CommentBody); postErr != nil {
			slog.Warn("mergetrack: post conflict comment", "repo", row.Repo, "pr", row.Number, "err", postErr)
		}
	}
	if err != nil {
		return err
	}
	if res.PreRebaseSHA != "" {
		_ = r.st.SetMergeTrackingPreRebaseSHA(prID, res.PreRebaseSHA)
	}
	_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseIdle, r.now().Add(unknownRecheck), "")
	r.emit(sse.EventMergeTrackConflictResolved, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number,
		"pushed": res.Pushed, "files": res.Files, "pre_rebase_sha": res.PreRebaseSHA,
	})
	return nil
}

// merge performs the direct merge, re-validating everything immediately before
// the request.
//
// The re-validation is not belt-and-braces: between the decision and this call
// the PR can gain a commit, lose an approval, enter a merge queue or be merged
// by someone else. Sending the expected SHA makes GitHub itself refuse a merge
// of anything other than the commit we evaluated, and a mismatch is treated as
// a block, never as something to retry. See theburrowhub/heimdallm#674.
func (r *Reconciler) merge(ctx context.Context, prID int64, row *store.MergeTracking, st *gh.MergeStatus, d Decision, in Input) error {
	fresh, err := r.gw.GetMergeStatus(row.Repo, row.Number)
	if err != nil {
		return fmt.Errorf("mergetrack: pre-merge revalidation: %w", err)
	}
	if fresh.Merged {
		return r.finishMerged(prID, row, fresh)
	}
	if fresh.HeadOID != st.HeadOID {
		detail := fmt.Sprintf("a commit landed between the check and the merge (%s → %s)",
			shortSHA(st.HeadOID), shortSHA(fresh.HeadOID))
		r.block(prID, row, ReasonHeadSHAMoved, detail)
		return nil
	}
	if fresh.IsInMergeQueue {
		r.block(prID, row, ReasonInMergeQueue, "the PR entered the merge queue")
		return nil
	}

	in.State = *row
	if second := Evaluate(fresh, in); !second.Ready {
		r.block(prID, row, second.PrimaryReason(), second.PrimaryDetail())
		return nil
	}

	// Disarm GitHub's auto-merge first: with it still armed, GitHub could fire
	// its own merge concurrently with ours.
	if fresh.AutoMerge != nil && fresh.NodeID != "" {
		if err := r.gw.DisableAutoMerge(fresh.NodeID); err != nil {
			slog.Warn("mergetrack: disable auto-merge before direct merge",
				"repo", row.Repo, "pr", row.Number, "err", err)
		}
	}

	method := MergeMethodForGitHub(d.MergeMethod)
	out, err := r.gw.MergePRAtSHA(row.Repo, row.Number, method, fresh.HeadOID)
	if err != nil {
		var rejected *gh.MergeRejectedError
		if errors.As(err, &rejected) && rejected.Reason == gh.MergeReasonSHAMismatch {
			// The head moved inside GitHub's own window. Block, never retry.
			r.block(prID, row, ReasonHeadSHAMoved, "GitHub rejected the merge: the head had moved")
			return nil
		}
		return err
	}
	if !out.Merged {
		return fmt.Errorf("mergetrack: GitHub accepted the merge request but reported merged=false")
	}

	if err := r.st.MarkMergeTrackingMerged(prID, r.now()); err != nil {
		return err
	}
	r.emit(sse.EventMergeTrackMerged, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number,
		"method": method, "sha": out.SHA,
	})
	slog.Info("mergetrack: merged", "repo", row.Repo, "pr", row.Number, "method", method, "sha", out.SHA)
	return nil
}

func (r *Reconciler) finishMerged(prID int64, row *store.MergeTracking, st *gh.MergeStatus) error {
	at := st.MergedAt
	if at.IsZero() {
		at = r.now()
	}
	if err := r.st.MarkMergeTrackingMerged(prID, at); err != nil {
		return err
	}
	r.emit(sse.EventMergeTrackMerged, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number, "sha": st.HeadOID,
	})
	return nil
}

// recordFailure increments the relevant attempt counter and applies a backoff.
func (r *Reconciler) recordFailure(prID int64, row *store.MergeTracking, d Decision, actionErr error) {
	kind := AttemptKindFor(d.Action)
	cooldown := r.now().Add(backoffFor(attemptsSoFar(row, kind)))
	if kind == "" {
		_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseBlocked, cooldown, actionErr.Error())
	} else if err := r.st.BumpMergeTrackingAttempt(prID, kind, cooldown, actionErr.Error()); err != nil {
		slog.Warn("mergetrack: bump attempt", "pr_id", prID, "err", err)
	} else {
		_ = r.st.ReleaseMergeTrackingAction(prID, store.MergePhaseBlocked, cooldown, actionErr.Error())
	}
	r.emitError(row, string(d.Action), actionErr)
}

func attemptsSoFar(row *store.MergeTracking, kind string) int {
	switch kind {
	case store.MergeAttemptUpdate:
		return row.UpdateAttempts
	case store.MergeAttemptConflict:
		return row.ConflictAttempts
	case store.MergeAttemptMerge:
		return row.MergeAttempts
	default:
		return 0
	}
}

// backoffFor doubles from one minute up to fifteen, matching the tier-3 watch
// backoff so operators only have one curve to reason about.
func backoffFor(attempts int) time.Duration {
	const (
		initial = time.Minute
		max     = 15 * time.Minute
	)
	d := initial
	for i := 0; i < attempts; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}

func (r *Reconciler) conflictRequest(row *store.MergeTracking, st *gh.MergeStatus, in Input) ConflictRequest {
	return ConflictRequest{
		Repo:                  row.Repo,
		PRNumber:              row.Number,
		PRTitle:               st.Title,
		HeadRef:               st.HeadRef,
		BaseRef:               st.BaseRef,
		ExpectedRemoteHeadSHA: st.HeadOID,
	}
}

func (r *Reconciler) viewerLogin(st *gh.MergeStatus) string {
	if r.viewer != nil {
		if login := r.viewer(); login != "" {
			return login
		}
	}
	// The readiness query asks for viewer { login }, so the snapshot is a
	// reliable fallback when the accessor is not wired.
	return st.ViewerLogin
}

func (r *Reconciler) emit(eventType string, data map[string]any) {
	if r.pub == nil {
		return
	}
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Warn("mergetrack: marshal sse payload", "event", eventType, "err", err)
		return
	}
	r.pub.Publish(sse.Event{Type: eventType, Data: string(payload)})
}

// block parks the row with an explicit reason and announces it. Used for the
// blocks discovered during an action, which the pre-action evaluation could not
// have known about.
func (r *Reconciler) block(prID int64, row *store.MergeTracking, reason Reason, detail string) {
	if err := r.st.BlockMergeTracking(prID, string(reason), detail, r.now().Add(r.defaultCooldown)); err != nil {
		slog.Warn("mergetrack: persist block", "pr_id", prID, "err", err)
	}
	r.emitBlock(prID, row, reason, detail)
}

func (r *Reconciler) emitBlock(prID int64, row *store.MergeTracking, reason Reason, detail string) {
	r.emit(sse.EventMergeTrackBlocked, map[string]any{
		"pr_id": prID, "repo": row.Repo, "number": row.Number,
		"reason": string(reason), "detail": detail,
	})
}

func (r *Reconciler) emitError(row *store.MergeTracking, action string, err error) {
	r.emit(sse.EventMergeTrackError, map[string]any{
		"repo": row.Repo, "number": row.Number, "action": action, "err": err.Error(),
	})
}
