package sse

import "testing"

func TestAutonomousEventConstants(t *testing.T) {
	pairs := map[string]string{
		EventAutonomousTaskSelected:   "autonomous_task_selected",
		EventAutonomousTaskReassigned: "autonomous_task_reassigned",
		EventAutonomousStageAdvanced:  "autonomous_stage_advanced",
		EventAutonomousReviewClass:    "autonomous_review_classified",
		EventAutonomousMergeSkipped:   "autonomous_merge_skipped",
		EventAutonomousMergeDone:      "autonomous_merge_done",
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("event constant mismatch: got %q want %q", got, want)
		}
	}
}
