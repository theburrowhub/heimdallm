package issues

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/github"
)

// PromoteIssueClient is the subset of *github.Client that PromoteReady
// uses. Scoped to the minimum so tests can inject an in-memory fake.
type PromoteIssueClient interface {
	ListOpenIssues(repo string) ([]*github.Issue, error)
	ListSubIssues(repo string, number int) ([]*github.Issue, error)
	GetIssue(repo string, number int) (*github.Issue, error)
	AddLabels(repo string, number int, labels []string) error
	RemoveLabels(repo string, number int, labels []string) error
	PostComment(repo string, number int, body string) error
}

// PromoteReady scans every monitored repo for open issues carrying a
// "blocked" label, checks whether each issue's declared dependencies have
// all closed, and — when they have — flips the blocked label(s) to the
// configured promote-to label. The next poll cycle then picks up the
// freshly-promoted issue through the normal FetchIssues path.
//
// Returns the number of promotions performed and the first fatal error.
// Per-issue failures (a bad dep, a transient GitHub 5xx) are logged and
// counted as "skipped", they don't abort the whole run: the next poll
// cycle retries naturally.
//
// The call is a no-op when issue tracking is disabled, when no blocked
// labels are configured, or when every repo in the list is empty — keeps
// default installs unaffected.
func PromoteReady(ctx context.Context, c PromoteIssueClient, cfg config.IssueTrackingConfig, repos []string) (int, error) {
	if !cfg.Enabled || len(cfg.BlockedLabels) == 0 {
		return 0, nil
	}
	promoteTo := cfg.ResolvePromoteToLabel()
	if promoteTo == "" {
		return 0, fmt.Errorf("issues promote: blocked_labels set but promote target unresolved")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	blockedSet := lowerSet(cfg.BlockedLabels)
	promoted := 0
	// issueCache is scoped to this PromoteReady pass. When two blocked
	// issues declare the same dependency (common: a shared schema
	// migration blocking both API and UI), we'd otherwise hit GetIssue
	// twice for the same ref. The cache is tiny (one *github.Issue per
	// unique ref) and lasts milliseconds.
	issueCache := make(map[string]*github.Issue)

	for _, repo := range repos {
		if err := ctx.Err(); err != nil {
			return promoted, err
		}
		issues, err := c.ListOpenIssues(repo)
		if err != nil {
			slog.Warn("issues promote: list failed, skipping repo this cycle", "repo", repo, "err", err)
			continue
		}

		for _, issue := range issues {
			// Per-issue cancellation check. A daemon shutdown mid-repo
			// should not churn through every blocked issue just because
			// we already entered the repo loop.
			if err := ctx.Err(); err != nil {
				return promoted, err
			}
			blockedOnIssue := intersectLabels(issue.LabelNames(), blockedSet)
			if len(blockedOnIssue) == 0 {
				continue
			}
			// Collect deps from BOTH sources: the `## Depends on` body
			// parser (cross-org-capable) AND GitHub's native sub-issues
			// REST (same-owner-only). Unioned and deduped so operators
			// can use either or both without double-counting.
			deps := ParseDependencies(issue.Body, repo)
			subIssues, err := c.ListSubIssues(repo, issue.Number)
			if err != nil {
				// Can't see native sub-issues this cycle: skip the whole
				// issue rather than promote on incomplete information.
				// Body-alone might say "ready" while a native sub-issue
				// we can't read is still open — worst-case scenario.
				slog.Warn("issues promote: sub-issues lookup failed, skipping issue this cycle",
					"repo", repo, "issue", issue.Number, "err", err)
				continue
			}
			for _, si := range subIssues {
				ref := IssueRef{Repo: si.Repo, Number: si.Number}
				if !containsRef(deps, ref) {
					deps = append(deps, ref)
				}
				// Pre-populate the cache with the sub-issue's state —
				// the sub_issues endpoint returns full issue objects,
				// saving a GetIssue round-trip during checkDeps.
				cacheKey := fmt.Sprintf("%s#%d", ref.Repo, ref.Number)
				if _, cached := issueCache[cacheKey]; !cached {
					issueCache[cacheKey] = si
				}
			}
			if len(deps) == 0 {
				// Carries a blocked label but no deps declared in either
				// source. Stay out — operator likely wants to unblock
				// manually.
				continue
			}
			states, err := checkDeps(ctx, c, deps, issueCache)
			if err != nil {
				slog.Warn("issues promote: dep check failed",
					"repo", repo, "issue", issue.Number, "err", err)
				continue
			}
			if !allClosed(states) {
				continue
			}
			if err := applyPromotion(c, issue, blockedOnIssue, promoteTo, states); err != nil {
				slog.Warn("issues promote: apply failed",
					"repo", repo, "issue", issue.Number, "err", err)
				continue
			}
			promoted++
			slog.Info("issues promote: promoted issue",
				"repo", repo, "issue", issue.Number,
				"from", blockedOnIssue, "to", promoteTo)
		}
	}
	return promoted, nil
}

// depState records a dependency reference paired with the GitHub state
// observed at check time. Rendered into the audit comment so operators can
// diagnose a TOCTOU edge case (a dep reopened between the check and the
// label flip) without spelunking logs.
type depState struct {
	Ref   IssueRef
	State string
}

// checkDeps resolves the state of every dep, sharing results through
// `cache` so a blocker referenced by multiple blocked issues in the same
// PromoteReady pass costs one GetIssue call, not one per referrer. A
// closed PR with state "merged" is reported by GitHub as state == "closed"
// too (merged is a sub-state we don't need to inspect — "closed" covers
// "this work has landed one way or another").
//
// `ctx` is consulted before every GetIssue fetch so a daemon shutdown
// partway through a long dep chain exits promptly instead of blocking
// on up to N × HTTP timeouts.
//
// On any GitHub call failure the function returns (nil, err) — the
// caller logs and skips this issue, the next cycle retries.
func checkDeps(ctx context.Context, c PromoteIssueClient, deps []IssueRef, cache map[string]*github.Issue) ([]depState, error) {
	out := make([]depState, 0, len(deps))
	for _, d := range deps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%s#%d", d.Repo, d.Number)
		got, ok := cache[key]
		if !ok {
			fetched, err := c.GetIssue(d.Repo, d.Number)
			if err != nil {
				return nil, fmt.Errorf("dep %s: %w", key, err)
			}
			cache[key] = fetched
			got = fetched
		}
		out = append(out, depState{Ref: d, State: got.State})
	}
	return out, nil
}

