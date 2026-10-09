package pipeline

import (
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/store"
)

// ReviewWindowLimits is a review budget per rolling minute, hour and day.
// Zero leaves that window unlimited. Mirrors config.ReviewLimitsConfig
// (pipeline cannot import config).
type ReviewWindowLimits struct {
	PerMinute int
	PerHour   int
	PerDay    int
}

func (l ReviewWindowLimits) any() bool { return l.PerMinute > 0 || l.PerHour > 0 || l.PerDay > 0 }

// ReviewBudgetScope is one budget a review must fit in. Kind is "global",
// "org", "repo" or "agent"; Key is the org, repo or agent it covers.
type ReviewBudgetScope struct {
	Kind   string
	Key    string
	Limits ReviewWindowLimits
}

func (s ReviewBudgetScope) label() string {
	if s.Key == "" {
		return s.Kind
	}
	return s.Kind + " " + s.Key
}

// ReviewBudgets is the set of budgets resolved for one review. Scopes apply
// whatever agent runs; Agents is consulted once an agent is chosen.
type ReviewBudgets struct {
	Scopes []ReviewBudgetScope
	Agents map[string]ReviewWindowLimits
}

// ReviewBudgetError reports that a review was deferred because a budget is
// spent. RetryAt is when the earliest blocking window frees a slot.
type ReviewBudgetError struct {
	Scope   string
	Window  string
	Limit   int
	RetryAt time.Time
}

func (e *ReviewBudgetError) Error() string {
	return fmt.Sprintf("review limit reached: %s allows %d per %s (next slot %s)",
		e.Scope, e.Limit, e.Window, e.RetryAt.Format(time.RFC3339))
}

var budgetWindows = []struct {
	name string
	dur  time.Duration
	pick func(ReviewWindowLimits) int
}{
	{"minute", time.Minute, func(l ReviewWindowLimits) int { return l.PerMinute }},
	{"hour", time.Hour, func(l ReviewWindowLimits) int { return l.PerHour }},
	{"day", 24 * time.Hour, func(l ReviewWindowLimits) int { return l.PerDay }},
}

// budgetTicket is a review that has been admitted but not stored yet. It
// keeps counting against every matching budget until released, so a burst of
// concurrent workers cannot all pass the same last free slot.
type budgetTicket struct {
	repo string
	cli  string
	at   time.Time
}

// reviewBudget tracks admitted-but-unfinished reviews. Finished reviews are
// counted from the reviews table, so budgets survive a restart.
type reviewBudget struct {
	mu      sync.Mutex
	pending map[*budgetTicket]struct{}
}

func scopeMatches(s ReviewBudgetScope, repo, cli string) bool {
	switch s.Kind {
	case "global":
		return true
	case "org":
		return strings.HasPrefix(repo, s.Key+"/")
	case "repo":
		return repo == s.Key
	case "agent":
		return cli == s.Key
	}
	return false
}

func scopeFilter(s ReviewBudgetScope) store.ReviewCountFilter {
	switch s.Kind {
	case "org":
		return store.ReviewCountFilter{Org: s.Key}
	case "repo":
		return store.ReviewCountFilter{Repo: s.Key}
	case "agent":
		return store.ReviewCountFilter{CLI: s.Key}
	}
	return store.ReviewCountFilter{}
}

// check returns nil when one more review fits in scope at now, or the error
// describing the first window that is full. Caller holds b.mu.
func (b *reviewBudget) check(st *store.Store, s ReviewBudgetScope, self *budgetTicket, now time.Time) (*ReviewBudgetError, error) {
	times, err := st.ReviewTimesSince(scopeFilter(s), now.Add(-24*time.Hour))
	if err != nil {
		return nil, err
	}
	for t := range b.pending {
		if t != self && scopeMatches(s, t.repo, t.cli) {
			times = append(times, t.at)
		}
	}
	var worst *ReviewBudgetError
	for _, w := range budgetWindows {
		limit := w.pick(s.Limits)
		if limit <= 0 {
			continue
		}
		var inWindow []time.Time
		for _, t := range times {
			if !t.Before(now.Add(-w.dur)) {
				inWindow = append(inWindow, t)
			}
		}
		if len(inWindow) < limit {
			continue
		}
		sortTimes(inWindow)
		// Once len-limit+1 of them age out there is room again; the one at
		// index len-limit is the last of those to leave.
		retryAt := inWindow[len(inWindow)-limit].Add(w.dur)
		if worst == nil || retryAt.After(worst.RetryAt) {
			worst = &ReviewBudgetError{Scope: s.label(), Window: w.name, Limit: limit, RetryAt: retryAt}
		}
	}
	return worst, nil
}

