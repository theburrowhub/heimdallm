package scheduler

import (
	"testing"
	"time"
)

// The scheduler outlives config reloads so per-repo back-off state is not lost.
// That made min_interval/max_interval the one pair of [polling] settings a
// reload could never apply — not even with a poller restart, since the
// scheduler is not recreated there — while GET /config already reported the new
// values. SetBounds closes that divergence without discarding state.

func TestSetBounds_AppliesWithoutLosingState(t *testing.T) {
	a := NewAdaptiveScheduler(time.Minute, 15*time.Minute)
	now := time.Now()

	// Build up some back-off state for a repo.
	a.Due(now, []string{"org/repo"})
	a.MarkIdle("org/repo", now)
	grown := a.Interval("org/repo")
	if grown <= time.Minute {
		t.Fatalf("setup: expected the interval to grow past min, got %s", grown)
	}

	a.SetBounds(2*time.Minute, 30*time.Minute)

	// State survives: the repo is still tracked, not reset to a fresh entry.
	if got := a.Interval("org/repo"); got != grown {
		t.Errorf("SetBounds discarded accumulated state: interval %s → %s", grown, got)
	}

	// New bounds are in force: a reset clamps to the NEW min, not the old one.
	a.MarkActive("org/repo", now)
	if got := a.Interval("org/repo"); got != 2*time.Minute {
		t.Errorf("after SetBounds, MarkActive should use the new min 2m, got %s", got)
	}
}

func TestSetBounds_ClampsAndIgnoresInvalid(t *testing.T) {
	a := NewAdaptiveScheduler(time.Minute, 15*time.Minute)

	// max < min is clamped up to min, mirroring the constructor.
	a.SetBounds(10*time.Minute, time.Minute)
	a.MarkIdle("k", time.Now())
	if got := a.Interval("k"); got != 10*time.Minute {
		t.Errorf("max should clamp up to min: interval = %s, want 10m", got)
	}

	// A non-positive min is ignored rather than wedging the scheduler at zero.
	a.SetBounds(0, time.Hour)
	a.MarkActive("k", time.Now())
	if got := a.Interval("k"); got != 10*time.Minute {
		t.Errorf("non-positive min should be ignored, interval = %s, want 10m", got)
	}
}
