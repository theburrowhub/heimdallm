package pipeline

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

// Admitted reviews that have not been stored yet must count, or N workers
// starting at once would all take the last free slot.
func TestReviewBudget_PendingTicketsCount(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var b reviewBudget
	scopes := []ReviewBudgetScope{{Kind: "org", Key: "acme", Limits: ReviewWindowLimits{PerMinute: 1}}}
	now := time.Now().UTC()

	first, blocked := b.admit(st, scopes, "acme/api", now, false)
	if first == nil || blocked != nil {
		t.Fatalf("first admission: ticket=%v blocked=%v", first, blocked)
	}
	if t2, blocked := b.admit(st, scopes, "acme/web", now, false); t2 != nil || blocked == nil {
		t.Fatalf("second concurrent admission must be blocked, got ticket=%v", t2)
	} else if !blocked.RetryAt.Equal(now.Add(time.Minute)) {
		t.Errorf("RetryAt = %v, want %v", blocked.RetryAt, now.Add(time.Minute))
	}
	// A pending ticket only counts against scopes it belongs to: another
	// org's budget does not see acme's in-flight review.
	otherOrg := []ReviewBudgetScope{{Kind: "org", Key: "other", Limits: ReviewWindowLimits{PerMinute: 1}}}
	if t3, blocked := b.admit(st, otherOrg, "other/repo", now, false); t3 == nil || blocked != nil {
		t.Fatalf("another org is outside acme's scope: ticket=%v blocked=%v", t3, blocked)
	}
	forced, blocked := b.admit(st, scopes, "acme/web", now, true)
	if forced == nil || blocked != nil {
		t.Fatalf("force must admit over a full budget: ticket=%v blocked=%v", forced, blocked)
	}
	b.release(forced)
	b.release(first)
	if t4, blocked := b.admit(st, scopes, "acme/web", now, false); t4 == nil || blocked != nil {
		t.Fatalf("after release the slot is free: ticket=%v blocked=%v", t4, blocked)
	}
	b.release(nil) // no-op
}

func TestReviewBudget_AgentAssignmentCountsPerAgent(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var b reviewBudget
	now := time.Now().UTC()
	t1, _ := b.admit(st, nil, "acme/api", now, false)
	if blocked := b.assignAgent(st, t1, "claude", ReviewWindowLimits{PerHour: 1}, now); blocked != nil {
		t.Fatalf("first claude review blocked: %v", blocked)
	}
	t2, _ := b.admit(st, nil, "acme/web", now, false)
	if blocked := b.assignAgent(st, t2, "claude", ReviewWindowLimits{PerHour: 1}, now); blocked == nil || blocked.Window != "hour" {
		t.Fatalf("second claude review must hit the hour window, got %v", blocked)
	}
	if blocked := b.assignAgent(st, t2, "codex", ReviewWindowLimits{PerHour: 1}, now); blocked != nil {
		t.Fatalf("codex has its own budget: %v", blocked)
	}
	if blocked := b.assignAgent(st, nil, "codex", ReviewWindowLimits{}, now); blocked != nil {
		t.Fatalf("unlimited agent with no ticket: %v", blocked)
	}
}

func TestReviewBudgetError_Message(t *testing.T) {
	e := &ReviewBudgetError{Scope: "repo acme/api", Window: "day", Limit: 5, RetryAt: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	if got := e.Error(); got != "review limit reached: repo acme/api allows 5 per day (next slot 2026-10-08T12:00:00Z)" {
		t.Errorf("Error() = %q", got)
	}
}
