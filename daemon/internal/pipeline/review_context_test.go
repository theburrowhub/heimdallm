package pipeline

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

func TestPartitionComments_SplitsByTimestamp(t *testing.T) {
	cutoff := time.Date(2026, 4, 15, 12, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "reviewer", Body: "old comment", CreatedAt: cutoff.Add(-1 * time.Hour)},
		{Author: "author", Body: "old response", CreatedAt: cutoff.Add(-30 * time.Minute)},
		{Author: "author", Body: "new push", CreatedAt: cutoff.Add(1 * time.Hour)},
		{Author: "other", Body: "new feedback", CreatedAt: cutoff.Add(2 * time.Hour)},
	}

	before, after := partitionComments(comments, cutoff)
	if len(before) != 2 {
		t.Errorf("before = %d, want 2", len(before))
	}
	if len(after) != 2 {
		t.Errorf("after = %d, want 2", len(after))
	}
}

func TestPartitionComments_AllBefore(t *testing.T) {
	cutoff := time.Now()
	comments := []github.Comment{
		{Author: "a", Body: "old", CreatedAt: cutoff.Add(-1 * time.Hour)},
	}
	before, after := partitionComments(comments, cutoff)
	if len(before) != 1 || len(after) != 0 {
		t.Errorf("before=%d after=%d, want 1/0", len(before), len(after))
	}
}

func TestPartitionComments_AllAfter(t *testing.T) {
	cutoff := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	comments := []github.Comment{
		{Author: "a", Body: "new", CreatedAt: time.Now()},
	}
	before, after := partitionComments(comments, cutoff)
	if len(before) != 0 || len(after) != 1 {
		t.Errorf("before=%d after=%d, want 0/1", len(before), len(after))
	}
}

func TestPartitionComments_Empty(t *testing.T) {
	before, after := partitionComments(nil, time.Now())
	if len(before) != 0 || len(after) != 0 {
		t.Error("expected empty slices for nil input")
	}
}
