package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/autonomous"
	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/executor"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/workgate"
)

func TestToReviewInputs_PreservesOrderAndFields(t *testing.T) {
	reviews := []gh.PRReview{
		{State: "COMMENTED", Body: "first pass", SubmittedAt: time.Unix(1, 0)},
		{State: "CHANGES_REQUESTED", Body: "please fix the lint", SubmittedAt: time.Unix(2, 0)},
		{State: "APPROVED", Body: "lgtm", SubmittedAt: time.Unix(3, 0)},
	}
	got := toReviewInputs(reviews)
	if len(got) != len(reviews) {
		t.Fatalf("len = %d, want %d", len(got), len(reviews))
	}
	for i := range reviews {
		if got[i].State != reviews[i].State {
			t.Errorf("input[%d].State = %q, want %q", i, got[i].State, reviews[i].State)
		}
		if got[i].Body != reviews[i].Body {
			t.Errorf("input[%d].Body = %q, want %q", i, got[i].Body, reviews[i].Body)
		}
		// No unresolved-thread API: always 0.
		if got[i].UnresolvedComments != 0 {
			t.Errorf("input[%d].UnresolvedComments = %d, want 0", i, got[i].UnresolvedComments)
		}
	}
}

func TestToReviewInputs_Empty(t *testing.T) {
	if got := toReviewInputs(nil); len(got) != 0 {
		t.Fatalf("len = %d, want 0", len(got))
	}
}

