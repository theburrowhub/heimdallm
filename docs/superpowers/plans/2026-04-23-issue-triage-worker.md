# Issue Triage Worker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor Fetcher to publish classified issues to NATS instead of running the pipeline directly, and create a triage worker consumer for review_only issues.

**Architecture:** Fetcher gets an optional `IssuePublisher`. When set, `review_only` issues publish to `heimdallm.issue.triage` and `develop` issues to `heimdallm.issue.implement`. The TriageWorker consumes from `triage-worker`, fetches the issue from GitHub, resolves config, and calls `issuePipe.Run` with ReviewOnly mode. The implement consumer (Task 8) will handle the other subject.

**Tech Stack:** Go, NATS JetStream (embedded), existing issues pipeline

**Spec:** `docs/superpowers/specs/2026-04-23-issue-triage-worker-design.md`

---

## File Map

| Action | File | Responsibility |
|--------|------|----------------|
| Modify | `daemon/internal/bus/publisher.go` | Add NATSIssuePublisher |
| Modify | `daemon/internal/bus/publisher_test.go` | Tests |
| Modify | `daemon/internal/issues/fetcher.go` | Add publisher field, publish when set |
| Create | `daemon/internal/worker/triage.go` | TriageWorker consumer |
| Create | `daemon/internal/worker/triage_test.go` | Tests |
| Modify | `daemon/cmd/heimdallm/main.go` | Wire publisher into Fetcher, triageHandler, TriageWorker startup |

---

### Task 1: NATSIssuePublisher

**Files:**
- Modify: `daemon/internal/bus/publisher.go`
- Modify: `daemon/internal/bus/publisher_test.go`

- [ ] **Step 1: Write failing tests**

Append to `daemon/internal/bus/publisher_test.go`:

```go
func TestIssuePublisher_Triage(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewIssuePublisher(b.JetStream())
	if err := pub.PublishIssueTriage(ctx, "org/repo", 10, 555); err != nil {
		t.Fatalf("PublishIssueTriage: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerTriage)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.IssueMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Repo != "org/repo" || got.Number != 10 || got.GithubID != 555 {
		t.Errorf("unexpected: %+v", got)
	}
}

func TestIssuePublisher_Implement(t *testing.T) {
	b := newTestBus(t)
	ctx := context.Background()

	pub := bus.NewIssuePublisher(b.JetStream())
	if err := pub.PublishIssueImplement(ctx, "org/repo", 20, 666); err != nil {
		t.Fatalf("PublishIssueImplement: %v", err)
	}

	cons, err := b.JetStream().Consumer(ctx, bus.StreamWork, bus.ConsumerImplement)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(2*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got bus.IssueMsg
	for m := range msgs.Messages() {
		if err := bus.Decode(m.Data(), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		m.Ack()
	}
	if got.Repo != "org/repo" || got.Number != 20 || got.GithubID != 666 {
		t.Errorf("unexpected: %+v", got)
	}
}
```

- [ ] **Step 2: Run to verify fails**

```bash
cd daemon
go test ./internal/bus/ -run "TestIssuePublisher" -v
```

- [ ] **Step 3: Add NATSIssuePublisher to publisher.go**

Append to `daemon/internal/bus/publisher.go`:

```go
// NATSIssuePublisher publishes classified issues to NATS JetStream.
type NATSIssuePublisher struct {
	js jetstream.JetStream
}

// NewIssuePublisher creates a publisher for issue triage and implement subjects.
func NewIssuePublisher(js jetstream.JetStream) *NATSIssuePublisher {
	return &NATSIssuePublisher{js: js}
}

// PublishIssueTriage publishes a review_only issue to the triage subject.
func (p *NATSIssuePublisher) PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error {
	data, err := Encode(IssueMsg{Repo: repo, Number: number, GithubID: githubID})
	if err != nil {
		return fmt.Errorf("bus: encode issue triage: %w", err)
	}
	msgID := fmt.Sprintf("issue-triage:%d", githubID)
	_, err = p.js.Publish(ctx, SubjIssueTriage, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish issue triage: %w", err)
	}
	return nil
}

// PublishIssueImplement publishes a develop issue to the implement subject.
func (p *NATSIssuePublisher) PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error {
	data, err := Encode(IssueMsg{Repo: repo, Number: number, GithubID: githubID})
	if err != nil {
		return fmt.Errorf("bus: encode issue implement: %w", err)
	}
	msgID := fmt.Sprintf("issue-impl:%d", githubID)
	_, err = p.js.Publish(ctx, SubjIssueImplement, data, jetstream.WithMsgID(msgID))
	if err != nil {
		return fmt.Errorf("bus: publish issue implement: %w", err)
	}
	return nil
}
```

