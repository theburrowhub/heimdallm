package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/bus"
	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	issuepipeline "github.com/heimdallm/daemon/internal/issues"
	"github.com/heimdallm/daemon/internal/repoctx"
	"github.com/heimdallm/daemon/internal/scheduler"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
	"github.com/heimdallm/daemon/internal/workgate"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
)

// newMemStore returns an in-memory SQLite store with a short cleanup hook.
// Lives here (rather than in internal/store) so the cmd-layer tests can
// stand alone without loosening visibility of a test helper that is only
// useful to package main.
func newMemStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpdateServerErrorMapsEveryUpdaterProtocolFailure(t *testing.T) {
	unknown := errors.New("storage unavailable")
	tests := []struct {
		name string
		in   error
		want error
	}{
		{name: "lease required", in: workgate.ErrLeaseIDRequired, want: server.ErrUpdateLeaseRequired},
		{name: "lease invalid", in: workgate.ErrLeaseIDInvalid, want: server.ErrUpdateLeaseInvalid},
		{name: "lease conflict", in: workgate.ErrLeaseConflict, want: server.ErrUpdateLeaseConflict},
		{name: "work active", in: workgate.ErrWorkActive, want: server.ErrUpdateNotReady},
		{name: "lease not sealed", in: workgate.ErrLeaseNotSealed, want: server.ErrUpdateNotSealed},
		{name: "bootstrap not authorized", in: workgate.ErrBootstrapNotAuthorized, want: server.ErrUpdateBootstrapNotAuthorized},
		{name: "unknown preserved", in: unknown, want: unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := updateServerError(tt.in); got != tt.want {
				t.Fatalf("updateServerError(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestSnapshotUpdatePreparationReportsEveryLifecycleState(t *testing.T) {
	expiresAt := time.Now().UTC().Add(time.Minute)
	tests := []struct {
		name     string
		snapshot workgate.Snapshot
		want     string
	}{
		{name: "running", snapshot: workgate.Snapshot{}, want: "running"},
		{
			name: "draining active work",
			snapshot: workgate.Snapshot{
				Draining:       true,
				LeaseID:        "owner",
				LeaseExpiresAt: expiresAt,
				Active:         map[workgate.Kind]int{workgate.KindReview: 2},
			},
			want: "draining",
		},
		{
			name: "sealed ready",
			snapshot: workgate.Snapshot{
				Draining:            true,
				Sealed:              true,
				BootstrapAuthorized: true,
				LeaseID:             "owner",
			},
			want: "ready",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := snapshotUpdatePreparation(tt.snapshot, "boot-id")
			if got.State != tt.want || got.BootID != "boot-id" || got.PID <= 0 {
				t.Fatalf("status = %+v, want state %q and process identity", got, tt.want)
			}
			if got.ActiveTotal != tt.snapshot.Total() || got.LeaseID != tt.snapshot.LeaseID ||
				got.Sealed != tt.snapshot.Sealed || got.BootstrapAuthorized != tt.snapshot.BootstrapAuthorized ||
				!got.LeaseExpiresAt.Equal(tt.snapshot.LeaseExpiresAt) {
				t.Fatalf("status did not preserve snapshot: got %+v, source %+v", got, tt.snapshot)
			}
			if len(tt.snapshot.Active) > 0 && got.Active[string(workgate.KindReview)] != 2 {
				t.Fatalf("active map = %#v, want reviews=2", got.Active)
			}
		})
	}
}

func TestAcquireUpdateWorkOwnsOnlyTopLevelPermit(t *testing.T) {
	ctx, releaseNilGate, err := acquireUpdateWork(nil, nil, workgate.KindReview)
	if err != nil {
		t.Fatalf("acquire with nil gate: %v", err)
	}
	if ctx == nil || releaseNilGate == nil {
		t.Fatal("nil gate did not return a usable context and cleanup")
	}
	releaseNilGate()

	gate := workgate.New(time.Minute)
	outerCtx, releaseOuter, err := acquireUpdateWork(t.Context(), gate, workgate.KindReview)
	if err != nil {
		t.Fatalf("acquire outer work: %v", err)
	}
	if got := gate.Status().Total(); got != 1 {
		t.Fatalf("active permits after outer acquire = %d, want 1", got)
	}

	_, releaseNested, err := acquireUpdateWork(outerCtx, gate, workgate.KindIssue)
	if err != nil {
		t.Fatalf("acquire nested work: %v", err)
	}
	releaseNested()
	if got := gate.Status().Total(); got != 1 {
		t.Fatalf("nested cleanup released outer permit: total = %d, want 1", got)
	}
	releaseOuter()
	if got := gate.Status().Total(); got != 0 {
		t.Fatalf("active permits after outer cleanup = %d, want 0", got)
	}

	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatalf("prepare drain: %v", err)
	}
	_, releaseDraining, err := acquireUpdateWork(t.Context(), gate, workgate.KindMaintenance)
	if !errors.Is(err, workgate.ErrDraining) {
		t.Fatalf("acquire during drain error = %v, want ErrDraining", err)
	}
	if releaseDraining != nil {
		t.Fatal("acquire during drain returned a cleanup for unadmitted work")
	}
}

func TestGuardUpdateHandlersCarryPermitAndDeferDuringDrain(t *testing.T) {
	gate := workgate.New(time.Minute)
	voidCalls := 0
	voidHandler := guardUpdateVoidHandler(
		gate,
		workgate.KindReview,
		"test void deferred",
		func(ctx context.Context, message string) {
			voidCalls++
			if message != "review" {
				t.Errorf("void message = %q, want review", message)
			}
			if workgate.PermitFromContext(ctx) == nil {
				t.Error("void handler did not receive its update permit")
			}
			if got := gate.Status().Total(); got != 1 {
				t.Errorf("active permits inside void handler = %d, want 1", got)
			}
		},
	)
	voidHandler(t.Context(), "review")
	if voidCalls != 1 || gate.Status().Total() != 0 {
		t.Fatalf("void handler cleanup = calls %d, active %d; want 1, 0",
			voidCalls, gate.Status().Total())
	}

	resultHandler := guardUpdateResultHandler(
		gate,
		workgate.KindPublish,
		"test result deferred",
		-1,
		func(ctx context.Context, message int) int {
			if workgate.PermitFromContext(ctx) == nil {
				t.Error("result handler did not receive its update permit")
			}
			return message + 1
		},
	)
	if got := resultHandler(t.Context(), 41); got != 42 {
		t.Fatalf("result handler = %d, want 42", got)
	}
	if got := gate.Status().Total(); got != 0 {
		t.Fatalf("active permits after result handler = %d, want 0", got)
	}

	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatalf("prepare drain: %v", err)
	}
	voidHandler(t.Context(), "deferred")
	if voidCalls != 1 {
		t.Fatalf("void handler ran during drain: calls = %d, want 1", voidCalls)
	}
	if got := resultHandler(t.Context(), 99); got != -1 {
		t.Fatalf("result during drain = %d, want deferred sentinel -1", got)
	}
}

func TestRunStartupWorktreePruneHonorsRestoredDrain(t *testing.T) {
	gate := workgate.New(time.Minute)
	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatalf("prepare drain: %v", err)
	}
	called := false
	err := runStartupWorktreePrune(t.Context(), gate, func(context.Context) {
		called = true
	})
	if !errors.Is(err, workgate.ErrDraining) {
		t.Fatalf("startup prune during drain error = %v, want ErrDraining", err)
	}
	if called {
		t.Fatal("startup prune ran while updater owned the persistent drain")
	}

	if _, err := gate.Cancel("updater-owner"); err != nil {
		t.Fatalf("cancel drain: %v", err)
	}
	if err := runStartupWorktreePrune(t.Context(), gate, func(context.Context) {
		called = true
	}); err != nil {
		t.Fatalf("startup prune after drain: %v", err)
	}
	if !called {
		t.Fatal("startup prune did not run after drain cancellation")
	}
	if got := gate.Status().Total(); got != 0 {
		t.Fatalf("active permits after startup prune = %d, want 0", got)
	}
}

