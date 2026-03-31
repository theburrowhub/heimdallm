package sse

import "fmt"

// Event type constants
const (
	EventPRDetected      = "pr_detected"
	EventReviewStarted   = "review_started"
	EventReviewCompleted = "review_completed"
	EventReviewError     = "review_error"
)

// Event represents a server-sent event.
type Event struct {
	Type string
	Data string
}

// Format returns the SSE wire format for this event.
func (e Event) Format() string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", e.Type, e.Data)
}

// Broker fans out published events to all active subscribers.
// A single goroutine (run) owns the subscribers map — no mutex needed.
type Broker struct {
	publish     chan Event
	subscribe   chan chan Event
	unsubscribe chan chan Event
	quit        chan struct{}
}

// NewBroker creates a new Broker. Call Start before publishing or subscribing.
func NewBroker() *Broker {
	return &Broker{
		publish:     make(chan Event, 16),
		subscribe:   make(chan chan Event),
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
func (b *Broker) Subscribe() chan Event {
	ch := make(chan Event, 8)
	b.subscribe <- ch
	return ch
}

// Unsubscribe removes a subscriber and closes its channel.
func (b *Broker) Unsubscribe(ch chan Event) {
	b.unsubscribe <- ch
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
		case ch := <-b.subscribe:
			subscribers[ch] = struct{}{}
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
			return
		}
	}
}
