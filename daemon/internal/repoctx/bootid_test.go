package repoctx

import "testing"

// TestBootLeftoverCollectable pins the valve's decision table. The retention
// rules it encodes are the whole reason the marker protocol can be relaxed
// safely, so each "not collectable" case matters as much as the positive one:
// getting any of them wrong reopens the race that cleanup.ready exists to close.
func TestBootLeftoverCollectable(t *testing.T) {
	if bootIdentity() == "" {
		t.Skip("platform reports no boot identity; the valve is disabled by design")
	}
	current := bootIdentity()

	t.Run("different boot is collectable", func(t *testing.T) {
		if !bootLeftoverCollectable(current + "-stale") {
			t.Error("a leftover stamped with another boot must be collectable: no " +
				"process from a previous boot can still hold the checkout")
		}
	})

	t.Run("same boot is retained", func(t *testing.T) {
		if bootLeftoverCollectable(current) {
			t.Error("a leftover from THIS boot must be retained — a CLI descendant " +
				"may still be using it even with no lock held")
		}
	})

	t.Run("unstamped legacy leftover is retained", func(t *testing.T) {
		if bootLeftoverCollectable("") {
			t.Error("an empty stamp means 'written before this field existed', which " +
				"is unknown, not stale")
		}
	})
}

// TestBootIdentityIsStable guards the contract the valve depends on: the token
// must not vary between calls, or a leftover written earlier in this same
// process would later look like it came from another boot and be reclaimed while
// still in use.
func TestBootIdentityIsStable(t *testing.T) {
	first := bootIdentity()
	for i := 0; i < 3; i++ {
		if got := bootIdentity(); got != first {
			t.Fatalf("boot identity changed between calls: %q then %q", first, got)
		}
	}
}