Note: different msgID prefixes (`issue-triage:` vs `issue-impl:`) so the same issue can be published to both subjects without dedup collision.

- [ ] **Step 4: Run tests**

```bash
cd daemon
go test ./internal/bus/ -run "TestIssuePublisher" -v
```

- [ ] **Step 5: Commit**

```bash
git add internal/bus/publisher.go internal/bus/publisher_test.go
git commit -m "feat(bus): add NATSIssuePublisher for triage and implement subjects (#306)"
```

---

### Task 2: Fetcher publishes to NATS

**Files:**
- Modify: `daemon/internal/issues/fetcher.go`

- [ ] **Step 1: Add IssuePublisher interface and field to Fetcher**

In `daemon/internal/issues/fetcher.go`, after the `issueMarkerFetcher` interface (around line 63), add:

```go
// IssuePublisher dispatches classified issues to NATS. When set on the
// Fetcher, ProcessRepo publishes to NATS instead of calling pipeline.Run.
type IssuePublisher interface {
	PublishIssueTriage(ctx context.Context, repo string, number int, githubID int64) error
	PublishIssueImplement(ctx context.Context, repo string, number int, githubID int64) error
}
```

Add a `publisher` field to the `Fetcher` struct:

```go
type Fetcher struct {
	client    IssuesFetcher
	comments  issueMarkerFetcher
	store     issueDedupStore
	pipeline  PipelineRunner
	publisher IssuePublisher // optional — when set, publishes to NATS instead of running pipeline
}
```

Add a setter method:

```go
// SetPublisher enables NATS-based dispatch. When set, ProcessRepo publishes
// classified issues to NATS instead of calling pipeline.Run directly.
func (f *Fetcher) SetPublisher(p IssuePublisher) {
	f.publisher = p
}
```

- [ ] **Step 2: Modify ProcessRepo dispatch to use publisher when set**

In `ProcessRepo`, replace the dispatch block (around line 132):

```go
		if _, runErr := f.pipeline.Run(ctx, issue, optsFor(issue)); runErr != nil {
			slog.Error("issues fetcher: pipeline run failed",
				"repo", repo, "number", issue.Number, "err", runErr)
			continue
		}
```

With:

```go
		if f.publisher != nil {
			var pubErr error
			switch issue.Mode {
			case config.IssueModeReviewOnly:
				pubErr = f.publisher.PublishIssueTriage(ctx, issue.Repo, issue.Number, issue.ID)
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
			if _, runErr := f.pipeline.Run(ctx, issue, optsFor(issue)); runErr != nil {
				slog.Error("issues fetcher: pipeline run failed",
					"repo", repo, "number", issue.Number, "err", runErr)
				continue
			}
		}
```

- [ ] **Step 3: Verify it compiles and all existing tests pass**

```bash
cd daemon
go build ./internal/issues/
go test ./internal/issues/ -v -count=1
```

Expected: All existing tests pass — they don't set a publisher, so the fallback path runs.

- [ ] **Step 4: Commit**

```bash
git add internal/issues/fetcher.go
git commit -m "feat(issues): Fetcher publishes to NATS when publisher is set (#306)"
```

---

### Task 3: TriageWorker

**Files:**
- Create: `daemon/internal/worker/triage.go`
- Create: `daemon/internal/worker/triage_test.go`

- [ ] **Step 1: Create triage.go**

