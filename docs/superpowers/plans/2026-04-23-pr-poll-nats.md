# PR Poll → NATS Publisher Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the unbounded goroutine-per-PR pattern in Tier 2 with a NATS JetStream publish, with dedup via `Nats-Msg-Id`.

**Architecture:** New `Tier2PRPublisher` interface in scheduler, implemented by `PRReviewPublisher` in bus/publisher.go. Tier 2's `processTick` publishes to NATS instead of spawning goroutines. Task 5 (consumer) will handle the actual review execution.

**Tech Stack:** Go, NATS JetStream (embedded, `daemon/internal/bus/`)

**Spec:** `docs/superpowers/specs/2026-04-23-pr-poll-nats-design.md`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `daemon/internal/bus/publisher.go` | Add PRReviewPublisher |
| Modify | `daemon/internal/bus/publisher_test.go` | Add PRReviewPublisher round-trip + dedup test |
| Modify | `daemon/internal/scheduler/tier2.go` | Add Tier2PRPublisher interface/field, replace goroutine with publish |
| Create | `daemon/internal/scheduler/tier2_test.go` | Tier 2 unit test with mock publisher |
| Modify | `daemon/internal/scheduler/pipeline.go` | Add PRPublisher to PipelineDeps |
| Modify | `daemon/cmd/heimdallm/main.go` | Wire PRPublisher |

---

### Task 1: PRReviewPublisher

**Files:**
- Modify: `daemon/internal/bus/publisher.go`
- Modify: `daemon/internal/bus/publisher_test.go`

- [ ] **Step 1: Write the failing test**

Append to `daemon/internal/bus/publisher_test.go`:

```go
func TestPRReviewPublisher_Publish(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewPRReviewPublisher(b.JetStream())

	err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123")
	if err != nil {
		t.Fatalf("PublishPRReview: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.PRReviewMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Repo != "org/repo" || got.Number != 42 || got.GithubID != 12345 || got.HeadSHA != "abc123" {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestPRReviewPublisher_Dedup(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewPRReviewPublisher(b.JetStream())

	// Publish same PR+SHA twice
	if err := pub.PublishPRReview(ctx, "org/repo", 1, 100, "sha1"); err != nil {
		t.Fatalf("publish 1: %v", err)
	}
	if err := pub.PublishPRReview(ctx, "org/repo", 1, 100, "sha1"); err != nil {
		t.Fatalf("publish 2: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(2, jetstream.FetchMaxWait(1*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	count := 0
	for m := range msgs.Messages() {
		count++
		m.Ack()
	}
	if count != 1 {
		t.Errorf("expected 1 (dedup), got %d", count)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd daemon
go test ./internal/bus/ -run "TestPRReviewPublisher" -v
```

Expected: FAIL — `bus.NewPRReviewPublisher` undefined.

- [ ] **Step 3: Add PRReviewPublisher to publisher.go**

Append to `daemon/internal/bus/publisher.go`:

```go
// PRReviewPublisher publishes PR review requests to NATS JetStream.
// Implements scheduler.Tier2PRPublisher.
type PRReviewPublisher struct {
	js jetstream.JetStream
}

// NewPRReviewPublisher creates a publisher that writes to SubjPRReview.
func NewPRReviewPublisher(js jetstream.JetStream) *PRReviewPublisher {
	return &PRReviewPublisher{js: js}
}

// PublishPRReview publishes a single PR review request with dedup via Nats-Msg-Id.
func (p *PRReviewPublisher) PublishPRReview(ctx context.Context, repo string, number int, githubID int64, headSHA string) error {
	data, err := Encode(PRReviewMsg{
		Repo:     repo,
		Number:   number,
		GithubID: githubID,
		HeadSHA:  headSHA,
	})
	if err != nil {
		return fmt.Errorf("bus: encode pr review: %w", err)
	}
	msgID := fmt.Sprintf("%d:%s", githubID, headSHA)
	_, err = p.js.Publish(ctx, SubjPRReview, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish pr review: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd daemon
go test ./internal/bus/ -run "TestPRReviewPublisher" -v
```

Expected: Both tests PASS.

- [ ] **Step 5: Run full bus test suite**

```bash
cd daemon
go test ./internal/bus/ -v -count=1
```

Expected: All tests pass (18 existing + 2 new = 20).

- [ ] **Step 6: Commit**

```bash
cd daemon
git add internal/bus/publisher.go internal/bus/publisher_test.go
git commit -m "feat(bus): add PRReviewPublisher with dedup via Nats-Msg-Id (#303)"
```

---

### Task 2: Tier2PRPublisher interface and refactor processTick

