# PR Review Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Create the first NATS consumer — a review worker that subscribes to `heimdallm.pr.review`, fetches the full PR from GitHub, calls the existing review pipeline, and pushes to WatchQueue.

**Architecture:** New `daemon/internal/worker/` package with `ReviewWorker` that handles NATS consumer plumbing. Business logic is injected via a handler closure from main.go that wraps the existing `runReview` function. A new `GetPR` method on the GitHub client provides full PR data from the API.

**Tech Stack:** Go, NATS JetStream (embedded), existing pipeline/github packages

**Spec:** `docs/superpowers/specs/2026-04-23-pr-review-worker-design.md`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `daemon/internal/github/client.go` | Add `GetPR(repo, number) (*PullRequest, error)` |
| Create | `daemon/internal/worker/review.go` | ReviewWorker — NATS consumer with handler callback |
| Create | `daemon/internal/worker/review_test.go` | ReviewWorker test with embedded NATS + mock handler |
| Modify | `daemon/cmd/heimdallm/main.go` | Handler closure + ReviewWorker startup |

---

### Task 1: GetPR method on GitHub client

**Files:**
- Modify: `daemon/internal/github/client.go`

- [ ] **Step 1: Add GetPR method**

In `daemon/internal/github/client.go`, after the `GetPRSnapshot` method (around line 442), add:

```go
// GetPR returns the full PullRequest struct for a single PR via the Pulls API.
// Used by the NATS review worker to hydrate a PRReviewMsg into a full PR.
func (c *Client) GetPR(repo string, number int) (*PullRequest, error) {
	path := fmt.Sprintf("/repos/%s/pulls/%d", repo, number)
	resp, err := c.do("GET", path, "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: get PR: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK {
		errBody := safeTruncate(string(body), maxErrBodyLen)
		return nil, fmt.Errorf("github: get PR (%s #%d): status %d: %s", repo, number, resp.StatusCode, errBody)
	}
	var pr PullRequest
	if err := json.Unmarshal(body, &pr); err != nil {
		return nil, fmt.Errorf("github: get PR: unmarshal: %w", err)
	}
	// Populate the Repo field the same way FetchPRs does.
	if pr.Head.Repo.FullName != "" {
		pr.Repo = pr.Head.Repo.FullName
	} else {
		pr.Repo = repo
	}
	return &pr, nil
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd daemon
go build ./internal/github/
```

Expected: Clean build.

- [ ] **Step 3: Commit**

```bash
cd daemon
git add internal/github/client.go
git commit -m "feat(github): add GetPR method for full PR fetch (#304)"
```

---

### Task 2: ReviewWorker

**Files:**
- Create: `daemon/internal/worker/review.go`
- Create: `daemon/internal/worker/review_test.go`

- [ ] **Step 1: Create review.go**

```go
// daemon/internal/worker/review.go
package worker

import (
	"context"
	"log/slog"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// ReviewWorker consumes PR review requests from NATS and delegates
// to a handler function that runs the actual review pipeline.
type ReviewWorker struct {
	js      jetstream.JetStream
	handler func(ctx context.Context, msg bus.PRReviewMsg)
}

// NewReviewWorker creates a worker that consumes from the review-worker
// durable consumer. The handler is called for each message and should
// contain the full review logic (fetch PR, run pipeline, push to watch queue).
func NewReviewWorker(js jetstream.JetStream, handler func(context.Context, bus.PRReviewMsg)) *ReviewWorker {
	return &ReviewWorker{js: js, handler: handler}
}

// Start begins consuming from the NATS review-worker consumer.
// Blocks until ctx is cancelled. Always acks messages — errors are
// logged inside the handler, not retried via NATS redelivery.
func (w *ReviewWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		return err
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}

	// Stop the iterator when context is cancelled so iter.Next() unblocks.
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			// Context cancelled or iterator stopped — clean exit.
			return nil
		}

		var prMsg bus.PRReviewMsg
		if err := bus.Decode(msg.Data(), &prMsg); err != nil {
			slog.Error("review-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Info("review-worker: processing",
			"repo", prMsg.Repo, "pr", prMsg.Number, "github_id", prMsg.GithubID)

		w.handler(ctx, prMsg)
		msg.Ack()
	}
}
```

- [ ] **Step 2: Create review_test.go**