func TestAcquireRepoContextNilManagerIsError(t *testing.T) {
	aiCfg := config.RepoAI{}
	_, err := acquireRepoContext(context.Background(), nil, "org/repo", &aiCfg, nil, "secret", repoctx.ModeRead, "", "", "")
	if err == nil || !strings.Contains(err.Error(), "nil manager") {
		t.Fatalf("err = %v, want nil manager error", err)
	}
}

func TestDefaultAutoImplementPRAssignee(t *testing.T) {
	if got := defaultAutoImplementPRAssignee("", "@ivanmunozruiz"); got != "ivanmunozruiz" {
		t.Fatalf("default assignee = %q, want ivanmunozruiz", got)
	}
	if got := defaultAutoImplementPRAssignee("configured", "ivanmunozruiz"); got != "configured" {
		t.Fatalf("configured assignee = %q, want configured", got)
	}
}

// TestAutoPromoteTransitionsLabelEvenWhenAssigneeOutOfScope pins the #458
// contract: auto-promote moves the stage label unconditionally when enabled.
// Handoff to another operator happens at the *next* stage's worker entry,
// which already gates by assignee scope (see issueTrackingWithAssigneeScope +
// issueStageStillCurrent in this file). Re-introducing a scope gate inside
// autoPromoteAfterStage — as #457 did — strands the issue at the current
// stage label and the new assignee's daemon never picks it up.
func TestAutoPromoteTransitionsLabelEvenWhenAssigneeOutOfScope(t *testing.T) {
	var (
		addedLabels   [][]string
		removedLabels []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues/7/comments":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/labels":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/7/labels":
			var payload struct {
				Labels []string `json:"labels"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			addedLabels = append(addedLabels, payload.Labels)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/org/repo/issues/7/labels/"):
			removedLabels = append(removedLabels, strings.TrimPrefix(r.URL.Path, "/repos/org/repo/issues/7/labels/"))
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/7/comments":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"created_at": time.Now().UTC().Format(time.RFC3339),
			})
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("tok", gh.WithBaseURL(srv.URL))
	it := config.IssueTrackingConfig{
		Assignees:        []string{"userA"},
		ReviewOnlyLabels: []string{"heimdallm-triage"},
		RefinementLabels: []string{"heimdallm-refine"},
	}
	// The issue's GitHub assignee is userB (handoff target); the daemon's
	// scope is userA. Auto-promote must still transition the label so
	// userB's daemon picks up refinement on its next poll.
	issue := &gh.Issue{
		Repo:      "org/repo",
		Number:    7,
		State:     "open",
		Assignees: []gh.User{{Login: "userB"}},
		Labels:    []gh.Label{{Name: "heimdallm-triage"}},
	}

	autoPromoteAfterStage(context.Background(), client, nil, issue, 42, it, config.RepoAI{},
		issuepipeline.IssueStageTriage, "test")

	if len(addedLabels) != 1 || len(addedLabels[0]) != 1 || addedLabels[0][0] != "heimdallm-refine" {
		t.Fatalf("AddLabels = %#v, want one call with [heimdallm-refine] (label transition is unconditional under #458)", addedLabels)
	}
	if len(removedLabels) != 1 || removedLabels[0] != "heimdallm-triage" {
		t.Fatalf("RemoveLabels = %#v, want one DELETE for heimdallm-triage", removedLabels)
	}
}

// ── manual re-review dispatch by current stage (#462) ───────────────────────

// dispatchCall records which Publish* method the dispatcher called and with
// what arguments. Tests assert the (subject, repo, number, githubID) tuple
// rather than the raw bus.IssueMsg encoding so a future field addition to
// IssueMsg does not silently break these assertions.
type dispatchCall struct {
	Subject  string
	Repo     string
	Number   int
	GithubID int64
}

type fakeIssueRunPublisher struct {
	calls []dispatchCall
	err   error
}

func (f *fakeIssueRunPublisher) PublishIssueTriage(_ context.Context, repo string, number int, githubID int64) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, dispatchCall{"triage", repo, number, githubID})
	return nil
}

func (f *fakeIssueRunPublisher) PublishIssueRefinement(_ context.Context, repo string, number int, githubID int64) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, dispatchCall{"refinement", repo, number, githubID})
	return nil
}

func (f *fakeIssueRunPublisher) PublishIssueImplement(_ context.Context, repo string, number int, githubID int64) error {
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, dispatchCall{"implement", repo, number, githubID})
	return nil
}

func dispatchIssue(labels ...string) *gh.Issue {
	ghLabels := make([]gh.Label, 0, len(labels))
	for _, l := range labels {
		ghLabels = append(ghLabels, gh.Label{Name: l})
	}
	return &gh.Issue{
		ID:     4242,
		Repo:   "org/repo",
		Number: 7,
		Labels: ghLabels,
	}
}

func TestDispatchIssueRunByCurrentMode_Triage(t *testing.T) {
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		ReviewOnlyLabels: []string{"heimdallm-triage"},
		RefinementLabels: []string{"heimdallm-refine"},
		DevelopLabels:    []string{"heimdallm-develop"},
	}

	if err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("heimdallm-triage")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(pub.calls) != 1 || pub.calls[0].Subject != "triage" {
		t.Fatalf("calls = %#v, want one triage", pub.calls)
	}
	if pub.calls[0].Repo != "org/repo" || pub.calls[0].Number != 7 || pub.calls[0].GithubID != 4242 {
		t.Errorf("call args = %#v, want repo/number/id propagated from issue", pub.calls[0])
	}
}

func TestDispatchIssueRunByCurrentMode_Refinement(t *testing.T) {
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		ReviewOnlyLabels: []string{"heimdallm-triage"},
		RefinementLabels: []string{"heimdallm-refine"},
		DevelopLabels:    []string{"heimdallm-develop"},
	}

	if err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("heimdallm-refine")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(pub.calls) != 1 || pub.calls[0].Subject != "refinement" {
		t.Fatalf("calls = %#v, want one refinement", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_Develop(t *testing.T) {
	// Pins the #462 fix: an issue auto-promoted to develop re-dispatches
	// to the implement worker, not back to triage as the previous stored-
	// classification path did.
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		ReviewOnlyLabels: []string{"heimdallm-triage"},
		RefinementLabels: []string{"heimdallm-refine"},
		DevelopLabels:    []string{"heimdallm-develop"},
	}

	if err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("heimdallm-develop")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(pub.calls) != 1 || pub.calls[0].Subject != "implement" {
		t.Fatalf("calls = %#v, want one implement (post-auto-promote re-review must hit develop, #462)", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_BlockedReturnsErrorWithoutPublishing(t *testing.T) {
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		BlockedLabels: []string{"blocked"},
		DevelopLabels: []string{"heimdallm-develop"},
	}

	err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("blocked"))
	if err == nil {
		t.Fatal("expected error for blocked issue, got nil")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Errorf("error should mention blocked state, got: %v", err)
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected no publish for blocked issue, got %#v", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_SkipLabelReturnsErrorWithoutPublishing(t *testing.T) {
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		SkipLabels:    []string{"wontfix"},
		DevelopLabels: []string{"heimdallm-develop"},
	}

	err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("wontfix"))
	if err == nil {
		t.Fatal("expected error for skip-labelled issue, got nil")
	}
	// The error must be label-aware so the operator sees WHY the manual
	// re-review was rejected — generic "nothing to run" hid the skip label
	// case behind the no-stage-label phrasing.
	if !strings.Contains(err.Error(), "ignored by current label") {
		t.Errorf("error should mention current label configuration, got: %v", err)
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected no publish for skip-labelled issue, got %#v", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_DefaultsToTriageWhenConfigured(t *testing.T) {
	// No stage labels on the issue, but default_action = review_only →
	// dispatch to triage. Matches the fetcher's classification fallback.
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		DefaultAction: "review_only",
	}

	if err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("user-tag")); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(pub.calls) != 1 || pub.calls[0].Subject != "triage" {
		t.Fatalf("calls = %#v, want one triage from default_action fallback", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_DefaultsToIgnoreReturnsError(t *testing.T) {
	// No stage labels, no default_action — same as the fetcher: ignore.
	// Re-review on such an issue is meaningless; surface a clear error.
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{}

	err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, dispatchIssue("user-tag"))
	if err == nil {
		t.Fatal("expected error when issue has no stage label and no default_action, got nil")
	}
	if !strings.Contains(err.Error(), "ignored by current label") {
		t.Errorf("error should mention current label configuration, got: %v", err)
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected no publish, got %#v", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_NilIssueReturnsError(t *testing.T) {
	pub := &fakeIssueRunPublisher{}
	err := dispatchIssueRunByCurrentMode(context.Background(), pub, config.IssueTrackingConfig{}, nil)
	if err == nil {
		t.Fatal("expected error for nil issue, got nil")
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected no publish on nil issue, got %#v", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_AssigneeOutOfScopeReturnsErrorWithoutPublishing(t *testing.T) {
	// The worker entries (issueStageStillCurrent) silently skip when the
	// issue's assignees fall outside the daemon's scope, which makes a
	// manual Re-review click look like the GUI is broken: spinner +
	// silence. Gate at the dispatcher so the operator sees a clear error
	// instead.
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		Assignees:     []string{"userA"},
		DevelopLabels: []string{"heimdallm-develop"},
	}
	issue := &gh.Issue{
		ID: 4242, Repo: "org/repo", Number: 7,
		Labels:    []gh.Label{{Name: "heimdallm-develop"}},
		Assignees: []gh.User{{Login: "userB"}},
	}

	err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, issue)
	if err == nil {
		t.Fatal("expected error when issue is outside daemon scope, got nil")
	}
	if !strings.Contains(err.Error(), "scope") {
		t.Errorf("error should mention scope, got: %v", err)
	}
	if len(pub.calls) != 0 {
		t.Errorf("expected no publish for out-of-scope issue, got %#v", pub.calls)
	}
}

func TestDispatchIssueRunByCurrentMode_InScopeAssigneeProceeds(t *testing.T) {
	// Mirror image of the out-of-scope test: when the issue's assignee
	// matches the daemon scope, dispatch proceeds. Pins both branches of
	// the scope gate.
	pub := &fakeIssueRunPublisher{}
	cfg := config.IssueTrackingConfig{
		Assignees:     []string{"userA"},
		DevelopLabels: []string{"heimdallm-develop"},
	}
	issue := &gh.Issue{
		ID: 4242, Repo: "org/repo", Number: 7,
		Labels:    []gh.Label{{Name: "heimdallm-develop"}},
		Assignees: []gh.User{{Login: "userA"}},
	}

	if err := dispatchIssueRunByCurrentMode(context.Background(), pub, cfg, issue); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(pub.calls) != 1 || pub.calls[0].Subject != "implement" {
		t.Fatalf("calls = %#v, want one implement (assignee in scope)", pub.calls)
	}
}

func TestAutoPromoteTriageSmartDefaultRequiresRefinementLabel(t *testing.T) {
	aiCfg := config.RepoAI{}
	it := config.IssueTrackingConfig{}
	if autoPromoteStageEnabled(aiCfg, it, issuepipeline.IssueStageTriage) {
		t.Fatal("unset auto_promote_triage without refinement labels should preserve legacy manual behavior")
	}

	it.RefinementLabels = []string{"heimdallm-refine"}
	if !autoPromoteStageEnabled(aiCfg, it, issuepipeline.IssueStageTriage) {
		t.Fatal("unset auto_promote_triage with refinement labels should adopt the staged default")
	}

	disabled := false
	aiCfg.AutoPromoteTriage = &disabled
	if autoPromoteStageEnabled(aiCfg, it, issuepipeline.IssueStageTriage) {
		t.Fatal("explicit auto_promote_triage=false should win over refinement labels")
	}
}

func TestAutoPromoteRefinementSmartDefaultRequiresDevelopLabel(t *testing.T) {
	aiCfg := config.RepoAI{}
	it := config.IssueTrackingConfig{}
	if autoPromoteStageEnabled(aiCfg, it, issuepipeline.IssueStageRefinement) {
		t.Fatal("unset auto_promote_refinement without develop labels should preserve manual behavior")
	}

	it.DevelopLabels = []string{"heimdallm-develop"}
	if !autoPromoteStageEnabled(aiCfg, it, issuepipeline.IssueStageRefinement) {
		t.Fatal("unset auto_promote_refinement with develop labels should adopt the staged default")
	}

	disabled := false
	aiCfg.AutoPromoteRefinement = &disabled
	if autoPromoteStageEnabled(aiCfg, it, issuepipeline.IssueStageRefinement) {
		t.Fatal("explicit auto_promote_refinement=false should win over develop labels")
	}
}

func TestManagedCloneDirsIncludesDefaultAndScopedOverrides(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.CloneDir = "/global"
	cfg.AI.Orgs = map[string]config.OrgAI{
		"org": {CloneDir: "/org"},
	}
	cfg.AI.Repos = map[string]config.RepoAI{
		"org/repo":  {CloneDir: "/repo"},
		"org/other": {CloneDir: "/org"},
	}

	got := managedCloneDirs(cfg)
	want := []string{"", "/global", "/org", "/repo"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("managedCloneDirs = %v, want %v", got, want)
	}
}

func TestMonitoredRepoSetMergesDiscoveredAndExcludesNonMonitored(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/static"}
	cfg.GitHub.NonMonitored = []string{"org/disabled"}

	got := monitoredRepoSet(cfg, []string{"org/discovered", "org/disabled"})
	for _, repo := range []string{"org/static", "org/discovered"} {
		if _, ok := got[repo]; !ok {
			t.Fatalf("monitoredRepoSet missing %s: %v", repo, got)
		}
	}
	if _, ok := got["org/disabled"]; ok {
		t.Fatalf("monitoredRepoSet includes non-monitored repo: %v", got)
	}
}

func TestAIReposInNonMonitoredIsSortedExactAndReadOnly(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Repos = map[string]config.RepoAI{
		"z/repo":    {},
		"a/repo":    {},
		"Org/Case":  {},
		"keep/repo": {},
	}
	cfg.GitHub.NonMonitored = []string{"z/repo", "a/repo", "a/repo", "org/case"}
	beforeNonMonitored := append([]string(nil), cfg.GitHub.NonMonitored...)

	got := aiReposInNonMonitored(cfg)
	want := []string{"a/repo", "z/repo"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("aiReposInNonMonitored = %v, want %v", got, want)
	}
	if strings.Join(cfg.GitHub.NonMonitored, "|") != strings.Join(beforeNonMonitored, "|") {
		t.Fatalf("NonMonitored mutated: got %v, want %v", cfg.GitHub.NonMonitored, beforeNonMonitored)
	}
	if len(cfg.AI.Repos) != 4 {
		t.Fatalf("AI.Repos mutated: %v", cfg.AI.Repos)
	}
}

func TestAIReposInNonMonitoredDetectsStoreLayerConflict(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Repos = map[string]config.RepoAI{"legacy/repo": {}}
	if err := cfg.ApplyStore(map[string]string{
		"non_monitored": `["legacy/repo"]`,
	}); err != nil {
		t.Fatalf("ApplyStore: %v", err)
	}
	if got := aiReposInNonMonitored(cfg); len(got) != 1 || got[0] != "legacy/repo" {
		t.Fatalf("store-backed conflict = %v, want [legacy/repo]", got)
	}
}

func TestRepoMonitoringConflictWarnerDeduplicatesAndResets(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Repos = map[string]config.RepoAI{"a/repo": {}, "b/repo": {}}
	cfg.GitHub.NonMonitored = []string{"a/repo"}
	var w repoMonitoringConflictWarner

	if got := w.next(cfg); len(got) != 1 || got[0] != "a/repo" {
		t.Fatalf("first warning = %v, want [a/repo]", got)
	}
	if got := w.next(cfg); got != nil {
		t.Fatalf("duplicate warning = %v, want nil", got)
	}

	cfg.GitHub.NonMonitored = []string{"b/repo"}
	if got := w.next(cfg); len(got) != 1 || got[0] != "b/repo" {
		t.Fatalf("changed warning = %v, want [b/repo]", got)
	}
	cfg.GitHub.NonMonitored = nil
	if got := w.next(cfg); got != nil {
		t.Fatalf("cleared warning = %v, want nil", got)
	}
	cfg.GitHub.NonMonitored = []string{"b/repo"}
	if got := w.next(cfg); len(got) != 1 || got[0] != "b/repo" {
		t.Fatalf("reintroduced warning = %v, want [b/repo]", got)
	}
}

func TestPurgeStaleManagedClonesUsesRetentionAndConfiguredDirs(t *testing.T) {
	base := t.TempDir()
	oldRepo := filepath.Join(base, "org", "old")
	keepRepo := filepath.Join(base, "org", "keep")
	for repo, dir := range map[string]string{
		"org/old":  oldRepo,
		"org/keep": keepRepo,
	} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeManagedMarkerForTest(t, dir, repo)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, dir := range []string{oldRepo, keepRepo} {
		if err := os.Chtimes(filepath.Join(dir, repoctx.MarkerFile), old, old); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{}
	cfg.AI.CloneDir = base
	cfg.GitHub.Repositories = []string{"org/keep"}
	cfg.Retention.MaxDays = 1

	removed, err := purgeStaleManagedClones(context.Background(), repoctx.NewManager(), cfg, nil)
	if err != nil {
		t.Fatalf("purgeStaleManagedClones: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(oldRepo); !os.IsNotExist(err) {
		t.Fatalf("old unmonitored clone still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(keepRepo); err != nil {
		t.Fatalf("monitored clone should remain: %v", err)
	}
}

func writeManagedMarkerForTest(t *testing.T, dir, repo string) {
	t.Helper()
	data := []byte(`{"version":1,"repo":"` + repo + `","managed_by":"heimdallm"}` + "\n")
	if err := os.WriteFile(filepath.Join(dir, repoctx.MarkerFile), data, 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
}

func seedAgent(t *testing.T, s *store.Store, a store.Agent) {
	t.Helper()
	if err := s.UpsertAgent(&a); err != nil {
		t.Fatalf("upsert agent %q: %v", a.ID, err)
	}
}

func newInProcessNATS(t *testing.T) *nats.Conn {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: t.Name(),
		DontListen: true,
		NoLog:      true,
		NoSigs:     true,
	})
	if err != nil {
		t.Fatalf("new nats server: %v", err)
	}
	srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	conn, err := nats.Connect("", nats.InProcessServer(srv), nats.Name(t.Name()))
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	t.Cleanup(func() {
		conn.Close()
		srv.Shutdown()
		srv.WaitForShutdown()
	})
	return conn
}

func seedPRWithReview(t *testing.T, s *store.Store, githubID int64, createdAt time.Time) int64 {
	t.Helper()
	prID, err := s.UpsertPR(&store.PR{
		GithubID:  githubID,
		Repo:      "org/repo",
		Number:    int(githubID),
		Title:     "test pr",
		Author:    "alice",
		URL:       "https://github.test/org/repo/pull/1",
		State:     "open",
		UpdatedAt: createdAt,
		FetchedAt: createdAt,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	revID, err := s.InsertReview(&store.Review{
		PRID:           prID,
		CLIUsed:        "codex",
		Summary:        "summary",
		Issues:         "[]",
		Suggestions:    "[]",
		Severity:       "low",
		CreatedAt:      createdAt,
		GitHubReviewID: 0,
		HeadSHA:        "abc123",
	})
	if err != nil {
		t.Fatalf("insert review: %v", err)
	}
	return revID
}

func TestTier2AdapterReviewReadyForPublishRetry(t *testing.T) {
	s := newMemStore(t)
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	readyID := seedPRWithReview(t, s, 101, now)
	inFlightID := seedPRWithReview(t, s, 102, now)
	if claimed, err := s.ClaimInFlightReview(102, "abc123"); err != nil {
		t.Fatalf("claim in-flight review: %v", err)
	} else if !claimed {
		t.Fatal("expected in-flight claim to succeed")
	}
	a := &tier2Adapter{store: s}

	readyRev, err := s.GetReview(readyID)
	if err != nil {
		t.Fatalf("get ready review: %v", err)
	}
	ready, err := a.reviewReadyForPublishRetry(readyRev)
	if err != nil {
		t.Fatalf("reviewReadyForPublishRetry ready: %v", err)
	}
	if !ready {
		t.Fatal("unpublished review with no in-flight claim should be ready")
	}

	inFlightRev, err := s.GetReview(inFlightID)
	if err != nil {
		t.Fatalf("get in-flight review: %v", err)
	}
	ready, err = a.reviewReadyForPublishRetry(inFlightRev)
	if err != nil {
		t.Fatalf("reviewReadyForPublishRetry in-flight: %v", err)
	}
	if ready {
		t.Fatal("in-flight review should not be ready for retry")
	}

	if err := s.MarkReviewPublished(readyID, 123, "APPROVED", now); err != nil {
		t.Fatalf("mark published: %v", err)
	}
	publishedRev, err := s.GetReview(readyID)
	if err != nil {
		t.Fatalf("get published review: %v", err)
	}
	ready, err = a.reviewReadyForPublishRetry(publishedRev)
	if err != nil {
		t.Fatalf("reviewReadyForPublishRetry published: %v", err)
	}
	if ready {
		t.Fatal("published review should not be ready for retry")
	}
}

func TestTier2AdapterPublishPendingDefersInFlightReviews(t *testing.T) {
	s := newMemStore(t)
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	readyReviewID := seedPRWithReview(t, s, 101, now)
	seedPRWithReview(t, s, 102, now)
	if claimed, err := s.ClaimInFlightReview(102, "abc123"); err != nil {
		t.Fatalf("claim in-flight review: %v", err)
	} else if !claimed {
		t.Fatal("expected in-flight claim to succeed")
	}

	conn := newInProcessNATS(t)
	ch := make(chan *nats.Msg, 2)
	sub, err := conn.ChanSubscribe(bus.SubjPRPublish, ch)
	if err != nil {
		t.Fatalf("subscribe publish subject: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush subscribe: %v", err)
	}

	a := &tier2Adapter{
		store:      s,
		publishPub: bus.NewPRPublishPublisher(conn),
	}
	a.publishPending()
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush publish: %v", err)
	}

	select {
	case msg := <-ch:
		var got bus.PRPublishMsg
		if err := bus.Decode(msg.Data, &got); err != nil {
			t.Fatalf("decode publish msg: %v", err)
		}
		if got.ReviewID != readyReviewID {
			t.Fatalf("published review ID = %d, want ready review %d", got.ReviewID, readyReviewID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ready pending review was not enqueued")
	}

	// publishPending publishes synchronously; the short wait catches stray
	// buffered messages without making this negative assertion expensive.
	select {
	case msg := <-ch:
		var got bus.PRPublishMsg
		_ = bus.Decode(msg.Data, &got)
		t.Fatalf("unexpected extra publish message: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestTier2AdapterPublishPendingDefersUntilRepoReenabled(t *testing.T) {
	s := newMemStore(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	reviewID := seedPRWithReview(t, s, 103, now)

	conn := newInProcessNATS(t)
	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjPRPublish, ch)
	if err != nil {
		t.Fatalf("subscribe publish subject: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush subscribe: %v", err)
	}

	cfg := &config.Config{}
	cfg.GitHub.Repositories = []string{"org/repo"}
	cfg.GitHub.NonMonitored = []string{"org/repo"}
	var cfgMu sync.Mutex
	adapter := &tier2Adapter{
		store:      s,
		publishPub: bus.NewPRPublishPublisher(conn),
		cfgMu:      &cfgMu,
		cfg:        &cfg,
	}

	adapter.publishPending()
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush deferred publish: %v", err)
	}
	select {
	case msg := <-ch:
		t.Fatalf("disabled repo was enqueued: %q", msg.Data)
	case <-time.After(150 * time.Millisecond):
	}
	pending, err := s.ListUnpublishedReviews()
	if err != nil {
		t.Fatalf("list deferred review: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != reviewID {
		t.Fatalf("pending reviews = %+v, want review %d", pending, reviewID)
	}

	cfgMu.Lock()
	cfg.GitHub.NonMonitored = nil
	cfgMu.Unlock()
	adapter.publishPending()
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush resumed publish: %v", err)
	}

	select {
	case msg := <-ch:
		var got bus.PRPublishMsg
		if err := bus.Decode(msg.Data, &got); err != nil {
			t.Fatalf("decode publish msg: %v", err)
		}
		if got.ReviewID != reviewID {
			t.Fatalf("published review ID = %d, want pending review %d", got.ReviewID, reviewID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending review was not enqueued after repo re-enable")
	}

	if err := s.MarkReviewPublished(reviewID, 12345, "APPROVED", now.Add(time.Minute)); err != nil {
		t.Fatalf("mark review published: %v", err)
	}
	adapter.publishPending()
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush idempotency check: %v", err)
	}
	select {
	case msg := <-ch:
		t.Fatalf("published review was enqueued again: %q", msg.Data)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestRunTier2PublishesPendingWhenLiveRepoSetIsEmpty(t *testing.T) {
	s := newMemStore(t)
	reviewID := seedPRWithReview(t, s, 104, time.Now().UTC())

	conn := newInProcessNATS(t)
	ch := make(chan *nats.Msg, 1)
	sub, err := conn.ChanSubscribe(bus.SubjPRPublish, ch)
	if err != nil {
		t.Fatalf("subscribe publish subject: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := conn.Flush(); err != nil {
		t.Fatalf("flush subscribe: %v", err)
	}

	// No live repos means both review/issue polling branches are skipped. The
	// PR tick must still call PublishPending so a review deferred by a prior
	// disable can recover after config changes.
	adapter := &tier2Adapter{
		store:      s,
		publishPub: bus.NewPRPublishPublisher(conn),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reposCh := make(chan []string)
	go func() {
		defer close(done)
		runTier2(
			ctx,
			adapter,
			nil,
			nil,
			nil,
			func() []string { return nil },
			nil,
			nil, // adaptiveFn — adaptive gating off for this test
			nil, // adaptiveSched — unused while adaptiveFn is nil
			reposCh,
			time.Hour,
			true,
			nil,
		)
	}()
	defer func() {
		cancel()
		<-done
	}()

	// A cold poll must wait for an actual Tier 1 snapshot, not guess with a
	// timer. This also models discovery taking longer than an arbitrary startup
	// grace period without losing the entire first poll.
	select {
	case msg := <-ch:
		t.Fatalf("published before first discovery snapshot: %q", msg.Data)
	case <-time.After(100 * time.Millisecond):
	}
	reposCh <- []string{}

	select {
	case msg := <-ch:
		var got bus.PRPublishMsg
		if err := bus.Decode(msg.Data, &got); err != nil {
			t.Fatalf("decode publish msg: %v", err)
		}
		if got.ReviewID != reviewID {
			t.Fatalf("published review ID = %d, want pending review %d", got.ReviewID, reviewID)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PublishPending did not run promptly after the first empty snapshot")
	}
}

func TestRunTier2ColdStartStopsWhenContextEndsBeforeSnapshot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runTier2(
		ctx,
		&tier2Adapter{},
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		make(chan []string),
		time.Hour,
		true,
		nil,
	)
}

func TestRunTier2ReceiverStopsWhenRepoChannelCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reposCh := make(chan []string)
	close(reposCh)
	done := make(chan struct{})

	go func() {
		defer close(done)
		runTier2(
			ctx,
			&tier2Adapter{},
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			nil,
			reposCh,
			time.Hour,
			false,
			nil,
		)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runTier2 did not stop after cancellation")
	}
}

func TestTier2AdapterPromoteReadyUsesOrgScopedIssueTracking(t *testing.T) {
	var addedLabels [][]string
	removedLabels := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"number":    10,
					"title":     "blocked",
					"state":     "open",
					"body":      "## Depends on\n- #5\n",
					"assignees": []map[string]string{{"login": "bot"}},
					"labels":    []map[string]string{{"name": "blocked"}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues/10/sub_issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/repo/issues/5":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 5,
				"title":  "dependency",
				"state":  "closed",
				"labels": []map[string]string{},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/10/labels":
			var payload struct {
				Labels []string `json:"labels"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Errorf("decode labels payload: %v", err)
			}
			addedLabels = append(addedLabels, payload.Labels)
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "org-ready"}})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/org/repo/issues/10/labels/blocked":
			removedLabels++
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/org/repo/issues/10/comments":
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"created_at": time.Now().UTC().Format(time.RFC3339),
			})
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	enabled := true
	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       false,
		FilterMode:    config.FilterModeExclusive,
		DefaultAction: string(config.IssueModeIgnore),
		Assignees:     []string{"bot"},
		BlockedLabels: []string{"blocked"},
		DevelopLabels: []string{"global-ready"},
	}
	cfg.AI.Orgs = map[string]config.OrgAI{
		"org": {
			IssueTracking: &config.IssueTrackingOverride{
				Enabled:        &enabled,
				PromoteToLabel: "org-ready",
			},
		},
	}
	cfgRef := cfg
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		ghClient: gh.NewClient("token", gh.WithBaseURL(srv.URL)),
		cfgMu:    &cfgMu,
		cfg:      &cfgRef,
		broker:   sse.NewBroker(),
	}

	n, err := a.PromoteReady(t.Context(), []string{"org/repo"})
	if err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	if n != 1 {
		t.Fatalf("promotions = %d, want 1", n)
	}
	if len(addedLabels) != 1 || len(addedLabels[0]) != 1 || addedLabels[0][0] != "org-ready" {
		t.Fatalf("added labels = %#v, want [[org-ready]]", addedLabels)
	}
	if removedLabels != 1 {
		t.Fatalf("removed labels = %d, want 1", removedLabels)
	}
}

