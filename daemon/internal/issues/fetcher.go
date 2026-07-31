package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// RecomputeGrace absorbs the small updated_at bump GitHub applies when the
// daemon posts its own comment. Without it, every triage would immediately
// re-trigger itself on the next poll.
//
// The PR pipeline in main.go currently duplicates this 30 s window inline.
// When #28 wires issues into the poll cycle it should import this constant
// and route the PR check through it too, so the two grace windows can't
// drift apart unnoticed.
const RecomputeGrace = 30 * time.Second

// MaxAutoImplementFailures is the maximum number of consecutive failed
// auto_implement push attempts before the fetcher permanently skips an issue.
// Exceeding this cap indicates a structural problem (non-fast-forward, repo
// access) that retrying cannot fix without human intervention. The user must
// resolve the conflict and dismiss or re-open the issue to resume processing.
// See issue #223.
const MaxAutoImplementFailures = 3

// IssuesFetcher is the subset of github.Client that fetches classified
// issues. Kept as an interface so the fetcher can be tested without an HTTP
// server standing in.
type IssuesFetcher interface {
	FetchIssues(repo string, cfg config.IssueTrackingConfig, authenticatedUser string) ([]*github.Issue, error)
}

// IssueSearcher is the optional Search API back-end used by PrefetchIssues.
// Keeping it separate from IssuesFetcher lets tests that only need FetchIssues
// inject a nil searcher without changes.
type IssueSearcher interface {
	SearchIssues(query string) ([]*github.Issue, error)
}

// PipelineRunner is the subset of *Pipeline the fetcher uses. Takes a
// context so shutdown cancellation propagates through the whole dispatch
// path down to the git / HTTP calls inside the pipeline.
type PipelineRunner interface {
	Run(ctx context.Context, issue *github.Issue, opts RunOptions) (*store.IssueReview, error)
}

// issueDedupStore is the store slice needed to decide whether an issue has
// already been processed with no new activity since.
type issueDedupStore interface {
	GetIssueByGithubID(githubID int64) (*store.Issue, error)
	LatestIssueReview(issueID int64) (*store.IssueReview, error)
	// CountFailedAutoImplement returns the number of stored
	// "auto_implement_failed" review rows for the issue. Used to enforce
	// MaxAutoImplementFailures; see issue #223.
	CountFailedAutoImplement(issueID int64) (int, error)
}

// issueMarkerFetcher fetches comments for an issue so the fetcher can scan
// for control markers (done/skip/retry) during the dedup check.
//
// The method is `FetchIssueCommentsOnly`, not the generic `FetchComments`,
// because the latter also calls the PR-only `/pulls/:n/comments` endpoint
// and fails with 404 on any issue number. That 404 cascade was the root
// cause of theburrowhub/heimdallm#292 — the marker scan silently fell
// through to the time-based dedup window for every tick, producing a
// re-triage loop.
type issueMarkerFetcher interface {
	FetchIssueCommentsOnly(repo string, number int) ([]github.Comment, error)
}

// IssuePublisher dispatches classified issues to NATS. When set on the
// Fetcher, ProcessRepo publishes to NATS instead of calling pipeline.Run.
type IssuePublisher interface {
	PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error
	PublishIssueRefinement(ctx context.Context, repo string, number int, githubID int64) error
	PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error
}

// OptionsFn lets the caller map each classified issue to its RunOptions.
// In production main.go resolves per-repo AI config here; tests can return a
// constant. ok=false skips this issue after classification; use it when the
// caller cannot prepare required execution context and has already surfaced
// the failure.
type OptionsFn func(issue *github.Issue) (opts RunOptions, ok bool)