```go
// daemon/internal/worker/review_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
	"github.com/nats-io/nats.go/jetstream"
)

func newTestBus(t *testing.T) *bus.Bus {
	t.Helper()
	dir := t.TempDir()
	b := bus.New(bus.Config{DataDir: dir, MaxConcurrentWorkers: 3})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("bus start: %v", err)
	}
	t.Cleanup(b.Stop)
	return b
}

func TestReviewWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		received []bus.PRReviewMsg
	)
	handler := func(_ context.Context, msg bus.PRReviewMsg) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg)
	}

	w := worker.NewReviewWorker(b.JetStream(), handler)

	// Start worker in background
	go func() {
		if err := w.Start(ctx); err != nil {
			t.Errorf("worker start: %v", err)
		}
	}()

	// Give the worker time to connect to the consumer
	time.Sleep(200 * time.Millisecond)

	// Publish a PR review message
	pub := bus.NewPRReviewPublisher(b.JetStream())
	if err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123"); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// Wait for handler to be called
	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 message handled, got %d", len(received))
	}
	msg := received[0]
	if msg.Repo != "org/repo" || msg.Number != 42 || msg.GithubID != 12345 || msg.HeadSHA != "abc123" {
		t.Errorf("unexpected message: %+v", msg)
	}

	// Verify message was acked (0 pending)
	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending > 0 {
		t.Errorf("expected 0 ack-pending, got %d", info.NumAckPending)
	}
}

func TestReviewWorker_AcksOnHandlerError(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	called := make(chan struct{}, 1)
	handler := func(_ context.Context, _ bus.PRReviewMsg) {
		// Simulate handler that panics/errors — but we don't panic,
		// the handler just returns. The worker should still ack.
		called <- struct{}{}
	}

	w := worker.NewReviewWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	// Publish
	data, _ := bus.Encode(bus.PRReviewMsg{Repo: "a/b", Number: 1, GithubID: 1, HeadSHA: "s1"})
	_, err := b.JetStream().Publish(ctx, bus.SubjPRReview, data, jetstream.WithMsgID("1:s1"))
	if err != nil {
		t.Fatalf("publish: %v", err)
	}

	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not called within timeout")
	}

	// Small delay for ack to propagate
	time.Sleep(200 * time.Millisecond)
	cancel()

	cons, err := b.JetStream().Consumer(context.Background(), bus.StreamWork, bus.ConsumerReview)
	if err != nil {
		t.Fatalf("get consumer: %v", err)
	}
	info, err := cons.Info(context.Background())
	if err != nil {
		t.Fatalf("consumer info: %v", err)
	}
	if info.NumAckPending > 0 {
		t.Errorf("message not acked after handler returned: ack-pending=%d", info.NumAckPending)
	}
}
```

- [ ] **Step 3: Verify it compiles and tests pass**

```bash
cd daemon
go test ./internal/worker/ -v -count=1
```

Expected: Both tests PASS.

- [ ] **Step 4: Commit**

```bash
cd daemon
git add internal/worker/review.go internal/worker/review_test.go
git commit -m "feat(worker): add ReviewWorker NATS consumer (#304)"
```

---

### Task 3: Wire ReviewWorker into main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Add worker import**

In `daemon/cmd/heimdallm/main.go`, add to the import block:

```go
"github.com/heimdallm/daemon/internal/worker"
```

- [ ] **Step 2: Add handler closure and worker startup**

In main.go, after the `pipe.Start(context.Background(), true)` call (around line 490) and before the defer that stops the pipeline (around line 495), add:

