# Discovery Poller → NATS Publisher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor Tier 1 discovery to publish the merged repo list to NATS JetStream instead of a Go channel, with a bridge consumer feeding the existing Tier 2.

**Architecture:** Tier 1 publishes `DiscoveryMsg` to `heimdallm.discovery.repos` via a `Tier1Publisher` interface. A bridge goroutine in `Pipeline.Start()` consumes from the NATS `discovery-consumer` and forwards to `reposChan` so Tier 2 remains unchanged. Task 4 will remove the bridge.

**Tech Stack:** Go, NATS JetStream (embedded, already in `daemon/internal/bus/`)

**Spec:** `docs/superpowers/specs/2026-04-23-discovery-poller-nats-design.md`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Create | `daemon/internal/bus/publisher.go` | RepoPublisher — NATS implementation of Tier1Publisher |
| Create | `daemon/internal/bus/publisher_test.go` | RepoPublisher publish + consume round-trip test |
| Modify | `daemon/internal/scheduler/tier1.go` | Replace `ReposChan` with `Publisher` interface in Tier1Deps |
| Modify | `daemon/internal/scheduler/pipeline.go` | Add `Publisher` + `JS` to PipelineDeps, add `bridgeDiscovery`, wire in Start |
| Create | `daemon/internal/scheduler/tier1_test.go` | Tier 1 unit tests with mock Publisher |
| Create | `daemon/internal/scheduler/bridge_test.go` | Bridge consumer test: NATS → reposChan |
| Modify | `daemon/cmd/heimdallm/main.go` | Wire Publisher and JS into PipelineDeps |

---

### Task 1: RepoPublisher

**Files:**
- Create: `daemon/internal/bus/publisher.go`
- Create: `daemon/internal/bus/publisher_test.go`

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/bus/publisher_test.go`:

```go
package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