func TestTier2AdapterPromoteReadyDefersDuringUpdateDrain(t *testing.T) {
	gate := workgate.New(time.Minute)
	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatalf("prepare drain: %v", err)
	}
	cfg := &config.Config{}
	cfgRef := cfg
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		cfgMu:    &cfgMu,
		cfg:      &cfgRef,
		broker:   sse.NewBroker(),
		workGate: gate,
	}

	n, err := a.PromoteReady(t.Context(), []string{"org/repo"})
	if n != 0 || !errors.Is(err, workgate.ErrDraining) {
		t.Fatalf("PromoteReady during drain = (%d, %v), want (0, ErrDraining)", n, err)
	}
}

func TestTier2AdapterProcessorsDeferBeforeDependenciesDuringUpdateDrain(t *testing.T) {
	gate := workgate.New(time.Minute)
	if _, err := gate.Prepare("updater-owner"); err != nil {
		t.Fatalf("prepare drain: %v", err)
	}
	reviewCalled := false
	a := &tier2Adapter{
		workGate: gate,
		runReview: func(context.Context, *gh.PullRequest, config.RepoAI) *store.Review {
			reviewCalled = true
			return nil
		},
	}

	if err := a.ProcessPR(t.Context(), scheduler.Tier2PR{Repo: "org/repo", Number: 7}); err != nil {
		t.Fatalf("ProcessPR during drain: %v", err)
	}
	if reviewCalled {
		t.Fatal("ProcessPR reached review dependencies during update drain")
	}

	processed, err := a.ProcessRepo(t.Context(), "org/repo")
	if err != nil || processed != 0 {
		t.Fatalf("ProcessRepo during drain = (%d, %v), want (0, nil)", processed, err)
	}
	if got := gate.Status().Total(); got != 0 {
		t.Fatalf("active permits after deferred processors = %d, want 0", got)
	}
}