**Files:**
- Modify: `daemon/internal/scheduler/tier2.go`
- Create: `daemon/internal/scheduler/tier2_test.go`
- Modify: `daemon/internal/scheduler/pipeline.go`

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/scheduler/tier2_test.go`:

```go
package scheduler_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/scheduler"
)

// mockPRPublisher records PublishPRReview calls.
type mockPRPublisher struct {
	mu    sync.Mutex
	calls []publishedPR
}

type publishedPR struct {
	Repo     string
	Number   int
	GithubID int64
	HeadSHA  string
}

func (m *mockPRPublisher) PublishPRReview(_ context.Context, repo string, number int, githubID int64, headSHA string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, publishedPR{Repo: repo, Number: number, GithubID: githubID, HeadSHA: headSHA})
	return nil
}

func (m *mockPRPublisher) getCalls() []publishedPR {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]publishedPR, len(m.calls))
	copy(cp, m.calls)
	return cp
}

// mockPRFetcher returns a fixed list of PRs.
type mockPRFetcher struct {
	prs []scheduler.Tier2PR
}

func (m *mockPRFetcher) FetchPRsToReview() ([]scheduler.Tier2PR, error) {
	return m.prs, nil
}

// mockStore controls which PRs are "already reviewed".
type mockStore struct {
	reviewed map[int64]bool
}

func (m *mockStore) PRAlreadyReviewed(githubID int64, _ time.Time) bool {
	return m.reviewed[githubID]
}