// toReviewInputs must feed ClassifyReview correctly: the latest review
// dominates, so a trailing clean APPROVED routes to the merge gate while a
// trailing CHANGES_REQUESTED routes to a fix.
func TestToReviewInputs_IntegratesWithClassifyReview(t *testing.T) {
	cases := []struct {
		name    string
		reviews []gh.PRReview
		want    autonomous.ReviewDecision
	}{
		{
			name:    "empty -> wait",
			reviews: nil,
			want:    autonomous.DecisionWait,
		},
		{
			name: "trailing clean approval -> merge gate",
			reviews: []gh.PRReview{
				{State: "CHANGES_REQUESTED", Body: "fix"},
				{State: "APPROVED", Body: "lgtm"},
			},
			want: autonomous.DecisionMergeGate,
		},
		{
			name: "trailing changes requested -> fix",
			reviews: []gh.PRReview{
				{State: "APPROVED", Body: "lgtm"},
				{State: "CHANGES_REQUESTED", Body: "regressed"},
			},
			want: autonomous.DecisionFix,
		},
		{
			name: "approved with actionable body -> fix",
			reviews: []gh.PRReview{
				{State: "APPROVED", Body: "please rename the field before merge"},
			},
			want: autonomous.DecisionFix,
		},
		{
			name: "commented -> fix",
			reviews: []gh.PRReview{
				{State: "COMMENTED", Body: "thoughts"},
			},
			want: autonomous.DecisionFix,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := autonomous.ClassifyReview(toReviewInputs(tc.reviews))
			if got != tc.want {
				t.Errorf("ClassifyReview = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIssueToCandidate_MapsFields(t *testing.T) {
	iss := &gh.Issue{
		ID:     987654,
		Number: 42,
		Title:  "Add retry to fetcher",
		Body:   "We should retry transient 5xx.",
		Assignees: []gh.User{
			{Login: "alice"},
			{Login: "bot[bot]"},
		},
		Labels: []gh.Label{
			{Name: "bug"},
			{Name: "develop"},
		},
		Repo: "org/repo",
	}
	const storeID int64 = 555

	c := issueToCandidate(iss, storeID)

	if c.Repo != "org/repo" {
		t.Errorf("Repo = %q, want org/repo", c.Repo)
	}
	if c.Number != 42 {
		t.Errorf("Number = %d, want 42", c.Number)
	}
	if c.GithubID != 987654 {
		t.Errorf("GithubID = %d, want 987654", c.GithubID)
	}
	if c.StoreID != storeID {
		t.Errorf("StoreID = %d, want %d", c.StoreID, storeID)
	}
	if c.Title != "Add retry to fetcher" {
		t.Errorf("Title = %q", c.Title)
	}
	if c.Body != "We should retry transient 5xx." {
		t.Errorf("Body = %q", c.Body)
	}
	wantAssignees := []string{"alice", "bot[bot]"}
	if len(c.Assignees) != len(wantAssignees) {
		t.Fatalf("Assignees = %v, want %v", c.Assignees, wantAssignees)
	}
	for i := range wantAssignees {
		if c.Assignees[i] != wantAssignees[i] {
			t.Errorf("Assignees[%d] = %q, want %q", i, c.Assignees[i], wantAssignees[i])
		}
	}
	wantLabels := []string{"bug", "develop"}
	if len(c.Labels) != len(wantLabels) {
		t.Fatalf("Labels = %v, want %v", c.Labels, wantLabels)
	}
	for i := range wantLabels {
		if c.Labels[i] != wantLabels[i] {
			t.Errorf("Labels[%d] = %q, want %q", i, c.Labels[i], wantLabels[i])
		}
	}
}

// TestHardenCoordinationComment_NeutralisesMentions verifies @-mentions are
// defanged so the posted comment cannot ping arbitrary users/teams.
func TestHardenCoordinationComment_NeutralisesMentions(t *testing.T) {
	out := hardenCoordinationComment("cc @alice and @org/team-9, thanks")
	if strings.Contains(out, "@alice") {
		t.Errorf("expected @alice to be neutralised, got %q", out)
	}
	if strings.Contains(out, "@org") {
		t.Errorf("expected @org to be neutralised, got %q", out)
	}
	// The zero-width space is inserted right after the @.
	if !strings.Contains(out, "@​alice") {
		t.Errorf("expected zero-width-space-neutralised mention, got %q", out)
	}
}

// TestHardenCoordinationComment_LengthCap verifies the comment is truncated to
// the cap with an ellipsis, bounding a runaway agent response.
func TestHardenCoordinationComment_LengthCap(t *testing.T) {
	long := strings.Repeat("x", maxCoordinationCommentLen+500)
	out := hardenCoordinationComment(long)
	if len([]rune(out)) > maxCoordinationCommentLen+1 { // +1 for the ellipsis rune
		t.Errorf("expected truncation to <= cap+ellipsis, got %d runes", len([]rune(out)))
	}
	if !strings.HasSuffix(out, "…") {
		t.Errorf("expected ellipsis suffix, got %q", out[len(out)-10:])
	}
}

// TestHardenCoordinationComment_Empty verifies whitespace-only input yields "".
func TestHardenCoordinationComment_Empty(t *testing.T) {
	if out := hardenCoordinationComment("   \n\t "); out != "" {
		t.Errorf("expected empty string for whitespace input, got %q", out)
	}
}

// TestStripCodeFences verifies triple-backticks are neutralised so an untrusted
// body cannot break out of the prompt code fence.
func TestStripCodeFences(t *testing.T) {
	in := "before ```bash\nrm -rf /\n``` after"
	out := stripCodeFences(in)
	if strings.Contains(out, "```") {
		t.Errorf("expected triple-backticks stripped, got %q", out)
	}
	if !strings.Contains(out, "before") || !strings.Contains(out, "after") {
		t.Errorf("expected surrounding text preserved, got %q", out)
	}
}

type recordingCoordinationCommentExecutor struct {
	primary  string
	fallback string
	cli      string
	prompt   string
	opts     executor.ExecOptions
}

func (r *recordingCoordinationCommentExecutor) Detect(primary, fallback string) (string, error) {
	r.primary = primary
	r.fallback = fallback
	return "codex", nil
}

func (r *recordingCoordinationCommentExecutor) ExecuteRaw(
	cli, prompt string,
	opts executor.ExecOptions,
) ([]byte, error) {
	r.cli = cli
	r.prompt = prompt
	r.opts = opts
	return []byte("  I can pick this up. Please chime in if you are already working on it.  "), nil
}

func TestCoordinationCommentGeneratorUsesShortExecutionTimeout(t *testing.T) {
	cfg := &config.Config{AI: config.AIConfig{
		Primary:  "codex",
		Fallback: "claude",
	}}
	runner := &recordingCoordinationCommentExecutor{}
	gen := &coordinationCommentGen{
		runner: runner,
		cfg:    &cfg,
		cfgMu:  &sync.Mutex{},
	}

	comment, err := gen.GenerateCoordinationComment(context.Background(), autonomous.Candidate{
		Repo:   "org/repo",
		Number: 42,
		Title:  "Coordinate safely",
	})
	if err != nil {
		t.Fatalf("GenerateCoordinationComment() error = %v", err)
	}
	if runner.primary != "codex" || runner.fallback != "claude" {
		t.Fatalf("Detect inputs = (%q, %q), want (codex, claude)", runner.primary, runner.fallback)
	}
	if runner.cli != "codex" {
		t.Fatalf("ExecuteRaw CLI = %q, want codex", runner.cli)
	}
	if runner.opts.Timeout != 5*time.Minute {
		t.Fatalf("coordination comment timeout = %v, want 5m", runner.opts.Timeout)
	}
	if !strings.Contains(runner.prompt, "Issue: #42") {
		t.Fatalf("coordination prompt does not contain the candidate issue: %q", runner.prompt)
	}
	if want := "I can pick this up. Please chime in if you are already working on it."; comment != want {
		t.Fatalf("comment = %q, want %q", comment, want)
	}
}

type recordingAutonomousStageRunner struct {
	err           error
	prNumber      int
	calls         []string
	permitMissing bool
	onStage       func(string)
}

func (r *recordingAutonomousStageRunner) RunStage(
	ctx context.Context,
	stage string,
	_ autonomous.Candidate,
) (autonomous.StageOutcome, error) {
	r.calls = append(r.calls, stage)
	if r.onStage != nil {
		r.onStage(stage)
	}
	if workgate.PermitFromContext(ctx) == nil {
		r.permitMissing = true
	}
	if r.err != nil {
		return autonomous.StageOutcome{}, r.err
	}
	outcome := autonomous.StageOutcome{Success: true}
	if stage == autonomous.StageDevelopment {
		outcome.PRNumber = r.prNumber
	}
	return outcome, nil
}

type autonomousPollerFixture struct {
	poller      *AutonomousPoller
	store       *store.Store
	gate        *workgate.Gate
	config      *config.Config
	githubCalls *atomic.Int32
	events      <-chan sse.Event
}

func newAutonomousPollerFixture(
	t *testing.T,
	issueStatus int,
	issueBody string,
	stageRunner autonomous.StageRunner,
) *autonomousPollerFixture {
	t.Helper()
	var githubCalls atomic.Int32
	githubServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		githubCalls.Add(1)
		switch {
		case r.URL.Path == "/repos/org/repo/issues":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(issueStatus)
			_, _ = w.Write([]byte(issueBody))
		case strings.HasPrefix(r.URL.Path, "/repos/org/repo/branches/"):
			http.NotFound(w, r)
		default:
			http.Error(w, "unexpected autonomous test request", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(githubServer.Close)

	st := newMemStore(t)
	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)
	events := broker.Subscribe()
	if events == nil {
		t.Fatal("subscribe autonomous events")
	}
	t.Cleanup(func() { broker.Unsubscribe(events) })

	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    config.FilterModeExclusive,
		Assignees:     []string{"heimdallm-bot"},
		DefaultAction: string(config.IssueModeReviewOnly),
	}
	cfg.Autonomous = config.AutonomousConfig{
		Enabled:    true,
		ClaimLease: "30m",
	}
	var cfgMu sync.Mutex
	gate := workgate.New(time.Minute)
	poller := &AutonomousPoller{
		ghClient: gh.NewClient("test-token", gh.WithBaseURL(githubServer.URL)),
		store:    st,
		broker:   broker,
		orch:     autonomous.NewOrchestrator(stageRunner, autonomous.NewPhaseGuard()),
		runner:   executor.New(),
		workGate: gate,
		cfg:      &cfg,
		cfgMu:    &cfgMu,
		botLogin: func() string { return "heimdallm-bot" },
		reposFn:  func() []string { return []string{"org/repo"} },
	}
	return &autonomousPollerFixture{
		poller:      poller,
		store:       st,
		gate:        gate,
		config:      cfg,
		githubCalls: &githubCalls,
		events:      events,
	}
}

const autonomousIssueFixture = `[{"id":9001,"number":42,"title":"Ship safely","body":"Add the updater","user":{"login":"alice"},"assignees":[{"login":"heimdallm-bot"}],"labels":[],"state":"open","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T11:00:00Z"}]`

func TestAutonomousPollerRunDefersBeforeGitHubAndClaimSideEffects(t *testing.T) {
	runner := &recordingAutonomousStageRunner{prNumber: 77}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, runner)
	if _, err := fixture.gate.Prepare("updater-owner"); err != nil {
		t.Fatalf("prepare update drain: %v", err)
	}

	fixture.poller.Run(context.Background())

	if got := fixture.githubCalls.Load(); got != 0 {
		t.Fatalf("GitHub calls during update drain = %d, want 0", got)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("autonomous stages during update drain = %v, want none", runner.calls)
	}
	if _, err := fixture.store.GetIssueByGithubID(9001); err == nil {
		t.Fatal("update drain persisted a candidate before returning")
	}
}

func TestAutonomousPollerRunDrivesCandidateUnderOnePermitAndClearsLease(t *testing.T) {
	runner := &recordingAutonomousStageRunner{prNumber: 77}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, runner)

	fixture.poller.Run(context.Background())

	wantStages := []string{
		autonomous.StageTriage,
		autonomous.StageRefinement,
		autonomous.StageDevelopment,
	}
	if strings.Join(runner.calls, ",") != strings.Join(wantStages, ",") {
		t.Fatalf("stages = %v, want %v", runner.calls, wantStages)
	}
	if runner.permitMissing {
		t.Fatal("stage runner did not receive the runRepo admission permit")
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("active permits after runRepo = %d, want 0", total)
	}
	storedIssue, err := fixture.store.GetIssueByGithubID(9001)
	if err != nil {
		t.Fatalf("load selected issue: %v", err)
	}
	claimed, err := fixture.store.IsIssueClaimedByAutonomous(storedIssue.ID)
	if err != nil {
		t.Fatalf("load autonomous claim flag: %v", err)
	}
	if !claimed {
		t.Fatal("selected issue was not marked claimed")
	}
	active, err := fixture.store.IsAutonomousClaimActive(storedIssue.ID, time.Now())
	if err != nil {
		t.Fatalf("load autonomous claim lease: %v", err)
	}
	if active {
		t.Fatal("successful development left its cooldown lease active")
	}
	select {
	case event := <-fixture.events:
		if event.Type != sse.EventAutonomousTaskSelected {
			t.Fatalf("first autonomous event = %q, want %q", event.Type, sse.EventAutonomousTaskSelected)
		}
	case <-time.After(time.Second):
		t.Fatal("autonomous selection event was not published")
	}
}

func TestAutonomousPollerExpectedDrainDuringDriveClearsCooldown(t *testing.T) {
	runner := &recordingAutonomousStageRunner{err: workgate.ErrDraining}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, runner)

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	storedIssue, err := fixture.store.GetIssueByGithubID(9001)
	if err != nil {
		t.Fatalf("load selected issue: %v", err)
	}
	active, err := fixture.store.IsAutonomousClaimActive(storedIssue.ID, time.Now())
	if err != nil {
		t.Fatalf("load autonomous claim lease: %v", err)
	}
	if active {
		t.Fatal("expected updater drain was charged as a failure cooldown")
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("active permits after expected drain = %d, want 0", total)
	}
}

func TestAutonomousPollerRealDriveFailureKeepsFallbackCooldown(t *testing.T) {
	runner := &recordingAutonomousStageRunner{err: errors.New("agent unavailable")}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, runner)
	fixture.config.Autonomous.ClaimLease = "not-a-duration"

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	storedIssue, err := fixture.store.GetIssueByGithubID(9001)
	if err != nil {
		t.Fatalf("load selected issue: %v", err)
	}
	active, err := fixture.store.IsAutonomousClaimActive(storedIssue.ID, time.Now())
	if err != nil {
		t.Fatalf("load autonomous claim lease: %v", err)
	}
	if !active {
		t.Fatal("real drive failure did not retain its fallback cooldown lease")
	}
}