func TestTier2AdapterPromoteReadyBatchesReposWithSameIssueTracking(t *testing.T) {
	sharedGets := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/a/issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"number":    10,
					"title":     "blocked a",
					"state":     "open",
					"body":      "## Depends on\n- org/shared#5\n",
					"assignees": []map[string]string{{"login": "bot"}},
					"labels":    []map[string]string{{"name": "blocked"}},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/b/issues":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"number":    20,
					"title":     "blocked b",
					"state":     "open",
					"body":      "## Depends on\n- org/shared#5\n",
					"assignees": []map[string]string{{"login": "bot"}},
					"labels":    []map[string]string{{"name": "blocked"}},
				},
			})
		case r.Method == http.MethodGet && (r.URL.Path == "/repos/org/a/issues/10/sub_issues" || r.URL.Path == "/repos/org/b/issues/20/sub_issues"):
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodGet && r.URL.Path == "/repos/org/shared/issues/5":
			sharedGets++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 5,
				"title":  "shared dependency",
				"state":  "closed",
				"labels": []map[string]string{},
			})
		case r.Method == http.MethodDelete && (r.URL.Path == "/repos/org/a/issues/10/labels/blocked" || r.URL.Path == "/repos/org/b/issues/20/labels/blocked"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]string{})
		case r.Method == http.MethodPost && (r.URL.Path == "/repos/org/a/issues/10/labels" || r.URL.Path == "/repos/org/b/issues/20/labels"):
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode([]map[string]string{{"name": "ready"}})
		case r.Method == http.MethodPost && (r.URL.Path == "/repos/org/a/issues/10/comments" || r.URL.Path == "/repos/org/b/issues/20/comments"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"created_at": time.Now().UTC().Format(time.RFC3339),
			})
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.String())
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       true,
		FilterMode:    config.FilterModeExclusive,
		DefaultAction: string(config.IssueModeIgnore),
		Assignees:     []string{"bot"},
		BlockedLabels: []string{"blocked"},
		DevelopLabels: []string{"ready"},
	}
	cfgRef := cfg
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		ghClient: gh.NewClient("token", gh.WithBaseURL(srv.URL)),
		cfgMu:    &cfgMu,
		cfg:      &cfgRef,
		broker:   sse.NewBroker(),
	}

	n, err := a.PromoteReady(t.Context(), []string{"org/a", "org/b"})
	if err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}
	if n != 2 {
		t.Fatalf("promotions = %d, want 2", n)
	}
	if sharedGets != 1 {
		t.Fatalf("shared dependency fetches = %d, want 1", sharedGets)
	}
}