func TestRunTier2_PublishesPRsToNATS(t *testing.T) {
	prPub := &mockPRPublisher{}
	fetcher := &mockPRFetcher{prs: []scheduler.Tier2PR{
		{ID: 1, Number: 10, Repo: "org/repo1", HeadSHA: "sha1", UpdatedAt: time.Now()},
		{ID: 2, Number: 20, Repo: "org/repo2", HeadSHA: "sha2", UpdatedAt: time.Now()},
		{ID: 3, Number: 30, Repo: "org/other", HeadSHA: "sha3", UpdatedAt: time.Now()},
	}}
	store := &mockStore{reviewed: map[int64]bool{2: true}} // PR 2 already reviewed

	reposChan := make(chan []string, 1)
	reposChan <- []string{"org/repo1", "org/repo2"} // org/other not monitored

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go scheduler.RunTier2(ctx, scheduler.Tier2Deps{
		Limiter:        scheduler.NewRateLimiter(100),
		WatchQueue:     scheduler.NewWatchQueue(),
		PRFetcher:      fetcher,
		PRProcessor:    &noopPRProcessor{},
		PRPublisher:    prPub,
		IssueProcessor: &noopIssueProcessor{},
		Store:          store,
		ConfigFn:       func() []string { return nil },
		Interval:       50 * time.Millisecond,
	}, reposChan, true)

	// Wait for the cold-start processTick to run
	time.Sleep(500 * time.Millisecond)
	cancel()

	calls := prPub.getCalls()

	// Only PR 1 should be published:
	//   PR 2 is already reviewed (skipped by store)
	//   PR 3 is not in monitored repos (skipped by monitoredSet)
	if len(calls) != 1 {
		t.Fatalf("expected 1 published PR, got %d: %+v", len(calls), calls)
	}
	if calls[0].GithubID != 1 || calls[0].HeadSHA != "sha1" {
		t.Errorf("unexpected PR published: %+v", calls[0])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd daemon
go test ./internal/scheduler/ -run TestRunTier2_PublishesPRs -v
```

Expected: FAIL — `scheduler.Tier2Deps` has no field `PRPublisher`.

- [ ] **Step 3: Add Tier2PRPublisher interface and field to tier2.go**

In `daemon/internal/scheduler/tier2.go`, add the interface after the existing `Tier2Store` interface (around line 62):

```go
// Tier2PRPublisher publishes PR review requests to NATS.
type Tier2PRPublisher interface {
	PublishPRReview(ctx context.Context, repo string, number int, githubID int64, headSHA string) error
}
```

Add `PRPublisher` field to `Tier2Deps` (around line 65):

```go
type Tier2Deps struct {
	Limiter        *RateLimiter
	WatchQueue     *WatchQueue
	PRFetcher      Tier2PRFetcher
	PRProcessor    Tier2PRProcessor
	PRPublisher    Tier2PRPublisher
	IssueProcessor Tier2IssueProcessor
	Promoter       Tier2Promoter
	Store          Tier2Store
	ConfigFn       func() []string // returns monitored repos for PR filtering
	Interval       time.Duration
}
```

- [ ] **Step 4: Replace goroutine spawn with NATS publish in processTick**

In `daemon/internal/scheduler/tier2.go`, in the `processTick` closure, replace the PR processing block. Change from:

```go
				for _, pr := range prs {
					if _, ok := monitoredSet[pr.Repo]; !ok {
						continue
					}
					if deps.Store.PRAlreadyReviewed(pr.ID, pr.UpdatedAt) {
						continue
					}
					go func(p Tier2PR) {
						if err := deps.PRProcessor.ProcessPR(ctx, p); err != nil {
							slog.Error("tier2: PR pipeline", "repo", p.Repo, "pr", p.Number, "err", err)
						}
						// Enqueue to Tier 3 watch
						deps.WatchQueue.Push(&WatchItem{
							Type: "pr", Repo: p.Repo, Number: p.Number, GithubID: p.ID,
						})
					}(pr)
				}
```

To:

```go
				for _, pr := range prs {
					if _, ok := monitoredSet[pr.Repo]; !ok {
						continue
					}
					if deps.Store.PRAlreadyReviewed(pr.ID, pr.UpdatedAt) {
						continue
					}
					if err := deps.PRPublisher.PublishPRReview(ctx, pr.Repo, pr.Number, pr.ID, pr.HeadSHA); err != nil {
						slog.Error("tier2: publish PR review", "repo", pr.Repo, "pr", pr.Number, "err", err)
					}
				}
```

- [ ] **Step 5: Run tier2 test**

```bash
cd daemon
go test ./internal/scheduler/ -run TestRunTier2_PublishesPRs -v
```

Expected: PASS.

- [ ] **Step 6: Add PRPublisher to PipelineDeps in pipeline.go**

In `daemon/internal/scheduler/pipeline.go`, add `PRPublisher` to the `PipelineDeps` struct. After the `JS` field:

```go
	// Tier 2
	PRFetcher      Tier2PRFetcher
	PRProcessor    Tier2PRProcessor
	PRPublisher    Tier2PRPublisher  // publishes PR review requests to NATS
	IssueProcessor Tier2IssueProcessor
```

And in `Start()`, update the `Tier2Deps` construction in the RunTier2 goroutine to include it. Add after `PRProcessor`:

```go
			PRPublisher:    p.deps.PRPublisher,
```

- [ ] **Step 7: Verify full scheduler build + tests**

```bash
cd daemon
go build ./internal/scheduler/
go test ./internal/scheduler/ -v -count=1
```

Expected: Build clean, all tests pass.

- [ ] **Step 8: Commit**

```bash
cd daemon
git add internal/scheduler/tier2.go internal/scheduler/tier2_test.go internal/scheduler/pipeline.go
git commit -m "feat(scheduler): replace goroutine spawn with NATS publish in Tier 2 (#303)"
```

---

### Task 3: Wire into main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Add PRPublisher to PipelineDeps in main.go**

In `daemon/cmd/heimdallm/main.go`, find the `scheduler.PipelineDeps{` struct literal (around line 456). After the `JS:` line, add:

```go
			PRPublisher:    bus.NewPRReviewPublisher(eventBus.JetStream()),
```

The section should look like:

```go
			Publisher:      bus.NewRepoPublisher(eventBus.JetStream()),
			JS:             eventBus.JetStream(),
			PRPublisher:    bus.NewPRReviewPublisher(eventBus.JetStream()),
			PRFetcher:      adapter,
```

- [ ] **Step 2: Verify build**

```bash
cd daemon
go build ./cmd/heimdallm/
```

Expected: Clean build.

- [ ] **Step 3: Run full test suite**

```bash
cd daemon
go test ./... -count=1
```

Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
cd daemon
git add cmd/heimdallm/main.go
git commit -m "feat: wire PR review NATS publisher into daemon (#303)"
```

---

### Task 4: Final validation

- [ ] **Step 1: Run affected packages with race detector**

```bash
cd daemon
go test ./internal/bus/ ./internal/scheduler/ ./cmd/heimdallm/ -race -count=1
```

Expected: All pass (except pre-existing tier3 race).

- [ ] **Step 2: Build the binary**

```bash
cd daemon
go build -o bin/heimdallm ./cmd/heimdallm/
```

- [ ] **Step 3: Smoke test**

```bash
cd daemon
HEIMDALLM_DATA_DIR=$(mktemp -d) HEIMDALLM_AI_PRIMARY=claude-code timeout 5 ./bin/heimdallm 2>&1 | head -15
```

Expected: Daemon starts, NATS started, Tier 1 discovers repos, Tier 2 receives repo list. PRs that pass filters should be published to NATS (visible in logs if a review-eligible PR exists). Since there is no consumer yet (Task 5), the messages sit in the HEIMDALLM_WORK stream — this is expected.

- [ ] **Step 4: Commit if adjustments needed**

Skip if no changes.