```go
// daemon/internal/worker/triage.go
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go/jetstream"
)

// TriageWorker consumes issue triage requests from NATS and delegates
// to a handler that runs the issue review pipeline in ReviewOnly mode.
type TriageWorker struct {
	js      jetstream.JetStream
	handler func(ctx context.Context, msg bus.IssueMsg)
}

// NewTriageWorker creates a worker that consumes from the triage-worker
// durable consumer.
func NewTriageWorker(js jetstream.JetStream, handler func(context.Context, bus.IssueMsg)) *TriageWorker {
	return &TriageWorker{js: js, handler: handler}
}

// Start begins consuming. Blocks until ctx is cancelled.
// Always acks messages — errors are logged inside the handler.
func (w *TriageWorker) Start(ctx context.Context) error {
	cons, err := w.js.Consumer(ctx, bus.StreamWork, bus.ConsumerTriage)
	if err != nil {
		return err
	}

	iter, err := cons.Messages(jetstream.PullMaxMessages(1))
	if err != nil {
		return err
	}

	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				return nil
			}
			return fmt.Errorf("triage-worker: iter.Next: %w", err)
		}

		var issueMsg bus.IssueMsg
		if err := bus.Decode(msg.Data(), &issueMsg); err != nil {
			slog.Error("triage-worker: decode message", "err", err)
			msg.Ack()
			continue
		}

		slog.Info("triage-worker: processing",
			"repo", issueMsg.Repo, "number", issueMsg.Number, "github_id", issueMsg.GithubID)

		w.safeHandle(ctx, issueMsg)
		msg.Ack()
	}
}

func (w *TriageWorker) safeHandle(ctx context.Context, msg bus.IssueMsg) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("triage-worker: handler panic",
				"repo", msg.Repo, "number", msg.Number, "panic", r,
				"stack", string(debug.Stack()))
		}
	}()
	w.handler(ctx, msg)
}
```

- [ ] **Step 2: Create triage_test.go**

```go
// daemon/internal/worker/triage_test.go
package worker_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/worker"
)

func TestTriageWorker_ConsumesAndCallsHandler(t *testing.T) {
	b := newTestBus(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var (
		mu       sync.Mutex
		received []bus.IssueMsg
	)
	handler := func(_ context.Context, msg bus.IssueMsg) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg)
	}

	w := worker.NewTriageWorker(b.JetStream(), handler)
	go func() { w.Start(ctx) }()
	time.Sleep(200 * time.Millisecond)

	pub := bus.NewIssuePublisher(b.JetStream())
	if err := pub.PublishIssueTriage(ctx, "org/repo", 42, 12345); err != nil {
		t.Fatalf("publish: %v", err)
	}

	time.Sleep(500 * time.Millisecond)
	cancel()

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1, got %d", len(received))
	}
	msg := received[0]
	if msg.Repo != "org/repo" || msg.Number != 42 || msg.GithubID != 12345 {
		t.Errorf("unexpected: %+v", msg)
	}
}
```

- [ ] **Step 3: Run tests**

```bash
cd daemon
go test ./internal/worker/ -v -count=1
```

Expected: All worker tests pass.

- [ ] **Step 4: Commit**

```bash
git add internal/worker/triage.go internal/worker/triage_test.go
git commit -m "feat(worker): add TriageWorker NATS consumer (#306)"
```

---

### Task 4: Wire into main.go

**Files:**
- Modify: `daemon/cmd/heimdallm/main.go`

- [ ] **Step 1: Create IssuePublisher and set on Fetcher**

Find where `issueFetcher` is created (search for `NewFetcher`). After it, add:

```go
	issuePublisher := bus.NewIssuePublisher(js)
	issueFetcher.SetPublisher(issuePublisher)
```

- [ ] **Step 2: Add triageHandler closure and TriageWorker startup**

After the publishWorker startup block, add:

```go
	// ── NATS issue triage worker ────────────────────────────────────────
	triageHandler := func(ctx context.Context, msg bus.IssueMsg) {
		ghIssue, err := ghClient.GetIssue(msg.Repo, msg.Number)
		if err != nil {
			slog.Error("triage-worker: fetch issue from GitHub",
				"repo", msg.Repo, "number", msg.Number, "err", err)
			return
		}
		ghIssue.Mode = config.IssueModeReviewOnly

		cfgMu.Lock()
		c := *cfg
		aiCfg := c.AIForRepo(msg.Repo)
		if aiCfg.Primary == "" {
			aiCfg.Primary = c.AI.Primary
		}
		agentCfg := c.AgentConfigFor(aiCfg.Primary)
		localDirBase := c.GitHub.LocalDirBase
		globalTimeout := c.AI.ExecutionTimeout
		cfgMu.Unlock()
		aiCfg.LocalDir = config.ResolveLocalDir(aiCfg.LocalDir, msg.Repo, localDirBase)

		extraFlags := agentCfg.ExtraFlags
		if extraFlags != "" {
			if err := executor.ValidateExtraFlags(extraFlags); err != nil {
				slog.Warn("triage-worker: extra_flags rejected", "err", err)
				extraFlags = ""
			}
		}

		issuePrompt, issueInstructions := resolveIssuePrompt(s, aiCfg.IssuePrompt, agentCfg.PromptID)
		implPrompt, implInstructions := resolveImplementPrompt(s, aiCfg.ImplementPrompt, agentCfg.PromptID)

		opts := issuepipeline.RunOptions{
			GitHubToken: token,
			Primary:     aiCfg.Primary,
			Fallback:    aiCfg.Fallback,
			ExecOpts: executor.ExecOptions{
				Model:                agentCfg.Model,
				MaxTurns:             agentCfg.MaxTurns,
				ApprovalMode:         agentCfg.ApprovalMode,
				ExtraFlags:           extraFlags,
				WorkDir:              aiCfg.LocalDir,
				Effort:               agentCfg.Effort,
				PermissionMode:       agentCfg.PermissionMode,
				Bare:                 agentCfg.Bare,
				DangerouslySkipPerms: agentCfg.DangerouslySkipPerms,
				NoSessionPersistence: agentCfg.NoSessionPersistence,
				Timeout:              resolveExecutionTimeout(globalTimeout, agentCfg.ExecutionTimeout),
			},
			IssuePromptOverride:     issuePrompt,
			IssueInstructions:       issueInstructions,
			ImplementPromptOverride: implPrompt,
			ImplementInstructions:   implInstructions,
			PRReviewers:             aiCfg.PRReviewers,
			PRAssignee:              aiCfg.PRAssignee,
			PRLabels:                aiCfg.PRLabels,
			PRDraft:                 aiCfg.PRDraft != nil && *aiCfg.PRDraft,
			GeneratePRDescription:   aiCfg.GeneratePRDescription != nil && *aiCfg.GeneratePRDescription,
		}

		if _, err := issuePipe.Run(ctx, ghIssue, opts); err != nil {
			slog.Error("triage-worker: pipeline run failed",
				"repo", msg.Repo, "number", msg.Number, "err", err)
		}
	}

	triageW := worker.NewTriageWorker(js, triageHandler)
	triageWCtx, triageWCancel := context.WithCancel(context.Background())
	defer triageWCancel()
	go func() {
		if err := triageW.Start(triageWCtx); err != nil {
			slog.Error("triage worker stopped", "err", err)
		}
	}()
```

**NOTE:** The handler uses `ghClient.GetIssue(repo, number)` — verify this method exists. If not, it needs to be added (same pattern as GetPR). Search for `GetIssue` in the github client.

- [ ] **Step 3: Verify build**

```bash
cd daemon
go build ./cmd/heimdallm/
```

If `GetIssue` doesn't exist, add it to `daemon/internal/github/client.go` following the `GetPR` pattern.

- [ ] **Step 4: Run full test suite**

```bash
cd daemon
go test ./... -count=1
```

- [ ] **Step 5: Commit**

```bash
git add cmd/heimdallm/main.go internal/github/client.go
git commit -m "feat: wire issue triage worker into daemon (#306)"
```

---

### Task 5: Final validation

- [ ] **Step 1: Run affected packages with race detector**

```bash
cd daemon
go test ./internal/worker/ ./internal/bus/ ./internal/issues/ ./cmd/heimdallm/ -race -count=1
```

- [ ] **Step 2: Build binary and smoke test**

```bash
cd daemon
go build -o bin/heimdallm ./cmd/heimdallm/
HEIMDALLM_DATA_DIR=$(mktemp -d) HEIMDALLM_AI_PRIMARY=claude-code timeout 8 ./bin/heimdallm 2>&1 | head -20
```

Expected: If issues are eligible for triage, logs show `"triage-worker: processing"`.

- [ ] **Step 3: Commit if adjustments needed**