```go
	// ── NATS PR review worker ───────────────────────────────────────────
	// Consumes PR review requests published by Tier 2 and runs the
	// existing review pipeline. This replaces the goroutine-per-PR
	// pattern that Tier 2 used to use.
	reviewHandler := func(ctx context.Context, msg bus.PRReviewMsg) {
		pr, err := ghClient.GetPR(msg.Repo, msg.Number)
		if err != nil {
			slog.Error("review-worker: fetch PR from GitHub",
				"repo", msg.Repo, "pr", msg.Number, "err", err)
			return
		}
		// Stale message guard: if HEAD SHA changed since publish, skip.
		// The next poll cycle will publish a new message with the updated SHA.
		if msg.HeadSHA != "" && pr.Head.SHA != msg.HeadSHA {
			slog.Info("review-worker: stale message (HEAD SHA changed), skipping",
				"repo", msg.Repo, "pr", msg.Number,
				"msg_sha", msg.HeadSHA, "current_sha", pr.Head.SHA)
			return
		}

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(pr.Repo)
		localDirBase := c.GitHub.LocalDirBase
		cfgMu.Unlock()
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, pr.Repo, localDirBase)

		runReview(pr, aiCfg)

		// Maintain Tier 3 watching (Task 9 replaces WatchQueue entirely).
		pipe.Queue().Push(&scheduler.WatchItem{
			Type: "pr", Repo: pr.Repo, Number: pr.Number, GithubID: pr.ID,
		})
	}

	reviewWorker := worker.NewReviewWorker(eventBus.JetStream(), reviewHandler)
	go func() {
		if err := reviewWorker.Start(context.Background()); err != nil {
			slog.Error("review worker stopped", "err", err)
		}
	}()
```

**IMPORTANT:** The `pipe` variable is captured by the closure. On config reload, `pipe` is reassigned to a new pipeline. The closure always reads the current `pipe`, which is correct because the WatchQueue belongs to the active pipeline.

However, `pipe` is reassigned under `cfgMu` during reload. The `pipe.Queue().Push(...)` call should also be under `cfgMu` to avoid a race. Update the handler to:

```go
		// Maintain Tier 3 watching (Task 9 replaces WatchQueue entirely).
		cfgMu.Lock()
		currentPipe := pipe
		cfgMu.Unlock()
		currentPipe.Queue().Push(&scheduler.WatchItem{
			Type: "pr", Repo: pr.Repo, Number: pr.Number, GithubID: pr.ID,
		})
```

Wait — `pipe` is NOT protected by `cfgMu` in the existing code. Let me check:

Actually, looking at the existing code, `pipe` is a local variable in `main()` that is reassigned during reload. The reload path does:
```go
cfgMu.Lock()
oldPipe := pipe
cfgMu.Unlock()
oldPipe.Stop()
pipe = buildPipeline(cfg)
pipe.Start(context.Background(), false)
```

So `pipe` assignment IS protected by `cfgMu` in the reload path. For the handler, reading `pipe` should also be under `cfgMu`. But this is a pre-existing pattern (the defer closure for shutdown also reads `pipe` without cfgMu). For consistency and safety, use:

```go
		cfgMu.Lock()
		q := pipe.Queue()
		cfgMu.Unlock()
		q.Push(&scheduler.WatchItem{
			Type: "pr", Repo: pr.Repo, Number: pr.Number, GithubID: pr.ID,
		})
```

- [ ] **Step 3: Verify build**

```bash
cd daemon
go build ./cmd/heimdallm/
```

Expected: Clean build.

- [ ] **Step 4: Run full test suite**

```bash
cd daemon
go test ./... -count=1
```

Expected: All tests pass.

- [ ] **Step 5: Commit**

```bash
cd daemon
git add cmd/heimdallm/main.go
git commit -m "feat: wire PR review NATS worker into daemon startup (#304)"
```

---

### Task 4: Final validation

- [ ] **Step 1: Run affected packages with race detector**

```bash
cd daemon
go test ./internal/worker/ ./internal/bus/ ./internal/github/ ./cmd/heimdallm/ -race -count=1
```

Expected: All pass.

- [ ] **Step 2: Build binary**

```bash
cd daemon
go build -o bin/heimdallm ./cmd/heimdallm/
```

- [ ] **Step 3: Smoke test**

```bash
cd daemon
HEIMDALLM_DATA_DIR=$(mktemp -d) HEIMDALLM_AI_PRIMARY=claude-code timeout 8 ./bin/heimdallm 2>&1 | head -20
```

Expected: Daemon starts. Logs should show:
1. `"bus: NATS started"` — NATS embeds
2. `"tier1: discovery complete"` — Tier 1 publishes to NATS
3. `"tier2: received repo list"` — bridge forwards to Tier 2
4. `"review-worker: processing"` — worker consumes PR messages
5. Review pipeline runs as before

This is the first time the full NATS loop works end-to-end for PRs: publish → NATS → worker → pipeline.Run.

- [ ] **Step 4: Commit if adjustments needed**

Skip if no changes.