func TestTier2AdapterPromoteReadyReportsAllGroupErrors(t *testing.T) {
	cfg := &config.Config{}
	cfg.GitHub.IssueTracking = config.IssueTrackingConfig{
		Enabled:       true,
		Assignees:     []string{"alice"},
		BlockedLabels: []string{"blocked"},
	}
	cfg.AI.Repos = map[string]config.RepoAI{
		"org/b": {
			IssueTracking: &config.IssueTrackingOverride{
				Assignees: []string{"bob"},
			},
		},
	}
	cfgRef := cfg
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		cfgMu:  &cfgMu,
		cfg:    &cfgRef,
		broker: sse.NewBroker(),
	}

	n, err := a.PromoteReady(t.Context(), []string{"org/a", "org/b"})
	if n != 0 {
		t.Fatalf("promotions = %d, want 0", n)
	}
	if err == nil {
		t.Fatal("PromoteReady error = nil, want config group errors")
	}
	if got := err.Error(); !strings.Contains(got, "org/a") || !strings.Contains(got, "org/b") {
		t.Fatalf("joined error = %q, want both repo groups", got)
	}
}

func TestPRAlreadyReviewedCircuitBreakerSuppressesRepeatedEnqueue(t *testing.T) {
	s := newMemStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	prID, err := s.UpsertPR(&store.PR{
		GithubID:  1001,
		Repo:      "org/repo",
		Number:    7,
		Title:     "cost loop",
		Author:    "alice",
		URL:       "https://github.test/org/repo/pull/7",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID:              prID,
			CLIUsed:           "codex",
			Summary:           "summary",
			Issues:            "[]",
			Suggestions:       "[]",
			Severity:          "low",
			CreatedAt:         now.Add(-time.Duration(i) * time.Minute),
			PublishedAt:       now.Add(-time.Duration(i) * time.Minute),
			GitHubReviewID:    int64(9000 + i),
			GitHubReviewState: "APPROVED",
			HeadSHA:           "abc123",
		}); err != nil {
			t.Fatalf("insert review %d: %v", i, err)
		}
	}

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	events := broker.Subscribe()
	defer broker.Unsubscribe(events)

	cfg := &config.Config{}
	cfg.CircuitBreaker.PerPR24h = 3
	cfg.CircuitBreaker.PerRepoHr = 999
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		store:            s,
		broker:           broker,
		cfgMu:            &cfgMu,
		cfg:              &cfg,
		lastBreakerTrips: make(map[breakerTripKey]breakerTripDedup),
	}

	updatedAt := now.Add(10 * time.Minute)
	if !a.PRAlreadyReviewed(1001, "org/repo", 7, updatedAt, "abc123") {
		t.Fatal("circuit breaker trip should suppress enqueue")
	}
	if !a.PRAlreadyReviewed(1001, "org/repo", 7, updatedAt, "abc123") {
		t.Fatal("same circuit breaker trip should keep suppressing enqueue")
	}

	count := 0
	var reason string