// Fetcher orchestrates: fetch issues for a repo, skip those already processed
// without new activity, dispatch the rest to the pipeline.
type Fetcher struct {
	client    IssuesFetcher
	comments  issueMarkerFetcher
	store     issueDedupStore
	pipeline  PipelineRunner
	publisher IssuePublisher // optional — when set, publishes to NATS instead of running pipeline
	botLogin  string         // GitHub login of the bot — used to ignore self-triggered updated_at bumps

	stageClient StageTransitionClient
	stageBroker Publisher

	// searcher is optional. When set, PrefetchIssues uses the Search API to
	// aggregate issue results across repos in a single call, populating
	// prefetched so ProcessRepo can skip the per-repo REST call.
	searcher   IssueSearcher
	prefetched map[string][]*github.Issue // set by PrefetchIssues; read by ProcessRepo
}

// NewFetcher wires the orchestrator. All dependencies are interfaces so
// tests inject lightweight fakes. The comments parameter provides comment
// fetching for control-marker scanning (#238); pass the same *github.Client
// used for issue fetching.
func NewFetcher(client IssuesFetcher, comments issueMarkerFetcher, s issueDedupStore, p PipelineRunner) *Fetcher {
	return &Fetcher{client: client, comments: comments, store: s, pipeline: p}
}

// SetPublisher enables NATS-based dispatch. When set, ProcessRepo publishes
// classified issues to NATS instead of calling pipeline.Run directly.
func (f *Fetcher) SetPublisher(p IssuePublisher) {
	f.publisher = p
}

// SetBotLogin sets the GitHub login of the bot account. When set, the
// dedup check ignores updated_at bumps caused by the bot's own comments,
// breaking the re-triage loop described in #362.
func (f *Fetcher) SetBotLogin(login string) {
	f.botLogin = login
}

// SetStageTransitioner enables audit + normalization when a user manually
// changes stage labels on GitHub. The next poll sees the new classification,
// records the transition, and then dispatches the new stage normally.
func (f *Fetcher) SetStageTransitioner(client StageTransitionClient, broker Publisher) {
	f.stageClient = client
	f.stageBroker = broker
}

// SetSearcher enables the aggregated Search API prefetch path. When set,
// PrefetchIssues is available for callers to warm the prefetch map before
// calling ProcessRepo.
func (f *Fetcher) SetSearcher(s IssueSearcher) {
	f.searcher = s
}

// EligibleFn is the per-repo eligibility callback passed to PrefetchIssues.
// It returns the effective IssueTrackingConfig for repo, whether autonomous
// mode is enabled (which disables issue processing), and whether the repo
// should be included at all (ok=false skips the repo silently).
type EligibleFn func(repo string) (it config.IssueTrackingConfig, autonomousEnabled bool, ok bool)

// assigneeKey builds a canonical string key for a resolved assignee set so
// repos that share the same effective assignees end up in the same search
// group. The key is the sorted, joined assignee list (case-preserved to match
// the GitHub login convention).
func assigneeKey(assignees []string) string {
	if len(assignees) == 0 {
		return ""
	}
	sorted := make([]string, len(assignees))
	copy(sorted, assignees)
	// simple insertion sort — lists are always tiny (1–5 entries)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	return strings.Join(sorted, "\x00")
}

