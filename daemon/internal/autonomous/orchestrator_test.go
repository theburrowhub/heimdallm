package autonomous

import (
	"context"
	"testing"
)

type fakeRunner struct {
	ran    []string
	failAt string
}

func (f *fakeRunner) RunStage(_ context.Context, stage string, c Candidate) (StageOutcome, error) {
	f.ran = append(f.ran, stage)
	if stage == f.failAt {
		return StageOutcome{Success: false}, nil
	}
	if stage == "development" {
		return StageOutcome{Success: true, PRNumber: 123}, nil
	}
	return StageOutcome{Success: true}, nil
}

func TestOrchestrator_HappyPathChainsAllStages(t *testing.T) {
	r := &fakeRunner{}
	o := NewOrchestrator(r, NewPhaseGuard())
	c := Candidate{Repo: "a/b", Number: 1, GithubID: 1, StoreID: 10}

	res, err := o.Drive(context.Background(), c)
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	want := []string{"triage", "refinement", "development"}
	if len(r.ran) != 3 || r.ran[0] != want[0] || r.ran[1] != want[1] || r.ran[2] != want[2] {
		t.Fatalf("stage order: want %v, got %v", want, r.ran)
	}
	if res.PRNumber != 123 {
		t.Errorf("want PR 123 surfaced, got %d", res.PRNumber)
	}
	if res.LastDone != "development" {
		t.Errorf("want LastDone=development, got %q", res.LastDone)
	}
}

func TestOrchestrator_StopsOnStageFailure(t *testing.T) {
	r := &fakeRunner{failAt: "refinement"}
	o := NewOrchestrator(r, NewPhaseGuard())
	res, err := o.Drive(context.Background(), Candidate{Repo: "a/b", Number: 2})
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if len(r.ran) != 2 {
		t.Fatalf("want stop after failed refinement, ran %v", r.ran)
	}
	if res.PRNumber != 0 {
		t.Errorf("no PR expected on failed chain")
	}
}

func TestOrchestrator_SingleFlightBlocksBusyPhase(t *testing.T) {
	guard := NewPhaseGuard()
	o := NewOrchestrator(&fakeRunner{}, guard)
	rel, ok := guard.TryEnter("triage")
	if !ok {
		t.Fatal("setup: could not pre-claim triage")
	}
	defer rel()
	res, err := o.Drive(context.Background(), Candidate{Repo: "a/b", Number: 3})
	if err != nil {
		t.Fatalf("Drive: %v", err)
	}
	if res.Started {
		t.Errorf("Drive must not start when triage phase is busy")
	}
}
