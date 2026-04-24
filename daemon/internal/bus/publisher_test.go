package bus_test

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/nats-io/nats.go"
)

func TestRepoPublisher_PublishRepos(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjDiscoveryRepos, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewRepoPublisher(conn)
	repos := []string{"org/repo1", "org/repo2", "org/repo3"}
	if err := pub.PublishRepos(ctx, repos); err != nil {
		t.Fatalf("PublishRepos: %v", err)
	}
	conn.Flush()

	select {
	case m := <-ch:
		var got bus.DiscoveryMsg
		if err := bus.Decode(m.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Repos) != 3 {
			t.Fatalf("expected 3 repos, got %d", len(got.Repos))
		}
		if got.Repos[0] != "org/repo1" || got.Repos[1] != "org/repo2" || got.Repos[2] != "org/repo3" {
			t.Errorf("unexpected repos: %v", got.Repos)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestPRReviewPublisher_Publish(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjPRReview, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewPRReviewPublisher(conn)
	if err := pub.PublishPRReview(ctx, "org/repo", 42, 12345, "abc123"); err != nil {
		t.Fatalf("PublishPRReview: %v", err)
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
		t.Fatal("timeout")
	}
}

func TestPRReviewPublisher_EmptyHeadSHA(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	pub := bus.NewPRReviewPublisher(conn)
	err := pub.PublishPRReview(ctx, "org/repo", 1, 100, "")
	if err == nil {
		t.Fatal("expected error for empty headSHA")
	}
}

func TestPRPublishPublisher_Publish(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjPRPublish, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewPRPublishPublisher(conn)
	if err := pub.PublishPRPublish(ctx, 42); err != nil {
		t.Fatalf("PublishPRPublish: %v", err)
	}
	conn.Flush()

	select {
	case m := <-ch:
		var got bus.PRPublishMsg
		if err := bus.Decode(m.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.ReviewID != 42 {
			t.Errorf("ReviewID = %d, want 42", got.ReviewID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestIssuePublisher_Triage(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjIssueTriage, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewIssuePublisher(conn)
	if err := pub.PublishIssueTriage(ctx, "org/repo", 10, 555); err != nil {
		t.Fatalf("PublishIssueTriage: %v", err)
	}
	conn.Flush()

	select {
	case m := <-ch:
		var got bus.IssueMsg
		if err := bus.Decode(m.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Repo != "org/repo" || got.Number != 10 || got.GithubID != 555 {
			t.Errorf("unexpected: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestIssuePublisher_Implement(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjIssueImplement, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewIssuePublisher(conn)
	if err := pub.PublishIssueImplement(ctx, "org/repo", 20, 666); err != nil {
		t.Fatalf("PublishIssueImplement: %v", err)
	}
	conn.Flush()

	select {
	case m := <-ch:
		var got bus.IssueMsg
		if err := bus.Decode(m.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Repo != "org/repo" || got.Number != 20 || got.GithubID != 666 {
			t.Errorf("unexpected: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

func TestStateCheckPublisher_Publish(t *testing.T) {
	env := newTestEnv(t)
	conn := env.bus.Conn()
	ctx := context.Background()

	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjStateCheck, ch)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	pub := bus.NewStateCheckPublisher(conn)
	if err := pub.PublishStateCheck(ctx, "pr", "org/repo", 42, 12345); err != nil {
		t.Fatalf("PublishStateCheck: %v", err)
	}
	conn.Flush()

	select {
	case m := <-ch:
		var got bus.StateCheckMsg
		if err := bus.Decode(m.Data, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.Type != "pr" || got.Repo != "org/repo" || got.Number != 42 || got.GithubID != 12345 {
			t.Errorf("unexpected: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}