// PrefetchIssues runs one aggregated GET /search/issues query PER DISTINCT
// ASSIGNEE SET covering all eligible repos, builds a per-repo map of raw
// []*github.Issue, and stores it internally so subsequent ProcessRepo calls for
// any of those repos can skip the per-repo REST GET.
//
// eligibleFn(repo) returns (effectiveIssueTrackingConfig, autonomousEnabled,
// ok). Repos for which autonomousEnabled==true or ok==false are excluded from
// the search scope (matching the gating in ProcessRepo).
//
// Grouping by assignee set is necessary because per-repo IssueTracking
// overrides can specify different Assignees than the global config. Running a
// single global-assignee query and storing results under a repo whose effective
// assignees differ would silently drop issues assigned to the override assignees.
//
// Label qualifiers are intentionally omitted from the aggregated queries
// (BuildAggregatedSearchQuery). The per-repo client-side ClassifyAndFilterIssues
// step (called in ProcessRepo) already applies classification and label/filter
// filtering precisely, so the aggregated query only needs to narrow by assignee
// and repo scope.
//
// On search error for any group, PrefetchIssues logs a warning, clears the
// internal prefetch map for that group's repos (leaving them absent so
// ProcessRepo falls back to per-repo FetchIssues), and returns the last error.
//
// PrefetchIssues is NOT goroutine-safe; call it BEFORE the parallel ProcessRepo
// fan-out and do not write the prefetch map again until all ProcessRepo calls
// for that cycle have returned.
func (f *Fetcher) PrefetchIssues(
	eligibleFn EligibleFn,
	authUser string,
	repos []string,
) (map[string][]*github.Issue, error) {
	if f.searcher == nil {
		return nil, nil
	}

	// Build groups: assigneeKey → (assignees, []repo)
	type group struct {
		assignees []string
		repos     []string
	}
	groups := make(map[string]*group)
	groupOrder := make([]string, 0) // preserve deterministic iteration order

	for _, r := range repos {
		if r == "" {
			continue
		}
		it, autonomous, ok := eligibleFn(r)
		if !ok || autonomous || !it.Enabled {
			continue
		}
		// Resolve effective assignees the same way ProcessRepo / FetchIssues does.
		resolved := it.WithDefaultAssignee(authUser).Assignees
		key := assigneeKey(resolved)
		if _, exists := groups[key]; !exists {
			groups[key] = &group{assignees: resolved}
			groupOrder = append(groupOrder, key)
		}
		groups[key].repos = append(groups[key].repos, r)
	}

	if len(groups) == 0 {
		return nil, nil
	}

	byRepo := make(map[string][]*github.Issue)
	var lastErr error
	totalRepos := 0
	totalIssues := 0

	for _, key := range groupOrder {
		g := groups[key]
		query := github.BuildAggregatedSearchQuery(g.assignees, g.repos)
		if query == "" {
			continue
		}
		raw, err := f.searcher.SearchIssues(query)
		if err != nil {
			slog.Warn("issues fetcher: search prefetch failed for assignee group, will fall back to per-repo fetch",
				"err", err, "repos", len(g.repos))
			lastErr = err
			continue
		}
		// Seed every repo in the group with a present-but-empty entry. The
		// search covered them all, so "no results for this repo" is a real
		// answer — without the seed, ProcessRepo's `_, ok := prefetched[repo]`
		// misses and spends a per-repo REST call every cycle on exactly the
		// idle repos the aggregation exists to eliminate. Only repos in a
		// group whose search FAILED are left absent, so they still fall back.
		//
		// canon maps the lowercased configured name back to the configured
		// name so results keyed by GitHub's canonical full_name land on the
		// key ProcessRepo will look up. Without it a case difference would
		// hit the empty seed and silently report zero issues for the repo.
		canon := make(map[string]string, len(g.repos))
		for _, r := range g.repos {
			canon[strings.ToLower(r)] = r
			if _, exists := byRepo[r]; !exists {
				byRepo[r] = nil
			}
		}
		for _, issue := range raw {
			if issue.Repo == "" {
				continue
			}
			key := issue.Repo
			if configured, ok := canon[strings.ToLower(issue.Repo)]; ok {
				key = configured
			}
			byRepo[key] = append(byRepo[key], issue)
		}
		totalRepos += len(g.repos)
		totalIssues += len(raw)
	}

	if len(byRepo) == 0 && lastErr != nil {
		return nil, lastErr
	}

	slog.Info("issues fetcher: search prefetch complete",
		"groups", len(groups), "repos", totalRepos, "issues", totalIssues)
	f.prefetched = byRepo
	return byRepo, lastErr
}

// ClearPrefetch discards the prefetch map. Call once per cycle after all
// ProcessRepo calls have completed so stale data cannot leak into the next cycle.
func (f *Fetcher) ClearPrefetch() {
	f.prefetched = nil
}

