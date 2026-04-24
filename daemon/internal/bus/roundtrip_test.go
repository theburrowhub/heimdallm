// daemon/internal/bus/roundtrip_test.go
package bus_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

func TestRoundtrip_PRReview(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()

	msg := bus.PRReviewMsg{
		Repo:     "org/repo",
		Number:   42,
		GithubID: 12345,
		HeadSHA:  "abc123",
	}
	data, err := bus.Encode(msg)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjPRReview, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	if err := conn.Publish(bus.SubjPRReview, data); err != nil {
		t.Fatalf("publish: %v", err)
	}
	conn.Flush()

	select {
	case m := <-ch:
		var got bus.PRReviewMsg
		if err := bus.Decode(m.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Repo != "org/repo" || got.Number != 42 || got.GithubID != 12345 || got.HeadSHA != "abc123" {
			t.Errorf("unexpected payload: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestEventsFanOut(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()

	ch1 := make(chan *nats.Msg, 1)
	ch2 := make(chan *nats.Msg, 1)
	sub1, err := conn.ChanSubscribe("heimdallm.events.>", ch1)
	if err != nil {
		t.Fatalf("chanSub1: %v", err)
	}
	defer sub1.Unsubscribe()
	sub2, err := conn.ChanSubscribe("heimdallm.events.>", ch2)
	if err != nil {
		t.Fatalf("chanSub2: %v", err)
	}
	defer sub2.Unsubscribe()

	data, _ := bus.Encode(bus.EventMsg{Type: "review_completed", Data: map[string]any{"pr": 1}})
	if err := conn.Publish(bus.SubjEventReviewCompleted, data); err != nil {
		t.Fatalf("publish: %v", err)
	}
	conn.Flush()

	for i, ch := range []chan *nats.Msg{ch1, ch2} {
		select {
		case m := <-ch:
			var got bus.EventMsg
			bus.Decode(m.Data, &got)
			if got.Type != "review_completed" {
				t.Errorf("sub%d: type = %q, want review_completed", i+1, got.Type)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("sub%d: timeout", i+1)
		}
	}
}

func TestCoreNATS_NoSubscriberLoss(t *testing.T) {
	// Verifies that with core NATS, messages published when no subscriber
	// is listening are silently dropped (fire-and-forget). This is
	// expected — pollers re-detect work every cycle.
	env := newTestEnv(t)
	conn := env.bus.Conn()

	data, _ := bus.Encode(bus.PRReviewMsg{Repo: "a/b", Number: 1, GithubID: 100, HeadSHA: "sha1"})
	// Publish with no subscriber — should not error.
	if err := conn.Publish(bus.SubjPRReview, data); err != nil {
		t.Fatalf("publish without subscriber: %v", err)
	}
	conn.Flush()

	// Now subscribe — should not receive the earlier message.
	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjPRReview, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	select {
	case <-ch:
		t.Error("received message published before subscription — core NATS should drop it")
	case <-time.After(200 * time.Millisecond):
		// Expected: no message
	}
}