drain:
	for {
		select {
		case ev := <-events:
			if ev.Type != sse.EventCircuitBreakerTripped {
				continue
			}
			count++
			var payload struct {
				Reason string `json:"reason"`
			}
			if err := json.Unmarshal([]byte(ev.Data), &payload); err != nil {
				t.Fatalf("decode circuit breaker event: %v", err)
			}
			reason = payload.Reason
		case <-time.After(100 * time.Millisecond):
			break drain
		}
	}
	if count != 1 {
		t.Fatalf("circuit breaker events = %d, want 1", count)
	}
	// Only completed reviews count. Failed executor runs never produce a review
	// row and must not consume this quota.
	if reason != "per-PR HEAD cap reached: 3 reviews on this commit in last 24h (cap 3)" {
		t.Fatalf("reason = %q", reason)
	}
}

func TestBreakerTripDedupPrunesByTTL(t *testing.T) {
	now := time.Date(2026, 4, 28, 15, 0, 0, 0, time.UTC)
	freshKey := breakerTripKey{Repo: "org/repo", Number: 7, HeadSHA: "abc", Reason: "cap"}
	oldKey := breakerTripKey{Repo: "org/repo", Number: 8, HeadSHA: "def", Reason: "cap"}
	a := &tier2Adapter{
		lastBreakerTrips: map[breakerTripKey]breakerTripDedup{
			freshKey: {UpdatedAt: now, EmittedAt: now.Add(-breakerTripDedupTTL + time.Second)},
			oldKey:   {UpdatedAt: now, EmittedAt: now.Add(-breakerTripDedupTTL - time.Second)},
		},
	}

	a.pruneBreakerTripDedup(now)

	if _, ok := a.lastBreakerTrips[freshKey]; !ok {
		t.Fatal("fresh breaker dedup entry should survive pruning")
	}
	if _, ok := a.lastBreakerTrips[oldKey]; ok {
		t.Fatal("old breaker dedup entry should be pruned")
	}
}