func TestAutonomousPollerDisabledAndFetchFailureArePermitSafe(t *testing.T) {
	runner := &recordingAutonomousStageRunner{}
	fixture := newAutonomousPollerFixture(t, http.StatusInternalServerError, `{"message":"boom"}`, runner)
	fixture.config.Autonomous.Enabled = false
	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")
	if got := fixture.githubCalls.Load(); got != 0 {
		t.Fatalf("disabled repo made %d GitHub calls", got)
	}

	fixture.config.Autonomous.Enabled = true
	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")
	if got := fixture.githubCalls.Load(); got != 1 {
		t.Fatalf("fetch failure GitHub calls = %d, want 1", got)
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("fetch failure leaked %d active permits", total)
	}
}

func TestAutonomousPollerNoCandidatesReleasesPermit(t *testing.T) {
	runner := &recordingAutonomousStageRunner{}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, `[]`, runner)

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	if len(runner.calls) != 0 {
		t.Fatalf("stages for empty candidate list = %v", runner.calls)
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("empty candidate list leaked %d active permits", total)
	}
}

func TestAutonomousPollerActiveClaimLeaseSkipsCandidate(t *testing.T) {
	runner := &recordingAutonomousStageRunner{}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, runner)
	now := time.Now().UTC()
	issueID, err := fixture.store.UpsertIssue(&store.Issue{
		GithubID:  9001,
		Repo:      "org/repo",
		Number:    42,
		Title:     "Ship safely",
		Author:    "alice",
		State:     "open",
		CreatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.SetAutonomousClaimUntil(issueID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	if len(runner.calls) != 0 {
		t.Fatalf("stages for actively claimed candidate = %v", runner.calls)
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("skipped candidate leaked %d active permits", total)
	}
}

func TestAutonomousPollerSelectorFailureReleasesPermit(t *testing.T) {
	runner := &recordingAutonomousStageRunner{}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, runner)
	if err := fixture.store.Close(); err != nil {
		t.Fatal(err)
	}

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	if len(runner.calls) != 0 {
		t.Fatalf("stages after selector store failure = %v", runner.calls)
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("selector failure leaked %d active permits", total)
	}
}

const autonomousOthersIssueFixture = `[{"id":9002,"number":43,"title":"Coordinate safely","body":"Take over politely","user":{"login":"alice"},"assignees":[{"login":"alice"}],"labels":[],"state":"open","created_at":"2026-08-01T10:00:00Z","updated_at":"2026-08-01T11:00:00Z"}]`

func TestAutonomousPollerClaimFailureStopsBeforeDriveAndReleasesPermit(t *testing.T) {
	runner := &recordingAutonomousStageRunner{}
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousOthersIssueFixture, runner)
	fixture.config.GitHub.IssueTracking.Assignees = []string{"alice"}
	fixture.config.Autonomous.TakeOthersTasks = true
	fixture.config.Autonomous.ReassignOnTake = false

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	if len(runner.calls) != 0 {
		t.Fatalf("stages after claim failure = %v", runner.calls)
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("claim failure leaked %d active permits", total)
	}
}

func TestAutonomousPollerLeaseClearFailureStillReleasesPermit(t *testing.T) {
	fixture := newAutonomousPollerFixture(t, http.StatusOK, autonomousIssueFixture, nil)
	runner := &recordingAutonomousStageRunner{prNumber: 77}
	runner.onStage = func(stage string) {
		if stage == autonomous.StageDevelopment {
			_ = fixture.store.Close()
		}
	}
	fixture.poller.orch = autonomous.NewOrchestrator(runner, autonomous.NewPhaseGuard())

	fixture.poller.runRepo(context.Background(), "org/repo", "heimdallm-bot")

	if len(runner.calls) != 3 {
		t.Fatalf("stages before lease-clear failure = %v", runner.calls)
	}
	if total := fixture.gate.Status().Total(); total != 0 {
		t.Fatalf("lease-clear failure leaked %d active permits", total)
	}
}