// ProcessRepo fetches every eligible issue for one repo and dispatches it to
// the pipeline. Returns the number of issues actually handed off and a
// non-nil error only when the fetch itself failed — per-issue pipeline
// failures are logged and counted but do not abort the run.
//
// When cfg.Enabled is false this is a no-op; the caller does not have to
// guard the call. ctx is passed through to pipeline.Run so a daemon
// shutdown cancels whatever issue is currently being processed.
func (f *Fetcher) ProcessRepo(ctx context.Context, repo string, cfg config.IssueTrackingConfig, authUser string, optsFor OptionsFn) (int, error) {
	if !cfg.Enabled {
		return 0, nil
	}
	cfg = cfg.WithDefaultAssignee(authUser)
	if len(cfg.Assignees) == 0 {
		slog.Warn("issues fetcher: issue tracking has no assignee scope, skipping repo",
			"repo", repo)
		return 0, nil
	}
	if optsFor == nil {
		return 0, fmt.Errorf("issues fetcher: nil OptionsFn")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Use prefetched search results when available; fall back to per-repo REST.
	// The prefetch map is populated by PrefetchIssues before the parallel
	// ProcessRepo fan-out begins and is read-only during that fan-out, so no
	// locking is needed here.
	var issues []*github.Issue
	var fetchErr error
	if prefetchedSlice, ok := f.prefetched[repo]; ok {
		slog.Debug("issues fetcher: using prefetched search results",
			"repo", repo, "count", len(prefetchedSlice))
		// The raw search results still need to go through the same
		// classification + filter pipeline that FetchIssues applies.
		// ClassifyAndFilterIssues is the exported version of that logic.
		issues = github.ClassifyAndFilterIssues(prefetchedSlice, repo, cfg, authUser)
	} else {
		// No prefetch for this repo — use the per-repo REST endpoint.
		issues, fetchErr = f.client.FetchIssues(repo, cfg, authUser)
		if fetchErr != nil {
			return 0, fmt.Errorf("issues fetcher: fetch %s: %w", repo, fetchErr)
		}
	}

	processed := 0
	for _, issue := range issues {
		// Abort the loop cleanly on cancellation so a shutdown does not get
		// stuck waiting for remaining issues when the caller already gave up.
		if err := ctx.Err(); err != nil {
			return processed, fmt.Errorf("issues fetcher: %s cancelled after %d processed: %w", repo, processed, err)
		}

		// A dedup lookup error intentionally falls through to "treat as
		// unprocessed" so a flaky store never stops the pipeline from running;
		// the explicit if / else if makes that control flow obvious.
		skip, reason, err := f.alreadyProcessed(issue)
		if err != nil {
			slog.Warn("issues fetcher: dedup check failed, treating as unprocessed",
				"repo", repo, "number", issue.Number, "err", err)
		} else if skip {
			slog.Debug("issues fetcher: skipping issue",
				"repo", repo, "number", issue.Number, "reason", reason)
			continue
		}
		if err := f.auditManualStageChange(ctx, issue, cfg); err != nil {
			slog.Warn("issues fetcher: manual stage transition audit failed",
				"repo", repo, "number", issue.Number, "err", err)
		}

		if f.publisher != nil {
			var pubErr error
			switch issue.Mode {
			case config.IssueModeReviewOnly:
				pubErr = f.publisher.PublishIssueTriage(ctx, issue.Repo, issue.Number, issue.ID)
			case config.IssueModeRefinement:
				pubErr = f.publisher.PublishIssueRefinement(ctx, issue.Repo, issue.Number, issue.ID)
			case config.IssueModeDevelop:
				pubErr = f.publisher.PublishIssueImplement(ctx, issue.Repo, issue.Number, issue.ID)
			default:
				slog.Debug("issues fetcher: skipping issue with unhandled mode",
					"repo", repo, "number", issue.Number, "mode", string(issue.Mode))
				continue
			}
			if pubErr != nil {
				slog.Error("issues fetcher: publish failed",
					"repo", repo, "number", issue.Number, "err", pubErr)
				continue
			}
		} else {
			opts, ok := optsFor(issue)
			if !ok {
				continue
			}
			if _, runErr := f.pipeline.Run(ctx, issue, opts); runErr != nil {
				slog.Error("issues fetcher: pipeline run failed",
					"repo", repo, "number", issue.Number, "err", runErr)
				continue
			}
		}
		processed++
	}
	return processed, nil
}

// alreadyProcessed reports whether the issue can be skipped because:
//   - it was dismissed by the user, or
//   - it was already reviewed and has no new activity (UpdatedAt ≤ last
//     review + grace window), or
//   - a PR was already created via auto_implement, or
//   - the auto_implement push has failed MaxAutoImplementFailures times,
//     indicating a structural problem that human intervention must resolve.
//
// The err return signals a lookup failure — the caller logs it and proceeds
// as if the issue were unprocessed, so a flaky store never stops the
// pipeline from running.
func (f *Fetcher) alreadyProcessed(issue *github.Issue) (bool, string, error) {
	row, err := f.store.GetIssueByGithubID(issue.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// First time we see this issue — process it.
			return false, "", nil
		}
		return false, "", err
	}

	// Comment-based control markers (#238). Checked before the dismiss and
	// dedup gates so a retry marker can override all of them. The API call
	// is skipped when the comment fetcher is nil (legacy callers / tests
	// that pre-date marker support).
	// cachedComments holds the comments fetched during marker scanning,
	// reused later for the bot-comment check to avoid a second API call.
	var cachedComments []github.Comment

	if f.comments != nil {
		comments, cmErr := f.comments.FetchIssueCommentsOnly(issue.Repo, issue.Number)
		if cmErr != nil {
			slog.Warn("issues fetcher: marker scan failed, falling through to dedup checks",
				"repo", issue.Repo, "number", issue.Number, "err", cmErr)
		} else {
			cachedComments = comments
			switch ScanMarkers(comments) {
			case MarkerResultRetry:
				return false, "", nil // force reprocess
			case MarkerResultSkip:
				return true, "skip marker", nil
			case MarkerResultDone:
				return true, "done marker", nil
			}
		}
	}

	if row.Dismissed {
		return true, "dismissed", nil
	}

	latest, err := f.store.LatestIssueReview(row.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Known issue, never reviewed — process it.
			return false, "", nil
		}
		return false, "", err
	}

	// If a previous run already created a PR via auto_implement, skip
	// unconditionally — re-running would fail with non-fast-forward or
	// create a duplicate PR.  The user should close the issue or dismiss
	// it to stop the pipeline from picking it up again.
	if latest.ActionTaken == "auto_implement" && latest.PRCreated > 0 {
		return true, "already implemented (PR created)", nil
	}
	if latest.ActionTaken == ActionAutoImplementNoChanges && issue.Mode == config.IssueModeDevelop {
		// Back-compat skip for rows written before #483 added MarkerDone
		// to the fallback comment. New no-changes runs are terminated by
		// the marker scan above; reaching here means the row was stored
		// without a marker, so we keep skipping until a human posts
		// MarkerRetry (handled before this block).
		return true, "auto_implement produced no changes (historical row, no done marker); add retry marker to reprocess", nil
	}

	// Bot-comment dedup: if the most recent comment is from the bot AND the
	// issue's current mode matches what was already done (ActionTaken), the
	// updated_at bump was self-triggered — skip. When the mode changed
	// (e.g. review_only → develop after a label swap), reprocess even if
	// the last comment is from the bot. This breaks the re-triage loop
	// (#362) without blocking mode transitions.
	if f.botLogin != "" && len(cachedComments) > 0 {
		last := cachedComments[len(cachedComments)-1]
		if strings.EqualFold(last.Author, f.botLogin) && latest.ActionTaken == string(issue.Mode) {
			return true, "last comment is from bot, same mode (self-triggered update)", nil
		}
	}

	// If the auto_implement push has failed too many times, stop retrying.
	failCount, fcErr := f.store.CountFailedAutoImplement(row.ID)
	if fcErr != nil {
		slog.Warn("issues fetcher: could not count failed auto_implement attempts, skipping cap check",
			"repo", issue.Repo, "number", issue.Number, "err", fcErr)
	} else if failCount >= MaxAutoImplementFailures {
		return true, fmt.Sprintf("auto_implement failed %d times (cap %d), requires human intervention", failCount, MaxAutoImplementFailures), nil
	}

	// Any adjacent forward stage promotion is real new work, regardless of
	// whether the bot or a human changed labels. Do not let RecomputeGrace hide
	// triage -> refinement or refinement -> development.
	if latestStage, latestOK := StageFromAction(latest.ActionTaken); latestOK {
		if currentStage, currentOK := StageFromMode(issue.Mode); currentOK && isForwardStageAdvance(latestStage, currentStage) {
			return false, "", nil
		}
	}

	ref := latest.CommentedAt
	if ref.IsZero() {
		ref = latest.CreatedAt
	}
	cutoff := ref.Add(RecomputeGrace)
	if !issue.UpdatedAt.After(cutoff) {
		return true, "no new activity since last review", nil
	}
	return false, "", nil
}