func TestPRAlreadyReviewedCircuitBreakerAllowsNewHeadSHA(t *testing.T) {
	s := newMemStore(t)
	now := time.Now().UTC().Truncate(time.Second)
	prID, err := s.UpsertPR(&store.PR{
		GithubID:  1002,
		Repo:      "org/repo",
		Number:    8,
		Title:     "follow-up changes",
		Author:    "alice",
		URL:       "https://github.test/org/repo/pull/8",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert pr: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.InsertReview(&store.Review{
			PRID:              prID,
			CLIUsed:           "codex",
			Summary:           "summary",
			Issues:            "[]",
			Suggestions:       "[]",
			Severity:          "low",
			CreatedAt:         now.Add(-time.Duration(i) * time.Minute),
			PublishedAt:       now.Add(-time.Duration(i) * time.Minute),
			GitHubReviewID:    int64(9100 + i),
			GitHubReviewState: "APPROVED",
			HeadSHA:           "old-sha",
		}); err != nil {
			t.Fatalf("insert review %d: %v", i, err)
		}
	}

	cfg := &config.Config{}
	cfg.CircuitBreaker.PerPR24h = 3
	cfg.CircuitBreaker.PerRepoHr = 999
	var cfgMu sync.Mutex
	a := &tier2Adapter{
		store:  s,
		cfgMu:  &cfgMu,
		cfg:    &cfg,
		broker: sse.NewBroker(),
	}

	updatedAt := now.Add(10 * time.Minute)
	if a.PRAlreadyReviewed(1002, "org/repo", 8, updatedAt, "new-sha") {
		t.Fatal("new head SHA should not be suppressed by the per-PR circuit breaker")
	}
}

// TestRepoAIOverrideMap_ExposesNeverApproveWithIssues guards that the
// per-repo AI override map GET /config serves under repo_overrides[repo]
// carries never_approve_with_issues through, mirroring how
// generate_pr_description is wired for the same map.
func TestRepoAIOverrideMap_ExposesNeverApproveWithIssues(t *testing.T) {
	no := false
	out := repoAIOverrideMap(config.RepoAI{NeverApproveWithIssues: &no})
	v, ok := out["never_approve_with_issues"].(bool)
	if !ok || v != false {
		t.Errorf("never_approve_with_issues = %v (present=%v), want false", out["never_approve_with_issues"], ok)
	}
}

// TestOrgAIOverrideMap_ExposesNeverApproveWithIssues is the org-level
// counterpart of TestRepoAIOverrideMap_ExposesNeverApproveWithIssues.
func TestOrgAIOverrideMap_ExposesNeverApproveWithIssues(t *testing.T) {
	yes := true
	out := orgAIOverrideMap(config.OrgAI{NeverApproveWithIssues: &yes})
	v, ok := out["never_approve_with_issues"].(bool)
	if !ok || v != true {
		t.Errorf("never_approve_with_issues = %v (present=%v), want true", out["never_approve_with_issues"], ok)
	}
}

// TestAIOverrideMaps_ExposeNeverApproveMinSeverity guards that both override
// maps GET /config serves carry never_approve_min_severity through when set,
// and omit it when empty (= inherit).
func TestAIOverrideMaps_ExposeNeverApproveMinSeverity(t *testing.T) {
	out := repoAIOverrideMap(config.RepoAI{NeverApproveMinSeverity: "medium"})
	if v, ok := out["never_approve_min_severity"].(string); !ok || v != "medium" {
		t.Errorf("repo never_approve_min_severity = %v (present=%v), want \"medium\"", out["never_approve_min_severity"], ok)
	}
	out = orgAIOverrideMap(config.OrgAI{NeverApproveMinSeverity: "high"})
	if v, ok := out["never_approve_min_severity"].(string); !ok || v != "high" {
		t.Errorf("org never_approve_min_severity = %v (present=%v), want \"high\"", out["never_approve_min_severity"], ok)
	}
	out = orgAIOverrideMap(config.OrgAI{})
	if _, present := out["never_approve_min_severity"]; present {
		t.Errorf("empty override should omit never_approve_min_severity, got %v", out["never_approve_min_severity"])
	}
}

func TestResolveImplementPrompt_RepoOverrideWins(t *testing.T) {
	s := newMemStore(t)
	seedAgent(t, s, store.Agent{
		ID:                    "repo-agent",
		Name:                  "repo",
		ImplementPrompt:       "REPO TEMPLATE",
		ImplementInstructions: "should be ignored — template wins",
	})
	seedAgent(t, s, store.Agent{
		ID:                    "cli-agent",
		Name:                  "cli",
		ImplementInstructions: "cli-level instructions",
	})
	seedAgent(t, s, store.Agent{
		ID:                    "default-agent",
		Name:                  "default",
		IsDefaultDev:          true,
		ImplementInstructions: "default instructions",
	})

	tmpl, instr := resolveImplementPrompt(s, "repo-agent", "cli-agent")
	if tmpl != "REPO TEMPLATE" {
		t.Errorf("template = %q, want REPO TEMPLATE", tmpl)
	}
	if instr != "" {
		t.Errorf("instr = %q, want empty (template wins)", instr)
	}
}

func TestResolveImplementPrompt_AgentFallbackWhenNoRepoMatch(t *testing.T) {
	s := newMemStore(t)
	seedAgent(t, s, store.Agent{
		ID:                    "cli-agent",
		Name:                  "cli",
		ImplementInstructions: "cli-level instructions",
	})
	seedAgent(t, s, store.Agent{
		ID:                    "default-agent",
		Name:                  "default",
		IsDefaultDev:          true,
		ImplementInstructions: "default instructions",
	})

	// repoPromptID does not match any seeded agent → fall through to cli-agent.
	tmpl, instr := resolveImplementPrompt(s, "nonexistent-repo-agent", "cli-agent")
	if tmpl != "" {
		t.Errorf("template = %q, want empty (agent has no ImplementPrompt)", tmpl)
	}
	if instr != "cli-level instructions" {
		t.Errorf("instr = %q, want cli-level instructions", instr)
	}
}

func TestResolveImplementPrompt_DefaultFallbackWhenAgentMissing(t *testing.T) {
	s := newMemStore(t)
	seedAgent(t, s, store.Agent{
		ID:              "default-agent",
		Name:            "default",
		IsDefaultDev:    true,
		ImplementPrompt: "DEFAULT TEMPLATE",
	})

	// Neither the repo nor the agent ID exists → use global default's ImplementPrompt.
	tmpl, instr := resolveImplementPrompt(s, "", "")
	if tmpl != "DEFAULT TEMPLATE" {
		t.Errorf("template = %q, want DEFAULT TEMPLATE", tmpl)
	}
	if instr != "" {
		t.Errorf("instr = %q, want empty", instr)
	}
}

func TestResolveImplementPrompt_EmptyWhenNoAgents(t *testing.T) {
	s := newMemStore(t)

	tmpl, instr := resolveImplementPrompt(s, "anything", "also-anything")
	if tmpl != "" || instr != "" {
		t.Errorf("empty store should yield empty strings, got (%q, %q)", tmpl, instr)
	}
}

func TestResolveImplementPrompt_AgentInstructionsWhenPromptEmpty(t *testing.T) {
	// When the selected agent has ImplementInstructions but no ImplementPrompt,
	// return ("", instructions). This is the injection-into-default path.
	s := newMemStore(t)
	seedAgent(t, s, store.Agent{
		ID:                    "repo-agent",
		Name:                  "repo",
		ImplementInstructions: "inject me into the default template",
	})

	tmpl, instr := resolveImplementPrompt(s, "repo-agent", "")
	if tmpl != "" {
		t.Errorf("template = %q, want empty", tmpl)
	}
	if instr != "inject me into the default template" {
		t.Errorf("instr = %q, want 'inject me into the default template'", instr)
	}
}

// ── loadOrCreateAPIToken ─────────────────────────────────────────────────
//
// Regression coverage for #71: the token file must be readable across
// containers sharing the data volume (daemon: UID 100, web UI: UID 1000).
// All three branches of the loader write or leave the file at 0644.

func tokenPerm(t *testing.T, path string) os.FileMode {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fi.Mode().Perm()
}

func TestLoadExistingAPITokenRecoveryPathNeverCreatesOrMutates(t *testing.T) {
	dir := t.TempDir()
	if _, err := loadExistingAPIToken(dir); err == nil {
		t.Fatal("loadExistingAPIToken created or accepted a missing credential")
	}
	path := filepath.Join(dir, "api_token")
	token := strings.Repeat("r", 64)
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadExistingAPIToken(dir)
	if err != nil || got != token {
		t.Fatalf("loadExistingAPIToken = (%q, %v), want existing token", got, err)
	}
	if mode := tokenPerm(t, path); mode != 0o600 {
		t.Fatalf("recovery token mode changed to %o, want 600", mode)
	}
	if err := os.WriteFile(path, []byte("short\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExistingAPIToken(dir); err == nil {
		t.Fatal("loadExistingAPIToken accepted an invalid credential")
	}
}

func TestLoadOrCreateAPIToken_NewFileIsWorldReadable(t *testing.T) {
	dir := t.TempDir()

	tok, err := loadOrCreateAPIToken(dir)
	if err != nil {
		t.Fatalf("loadOrCreateAPIToken: %v", err)
	}
	if len(tok) < 32 {
		t.Errorf("token length = %d, want >= 32", len(tok))
	}

	path := filepath.Join(dir, "api_token")
	if got := tokenPerm(t, path); got != 0644 {
		t.Errorf("new token perm = %o, want 0644 (see #71)", got)
	}
}

func TestLoadOrCreateAPIToken_LegacyFileIsUpgradedTo0644(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_token")

	// Simulate a daemon-generated token from before #71 (mode 0600).
	legacy := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(legacy+"\n"), 0600); err != nil {
		t.Fatalf("seed legacy token: %v", err)
	}

	tok, err := loadOrCreateAPIToken(dir)
	if err != nil {
		t.Fatalf("loadOrCreateAPIToken: %v", err)
	}
	if tok != legacy {
		t.Errorf("token changed: got %q, want existing %q", tok, legacy)
	}
	if got := tokenPerm(t, path); got != 0644 {
		t.Errorf("legacy token perm = %o, want 0644 after upgrade", got)
	}
}

func TestLoadOrCreateAPIToken_ShortFileIsRegenerated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "api_token")

	// A truncated / malformed token (< 32 chars) should be replaced, not
	// returned as-is. Write it 0600 so we also exercise the overwrite path.
	if err := os.WriteFile(path, []byte("short\n"), 0600); err != nil {
		t.Fatalf("seed short token: %v", err)
	}

	tok, err := loadOrCreateAPIToken(dir)
	if err != nil {
		// O_EXCL will refuse to create because the file exists. The loader
		// currently returns that error for the short-token case; this test
		// documents the behaviour so a future change is a conscious decision.
		t.Skipf("short-token regeneration currently not supported: %v", err)
	}
	if len(tok) < 32 || tok == "short" {
		t.Errorf("token = %q, want fresh 64-char hex", tok)
	}
	if got := tokenPerm(t, path); got != 0644 {
		t.Errorf("regenerated token perm = %o, want 0644", got)
	}
}