// allClosed reports whether every recorded state is "closed".
func allClosed(states []depState) bool {
	for _, s := range states {
		if s.State != "closed" {
			return false
		}
	}
	return true
}

// applyPromotion flips the blocked label(s) to the promote-to label and
// leaves an audit comment listing the dep states as observed at check
// time. Calls are ordered remove → add → comment: if add fails we've
// already removed, which is less bad than the reverse (double-labelled,
// in both states at once). The comment payload includes the dep states
// so a TOCTOU race (a dep reopening between check and flip) is diagnosable
// from the GitHub timeline alone.
func applyPromotion(c PromoteIssueClient, issue *github.Issue, blockedLabels []string, promoteTo string, states []depState) error {
	if err := c.RemoveLabels(issue.Repo, issue.Number, blockedLabels); err != nil {
		return fmt.Errorf("remove blocked: %w", err)
	}
	if err := c.AddLabels(issue.Repo, issue.Number, []string{promoteTo}); err != nil {
		// Compensating action: the blocked label is already removed, so
		// if we return now the issue is orphaned — invisible to the
		// promotion pass (no blocked label) AND to the normal pipeline
		// (no promote-to label). Best-effort re-apply of the blocked
		// label(s) keeps the issue in the queue so the next cycle
		// retries. If that ALSO fails, we can only log loudly; the
		// operator gets a paper trail in the logs.
		if reErr := c.AddLabels(issue.Repo, issue.Number, blockedLabels); reErr != nil {
			slog.Error("issues promote: could not restore blocked label after AddLabels failure; issue may be orphaned",
				"repo", issue.Repo, "issue", issue.Number,
				"original_err", err, "restore_err", reErr)
		}
		return fmt.Errorf("add promote-to: %w", err)
	}
	if err := c.PostComment(issue.Repo, issue.Number, auditCommentBody(promoteTo, states)); err != nil {
		// Comment failure is non-fatal — labels already flipped, the
		// next poll cycle will process the issue normally. Log only.
		slog.Warn("issues promote: audit comment failed (labels already flipped)",
			"repo", issue.Repo, "issue", issue.Number, "err", err)
	}
	return nil
}

// auditCommentBody renders the Markdown posted on successful promotion.
// Lists each dep + state so operators can diagnose premature promotions
// without reading daemon logs.
func auditCommentBody(promoteTo string, states []depState) string {
	var sb strings.Builder
	sb.WriteString("All declared dependencies are closed, promoting to `")
	sb.WriteString(promoteTo)
	sb.WriteString("`.\n\n**Dependency states at promotion time:**\n")
	for _, s := range states {
		sb.WriteString(fmt.Sprintf("- `%s#%d`: %s\n", s.Ref.Repo, s.Ref.Number, s.State))
	}
	sb.WriteString("\n---\n*Promoted by Heimdallm*")
	return sb.String()
}

// containsRef reports whether refs already includes target. Cheap linear
// scan — dep lists are small (single digits in practice), so the map
// allocation overhead of a set wouldn't pay off.
func containsRef(refs []IssueRef, target IssueRef) bool {
	for _, r := range refs {
		if r.Repo == target.Repo && r.Number == target.Number {
			return true
		}
	}
	return false
}

func lowerSet(xs []string) map[string]struct{} {
	m := make(map[string]struct{}, len(xs))
	for _, x := range xs {
		m[strings.ToLower(strings.TrimSpace(x))] = struct{}{}
	}
	return m
}

// intersectLabels returns the subset of `have` that are also in `want`
// (case-insensitive). Preserves the casing from `have` so RemoveLabels
// hits the exact label GitHub stores.
func intersectLabels(have []string, want map[string]struct{}) []string {
	var out []string
	for _, h := range have {
		key := strings.ToLower(strings.TrimSpace(h))
		if _, ok := want[key]; ok {
			out = append(out, h)
		}
	}
	return out
}
