// Package discovery automatically discovers GitHub repositories by topic tag.
// The static repository list in config is augmented with whatever the Search
// API returns for `topic:<tag> org:<org>` across the configured orgs.
//
// The service keeps an in-memory cache that is only replaced on a successful
// refresh: transient API errors (rate limiting, 5xx) never wipe the last
// known-good list, so an outage cannot silently stop the daemon from polling
// previously discovered repos.
package discovery

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// ReposFetcher is the subset of the GitHub client used for discovery.
// It is an interface so tests can swap in a fake without an HTTP server.
type ReposFetcher interface {
	FetchReposByTopic(topic string, orgs []string) ([]string, error)
}

// Service runs a background loop that refreshes the list of repositories
// that carry the configured topic tag. Callers read the current list with
// Discovered(); the merge with the static list happens at the call site.
type Service struct {
	fetcher ReposFetcher

	mu        sync.RWMutex
	cache     []string
	lastErr   error
	lastAt    time.Time
	lastCount int
}

// NewService wires a fetcher into a discovery service.
func NewService(fetcher ReposFetcher) *Service {
	return &Service{fetcher: fetcher}
}

// Discovered returns a copy of the most recent successful discovery list.
// The slice is safe for the caller to mutate.
func (s *Service) Discovered() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.cache) == 0 {
		return nil
	}
	out := make([]string, len(s.cache))
	copy(out, s.cache)
	return out
}

// LastError returns the most recent refresh error, or nil on success.
func (s *Service) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

// Refresh performs a single discovery query and updates the cache on success.
// On full failure the cache is preserved — degrade to previously known list.
// Partial failures (some orgs OK, others not) update the cache with what was
// returned and also record the error, so the caller can log it.
func (s *Service) Refresh(topic string, orgs []string) error {
	repos, err := s.fetcher.FetchReposByTopic(topic, orgs)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = err
	s.lastAt = time.Now()

	// Only overwrite the cache if we got at least one repo back. This is the
	// key safety property: a total outage returns (nil, err) and we keep the
	// previous list; a partial outage returns (some, err) and we update with
	// what we have.
	if len(repos) > 0 {
		prev := s.lastCount
		s.cache = repos
		s.lastCount = len(repos)
		if prev > 0 && len(repos) < prev {
			slog.Warn("discovery: repo count decreased",
				"previous", prev, "current", len(repos))
		}
	} else if err != nil {
		slog.Warn("discovery: refresh failed, keeping previous cache",
			"cached", len(s.cache), "err", err)
	}
	return err
}

// Run blocks until ctx is cancelled, refreshing the cache every interval.
// The first refresh happens immediately; subsequent refreshes wait for the
// full interval. Errors are logged but never stop the loop.
func (s *Service) Run(ctx context.Context, interval time.Duration, topic string, orgs []string) {
	if interval <= 0 {
		slog.Warn("discovery: non-positive interval, loop disabled", "interval", interval)
		return
	}
	if err := s.Refresh(topic, orgs); err != nil {
		slog.Warn("discovery: initial refresh failed", "err", err)
	} else {
		slog.Info("discovery: initial refresh",
			"topic", topic, "orgs", orgs, "count", len(s.Discovered()))
	}

	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := s.Refresh(topic, orgs); err != nil {
				slog.Warn("discovery: refresh failed", "err", err)
				continue
			}
			slog.Info("discovery: refreshed",
				"topic", topic, "orgs", orgs, "count", len(s.Discovered()))
		}
	}
}

// MergeRepos returns the union of static and discovered repositories,
// preserving the order of static entries (stable for config-driven overrides)
// and appending discovered entries that are not already present.
// Repos listed in nonMonitored are excluded unconditionally (absolute blacklist).
func MergeRepos(static, discovered, nonMonitored []string) []string {
	if len(static) == 0 && len(discovered) == 0 {
		return nil
	}
	blacklist := make(map[string]struct{}, len(nonMonitored))
	for _, r := range nonMonitored {
		if r != "" {
			blacklist[r] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(static)+len(discovered))
	out := make([]string, 0, len(static)+len(discovered))
	for _, r := range static {
		if r == "" {
			continue
		}
		if _, blocked := blacklist[r]; blocked {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	for _, r := range discovered {
		if r == "" {
			continue
		}
		if _, blocked := blacklist[r]; blocked {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// InferOrgs extracts unique organization prefixes from "org/repo" strings.
// Returns sorted, deduplicated org names. Used when discovery_orgs is empty.
func InferOrgs(repos []string) []string {
	set := make(map[string]struct{})
	for _, r := range repos {
		if idx := strings.IndexByte(r, '/'); idx > 0 {
			set[r[:idx]] = struct{}{}
		}
	}
	orgs := make([]string, 0, len(set))
	for o := range set {
		orgs = append(orgs, o)
	}
	sort.Strings(orgs)
	return orgs
}
