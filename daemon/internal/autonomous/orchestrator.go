package autonomous

import "context"

// StageOutcome is the result of running one pipeline stage.
type StageOutcome struct {
	Success  bool
	PRNumber int // set by the development stage when a PR is created
}

// StageRunner executes a single pipeline stage for a candidate. The production
// implementation maps stage -> issues.Pipeline.Run with the right options
// (full agentic ExecOptions for development) and advances the GitHub stage
// labels as an audit trail — without waiting for a human, since autonomous
// mode overrides label gating.
type StageRunner interface {
	RunStage(ctx context.Context, stage string, c Candidate) (StageOutcome, error)
}

// DriveResult summarises one Drive call.
type DriveResult struct {
	Started  bool
	PRNumber int
	LastDone string // last successfully completed stage
}

// stages is the fixed chain. Review is handled asynchronously by Tier 3, not
// by Drive, so it is intentionally absent here.
var stages = []string{"triage", "refinement", "development"}

// Orchestrator drives one issue through the stage chain single-flight.
type Orchestrator struct {
	runner StageRunner
	guard  *PhaseGuard
}

// NewOrchestrator builds an orchestrator.
func NewOrchestrator(runner StageRunner, guard *PhaseGuard) *Orchestrator {
	return &Orchestrator{runner: runner, guard: guard}
}

// Drive runs the candidate through triage->refinement->development. Each stage
// must claim its single-flight slot; if a stage's slot is busy, Drive stops
// cleanly (Started reflects whether any stage ran). A non-success stage stops
// the chain (the issue stays where it is for the next tick / human inspection).
func (o *Orchestrator) Drive(ctx context.Context, c Candidate) (DriveResult, error) {
	var res DriveResult
	for _, stage := range stages {
		rel, ok := o.guard.TryEnter(stage)
		if !ok {
			return res, nil
		}
		res.Started = true
		out, err := o.runner.RunStage(ctx, stage, c)
		rel()
		if err != nil {
			return res, err
		}
		if !out.Success {
			return res, nil
		}
		res.LastDone = stage
		if out.PRNumber != 0 {
			res.PRNumber = out.PRNumber
		}
	}
	return res, nil
}
