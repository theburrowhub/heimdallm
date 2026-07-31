package github

import (
	"strings"
	"testing"
)

// The entry-count cap alone does not bound memory: at the ≤1 MiB per-body
// ceiling, maxCacheEntries would allow ~2 GiB resident in a long-lived daemon.
// These tests pin the aggregate byte budget that backstops it.

func TestConditionalCache_EvictsOnByteBudget(t *testing.T) {
	c := NewConditionalCache()

	// Bodies sized so the budget, not the entry count, is the binding limit:
	// 8 of these exceed maxCacheBytes while staying far below maxCacheEntries.
	const bodySize = maxCacheBytes / 8
	body := strings.Repeat("x", bodySize)

	for i := 0; i < 12; i++ {
		c.Put(string(rune('a'+i)), "etag", []byte(body))
		if c.Bytes() > maxCacheBytes {
			t.Fatalf("after %d puts: bytes=%d exceeds budget %d", i+1, c.Bytes(), maxCacheBytes)
		}
	}

	if got := len(c.entries); got >= 12 {
		t.Errorf("expected eviction under the byte budget, still holding %d entries", got)
	}
	if c.Bytes() > maxCacheBytes {
		t.Errorf("bytes=%d over budget %d", c.Bytes(), maxCacheBytes)
	}
}

func TestConditionalCache_ByteCountTracksReplaceAndEvict(t *testing.T) {
	c := NewConditionalCache()

	c.Put("k", "e1", []byte("12345"))
	if c.Bytes() != 5 {
		t.Fatalf("after first put: bytes=%d, want 5", c.Bytes())
	}

	// Replacing a key must release the previous body, not double-count it.
	c.Put("k", "e2", []byte("123"))
	if c.Bytes() != 3 {
		t.Errorf("after replace: bytes=%d, want 3 (old body not released)", c.Bytes())
	}
	if len(c.entries) != 1 {
		t.Errorf("replace should not add an entry, have %d", len(c.entries))
	}

	c.Put("j", "e3", []byte("1234567"))
	if c.Bytes() != 10 {
		t.Errorf("after second key: bytes=%d, want 10", c.Bytes())
	}

	// Eviction must decrement the counter too, or the cache slowly starves
	// itself as a phantom byte count accumulates.
	c.mu.Lock()
	c.evictOldestLocked()
	c.mu.Unlock()
	if c.Bytes() != 7 {
		t.Errorf("after evicting the oldest (3 bytes): bytes=%d, want 7", c.Bytes())
	}
}

// A single body larger than the whole budget must not spin the eviction loop.
func TestConditionalCache_OversizedBodyDoesNotSpin(t *testing.T) {
	c := NewConditionalCache()
	c.Put("small", "e", []byte("x"))
	c.Put("huge", "e", make([]byte, maxCacheBytes+1))

	if _, _, ok := c.Get("huge"); !ok {
		t.Error("oversized body should still be stored (served once, evicted next)")
	}
	if len(c.entries) != 1 {
		t.Errorf("oversized body should have displaced everything else, have %d entries", len(c.entries))
	}
}