func TestRepoPublisher_PublishRepos(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewRepoPublisher(b.JetStream())
	repos := []string{"org/repo1", "org/repo2", "org/repo3"}

	if err := pub.PublishRepos(ctx, repos); err != nil {
		t.Fatalf("PublishRepos: %v", err)
	}

	// Consume from the discovery-consumer
	cons, err := b.JetStream().Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.DiscoveryMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}

	if len(got.Repos) != 3 {
		t.Fatalf("expected 3 repos, got %d", len(got.Repos))
	}
	if got.Repos[0] != "org/repo1" || got.Repos[1] != "org/repo2" || got.Repos[2] != "org/repo3" {
		t.Errorf("unexpected repos: %v", got.Repos)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/bus/ -run TestRepoPublisher -v
```

Expected: FAIL — `bus.NewRepoPublisher` undefined.

- [ ] **Step 3: Create publisher.go**

Create `daemon/internal/bus/publisher.go`:

```go
package bus

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// RepoPublisher publishes discovered repo lists to NATS JetStream.
// Implements scheduler.Tier1Publisher.
type RepoPublisher struct {
	js jetstream.JetStream
}

// NewRepoPublisher creates a publisher that writes to SubjDiscoveryRepos.
func NewRepoPublisher(js jetstream.JetStream) *RepoPublisher {
	return &RepoPublisher{js: js}
}

// PublishRepos serializes the repo list and publishes it to the discovery subject.
func (p *RepoPublisher) PublishRepos(ctx context.Context, repos []string) error {
	data, err := Encode(DiscoveryMsg{Repos: repos})
	if err != nil {
		return fmt.Errorf("bus: encode discovery: %w", err)
	}
	_, err = p.js.Publish(ctx, SubjDiscoveryRepos, data)
	if err != nil {
		return fmt.Errorf("bus: publish discovery: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/bus/ -run TestRepoPublisher -v
```

Expected: PASS.

- [ ] **Step 5: Run full bus test suite**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/bus/ -v -count=1
```

Expected: All 18 tests pass (17 existing + 1 new).

- [ ] **Step 6: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
git add internal/bus/publisher.go internal/bus/publisher_test.go
git commit -m "feat(bus): add RepoPublisher for discovery subject (#302)"
```

---

### Task 2: Tier1Publisher interface and refactor Tier 1

**Files:**
- Modify: `daemon/internal/scheduler/tier1.go`
- Create: `daemon/internal/scheduler/tier1_test.go`

- [ ] **Step 1: Write the failing test for Tier 1 with mock publisher**

Create `daemon/internal/scheduler/tier1_test.go`:

```go
package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

// mockPublisher records calls to PublishRepos.
type mockPublisher struct {
	mu    sync.Mutex
	calls [][]string
}

func (m *mockPublisher) PublishRepos(_ context.Context, repos []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(repos))
	copy(cp, repos)
	m.calls = append(m.calls, cp)
	return nil
}

func (m *mockPublisher) getCalls() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// fakeDiscovery returns a fixed list of discovered repos.
type fakeDiscovery struct {
	repos []string
}

func (f *fakeDiscovery) Discovered() []string {
	return f.repos
}

func TestRunTier1_PublishesRepos(t *testing.T) {
	pub := &mockPublisher{}
	disc := &fakeDiscovery{repos: []string{"org/discovered"}}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	go scheduler.RunTier1(ctx, scheduler.Tier1Deps{
		Discovery: disc,
		Limiter:   scheduler.NewRateLimiter(100),
		Publisher: pub,
		ConfigFn: func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos: []string{"org/static"},
			}
		},
		Interval: 50 * time.Millisecond,
	})

	// Wait for at least the initial publish
	time.Sleep(100 * time.Millisecond)
	cancel()

	calls := pub.getCalls()
	if len(calls) == 0 {
		t.Fatal("PublishRepos never called")
	}

	// First call should contain both static + discovered
	first := calls[0]
	if len(first) != 2 {
		t.Fatalf("expected 2 repos, got %d: %v", len(first), first)
	}
	has := map[string]bool{}
	for _, r := range first {
		has[r] = true
	}
	if !has["org/static"] || !has["org/discovered"] {
		t.Errorf("expected org/static and org/discovered, got %v", first)
	}
}

func TestRunTier1_ExcludesNonMonitored(t *testing.T) {
	pub := &mockPublisher{}
	disc := &fakeDiscovery{repos: []string{"org/discovered"}}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go scheduler.RunTier1(ctx, scheduler.Tier1Deps{
		Discovery: disc,
		Limiter:   scheduler.NewRateLimiter(100),
		Publisher: pub,
		ConfigFn: func() scheduler.Tier1Config {
			return scheduler.Tier1Config{
				StaticRepos:  []string{"org/static", "org/skip"},
				NonMonitored: []string{"org/skip"},
			}
		},
		Interval: 50 * time.Millisecond,
	})

	time.Sleep(100 * time.Millisecond)
	cancel()

	calls := pub.getCalls()
	if len(calls) == 0 {
		t.Fatal("PublishRepos never called")
	}
	for _, repo := range calls[0] {
		if repo == "org/skip" {
			t.Error("non-monitored repo org/skip was included")
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/scheduler/ -run TestRunTier1 -v
```

Expected: FAIL — `scheduler.Tier1Deps` has no field `Publisher`.

- [ ] **Step 3: Refactor tier1.go — replace ReposChan with Publisher**

Replace the entire content of `daemon/internal/scheduler/tier1.go`:

```go
package scheduler

import (
	"context"
	"log/slog"
	"time"
)

// Tier1Discovery is the interface the discovery tier needs.
type Tier1Discovery interface {
	Discovered() []string
}

// Tier1Publisher publishes the discovered repo list.
type Tier1Publisher interface {
	PublishRepos(ctx context.Context, repos []string) error
}

// Tier1Config provides the repo lists for merging.
type Tier1Config struct {
	StaticRepos    []string
	NonMonitored   []string
	DiscoveryTopic string
	DiscoveryOrgs  []string
}

// Tier1Deps holds all dependencies for the discovery tier.
type Tier1Deps struct {
	Discovery Tier1Discovery
	Limiter   *RateLimiter
	Publisher Tier1Publisher
	ConfigFn  func() Tier1Config
	Interval  time.Duration
}

// RunTier1 runs the discovery tier. It periodically merges static repos
// with discovered repos and publishes the full list to NATS.
func RunTier1(ctx context.Context, deps Tier1Deps) {
	// Publish initial repos immediately
	sendRepos(ctx, deps)

	ticker := time.NewTicker(deps.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := deps.Limiter.Acquire(ctx, TierDiscovery); err != nil {
				return
			}
			sendRepos(ctx, deps)
		}
	}
}

func sendRepos(ctx context.Context, deps Tier1Deps) {
	cfg := deps.ConfigFn()
	discovered := deps.Discovery.Discovered()

	// Merge static + discovered, exclude non-monitored
	nonMon := make(map[string]struct{}, len(cfg.NonMonitored))
	for _, r := range cfg.NonMonitored {
		nonMon[r] = struct{}{}
	}
	seen := make(map[string]struct{})
	var repos []string
	for _, r := range cfg.StaticRepos {
		if _, skip := nonMon[r]; skip {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		repos = append(repos, r)
	}
	for _, r := range discovered {
		if _, skip := nonMon[r]; skip {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		repos = append(repos, r)
	}

	slog.Info("tier1: discovery complete", "repos", len(repos))
	if err := deps.Publisher.PublishRepos(ctx, repos); err != nil {
		slog.Error("tier1: publish repos failed", "err", err)
	}
}
```

- [ ] **Step 4: Run tier1 tests**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/scheduler/ -run TestRunTier1 -v
```

Expected: Both tests PASS.

- [ ] **Step 5: Verify pipeline.go still compiles**

The `pipeline.go` still references `ReposChan` in `Tier1Deps` — it will fail to compile now. We need to update it in the next task. For now, verify tier1 tests pass in isolation:

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/scheduler/ -run "TestRunTier1|TestScheduler|TestQueue|TestRate" -v
```

Expected: All scheduler tests that don't depend on pipeline.go pass.

- [ ] **Step 6: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
git add internal/scheduler/tier1.go internal/scheduler/tier1_test.go
git commit -m "feat(scheduler): replace ReposChan with Tier1Publisher interface (#302)"
```

---

### Task 3: Bridge consumer and PipelineDeps update

**Files:**
- Modify: `daemon/internal/scheduler/pipeline.go`
- Create: `daemon/internal/scheduler/bridge_test.go`

- [ ] **Step 1: Update PipelineDeps and Pipeline.Start in pipeline.go**

Replace the entire content of `daemon/internal/scheduler/pipeline.go`:

```go
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// PipelineConfig holds the interval configuration for each tier.
type PipelineConfig struct {
	DiscoveryInterval time.Duration // Tier 1 (default 15m)
	PollInterval      time.Duration // Tier 2 (default 5m)
	WatchInterval     time.Duration // Tier 3 base (default 1m)
	RateLimitPerHour  int           // total API budget (default 4500)
}

// PipelineDeps bundles all external dependencies the pipeline needs.
type PipelineDeps struct {
	// Tier 1
	Discovery     Tier1Discovery
	Tier1ConfigFn func() Tier1Config
	Publisher     Tier1Publisher // publishes discovery results to NATS

	// NATS bridge (interim — Task 4 removes when Tier 2 consumes directly)
	JS jetstream.JetStream

	// Tier 2
	PRFetcher      Tier2PRFetcher
	PRProcessor    Tier2PRProcessor
	IssueProcessor Tier2IssueProcessor
	Promoter       Tier2Promoter
	Store          Tier2Store
	Tier2ConfigFn  func() []string // monitored repos

	// Tier 3
	ItemChecker Tier3ItemChecker
}

// Pipeline orchestrates the 3-tier polling architecture.
type Pipeline struct {
	cfg  PipelineConfig
	deps PipelineDeps

	limiter *RateLimiter
	queue   *WatchQueue

	cancel   context.CancelFunc
	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewPipeline creates a new pipeline. Call Start to begin processing.
func NewPipeline(cfg PipelineConfig, deps PipelineDeps) *Pipeline {
	if cfg.RateLimitPerHour <= 0 {
		cfg.RateLimitPerHour = 4500
	}
	if cfg.DiscoveryInterval <= 0 {
		cfg.DiscoveryInterval = 15 * time.Minute
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Minute
	}
	if cfg.WatchInterval <= 0 {
		cfg.WatchInterval = 1 * time.Minute
	}
	return &Pipeline{
		cfg:     cfg,
		deps:    deps,
		limiter: NewRateLimiter(cfg.RateLimitPerHour),
		queue:   NewWatchQueue(),
	}
}

// Start launches all 3 tiers and the rate limiter refill goroutine.
//
// coldStart controls whether Tier 2 runs its first processTick immediately.
// Pass true on initial daemon startup, false on a pipeline reload triggered
// by config change. See RunTier2 for the rationale — in short, a config
// reload can come from a UI PATCH and firing Tier 2 before backoff state
// settles would amplify any in-flight review loop.
func (p *Pipeline) Start(parentCtx context.Context, coldStart bool) {
	ctx, cancel := context.WithCancel(parentCtx)
	p.cancel = cancel

	reposChan := make(chan []string, 1)

	// Rate limiter hourly refill
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.limiter.Refill()
				slog.Info("pipeline: rate limiter refilled")
			}
		}
	}()

	// Tier 1: Discovery — publishes to NATS
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		RunTier1(ctx, Tier1Deps{
			Discovery: p.deps.Discovery,
			Limiter:   p.limiter,
			Publisher: p.deps.Publisher,
			ConfigFn:  p.deps.Tier1ConfigFn,
			Interval:  p.cfg.DiscoveryInterval,
		})
	}()

	// Bridge: NATS discovery-consumer → reposChan (interim, Task 4 removes)
	if p.deps.JS != nil {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.bridgeDiscovery(ctx, reposChan)
		}()
	}

	// Tier 2: Per-repo
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		RunTier2(ctx, Tier2Deps{
			Limiter:        p.limiter,
			WatchQueue:     p.queue,
			PRFetcher:      p.deps.PRFetcher,
			PRProcessor:    p.deps.PRProcessor,
			IssueProcessor: p.deps.IssueProcessor,
			Promoter:       p.deps.Promoter,
			Store:          p.deps.Store,
			ConfigFn:       p.deps.Tier2ConfigFn,
			Interval:       p.cfg.PollInterval,
		}, reposChan, coldStart)
	}()

	// Tier 3: Per-item watch
	if p.deps.ItemChecker != nil {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			RunTier3(ctx, Tier3Deps{
				Limiter:  p.limiter,
				Queue:    p.queue,
				Checker:  p.deps.ItemChecker,
				Interval: p.cfg.WatchInterval,
			})
		}()
	}

	slog.Info("pipeline: started",
		"discovery", p.cfg.DiscoveryInterval,
		"poll", p.cfg.PollInterval,
		"watch", p.cfg.WatchInterval,
		"rate_limit", p.cfg.RateLimitPerHour)
}

// bridgeDiscovery consumes from the NATS discovery-consumer and forwards
// repo lists to the reposChan that Tier 2 reads. This is a transitional
// bridge — Task 4 will have Tier 2 consume from NATS directly.
func (p *Pipeline) bridgeDiscovery(ctx context.Context, out chan<- []string) {
	cons, err := p.deps.JS.Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		slog.Error("bridge: get discovery consumer", "err", err)
		return
	}
	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		slog.Error("bridge: start message iterator", "err", err)
		return
	}
	defer iter.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := iter.Next()
		if err != nil {
			// iter.Next returns error when context is cancelled or iter is stopped
			return
		}

		var dm bus.DiscoveryMsg
		if err := bus.Decode(msg.Data(), &dm); err != nil {
			slog.Error("bridge: decode discovery msg", "err", err)
			msg.Ack()
			continue
		}

		select {
		case out <- dm.Repos:
		case <-ctx.Done():
			return
		}
		msg.Ack()
	}
}

// Stop cancels all goroutines and waits for them to finish.
// It is idempotent — calling Stop multiple times is safe (e.g. the reload
// path stops the old pipeline, and the deferred shutdown may also call Stop
// if it reads a stale pointer).
func (p *Pipeline) Stop() {
	p.stopOnce.Do(func() {
		if p.cancel != nil {
			p.cancel()
		}
		p.wg.Wait()
		slog.Info("pipeline: stopped")
	})
}

// Queue returns the watch queue for external inspection/testing.
func (p *Pipeline) Queue() *WatchQueue {
	return p.queue
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go build ./internal/scheduler/
```

Expected: Clean build. The import of `bus` and `jetstream` should resolve.

- [ ] **Step 3: Write bridge test**

Create `daemon/internal/scheduler/bridge_test.go`:

```go
package scheduler_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/scheduler"
)

func TestBridgeDiscovery_ForwardsRepos(t *testing.T) {
	// Start a real NATS bus
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 3})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("bus start: %v", err)
	}
	defer b.Stop()

	pub := bus.NewRepoPublisher(b.JetStream())

	// Build a minimal pipeline with the NATS bridge
	pipe := scheduler.NewPipeline(scheduler.PipelineConfig{
		DiscoveryInterval: 1 * time.Hour, // won't tick in this test
		PollInterval:      1 * time.Hour,
		WatchInterval:     1 * time.Hour,
	}, scheduler.PipelineDeps{
		Discovery: &fakeDiscovery{repos: nil},
		Tier1ConfigFn: func() scheduler.Tier1Config {
			return scheduler.Tier1Config{}
		},
		Publisher:      pub,
		JS:             b.JetStream(),
		PRFetcher:      &noopPRFetcher{},
		PRProcessor:    &noopPRProcessor{},
		IssueProcessor: &noopIssueProcessor{},
		Store:          &noopStore{},
		Tier2ConfigFn:  func() []string { return nil },
	})
	pipe.Start(context.Background(), false)
	defer pipe.Stop()

	// Give the bridge goroutine time to start its consumer iterator
	time.Sleep(200 * time.Millisecond)

	// Publish a discovery message to NATS
	if err := pub.PublishRepos(context.Background(), []string{"org/repo1", "org/repo2"}); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// The bridge should forward it to Tier 2 via reposChan.
	// We can't directly observe reposChan, but we can verify the message
	// was consumed (acked) by checking the stream has 0 pending.
	time.Sleep(500 * time.Millisecond)

	ctx := context.Background()
	cons, err := b.JetStream().Consumer(ctx, bus.StreamDiscovery, bus.ConsumerDiscovery)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(ctx)
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumPending > 0 {
		t.Errorf("expected 0 pending messages (bridge should have consumed), got %d", info.NumPending)
	}
	if info.NumAckPending > 0 {
		t.Errorf("expected 0 ack-pending (bridge should have acked), got %d", info.NumAckPending)
	}
}

// ── noop stubs for PipelineDeps (Tier 2 unused in bridge test) ──────────

type noopPRFetcher struct{}

func (n *noopPRFetcher) FetchPRsToReview() ([]scheduler.Tier2PR, error) { return nil, nil }

type noopPRProcessor struct{}

func (n *noopPRProcessor) ProcessPR(_ context.Context, _ scheduler.Tier2PR) error { return nil }
func (n *noopPRProcessor) PublishPending()                                        {}

type noopIssueProcessor struct{}

func (n *noopIssueProcessor) ProcessRepo(_ context.Context, _ string) (int, error) { return 0, nil }

type noopStore struct{}

func (n *noopStore) PRAlreadyReviewed(_ int64, _ time.Time) bool { return true }
```

- [ ] **Step 4: Run bridge test**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/scheduler/ -run TestBridgeDiscovery -v -count=1
```

Expected: PASS.

- [ ] **Step 5: Run full scheduler test suite**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./internal/scheduler/ -v -count=1
```

Expected: All tests pass (existing + new).

- [ ] **Step 6: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
git add internal/scheduler/pipeline.go internal/scheduler/bridge_test.go
git commit -m "feat(scheduler): add NATS bridge consumer in Pipeline.Start (#302)"
```

---

### Task 4: Wire into main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Add bus import if not present**

In `daemon/cmd/heimdallm/main.go`, verify `"github.com/heimdallm/daemon/internal/bus"` is already in the import block (it was added in the bus-embed PR). If not, add it.

- [ ] **Step 2: Add Publisher and JS to PipelineDeps**

In `daemon/cmd/heimdallm/main.go`, find the `buildPipeline` function (around line 450). In the `scheduler.PipelineDeps{` struct literal, after the `Tier1ConfigFn` closure and before `PRFetcher`, add:

```go
			Publisher: bus.NewRepoPublisher(eventBus.JetStream()),
			JS:        eventBus.JetStream(),
```

The full section should look like:

```go
		}, scheduler.PipelineDeps{
			Discovery: discoverySvc,
			Tier1ConfigFn: func() scheduler.Tier1Config {
				// ... existing closure unchanged ...
			},
			Publisher:      bus.NewRepoPublisher(eventBus.JetStream()),
			JS:             eventBus.JetStream(),
			PRFetcher:      adapter,
			PRProcessor:    adapter,
			IssueProcessor: adapter,
			Promoter:       adapter,
			Store:          adapter,
			Tier2ConfigFn: func() []string {
				// ... existing closure unchanged ...
			},
			ItemChecker: adapter,
		})
```

**IMPORTANT:** The `buildPipeline` function is a closure that captures `eventBus` from its outer scope. `eventBus` was wired in the bus-embed PR and is available in scope.

- [ ] **Step 3: Verify build**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go build ./cmd/heimdallm/
```

Expected: Clean build.

- [ ] **Step 4: Run full test suite**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./... -count=1
```

Expected: All tests pass. No regressions.

- [ ] **Step 5: Commit**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
git add cmd/heimdallm/main.go
git commit -m "feat: wire discovery NATS publisher and bridge into daemon (#302)"
```

---

### Task 5: Final validation

- [ ] **Step 1: Run full test suite with race detector**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go test ./... -race -count=1
```

Expected: All tests pass, no races (except the pre-existing race in tier3_test.go which is not ours).

- [ ] **Step 2: Build the binary**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
go build -o bin/heimdallm ./cmd/heimdallm/
```

Expected: Binary builds.

- [ ] **Step 3: Smoke test**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
HEIMDALLM_DATA_DIR=$(mktemp -d) HEIMDALLM_AI_PRIMARY=claude-code timeout 5 ./bin/heimdallm 2>&1 | head -20
```

Expected: Log shows `"bus: NATS started"` followed by `"tier1: discovery complete"`. The discovery flow now goes through NATS → bridge → Tier 2 instead of through a Go channel.

- [ ] **Step 4: Commit if adjustments needed**

```bash
cd /Users/jamuriano/personal-workspace/auto-pr/daemon
git add -A
git commit -m "chore: final adjustments from validation pass (#302)"
```

Skip if no changes needed.