func sortTimes(ts []time.Time) {
	slices.SortFunc(ts, func(a, b time.Time) int { return a.Compare(b) })
}

// admit reserves a slot in every scope for a review of repo, or returns the
// budget that is full. A store error admits the review (fail-open, logged):
// a budget is a spend preference, and a broken counter must not stop every
// review.
//
// force admits regardless (an operator's manual re-review), still charging
// the slot so automatic work after it sees the spend.
func (b *reviewBudget) admit(st *store.Store, scopes []ReviewBudgetScope, repo string, now time.Time, force bool) (*budgetTicket, *ReviewBudgetError) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ticket := &budgetTicket{repo: repo, at: now}
	var worst *ReviewBudgetError
	for _, s := range scopes {
		if !s.Limits.any() {
			continue
		}
		blocked, err := b.check(st, s, ticket, now)
		if err != nil {
			slog.Warn("pipeline: review budget check failed, admitting", "scope", s.label(), "err", err)
			continue
		}
		if blocked != nil && (worst == nil || blocked.RetryAt.After(worst.RetryAt)) {
			worst = blocked
		}
	}
	if worst != nil && !force {
		return nil, worst
	}
	if b.pending == nil {
		b.pending = make(map[*budgetTicket]struct{})
	}
	b.pending[ticket] = struct{}{}
	return ticket, nil
}

// assignAgent charges ticket to cli's budget, or reports that cli is out of
// budget (the ticket is left unassigned so another agent can be tried).
func (b *reviewBudget) assignAgent(st *store.Store, ticket *budgetTicket, cli string, limits ReviewWindowLimits, now time.Time) *ReviewBudgetError {
	b.mu.Lock()
	defer b.mu.Unlock()
	if limits.any() {
		blocked, err := b.check(st, ReviewBudgetScope{Kind: "agent", Key: cli, Limits: limits}, ticket, now)
		if err != nil {
			slog.Warn("pipeline: agent review budget check failed, admitting", "agent", cli, "err", err)
		} else if blocked != nil {
			return blocked
		}
	}
	if ticket != nil {
		ticket.cli = cli
	}
	return nil
}

func (b *reviewBudget) release(ticket *budgetTicket) {
	if ticket == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.pending, ticket)
}

// ReviewBudgetUsage is one window of one budget, for GET /review-limits.
type ReviewBudgetUsage struct {
	Window  string    `json:"window"`
	Used    int       `json:"used"`
	Limit   int       `json:"limit"`
	ResetAt time.Time `json:"reset_at,omitempty"`
}

// ReviewBudgetStatus is the live usage of one budget.
type ReviewBudgetStatus struct {
	Kind    string              `json:"kind"`
	Key     string              `json:"key,omitempty"`
	Windows []ReviewBudgetUsage `json:"windows"`
}

// ReviewBudgetStatus reports current usage of every scope, counting stored
// reviews plus reviews admitted and still running.
func (p *Pipeline) ReviewBudgetStatus(scopes []ReviewBudgetScope, now time.Time) ([]ReviewBudgetStatus, error) {
	p.budget.mu.Lock()
	defer p.budget.mu.Unlock()
	out := make([]ReviewBudgetStatus, 0, len(scopes))
	for _, s := range scopes {
		times, err := p.store.ReviewTimesSince(scopeFilter(s), now.Add(-24*time.Hour))
		if err != nil {
			return nil, err
		}
		for t := range p.budget.pending {
			if scopeMatches(s, t.repo, t.cli) {
				times = append(times, t.at)
			}
		}
		sortTimes(times)
		st := ReviewBudgetStatus{Kind: s.Kind, Key: s.Key, Windows: []ReviewBudgetUsage{}}
		for _, w := range budgetWindows {
			limit := w.pick(s.Limits)
			if limit <= 0 {
				continue
			}
			u := ReviewBudgetUsage{Window: w.name, Limit: limit}
			for _, t := range times {
				if !t.Before(now.Add(-w.dur)) {
					if u.Used == 0 {
						// The oldest review in the window frees the first slot.
						u.ResetAt = t.Add(w.dur)
					}
					u.Used++
				}
			}
			st.Windows = append(st.Windows, u)
		}
		out = append(out, st)
	}
	return out, nil
}

