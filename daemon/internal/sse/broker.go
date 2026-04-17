package sse

import "fmt"

// Event type constants
const (
	EventPRDetected      = "pr_detected"
	EventReviewStarted   = "review_started"
	EventReviewCompleted = "review_completed"
	EventReviewError     = "review_error"

	// Issue tracking pipeline (#26 onward).
	EventIssueDetected        = "issue_detected"
	EventIssueReviewStarted   = "issue_review_started"
	EventIssueReviewCompleted = "issue_review_completed"
	EventIssueImplemented     = "issue_implemented" // reserved for #27 (auto_implement PR created)
	EventIssueReviewError     = "issue_review_error"
)

// maxSubscribers limits the number of concurrent SSE connections to prevent
// resource exhaustion from a local process opening unbounded connections.
const maxSubscribers = 10

// Event represents a server-sent event.
type Event struct {
	Type string
	Data string
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
