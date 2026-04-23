package bus

// NATS subject constants for the Heimdallm event bus.
// Organized by functional domain: discovery, PR workflow, issue workflow,
// state checking, and events (SSE bridge).

const (
	// Discovery
	SubjDiscoveryRepos = "heimdallm.discovery.repos"

	// PR workflow
	SubjPRReview  = "heimdallm.pr.review"
	SubjPRPublish = "heimdallm.pr.publish"

	// Issue workflow
	SubjIssueTriage    = "heimdallm.issue.triage"
	SubjIssueImplement = "heimdallm.issue.implement"

	// State checking
	SubjStateCheck = "heimdallm.state.check"

	// Events (SSE bridge) — publishers use specific sub-subjects,
	// consumers subscribe to the wildcard "heimdallm.events.>".
	SubjEventPrefix          = "heimdallm.events."
	SubjEventReviewStarted   = "heimdallm.events.review_started"
	SubjEventReviewCompleted = "heimdallm.events.review_completed"
	SubjEventReviewError     = "heimdallm.events.review_error"
	SubjEventReviewSkipped   = "heimdallm.events.review_skipped"
	SubjEventBreakerTripped  = "heimdallm.events.circuit_breaker_tripped"
	SubjEventPRStateChanged  = "heimdallm.events.pr_state_changed"
	SubjEventPRDetected      = "heimdallm.events.pr_detected"
	SubjEventIssueDetected   = "heimdallm.events.issue_detected"
	SubjEventIssueTriage     = "heimdallm.events.issue_review_completed"
	SubjEventIssueImplement  = "heimdallm.events.issue_implemented"
	SubjEventIssueState      = "heimdallm.events.issue_state_changed"
	SubjEventRepoDiscovered  = "heimdallm.events.repo_discovered"
)

// Stream names
const (
	StreamWork      = "HEIMDALLM_WORK"
	StreamDiscovery = "HEIMDALLM_DISCOVERY"
	StreamEvents    = "HEIMDALLM_EVENTS"
)

// Consumer names
const (
	ConsumerReview    = "review-worker"
	ConsumerPublish   = "publish-worker"
	ConsumerTriage    = "triage-worker"
	ConsumerImplement = "implement-worker"
	ConsumerState     = "state-worker"
	ConsumerDiscovery = "discovery-consumer"
)
