package sse

import "fmt"

// Event type constants
const (
	EventHeartbeat             = "heartbeat"
	EventPRDetected            = "pr_detected"
	EventReviewStarted         = "review_started"
	EventReviewCompleted       = "review_completed"
	EventReviewError           = "review_error"
	EventReviewSkipped         = "review_skipped"
	EventCircuitBreakerTripped = "circuit_breaker_tripped"

	// Issue tracking pipeline (#26 onward).
	EventIssueDetected        = "issue_detected"
	EventIssueReviewStarted   = "issue_review_started"
	EventIssueReviewCompleted = "issue_review_completed"
	// Emitted after the refinement stage stores its plan and posts the GitHub comment.
	EventIssueRefinementDone = "issue_refinement_done"
	EventIssueImplemented    = "issue_implemented" // reserved for #27 (auto_implement PR created)
	EventIssueReviewError    = "issue_review_error"
	EventIssuePromoted       = "issue_promoted" // issue stage/dependency label promotion

	// EventRepoDiscovered fires when the poll cycle sees a PR whose repo
	// is not yet in monitored or non-monitored. Payload: {"repo": "org/name"}.
	EventRepoDiscovered = "repo_discovered"

	// EventPollingStarted fires once per poll cycle, before any repo work,
	// to give the GUI Server screen a "what's happening now" signal.
	// Payload: {"kind": "prs"|"issues", "repos": [...]}.
	EventPollingStarted = "polling_started"

	// EventPollingCompleted fires once per poll cycle, after all repos in
	// the cycle have been processed (or skipped). Payload: {"kind", "count",
	// "duration_ms"}. Not emitted when the cycle is cancelled mid-flight.
	EventPollingCompleted = "polling_completed"

	EventPRStateChanged    = "pr_state_changed"
	EventIssueStateChanged = "issue_state_changed"

	// EventPRReviewStateChanged fires when Tier 3 observes a change in
	// the aggregated external review state of an auto_implement-created
	// PR (#482). Payload includes pr_id, repo, number, state, reviewer,
	// prev_state. Only fires for PRs whose `auto_implement_issue_id`
	// column is non-zero — standard PRs never publish this event.
	EventPRReviewStateChanged = "pr_review_state_changed"

	// EventRepoRenamed fires when the rename reconciler propagates a
	// GitHub repo/org rename across daemon state (#489). Payload:
	// {"old_repo": "...", "new_repo": "...", "worktree_purged": bool}.
	// Flutter consumers refresh the repo / PR / issue lists and dismiss
	// cached entries keyed on `old_repo`.
	EventRepoRenamed = "repo_renamed"

	// EventRepoNonMonitoredStale fires when the rename probe detects
	// that an entry in `github.non_monitored` has been renamed on
	// GitHub (#493 follow-up to #489). The daemon deliberately does
	// NOT auto-rewrite non_monitored entries — they reflect an
	// explicit operator-disabled state — so this event surfaces the
	// stale slug for human action. Payload:
	// {"old_repo": "...", "new_repo": "..."}.
	// The probe dedupes warnings per (old, new) pair across its
	// in-memory lifetime, so this event fires at most once per
	// detected drift per daemon start.
	EventRepoNonMonitoredStale = "repo_non_monitored_stale"

	// Autonomous end-to-end mode (spec 2026-06-12).
	EventAutonomousTaskSelected   = "autonomous_task_selected"     // {repo, number, bucket}
	EventAutonomousTaskReassigned = "autonomous_task_reassigned"   // {repo, number, assignee}
	EventAutonomousStageAdvanced  = "autonomous_stage_advanced"    // {repo, number, from, to}
	EventAutonomousReviewClass    = "autonomous_review_classified" // {repo, number, decision}
	EventAutonomousMergeSkipped   = "autonomous_merge_skipped"     // {repo, number, reason}
	EventAutonomousMergeDone      = "autonomous_merge_done"        // {repo, number, method}
)

// maxSubscribers limits the number of concurrent SSE connections to prevent
// resource exhaustion from a local process opening unbounded connections.
const maxSubscribers = 10

// Event represents a server-sent event.
type Event struct {
	Type string
	Data string
	// NATSForwarded is internal delivery metadata. Producers that synchronously
	// publish an event to NATS set it before copying the event to the local
	// broker, preventing a duplicate bridge publish while preserving fallback
	// when the synchronous handoff fails.
	NATSForwarded bool
}

// Format returns the SSE wire format for this event.
func (e Event) Format() string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", e.Type, e.Data)
}

// subscribeRequest bundles the new channel with a reply channel so that the
// broker goroutine can signal acceptance or rejection without a mutex.
type subscribeRequest struct {
	ch    chan Event
	reply chan bool // true = accepted, false = rejected (limit reached)
}

// Broker fans out published events to all active subscribers.
// A single goroutine (run) owns the subscribers map — no mutex needed.
type Broker struct {
	publish     chan Event
	subscribe   chan subscribeRequest
	unsubscribe chan chan Event
	quit        chan struct{}
}

// NewBroker creates a new Broker. Call Start before publishing or subscribing.
func NewBroker() *Broker {
	return &Broker{
		publish:     make(chan Event, 16),
		subscribe:   make(chan subscribeRequest),
		unsubscribe: make(chan chan Event),
		quit:        make(chan struct{}),
	}
}

// Start launches the broker's internal goroutine.
func (b *Broker) Start() {
	go b.run()
}

// Stop shuts down the broker goroutine.
func (b *Broker) Stop() {
	close(b.quit)
}

// Subscribe registers a new subscriber and returns its event channel.
// Returns nil if the subscriber limit (maxSubscribers) has been reached or if
// the broker has already been stopped. Callers must check for nil before using
// the returned channel.
func (b *Broker) Subscribe() chan Event {
	ch := make(chan Event, 8)
	req := subscribeRequest{ch: ch, reply: make(chan bool, 1)}
	select {
	case b.subscribe <- req:
	case <-b.quit:
		return nil
	}
	select {
	case accepted := <-req.reply:
		if !accepted {
			return nil
		}
	case <-b.quit:
		return nil
	}
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Broker) Unsubscribe(ch chan Event) {
	select {
	case b.unsubscribe <- ch:
	case <-b.quit:
		// broker already stopped; channel was closed by run()
	}
}

// Publish sends an event to all current subscribers (non-blocking; drops if
// the publish buffer is full).
func (b *Broker) Publish(e Event) {
	select {
	case b.publish <- e:
	default:
		// Drop if publish buffer full.
	}
}

func (b *Broker) run() {
	subscribers := make(map[chan Event]struct{})
	for {
		select {
		case req := <-b.subscribe:
			if len(subscribers) >= maxSubscribers {
				req.reply <- false
			} else {
				subscribers[req.ch] = struct{}{}
				req.reply <- true
			}
		case ch := <-b.unsubscribe:
			delete(subscribers, ch)
			close(ch)
		case event := <-b.publish:
			for ch := range subscribers {
				select {
				case ch <- event:
				default:
					// Drop if subscriber buffer full.
				}
			}
		case <-b.quit:
			for ch := range subscribers {
				close(ch)
			}
			return
		}
	}
}