func (f *Fetcher) auditManualStageChange(ctx context.Context, issue *github.Issue, cfg config.IssueTrackingConfig) error {
	if f.stageClient == nil || issue == nil {
		return nil
	}
	to, ok := StageFromMode(issue.Mode)
	if !ok {
		return nil
	}
	row, err := f.store.GetIssueByGithubID(issue.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	latest, err := f.store.LatestIssueReview(row.ID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	if latest == nil {
		return nil
	}
	from, ok := StageFromAction(latest.ActionTaken)
	if !ok || from == to {
		return nil
	}

	var comments []github.Comment
	if f.comments != nil {
		got, err := f.comments.FetchIssueCommentsOnly(issue.Repo, issue.Number)
		if err != nil {
			slog.Warn("issues fetcher: manual stage audit comment fetch failed, continuing without dedup context",
				"repo", issue.Repo, "number", issue.Number, "err", err)
		} else {
			comments = got
		}
	}
	ref := latest.CommentedAt
	if ref.IsZero() {
		ref = latest.CreatedAt
	}
	alreadyAudited := hasStagePromotionCommentSince(comments, from, to, ref)
	targetAudited := alreadyAudited || hasStagePromotionTargetCommentSince(comments, to, ref)
	if targetAudited && stageTransitionApplied(issue, cfg, to) {
		slog.Debug("issues fetcher: stage transition already audited since latest review",
			"repo", issue.Repo, "number", issue.Number, "from", from, "to", to)
		return nil
	}
	if targetAudited {
		slog.Debug("issues fetcher: stage transition audit exists but labels need normalization",
			"repo", issue.Repo, "number", issue.Number, "from", from, "to", to)
	}

	return TransitionIssueStage(ctx, f.stageClient, StageTransition{
		Issue:          issue,
		StoreIssueID:   row.ID,
		Config:         cfg,
		From:           from,
		To:             to,
		Trigger:        StagePromotionManualGitHub,
		Time:           time.Now().UTC(),
		RecentComments: comments,
		SuppressAudit:  targetAudited,
		Broker:         f.stageBroker,
	})
}

var issueStageOrder = []IssueStage{
	IssueStageTriage,
	IssueStageRefinement,
	IssueStageDevelopment,
}

func isForwardStageAdvance(from, to IssueStage) bool {
	fromIdx, fromOK := issueStageOrdinal(from)
	toIdx, toOK := issueStageOrdinal(to)
	return fromOK && toOK && toIdx == fromIdx+1
}

func issueStageOrdinal(stage IssueStage) (int, bool) {
	for idx, candidate := range issueStageOrder {
		if candidate == stage {
			return idx, true
		}
	}
	return 0, false
}
