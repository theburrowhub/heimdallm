// Package quota reads how much of each agent's usage allowance is left —
// Claude's 5-hour and weekly windows, Codex's rate-limit windows, Copilot's
// monthly premium requests, Gemini's per-model buckets and an OpenRouter
// key's credit — so review flows can pick an agent by its remaining quota.
//
// Every source reuses credentials the agent's own CLI already stored on this
// machine (as lcm-tracker and Orca do), only to call that provider's own
// usage endpoint. Tokens are never logged, cached on disk or returned: a
// Provider carries percentages and reset times only.
package quota

import (
	"context"
	"sync"
	"time"
)

// Window kinds. Flows match conditions by kind.
const (
	KindSession = "session" // Claude 5h, Codex primary window
	KindWeekly  = "weekly"  // Claude 7d, Codex secondary window
	KindMonthly = "monthly" // Copilot premium requests
	KindModel   = "model"   // Gemini per-model bucket (Label = model id)
	KindCredit  = "credit"  // OpenRouter key spend against its limit
)

// Window is one allowance being tracked.
type Window struct {
	Kind  string `json:"kind"`
	Label string `json:"label,omitempty"`
	// UsedPercent is how much of the window is spent, 0-100.
	UsedPercent float64   `json:"used_percent"`
	ResetsAt    time.Time `json:"resets_at,omitempty"`
}

// Error kinds: a closed set, never a raw error that could carry a path or a
// response body.
const (
	ErrNotConfigured = "not_configured" // no credentials for this agent
	ErrExpired       = "expired"        // credentials rejected or expired
	ErrUnavailable   = "unavailable"    // provider unreachable or failing
	ErrUnsupported   = "unsupported"    // the agent exposes no quota
)

// Provider is one agent's quota picture.
type Provider struct {
	Agent     string    `json:"agent"`
	Available bool      `json:"available"`
	Windows   []Window  `json:"windows,omitempty"`
	FetchedAt time.Time `json:"fetched_at,omitempty"`
	Error     string    `json:"error,omitempty"`
}

// Window returns the window of kind (and label, for per-model kinds), or
// false.
func (p Provider) Window(kind, label string) (Window, bool) {
	for _, w := range p.Windows {
		if w.Kind == kind && (label == "" || w.Label == label) {
			return w, true
		}
	}
	return Window{}, false
}

// MaxUsed is the most-spent window, for "any window" conditions.
func (p Provider) MaxUsed() (Window, bool) {
	var best Window
	found := false
	for _, w := range p.Windows {
		if !found || w.UsedPercent > best.UsedPercent {
			best, found = w, true
		}
	}
	return best, found
}

// Source reads one agent's quota.
type Source interface {
	Agent() string
	Read(ctx context.Context) Provider
}

// cacheTTL is how long a reading is reused. Quota endpoints are cheap but
// rate limited, and a flow evaluation per review must not hit them each time.
const cacheTTL = 2 * time.Minute

// maxStale is how old a reading may be and still be returned while a fresh
// one is read in the background. Older readings are not trusted for a flow
// decision: the caller waits for the read instead.
const maxStale = 10 * time.Minute

// readTimeout bounds one source read.
const readTimeout = 10 * time.Second

// Service caches every source's reading.
type Service struct {
	sources map[string]Source
	order   []string
	now     func() time.Time

	mu    sync.Mutex
	cache map[string]Provider
	// inflight holds the running read of each agent; its channel is closed
	// when the reading is stored, so concurrent callers share one read.
	inflight map[string]chan struct{}
}

// NewService returns a service over sources.
func NewService(sources ...Source) *Service {
	s := &Service{sources: map[string]Source{}, now: time.Now, cache: map[string]Provider{}, inflight: map[string]chan struct{}{}}
	for _, src := range sources {
		s.sources[src.Agent()] = src
		s.order = append(s.order, src.Agent())
	}
	return s
}

// Get returns agent's quota, read through the cache. An agent with no source
// reports ErrUnsupported.
//
// One slow provider must not hold up every review: a reading past cacheTTL
// but within maxStale is returned at once while it is refreshed in the
// background, and a caller with no usable reading waits for the read only as
// long as its ctx allows. A caller that gives up gets ErrUnavailable, which
// flow conditions treat as not met.
func (s *Service) Get(ctx context.Context, agent string) Provider {
	src, ok := s.sources[agent]
	if !ok {
		return Provider{Agent: agent, Error: ErrUnsupported}
	}
	s.mu.Lock()
	p, cached := s.cache[agent]
	age := s.now().Sub(p.FetchedAt)
	if cached && age < cacheTTL {
		s.mu.Unlock()
		return p
	}
	done, busy := s.inflight[agent]
	if !busy {
		done = make(chan struct{})
		s.inflight[agent] = done
		go s.refresh(agent, src, done)
	}
	s.mu.Unlock()
	if cached && age < maxStale {
		return p
	}
	select {
	case <-done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cache[agent]
	case <-ctx.Done():
		return Provider{Agent: agent, Error: ErrUnavailable}
	}
}

// refresh reads one source and stores the result. It runs detached from any
// caller, bounded by readTimeout, so a caller that gives up does not waste the
// read for the next one; at most one refresh per agent runs at a time.
func (s *Service) refresh(agent string, src Source, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
	p := src.Read(ctx)
	cancel()
	p.Agent = agent
	if p.FetchedAt.IsZero() {
		p.FetchedAt = s.now()
	}
	s.mu.Lock()
	s.cache[agent] = p
	delete(s.inflight, agent)
	s.mu.Unlock()
	close(done)
}

// Snapshot reads every source.
func (s *Service) Snapshot(ctx context.Context) []Provider {
	out := make([]Provider, len(s.order))
	var wg sync.WaitGroup
	for i, agent := range s.order {
		wg.Add(1)
		go func(i int, agent string) {
			defer wg.Done()
			out[i] = s.Get(ctx, agent)
		}(i, agent)
	}
	wg.Wait()
	return out
}

func clampPercent(p float64) float64 {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	}
	return p
}
