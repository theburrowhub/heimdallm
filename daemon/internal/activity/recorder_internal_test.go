package activity

import "testing"

func TestRecorder_FirstOnceSkipResetsWhenFull(t *testing.T) {
	r := &Recorder{recordedOnce: make(map[onceSkipKey]bool, maxRecordedOnce)}
	for i := 0; i < maxRecordedOnce; i++ {
		r.recordedOnce[onceSkipKey{Repo: "org/name", PRNumber: i}] = true
	}
	if !r.firstOnceSkip(onceSkipKey{Repo: "org/name", PRNumber: -1}) {
		t.Fatal("new key must be recorded")
	}
	if len(r.recordedOnce) != 1 {
		t.Fatalf("set size = %d, want 1 after reset", len(r.recordedOnce))
	}
}