// ErrNoFlowAgent means the review flow matched no available agent (every
// rule's conditions failed, or its agents are not installed). The review is
// deferred: the next poll re-evaluates the flow.
var ErrNoFlowAgent = errors.New("pipeline: the review flow selected no available agent")

// agentFits reports, without charging it, whether one more review fits in
// cli's own budget.
func (b *reviewBudget) agentFits(st *store.Store, ticket *budgetTicket, cli string, limits ReviewWindowLimits, now time.Time) *ReviewBudgetError {
	if !limits.any() {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	blocked, err := b.check(st, ReviewBudgetScope{Kind: "agent", Key: cli, Limits: limits}, ticket, now)
	if err != nil {
		slog.Warn("pipeline: agent review budget check failed, admitting", "agent", cli, "err", err)
		return nil
	}
	return blocked
}

// selectCLIs returns the agents to try for a review, in order: the flow's
// candidates (RunOptions.Candidates) or primary then fallback, keeping those
// that are installed and still have room in their own review budget. The
// first one reviews; the others take over if it runs out of quota. When
// every installed agent is out of budget it returns the *ReviewBudgetError
// that frees first, so the caller defers instead of failing.
func (p *Pipeline) selectCLIs(primary, fallback string, opts RunOptions, ticket *budgetTicket) ([]string, error) {
	now := time.Now().UTC()
	var candidates []string
	if opts.Candidates != nil {
		candidates = opts.Candidates()
		if len(candidates) == 0 {
			return nil, ErrNoFlowAgent
		}
	} else if len(opts.Budgets.Agents) == 0 {
		// Legacy callers without a flow or agent budgets: exactly the old
		// Detect(primary, fallback) behaviour.
		cli, err := p.executor.Detect(primary, fallback)
		if err != nil {
			return nil, err
		}
		return []string{cli}, nil
	} else {
		candidates = []string{primary, fallback}
	}

	var usable []string
	var soonest *ReviewBudgetError
	seen := map[string]bool{}
	for _, candidate := range candidates {
		if candidate == "" || seen[candidate] {
			continue
		}
		seen[candidate] = true
		cli, err := p.executor.Detect(candidate, "")
		if err != nil || seen[cli+"\x00resolved"] {
			continue
		}
		seen[cli+"\x00resolved"] = true
		limits := opts.Budgets.Agents[cli]
		if opts.Force {
			// Charge the agent but never defer an operator's manual run.
			limits = ReviewWindowLimits{}
		}
		if blocked := p.budget.agentFits(p.store, ticket, cli, limits, now); blocked != nil {
			slog.Info("pipeline: agent out of review budget, trying the next one",
				"agent", cli, "scope", blocked.Scope, "window", blocked.Window, "retry_at", blocked.RetryAt)
			if soonest == nil || blocked.RetryAt.Before(soonest.RetryAt) {
				soonest = blocked
			}
			continue
		}
		usable = append(usable, cli)
	}
	if len(usable) > 0 {
		return usable, nil
	}
	if soonest != nil {
		return nil, soonest
	}
	if opts.Candidates != nil {
		return nil, ErrNoFlowAgent
	}
	// Nothing installed: keep Detect's own error for the caller.
	_, err := p.executor.Detect(primary, fallback)
	if err == nil {
		err = fmt.Errorf("pipeline: no AI agent available (tried %q, %q)", primary, fallback)
	}
	return nil, err
}

// execOptionsFor returns the execution options for cli: its own settings
// when the caller resolved them per agent (flows), otherwise the primary's
// options with provider-specific fields dropped for a different agent.
func execOptionsFor(cli, primary string, opts RunOptions) executor.ExecOptions {
	if o, ok := opts.AgentExecOpts[cli]; ok {
		return o
	}
	return executor.OptionsForSelectedCLI(primary, cli, opts.ExecOpts)
}

// deferForBudget publishes the review_limit skip for a review that did not
// fit in a budget. retry_at tells the UI when it will be picked up again.
func (p *Pipeline) deferForBudget(pr *github.PullRequest, e *ReviewBudgetError) {
	slog.Info("pipeline: review limit reached — deferring review",
		"repo", pr.Repo, "pr", pr.Number, "scope", e.Scope, "window", e.Window,
		"limit", e.Limit, "retry_at", e.RetryAt)
	p.publishSkippedWith(pr, SkipReasonReviewLimit, map[string]any{
		"limit_scope":  e.Scope,
		"limit_window": e.Window,
		"limit":        e.Limit,
		"retry_at":     e.RetryAt.UTC().Format(time.RFC3339),
	})
}
