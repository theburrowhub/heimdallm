package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	gh "github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
)

// clusterCfg builds a validated Config with a two-instance registry and the
// given routing rules.
func clusterCfg(t *testing.T, selfID string, routing config.RoutingConfig) *config.Config {
	t.Helper()
	c := &config.Config{}
	c.AI.Primary = "claude"
	c.Cluster.Role = config.RoleWorker
	c.Cluster.InstanceID = selfID
	c.Cluster.DefaultInstance = "hub-1"
	c.Cluster.Instances = map[string]config.InstanceConfig{
		"hub-1": {Name: "hub", BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"srv-a": {Name: "srv-a", BaseURL: "http://127.0.0.1:7843", Token: "t"},
	}
	c.Cluster.Routing = routing
	// applyDefaults is unexported in package config, so fill the few fields
	// Validate insists on rather than round-tripping through a temp file.
	c.GitHub.PollInterval = "5m"
	if err := c.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	return c
}

func TestEnsureInstanceIDPrefersExplicitValue(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "explicit-id"

	id, err := ensureInstanceID(cfg, dir)
	if err != nil {
		t.Fatalf("ensureInstanceID: %v", err)
	}
	if id != "explicit-id" {
		t.Errorf("id = %q, want the configured value", id)
	}
	// An explicit id must not cause a file to be written.
	if _, err := os.Stat(filepath.Join(dir, "instance_id")); !os.IsNotExist(err) {
		t.Error("instance_id file was written despite an explicit id")
	}
}

func TestEnsureInstanceIDRejectsInvalidExplicitValue(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "has/slash"
	if _, err := ensureInstanceID(cfg, t.TempDir()); err == nil {
		t.Error("ensureInstanceID accepted an id that is unsafe in a URL path")
	}
}

// A daemon that is not part of a cluster must not have a file written into its
// data directory for a feature it is not using.
func TestEnsureInstanceIDNoOpWhenClusterDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}

	id, err := ensureInstanceID(cfg, dir)
	if err != nil {
		t.Fatalf("ensureInstanceID: %v", err)
	}
	if id != "" {
		t.Errorf("id = %q, want empty for a non-cluster daemon", id)
	}
	if _, err := os.Stat(filepath.Join(dir, "instance_id")); !os.IsNotExist(err) {
		t.Error("instance_id file was written on a daemon with no [cluster]")
	}
}

func TestEnsureInstanceIDGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleWorker

	first, err := ensureInstanceID(cfg, dir)
	if err != nil {
		t.Fatalf("ensureInstanceID: %v", err)
	}
	if err := config.ValidateInstanceID(first); err != nil {
		t.Errorf("generated id %q is not valid: %v", first, err)
	}

	// The identity must survive a restart, and an operator rewriting or
	// deleting config.toml is exactly when a hub must not suddenly consider
	// this a different machine.
	second, err := ensureInstanceID(cfg, dir)
	if err != nil {
		t.Fatalf("second ensureInstanceID: %v", err)
	}
	if second != first {
		t.Errorf("id changed across restarts: %q then %q", first, second)
	}
}

func TestEnsureInstanceIDRegeneratesUnusableStoredValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "instance_id"), []byte("!!! not valid !!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleWorker

	// A corrupted file must not wedge the daemon at boot.
	id, err := ensureInstanceID(cfg, dir)
	if err != nil {
		t.Fatalf("ensureInstanceID: %v", err)
	}
	if err := config.ValidateInstanceID(id); err != nil {
		t.Errorf("regenerated id %q is not valid: %v", id, err)
	}
}

func TestClusterStateInertWithoutConfig(t *testing.T) {
	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cs := newClusterState(cfg, nil, nil)

	// The no-regression invariant: with no [cluster] this daemon owns
	// everything, exactly as it did before instances existed.
	for _, repo := range []string{"acme/tools", "other/thing"} {
		if !cs.Owns(repo) {
			t.Errorf("Owns(%q) = false without [cluster]", repo)
		}
	}
	repos := []string{"a/b", "c/d"}
	if got := cs.FilterOwned(repos); len(got) != 2 {
		t.Errorf("FilterOwned = %v, want everything", got)
	}
	// A worker or standalone daemon must not run a prober.
	if cs.Prober() != nil {
		t.Error("a non-hub daemon built a health prober")
	}
	if id, _, role := cs.Identity(); id != "" || role != "" {
		t.Errorf("Identity() = %q/%q, want empty on a standalone daemon", id, role)
	}
}

func TestClusterStatePartitionsRepos(t *testing.T) {
	routing := config.RoutingConfig{
		Orgs:  map[string]string{"theirs": "srv-a"},
		Repos: map[string]string{"theirs/mine": "hub-1"},
	}
	hub := newClusterState(clusterCfg(t, "hub-1", routing), nil, nil)
	worker := newClusterState(clusterCfg(t, "srv-a", routing), nil, nil)

	cases := map[string][2]bool{ // repo -> {hub owns, worker owns}
		"theirs/mine":  {true, false}, // explicit repo rule
		"theirs/other": {false, true}, // org rule
		"ours/thing":   {true, false}, // default_instance = hub-1
	}
	for repo, want := range cases {
		if got := hub.Owns(repo); got != want[0] {
			t.Errorf("hub.Owns(%q) = %v, want %v", repo, got, want[0])
		}
		if got := worker.Owns(repo); got != want[1] {
			t.Errorf("worker.Owns(%q) = %v, want %v", repo, got, want[1])
		}
		// Exactly one daemon must act on each repo: that is what makes
		// partitioned polling safe without a distributed lock.
		if hub.Owns(repo) == worker.Owns(repo) {
			t.Errorf("%q: ownership is not exclusive (both %v)", repo, hub.Owns(repo))
		}
	}
}

func TestClusterStateUpdateAppliesNewRouting(t *testing.T) {
	cs := newClusterState(clusterCfg(t, "hub-1", config.RoutingConfig{
		Repos: map[string]string{"acme/x": "hub-1"},
	}), nil, nil)
	if !cs.Owns("acme/x") {
		t.Fatal("precondition: hub should own acme/x")
	}

	cs.Update(clusterCfg(t, "hub-1", config.RoutingConfig{
		Repos: map[string]string{"acme/x": "srv-a"},
	}))
	if cs.Owns("acme/x") {
		t.Error("hub still owns acme/x after it was routed away on reload")
	}
}

// Reload must not reset the rotation: the counters live in the Router, and
// rebuilding it would send every operation back to the first pool member.
func TestClusterStateUpdatePreservesRoundRobin(t *testing.T) {
	cfg := clusterCfg(t, "hub-1", config.RoutingConfig{
		Mode:           config.ModeDispatch,
		RoundRobinPool: []string{"hub-1", "srv-a"},
	})
	cs := newClusterState(cfg, nil, nil)

	first := cs.Router().Next(config.OpReview)
	cs.Update(cfg)
	second := cs.Router().Next(config.OpReview)

	if first == second {
		t.Errorf("both picks went to %q; the rotation was reset by the reload", first)
	}
}

func TestClusterStateHubBuildsProber(t *testing.T) {
	cfg := clusterCfg(t, "hub-1", config.RoutingConfig{})
	cfg.Cluster.Role = config.RoleHub
	cs := newClusterState(cfg, nil, nil)

	if cs.Prober() == nil {
		t.Error("a hub must build a health prober")
	}
	id, name, role := cs.Identity()
	if id != "hub-1" || role != config.RoleHub {
		t.Errorf("Identity() = %q/%q, want hub-1/hub", id, role)
	}
	// The name defaults to the hostname so a second machine is recognisable
	// without the operator naming it first.
	if name == "" {
		t.Error("Identity() name is empty; want the hostname fallback")
	}
}

func TestClusterEventsPublishTransitions(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	sub := broker.Subscribe()
	if sub == nil {
		t.Fatal("broker subscribe returned nil")
	}

	events := newClusterEvents(broker, nil)
	events.InstanceStateChanged(instances.State{InstanceID: "srv-a", Name: "srv-a", Reachable: false, LastError: "refused"})

	select {
	case ev := <-sub:
		if ev.Type != sse.EventInstanceDown {
			t.Errorf("event type = %q, want %q", ev.Type, sse.EventInstanceDown)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event published for an instance going down")
	}

	events.InstanceStateChanged(instances.State{InstanceID: "srv-a", Name: "srv-a", Reachable: true, Version: "0.9.0"})
	select {
	case ev := <-sub:
		if ev.Type != sse.EventInstanceUp {
			t.Errorf("event type = %q, want %q", ev.Type, sse.EventInstanceUp)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event published for an instance recovering")
	}
}

func TestNewClusterEventsNilBroker(t *testing.T) {
	// A daemon started without a broker must not panic when a probe flips.
	if got := newClusterEvents(nil, nil); got != nil {
		t.Errorf("newClusterEvents(nil, nil) = %v, want nil", got)
	}
	// A recovery callback still needs a publisher even with no broker: the
	// notes must be cleared on recovery whether or not anyone is watching.
	if got := newClusterEvents(nil, func(string) {}); got == nil {
		t.Error("newClusterEvents(nil, cb) = nil; the recovery callback would never fire")
	}
}

func TestClusterProbeIntervalFallback(t *testing.T) {
	cfg := &config.Config{}
	for _, raw := range []string{"", "nonsense", "-5s", "0s"} {
		cfg.Cluster.ProbeInterval = raw
		if got := clusterProbeInterval(cfg); got != 30*time.Second {
			t.Errorf("clusterProbeInterval(%q) = %v, want the 30s fallback", raw, got)
		}
	}
	cfg.Cluster.ProbeInterval = "15s"
	if got := clusterProbeInterval(cfg); got != 15*time.Second {
		t.Errorf("clusterProbeInterval(15s) = %v", got)
	}
}

// ------------------------------------------------- Tier 2 ownership filtering

// tier2OwnershipHarness serves one PR per repo from a fake GitHub.
func tier2OwnershipHarness(t *testing.T, repos []string) *tier2Adapter {
	t.Helper()
	items := make([]gh.PullRequest, 0, len(repos))
	for i, repo := range repos {
		items = append(items, gh.PullRequest{
			ID:        int64(9000 + i),
			Number:    i + 1,
			Title:     "PR on " + repo,
			State:     "open",
			User:      gh.User{Login: "alice"},
			Head:      gh.Branch{Repo: gh.Repo{FullName: repo}},
			UpdatedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/user":
			_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
		case "/search/issues":
			_ = json.NewEncoder(w).Encode(struct {
				Items []gh.PullRequest `json:"items"`
			}{Items: items})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	broker := sse.NewBroker()
	broker.Start()
	t.Cleanup(broker.Stop)

	var (
		loginMu sync.Mutex
		login   = "heimdallm-bot"
		cfgMu   sync.Mutex
		cfg     = &config.Config{}
	)
	return &tier2Adapter{
		ghClient:             gh.NewClient("fake-token", gh.WithBaseURL(srv.URL)),
		store:                newMemStore(t),
		broker:               broker,
		cfgMu:                &cfgMu,
		cfg:                  &cfg,
		loginMu:              &loginMu,
		login:                &login,
		publishOrderedEvents: func([]sse.Event) error { return nil },
		lastSkippedUpdatedAt: make(map[int64]time.Time),
	}
}

func TestTier2FetchFiltersUnownedRepos(t *testing.T) {
	mine, theirs := "acme/mine", "acme/theirs"
	a := tier2OwnershipHarness(t, []string{mine, theirs})
	a.owns = func(repo string) bool { return repo == mine }
	// theirs is routed away AND successfully dispatched: it must not also be
	// reviewed here.
	a.dispatchPR = func(repo string, prID int64, prURL string) bool { return repo == theirs }

	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(out) != 1 || out[0].Repo != mine {
		t.Fatalf("returned %+v, want only the owned repo %q", out, mine)
	}

	// Discovery must stay global: the instance still learns about the repo it
	// does not own, so the GUI shows the whole estate. Only acting is narrowed.
	a.cfgMu.Lock()
	known := append(append([]string(nil), (*a.cfg).GitHub.Repositories...), (*a.cfg).GitHub.NonMonitored...)
	a.cfgMu.Unlock()
	if !containsRepo(known, theirs) {
		t.Errorf("discovered repos = %v, want the unowned repo %q still discovered", known, theirs)
	}
}

// The core guarantee behind routing: a PR whose repo is routed away must
// never simply vanish. If there is no working way to hand it to its owner —
// no dispatch function wired, or the dispatch call itself reports it could
// not be handled (owner down, unreachable, dispatch failed) — this instance
// must review it locally instead of dropping it. This is exactly what let
// overmind-swarm PRs disappear from the queue entirely once routing engaged.
func TestTier2FetchFallsBackToLocalWhenDispatchUnavailable(t *testing.T) {
	theirs := "acme/theirs"
	a := tier2OwnershipHarness(t, []string{theirs})
	a.owns = func(string) bool { return false }
	a.dispatchPR = nil // no dispatch capability wired at all

	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(out) != 1 || out[0].Repo != theirs {
		t.Fatalf("returned %+v, want the PR reviewed locally when dispatch is unavailable", out)
	}
}

func TestTier2FetchFallsBackToLocalWhenDispatchFails(t *testing.T) {
	theirs := "acme/theirs"
	a := tier2OwnershipHarness(t, []string{theirs})
	a.owns = func(string) bool { return false }
	a.dispatchPR = func(string, int64, string) bool { return false } // owner down/unreachable

	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(out) != 1 || out[0].Repo != theirs {
		t.Fatalf("returned %+v, want the PR reviewed locally when dispatch fails", out)
	}
}

// A nil predicate is the single-daemon path and must change nothing.
func TestTier2FetchWithoutOwnershipFilter(t *testing.T) {
	a := tier2OwnershipHarness(t, []string{"acme/one", "acme/two"})
	out, err := a.FetchPRsToReview()
	if err != nil {
		t.Fatalf("FetchPRsToReview: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("returned %d PRs, want both when no ownership filter is set", len(out))
	}
}

func TestTier2ProcessRepoSkipsUnowned(t *testing.T) {
	a := tier2OwnershipHarness(t, nil)
	a.owns = func(string) bool { return false }
	// A healthy, trusted owner: safe to skip and let it process its own issues.
	a.ownerCanHandleIssues = func(string) bool { return true }

	n, err := a.ProcessRepo(t.Context(), "acme/not-mine")
	if err != nil {
		t.Fatalf("ProcessRepo: %v", err)
	}
	if n != 0 {
		t.Errorf("processed %d issues on an unowned repo with a healthy owner, want 0", n)
	}
}

// The routing decision behind ProcessRepo's guard — "process locally" vs
// "trust the routed owner" — as a pure predicate, exercised directly rather
// than through ProcessRepo's full issue-processing machinery (whose (0, nil)
// return on this harness's zero-value config would be indistinguishable
// between "skipped due to routing" and "skipped, issue tracking disabled").
//
// A repo routed away with no healthy owner to trust must not simply be
// abandoned: that is exactly what let issues on a repo routed to a down
// instance go completely unattended before this existed.
func TestTier2ShouldProcessLocally(t *testing.T) {
	tests := []struct {
		name           string
		owns           func(string) bool
		ownerCanHandle func(string) bool
		wantSkip       bool
	}{
		{"owned", func(string) bool { return true }, nil, false},
		{"unowned, no dispatch wired", func(string) bool { return false }, nil, false},
		{"unowned, owner unhealthy", func(string) bool { return false }, func(string) bool { return false }, false},
		{"unowned, owner healthy", func(string) bool { return false }, func(string) bool { return true }, true},
		{"single-daemon (owns nil)", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			skip := !tier2ShouldProcessLocally(tt.owns, tt.ownerCanHandle, "acme/repo")
			if skip != tt.wantSkip {
				t.Errorf("skip = %v, want %v", skip, tt.wantSkip)
			}
		})
	}
}

func containsRepo(haystack []string, needle string) bool {
	for _, s := range haystack {
		if strings.EqualFold(s, needle) {
			return true
		}
	}
	return false
}

func TestClusterStateSnapshotIsUsable(t *testing.T) {
	cfg := clusterCfg(t, "hub-1", config.RoutingConfig{
		Repos: map[string]string{"acme/x": "hub-1"},
	})
	cfg.Cluster.Role = config.RoleHub
	cs := newClusterState(cfg, nil, nil)

	snap := cs.Snapshot()
	if snap.SelfID != "hub-1" || snap.Role != config.RoleHub {
		t.Errorf("snapshot = %+v", snap)
	}
	if snap.Registry == nil || snap.Registry.Len() != 2 {
		t.Errorf("snapshot registry = %+v", snap.Registry)
	}
	if snap.Propagator == nil {
		t.Error("snapshot has no propagator")
	}
	// The Router must be the SAME object across snapshots: the round-robin
	// counters live in it, so rebuilding it per request would reset the
	// rotation and send every operation to the first pool member.
	if snap.Router != cs.Snapshot().Router {
		t.Error("Snapshot() returned a different Router; the rotation would reset on every request")
	}
	if snap.Router != cs.Router() {
		t.Error("Snapshot().Router differs from Router()")
	}
}

func TestClusterStateRunProberStopsOnCancel(t *testing.T) {
	cfg := clusterCfg(t, "hub-1", config.RoutingConfig{})
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.ProbeInterval = "10s"
	cs := newClusterState(cfg, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		cs.RunProber(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunProber did not return after cancellation")
	}
}

// A worker has no prober, so the goroutine main starts for it must be a no-op
// rather than blocking shutdown.
func TestClusterStateRunProberNoOpOnWorker(t *testing.T) {
	cs := newClusterState(clusterCfg(t, "srv-a", config.RoutingConfig{}), nil, nil)
	done := make(chan struct{})
	go func() {
		cs.RunProber(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunProber blocked on a daemon with no prober")
	}
}

func TestWireClusterOnlyMountsTheControlPlaneOnAHub(t *testing.T) {
	st := newMemStore(t)

	worker := server.New(st, nil, nil, "tok")
	wireCluster(worker, newClusterState(clusterCfg(t, "srv-a", config.RoutingConfig{}), nil, nil), st)

	req := httptest.NewRequest(http.MethodGet, "/instances", nil)
	req.Header.Set("X-Heimdallm-Token", "tok")
	rec := httptest.NewRecorder()
	worker.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("worker GET /instances = %d, want 404", rec.Code)
	}
	// Identity is still published: it is how a hub recognises this daemon.
	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	worker.Router().ServeHTTP(healthRec, healthReq)
	if !strings.Contains(healthRec.Body.String(), "srv-a") {
		t.Errorf("/health = %s, want the instance identity", healthRec.Body)
	}

	hubCfg := clusterCfg(t, "hub-1", config.RoutingConfig{})
	hubCfg.Cluster.Role = config.RoleHub
	hub := server.New(st, nil, nil, "tok")
	wireCluster(hub, newClusterState(hubCfg, st, nil), st)

	hubRec := httptest.NewRecorder()
	hubReq := httptest.NewRequest(http.MethodGet, "/instances", nil)
	hubReq.Header.Set("X-Heimdallm-Token", "tok")
	hub.Router().ServeHTTP(hubRec, hubReq)
	if hubRec.Code != http.StatusOK {
		t.Errorf("hub GET /instances = %d, want 200: %s", hubRec.Code, hubRec.Body)
	}
}

func TestResolvedSelfName(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.InstanceName = "  Explicit  "
	if got := resolvedSelfName(cfg); got != "Explicit" {
		t.Errorf("resolvedSelfName = %q, want the trimmed explicit name", got)
	}

	// Falling back to the hostname means a second machine is recognisable
	// without the operator naming it first — but only once clustering is on.
	cfg.Cluster.InstanceName = "   "
	cfg.Cluster.Role = config.RoleHub
	host, _ := os.Hostname()
	if got := resolvedSelfName(cfg); got != host && got != "hub-1" {
		t.Errorf("resolvedSelfName = %q, want the hostname or the id", got)
	}

	// A daemon outside a cluster has no instance to label, so inventing a name
	// would put an identity on /health for a feature nobody is using.
	plain := &config.Config{}
	if got := resolvedSelfName(plain); got != "" {
		t.Errorf("resolvedSelfName on a non-cluster daemon = %q, want empty", got)
	}
}

func TestInstanceIDPrefix(t *testing.T) {
	prefix := instanceIDPrefix()
	if prefix == "" || !strings.HasSuffix(prefix, "-") {
		t.Errorf("instanceIDPrefix() = %q, want a hyphen-terminated prefix", prefix)
	}
	// The prefix has to be safe as the head of a validated instance id.
	if err := config.ValidateInstanceID(prefix + "abcdef"); err != nil {
		t.Errorf("prefix %q does not produce a valid id: %v", prefix, err)
	}
	if len(prefix) > 14 {
		t.Errorf("prefix %q is longer than the 12-char cap plus separator", prefix)
	}
}

func TestClusterStateNilReceiverIsPermissive(t *testing.T) {
	// The pollers hold this by value through a closure; a nil state must mean
	// "own everything" rather than panic or silently stop all work.
	var cs *clusterState
	if !cs.Owns("acme/tools") {
		t.Error("nil clusterState.Owns = false, want true")
	}
	repos := []string{"a/b", "c/d"}
	if got := cs.FilterOwned(repos); len(got) != 2 {
		t.Errorf("nil clusterState.FilterOwned = %v, want everything", got)
	}
}

func TestEnsureInstanceIDRejectsAnUnwritableDataDir(t *testing.T) {
	// A data dir the daemon cannot write is a broken install, and the error
	// has to name the path rather than surfacing later as a changing identity.
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Skip("cannot make the directory read-only in this environment")
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleWorker
	if _, err := ensureInstanceID(cfg, dir); err == nil {
		t.Error("ensureInstanceID = nil on an unwritable data dir")
	}
}

func TestEnsureInstanceIDAdoptsAConcurrentWinner(t *testing.T) {
	// Two processes starting together must converge on one id instead of
	// silently diverging.
	dir := t.TempDir()
	path := filepath.Join(dir, "instance_id")
	if err := os.WriteFile(path, []byte("winner-abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleWorker

	id, err := ensureInstanceID(cfg, dir)
	if err != nil {
		t.Fatalf("ensureInstanceID: %v", err)
	}
	if id != "winner-abc123" {
		t.Errorf("id = %q, want the id already on disk", id)
	}
}

func TestEnsureSelfInstanceSeedsTheHub(t *testing.T) {
	// Registering the first remote instance must not fail because the hub has
	// not described its own machine in config.toml.
	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cfg.Server.Port = 7842
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.InstanceName = "Local hub"
	cfg.Cluster.Instances = map[string]config.InstanceConfig{
		"srv-a": {BaseURL: "http://10.0.0.11:7842", Token: "t"},
	}

	ensureSelfInstance(cfg, "/data")

	self, ok := cfg.Cluster.Instances["hub-1"]
	if !ok {
		t.Fatal("the hub was not seeded into its own registry")
	}
	if self.Name != "Local hub" {
		t.Errorf("name = %q, want the configured instance_name", self.Name)
	}
	// Loopback because nothing ever dials it: the proxy short-circuits the
	// hub's own id and serves locally.
	if self.BaseURL != "http://127.0.0.1:7842" {
		t.Errorf("base_url = %q, want loopback on the configured port", self.BaseURL)
	}
	if self.TokenFile != "/data/api_token" {
		t.Errorf("token_file = %q, want the daemon's own token", self.TokenFile)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("the seeded config does not validate: %v", err)
	}
}

func TestEnsureSelfInstanceRespectsAnExplicitEntry(t *testing.T) {
	// An operator who writes their own entry keeps full control.
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.Instances = map[string]config.InstanceConfig{
		"hub-1": {Name: "Mine", BaseURL: "http://10.0.0.1:9999", Token: "explicit"},
	}

	ensureSelfInstance(cfg, "/data")

	if got := cfg.Cluster.Instances["hub-1"]; got.Token != "explicit" || got.BaseURL != "http://10.0.0.1:9999" {
		t.Errorf("entry = %+v, want the operator's own", got)
	}
}

func TestEnsureSelfInstanceNoOpOnANonHub(t *testing.T) {
	// A worker has no registry of its own to appear in.
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleWorker
	cfg.Cluster.InstanceID = "srv-a"
	ensureSelfInstance(cfg, "/data")
	if len(cfg.Cluster.Instances) != 0 {
		t.Errorf("instances = %v, want none on a worker", cfg.Cluster.Instances)
	}

	// And a hub with no identity has nothing to key an entry on.
	hub := &config.Config{}
	hub.Cluster.Role = config.RoleHub
	ensureSelfInstance(hub, "/data")
	if len(hub.Cluster.Instances) != 0 {
		t.Errorf("instances = %v, want none without an instance_id", hub.Cluster.Instances)
	}
}

// ------------------------------------------------- Reload-time identity

// A daemon that booted before [cluster] existed freezes instanceID at "" for
// its whole lifetime unless a reload re-resolves it. This is what let a hub
// silently own every repo (Router.Owns fails open on selfID=="") after an
// operator turned clustering on without restarting.
func TestResolveReloadInstanceIDKeepsExistingWhenAlreadySet(t *testing.T) {
	dir := t.TempDir()
	newCfg := &config.Config{}
	newCfg.Cluster.Role = config.RoleHub

	got, err := resolveReloadInstanceID("hub-1", newCfg, dir)
	if err != nil {
		t.Fatalf("resolveReloadInstanceID: %v", err)
	}
	if got != "hub-1" {
		t.Errorf("id = %q, want the already-resolved value unchanged", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "instance_id")); !os.IsNotExist(err) {
		t.Error("instance_id file was written despite an already-resolved id")
	}
}

func TestResolveReloadInstanceIDNoOpWhenClusterStaysDisabled(t *testing.T) {
	dir := t.TempDir()
	newCfg := &config.Config{} // no [cluster] at all

	got, err := resolveReloadInstanceID("", newCfg, dir)
	if err != nil {
		t.Fatalf("resolveReloadInstanceID: %v", err)
	}
	if got != "" {
		t.Errorf("id = %q, want empty when clustering is still disabled", got)
	}
}

func TestResolveReloadInstanceIDGeneratesWhenClusterNewlyEnabled(t *testing.T) {
	dir := t.TempDir()
	newCfg := &config.Config{}
	newCfg.Cluster.Role = config.RoleHub // just turned on in this reload's TOML

	got, err := resolveReloadInstanceID("", newCfg, dir)
	if err != nil {
		t.Fatalf("resolveReloadInstanceID: %v", err)
	}
	if err := config.ValidateInstanceID(got); err != nil {
		t.Errorf("resolved id %q is not valid: %v", got, err)
	}
	// Must persist so this identity survives a real restart too.
	if _, err := os.Stat(filepath.Join(dir, "instance_id")); err != nil {
		t.Errorf("instance_id file was not written: %v", err)
	}
}

func TestResolveReloadInstanceIDPicksUpExplicitConfiguredID(t *testing.T) {
	dir := t.TempDir()
	newCfg := &config.Config{}
	newCfg.Cluster.Role = config.RoleHub
	newCfg.Cluster.InstanceID = "operator-chosen"

	got, err := resolveReloadInstanceID("", newCfg, dir)
	if err != nil {
		t.Fatalf("resolveReloadInstanceID: %v", err)
	}
	if got != "operator-chosen" {
		t.Errorf("id = %q, want the operator's explicit id from this reload", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "instance_id")); !os.IsNotExist(err) {
		t.Error("instance_id file was written despite an explicit id in the new config")
	}
}

// ------------------------------------------------- Prober built on reload

// A daemon that booted as a worker/standalone and is promoted to hub by a
// reload (not a restart) must get a prober so /instances stops 404ing and
// health probing actually starts — not just the next full process restart.
func TestClusterStateUpdateBuildsProberOnRoleTransition(t *testing.T) {
	workerCfg := clusterCfg(t, "hub-1", config.RoutingConfig{})
	workerCfg.Cluster.Role = config.RoleWorker
	cs := newClusterState(workerCfg, nil, nil)
	if cs.Prober() != nil {
		t.Fatal("precondition: a worker must not have a prober")
	}

	hubCfg := clusterCfg(t, "hub-1", config.RoutingConfig{})
	hubCfg.Cluster.Role = config.RoleHub
	built := cs.Update(hubCfg)

	if !built {
		t.Error("Update() = false, want true when a prober was just built")
	}
	if cs.Prober() == nil {
		t.Error("Update() did not build a prober on the worker->hub transition")
	}
}

// The reverse and steady-state cases must not rebuild or report a spurious
// transition on every ordinary reload.
func TestClusterStateUpdateNoOpProberOutsideTransition(t *testing.T) {
	hubCfg := clusterCfg(t, "hub-1", config.RoutingConfig{})
	hubCfg.Cluster.Role = config.RoleHub
	cs := newClusterState(hubCfg, nil, nil)
	original := cs.Prober()
	if original == nil {
		t.Fatal("precondition: a hub must have a prober")
	}

	built := cs.Update(hubCfg)
	if built {
		t.Error("Update() = true on a daemon that was already a hub")
	}
	if cs.Prober() != original {
		t.Error("Update() replaced an already-built prober")
	}

	worker := newClusterState(clusterCfg(t, "srv-a", config.RoutingConfig{}), nil, nil)
	if got := worker.Update(clusterCfg(t, "srv-a", config.RoutingConfig{})); got {
		t.Error("Update() = true on a daemon that stayed a worker")
	}
}

// ------------------------------------------------- Dispatch with local fallback

// dispatchRemote is a fake worker instance that records what it received and
// can be made to fail on demand, for the dispatch tests below.
type dispatchRemote struct {
	*httptest.Server
	mu        sync.Mutex
	healthy   bool
	addPR     []string
	reviewed  []int64
	triaged   []int64
	failNext  bool
	failCount int
}

func newDispatchRemote(t *testing.T) *dispatchRemote {
	t.Helper()
	d := &dispatchRemote{healthy: true}
	d.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		switch {
		case r.URL.Path == "/health":
			if !d.healthy {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "instance_id": "srv-a", "role": "worker"})
		case r.URL.Path == "/prs/add":
			d.addPR = append(d.addPR, "called")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case strings.HasPrefix(r.URL.Path, "/prs/") && strings.HasSuffix(r.URL.Path, "/review"):
			if d.failNext {
				d.failCount++
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			d.reviewed = append(d.reviewed, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case strings.HasPrefix(r.URL.Path, "/issues/") && strings.HasSuffix(r.URL.Path, "/review"):
			if d.failNext {
				d.failCount++
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			d.triaged = append(d.triaged, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(d.Close)
	return d
}

func (d *dispatchRemote) setHealthy(v bool) {
	d.mu.Lock()
	d.healthy = v
	d.mu.Unlock()
}

func (d *dispatchRemote) setFailNext(v bool) {
	d.mu.Lock()
	d.failNext = v
	d.mu.Unlock()
}

func (d *dispatchRemote) reviewCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.reviewed)
}

func (d *dispatchRemote) triageCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.triaged)
}

func (d *dispatchRemote) addPRCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.addPR)
}

// dispatchHub builds a hub clusterState routing "theirs/*" to a live fake
// remote, with a real prober so HealthyIDs() reflects the remote's actual
// health rather than the unprobed fail-open default.
func dispatchHub(t *testing.T, remote *dispatchRemote) *clusterState {
	t.Helper()
	return dispatchHubWithBroker(t, remote, nil)
}

// probeUntilConfirmedDown runs enough probe cycles for the prober to give up
// on the remote, i.e. to reach cluster.takeover_after_failed_probes.
func probeUntilConfirmedDown(t *testing.T, cs *clusterState) {
	t.Helper()
	for i := 0; i < cs.takeoverThreshold(); i++ {
		cs.Prober().ProbeAll(context.Background())
	}
	if !cs.confirmedDown("srv-a") {
		t.Fatalf("prober still trusts srv-a after %d failed probes", cs.takeoverThreshold())
	}
}

func dispatchHubWithBroker(t *testing.T, remote *dispatchRemote, broker *sse.Broker) *clusterState {
	t.Helper()
	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.Instances = map[string]config.InstanceConfig{
		"hub-1": {Name: "hub", BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"srv-a": {Name: "srv-a", BaseURL: remote.URL, Token: "secret"},
	}
	cfg.Cluster.Routing = config.RoutingConfig{Orgs: map[string]string{"theirs": "srv-a"}}
	cfg.GitHub.PollInterval = "5m"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	cs := newClusterState(cfg, nil, broker)
	cs.Prober().ProbeAll(context.Background()) // establish real health, not the unprobed fail-open default
	return cs
}

func TestClusterStateDispatchPRReviewToHealthyOwner(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	handled := cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "https://github.com/theirs/repo/pull/1")
	if !handled {
		t.Fatal("DispatchPRReview() = false, want true when the owner is healthy")
	}
	if remote.addPRCount() != 1 {
		t.Errorf("AddPR calls = %d, want 1", remote.addPRCount())
	}
	if remote.reviewCount() != 1 {
		t.Errorf("review calls = %d, want 1", remote.reviewCount())
	}
}

// The whole point of dispatch-with-fallback: an owner that is down must never
// make a PR go unreviewed. This is the exact gap that let overmind-swarm PRs
// disappear from the queue entirely once routing engaged.
//
// #765 narrowed *when* it fires, not whether: the takeover now waits for
// cluster.takeover_after_failed_probes consecutive failures instead of acting
// on the first one. See TestClusterStateDispatchPRReviewDefersToAnOwnerNotYetConfirmedDown
// for the case that used to duplicate reviews.
func TestClusterStateDispatchPRReviewFallsBackWhenOwnerConfirmedDown(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)

	handled := cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if handled {
		t.Error("DispatchPRReview() = true, want false once the owner is confirmed down")
	}
	if remote.reviewCount() != 0 {
		t.Errorf("review calls = %d, want 0 on an unhealthy owner", remote.reviewCount())
	}
}

// The #765 regression test. One failed probe means "I cannot reach it", and a
// partitioned owner is still polling GitHub and reviewing the repos it owns —
// so reviewing here as well publishes two independently-reasoned verdicts on
// the same PR. The hub must leave the work alone until it has real grounds to
// believe the owner stopped working.
func TestClusterStateDispatchPRReviewDefersToAnOwnerNotYetConfirmedDown(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setHealthy(false)
	cs.Prober().ProbeAll(context.Background()) // exactly one failed probe

	if cs.confirmedDown("srv-a") {
		t.Fatal("one failed probe must not confirm an instance down")
	}
	if handled := cs.DispatchPRReview(context.Background(), "theirs/repo", 42, ""); !handled {
		t.Error("DispatchPRReview() = false, want true — a single missed probe must not trigger a local takeover")
	}
}

// A nominally healthy owner whose review call itself fails (transient error,
// auth rotated, disk full) must not be taken over on the strength of that one
// call: the RPC failing says nothing about whether the owner is still
// reviewing its own repos.
func TestClusterStateDispatchPRReviewDefersOnRemoteError(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setFailNext(true)

	handled := cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if !handled {
		t.Error("DispatchPRReview() = false, want true — a failed RPC to a healthy owner is not grounds to duplicate its work")
	}
}

// PR review feedback: the health probe is unauthenticated while every dispatch
// RPC is not, so an owner that answers /health and rejects our calls (cluster
// token rotated on the remote, repo missing from its own config, permission
// error) never accumulates a probe failure. Deferring on the probe history
// alone would leave that work undone by anyone, forever.
func TestClusterStateTakesOverAnOwnerThatKeepsRejectingDispatch(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setFailNext(true) // /health still 200s; only the RPC fails

	for i := 1; i < cs.takeoverThreshold(); i++ {
		if !cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
			t.Fatalf("took over after %d rejected RPCs, want to defer until %d", i, cs.takeoverThreshold())
		}
	}
	if cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
		t.Errorf("still deferring after %d rejected RPCs to a probe-healthy owner; the work would never be done",
			cs.takeoverThreshold())
	}
	if cs.confirmedDown("srv-a") {
		t.Error("precondition broken: the owner answered every health probe, so it must not be confirmed down")
	}
}

// The counter must be consecutive: a repo whose dispatch fails once, succeeds,
// then fails again is a blip, not an owner refusing the work.
func TestClusterStateDispatchFailureCounterResetsOnSuccess(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	for i := 1; i < cs.takeoverThreshold(); i++ {
		remote.setFailNext(true)
		if !cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
			t.Fatalf("took over after %d rejected RPCs, want to defer", i)
		}
		remote.setFailNext(false)
		if !cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
			t.Fatal("a successful dispatch reported as not handled")
		}
	}
	remote.setFailNext(true)
	if !cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
		t.Error("took over on an isolated failure; the successes in between must reset the count")
	}
}

// The counter is per (operation, repo): one repo the remote refuses must not
// drag another repo, or another operation, into a takeover with it.
func TestClusterStateDispatchFailureCounterIsPerWorkUnit(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setFailNext(true)

	for i := 0; i < cs.takeoverThreshold(); i++ {
		cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	}
	if cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
		t.Fatal("precondition: theirs/repo should have been taken over by now")
	}
	if !cs.DispatchIssueReview(context.Background(), "theirs/repo", 99) {
		t.Error("a repo taken over for review also took over issue triage on the first rejection")
	}
	if !cs.DispatchPRReview(context.Background(), "theirs/other", 43, "") {
		t.Error("one repo's rejections triggered a takeover of a different repo")
	}
}

func TestClusterStateDispatchPRReviewFallsBackOnRemoteErrorFromADeadOwner(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setFailNext(true)
	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)

	handled := cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if handled {
		t.Error("DispatchPRReview() = true, want false when the owner is confirmed down and the call fails")
	}
}

// A takeover is the operator's only signal that the hub may be duplicating a
// partitioned instance's reviews, so it must reach the SSE stream — #765 was
// invisible because only "instance became unreachable" was ever reported, and
// that reads as "not working", not "working twice".
func TestClusterStateAnnouncesATakeoverOnce(t *testing.T) {
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	sub := broker.Subscribe()
	if sub == nil {
		t.Fatal("broker subscribe returned nil")
	}

	remote := newDispatchRemote(t)
	cs := dispatchHubWithBroker(t, remote, broker)
	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)

	// The prober shares this broker, so instance_down events land here too.
	// Count only the takeovers.
	drainTakeovers := func(wait time.Duration) int {
		n := 0
		deadline := time.After(wait)
		for {
			select {
			case ev := <-sub:
				if ev.Type == sse.EventInstanceTakeover {
					n++
				}
			case <-deadline:
				return n
			}
		}
	}
	drainTakeovers(300 * time.Millisecond) // discard the probe transitions

	cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if got := drainTakeovers(2 * time.Second); got != 1 {
		t.Fatalf("instance_takeover events = %d, want 1 when the hub takes over a routed repo", got)
	}

	// Every poll cycle hits this path while the outage lasts; only the first
	// one may be reported.
	cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if got := drainTakeovers(300 * time.Millisecond); got != 0 {
		t.Errorf("instance_takeover events on the second cycle = %d, want 0", got)
	}
}

// A recovered instance clears the ledger, so a second outage is reported as a
// new event instead of being swallowed as a repeat of the first.
func TestClusterStateReannouncesATakeoverAfterRecovery(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)
	cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if cs.notes.claim("srv-a", "takeover:review|theirs/repo") {
		t.Fatal("takeover was not recorded as announced")
	}

	remote.setHealthy(true)
	cs.Prober().ProbeAll(context.Background()) // the transition clears the notes

	if !cs.notes.claim("srv-a", "takeover:review|theirs/repo") {
		t.Error("recovery did not clear the notes; a second outage would go unreported")
	}
}

// A routing rule naming a disabled instance (or one whose token stopped
// resolving) must fall straight through to local handling. Deferring to it
// would leave the repo unreviewed forever: it is absent from HealthyIDs and
// the prober has no state for it, so it is never "confirmed down" either.
func TestClusterStateActsLocallyWhenTheRoutedOwnerIsDisabled(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	disabled := false
	cfg.Cluster.Instances = map[string]config.InstanceConfig{
		"hub-1": {Name: "hub", BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"srv-a": {Name: "srv-a", BaseURL: remote.URL, Token: "secret", Enabled: &disabled},
	}
	cfg.Cluster.Routing = config.RoutingConfig{Orgs: map[string]string{"theirs": "srv-a"}}
	cfg.GitHub.PollInterval = "5m"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	cs.Update(cfg)

	if handled := cs.DispatchPRReview(context.Background(), "theirs/repo", 42, ""); handled {
		t.Error("DispatchPRReview() = true, want false — a disabled owner is not going to review anything")
	}
	if !tier2ShouldProcessLocally(cs.Owns, cs.OwnerCanHandle, "theirs/repo") {
		t.Error("issue triage deferred to a disabled instance; the repo would go unattended")
	}
}

func TestClusterStateOwnerCanHandleDefersWhileMerelyUnreachable(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setHealthy(false)
	cs.Prober().ProbeAll(context.Background())

	if !cs.OwnerCanHandle("theirs/repo") {
		t.Error("OwnerCanHandle() = false, want true — issue triage must not be duplicated on one missed probe")
	}
}

func TestClusterStateOwnerCanHandleFalseOnceConfirmedDown(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)

	if cs.OwnerCanHandle("theirs/repo") {
		t.Error("OwnerCanHandle() = true, want false — a dead owner's issues must not go unattended")
	}
}

func TestClusterTakeoverThresholdDefaults(t *testing.T) {
	if got := clusterTakeoverThreshold(nil); got != config.DefaultTakeoverAfterFailedProbes {
		t.Errorf("clusterTakeoverThreshold(nil) = %d, want %d", got, config.DefaultTakeoverAfterFailedProbes)
	}
	cfg := &config.Config{}
	if got := clusterTakeoverThreshold(cfg); got != config.DefaultTakeoverAfterFailedProbes {
		t.Errorf("unset threshold = %d, want the default %d", got, config.DefaultTakeoverAfterFailedProbes)
	}
	// A 0 or negative that slipped past validation must still resolve to the
	// default rather than "take over on the first missed probe" (#765).
	for _, bad := range []int{0, -3} {
		n := bad
		cfg.Cluster.TakeoverAfterFailedProbes = &n
		if got := clusterTakeoverThreshold(cfg); got != config.DefaultTakeoverAfterFailedProbes {
			t.Errorf("threshold %d resolved to %d, want the default %d",
				bad, got, config.DefaultTakeoverAfterFailedProbes)
		}
	}
	seven := 7
	cfg.Cluster.TakeoverAfterFailedProbes = &seven
	if got := clusterTakeoverThreshold(cfg); got != 7 {
		t.Errorf("configured threshold = %d, want 7", got)
	}
}

func TestClusterStateDispatchIssueReviewToHealthyOwner(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	handled := cs.DispatchIssueReview(context.Background(), "theirs/repo", 99)
	if !handled {
		t.Fatal("DispatchIssueReview() = false, want true when the owner is healthy")
	}
	if remote.triageCount() != 1 {
		t.Errorf("triage calls = %d, want 1", remote.triageCount())
	}
}

// A repo this daemon already owns (or that has no configured owner at all)
// has nothing to dispatch — the caller is expected not to even ask, but the
// method must still degrade to "handle locally" rather than erroring.
func TestClusterStateDispatchNoOpWhenNotRouted(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	if handled := cs.DispatchPRReview(context.Background(), "ours/repo", 1, ""); handled {
		t.Error("DispatchPRReview() = true for a repo with no configured remote owner")
	}
	if remote.reviewCount() != 0 {
		t.Errorf("review calls = %d, want 0", remote.reviewCount())
	}
}

// OwnerCanHandle backs the issue-processing path, which has no single issue
// id to dispatch at the point it decides whether to skip a repo. It must
// agree with DispatchPRReview's notion of "safe to hand off" so a repo is
// never left completely unattended just because its routed owner is down.
//
// Since #765 "down" means confirmed down, not merely unreachable — see
// TestClusterStateOwnerCanHandleDefersWhileMerelyUnreachable for the
// distinction and why triaging locally on one missed probe was wrong.
func TestClusterStateOwnerCanHandleReflectsHealth(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	if !cs.OwnerCanHandle("theirs/repo") {
		t.Error("OwnerCanHandle() = false, want true for a routed, healthy owner")
	}
	if cs.OwnerCanHandle("ours/repo") {
		t.Error("OwnerCanHandle() = true for a repo with no configured remote owner")
	}

	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)
	if cs.OwnerCanHandle("theirs/repo") {
		t.Error("OwnerCanHandle() = true for an owner confirmed down")
	}
}

// PR review feedback (HIGH): only a hub builds a prober, so on a
// worker/standalone daemon with routing configured confirmedDown() can never
// become true. Deferring there would skip the work forever with nothing able
// to escalate it — the exact class of bug dispatch-with-fallback exists to
// prevent. A daemon that cannot observe its peers has no grounds to defer.
func TestClusterStateWithoutAProberFallsBackLocallyOnRPCFailure(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)

	// Demote to a worker: same registry and routing, no prober.
	cs.mu.Lock()
	cs.prober = nil
	cs.mu.Unlock()
	if cs.hasProber() {
		t.Fatal("precondition: the prober should be gone")
	}

	// Every cycle, not just the first: there is no failure count that can
	// escalate on a daemon with no health history, so it must never defer.
	remote.setFailNext(true)
	for i := 1; i <= cs.takeoverThreshold()+1; i++ {
		if cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "") {
			t.Fatalf("DispatchPRReview() = true on attempt %d without a prober; the review would be skipped", i)
		}
	}
}

// A config reload that drops an instance must drop what we remember about it,
// the way Prober.Update prunes its own states.
func TestClusterStateUpdatePrunesNotesForRemovedInstances(t *testing.T) {
	remote := newDispatchRemote(t)
	cs := dispatchHub(t, remote)
	remote.setHealthy(false)
	probeUntilConfirmedDown(t, cs)
	cs.DispatchPRReview(context.Background(), "theirs/repo", 42, "")
	if cs.notes.claim("srv-a", "takeover:review|theirs/repo") {
		t.Fatal("precondition: the takeover should be recorded")
	}

	// Reload without srv-a.
	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.Instances = map[string]config.InstanceConfig{
		"hub-1": {Name: "hub", BaseURL: "http://127.0.0.1:7842", Token: "t"},
	}
	cfg.GitHub.PollInterval = "5m"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	cs.Update(cfg)

	if !cs.notes.claim("srv-a", "takeover:review|theirs/repo") {
		t.Error("notes for a removed instance survived the reload")
	}
}

// The notes are keyed per operation as well as per repo: PR review and issue
// triage are taken over through different call paths, and a repo-only key let
// whichever fired first silence the other.
func TestClusterStateNotesAreKeyedPerOperation(t *testing.T) {
	var n instanceNotes
	if !n.claim("srv-a", "takeover:review|o/r") {
		t.Fatal("first review notice suppressed")
	}
	if !n.claim("srv-a", "takeover:issue_triage|o/r") {
		t.Error("the review notice suppressed the issue-triage notice for the same repo")
	}
	if n.claim("srv-a", "takeover:review|o/r") {
		t.Error("the review notice was reported twice")
	}
	n.forget("srv-a")
	if !n.claim("srv-a", "takeover:review|o/r") {
		t.Error("forget() did not clear the notice")
	}
}

func TestClusterStateDispatchNilReceiverIsPermissive(t *testing.T) {
	var cs *clusterState
	if cs.DispatchPRReview(context.Background(), "any/repo", 1, "") {
		t.Error("nil clusterState.DispatchPRReview = true, want false (fall back to local)")
	}
	if cs.DispatchIssueReview(context.Background(), "any/repo", 1) {
		t.Error("nil clusterState.DispatchIssueReview = true, want false (fall back to local)")
	}
}

// ------------------------------------------------- Automatic config propagation

// propagateTarget is a fake worker that records PATCH /config bodies.
type propagateTarget struct {
	*httptest.Server
	mu      sync.Mutex
	patches []map[string]any
}

func newPropagateTarget(t *testing.T) *propagateTarget {
	t.Helper()
	pt := &propagateTarget{}
	pt.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "instance_id": "srv-a", "role": "worker"})
		case r.URL.Path == "/config" && r.Method == http.MethodPatch:
			var patch map[string]any
			_ = json.NewDecoder(r.Body).Decode(&patch)
			pt.mu.Lock()
			pt.patches = append(pt.patches, patch)
			pt.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(pt.Close)
	return pt
}

func (pt *propagateTarget) received() []map[string]any {
	pt.mu.Lock()
	defer pt.mu.Unlock()
	return append([]map[string]any(nil), pt.patches...)
}

func propagateHub(t *testing.T, remote *propagateTarget) *clusterState {
	t.Helper()
	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.Instances = map[string]config.InstanceConfig{
		"hub-1": {Name: "hub", BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"srv-a": {Name: "srv-a", BaseURL: remote.URL, Token: "secret"},
	}
	cfg.GitHub.PollInterval = "5m"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("config invalid: %v", err)
	}
	cs := newClusterState(cfg, nil, nil)
	cs.Prober().ProbeAll(context.Background())
	return cs
}

func writeTestConfigTOML(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPropagateClusterConfigPushesToHealthyRemote(t *testing.T) {
	remote := newPropagateTarget(t)
	cs := propagateHub(t, remote)
	cfgPath := writeTestConfigTOML(t, `
[ai]
primary = "codex"
review_mode = "single"
`)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	sub := broker.Subscribe()

	propagateClusterConfig(context.Background(), cfgPath, cs, broker)

	got := remote.received()
	if len(got) != 1 {
		t.Fatalf("remote received %d PATCH /config calls, want 1", len(got))
	}
	ai, ok := got[0]["ai"].(map[string]any)
	if !ok || ai["primary"] != "codex" {
		t.Errorf("patch = %+v, want ai.primary=codex from the file on disk", got[0])
	}

	select {
	case ev := <-sub:
		if ev.Type != sse.EventConfigPropagated {
			t.Errorf("event type = %q, want %q", ev.Type, sse.EventConfigPropagated)
		}
	case <-time.After(2 * time.Second):
		t.Error("no config_propagated event published")
	}
}

// cluster.instance_id must never leak to a worker: propagating it back would
// let the hub silently overwrite the identity ensureSelfInstance seeded on
// the other side.
func TestPropagateClusterConfigOmitsLocalOnlyKeys(t *testing.T) {
	remote := newPropagateTarget(t)
	cs := propagateHub(t, remote)
	cfgPath := writeTestConfigTOML(t, `
[ai]
primary = "codex"

[cluster]
role = "hub"
instance_id = "hub-1"

[github]
repositories = ["acme/one"]
`)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	propagateClusterConfig(context.Background(), cfgPath, cs, broker)

	got := remote.received()
	if len(got) != 1 {
		t.Fatalf("remote received %d PATCH /config calls, want 1", len(got))
	}
	if _, present := got[0]["cluster"]; present {
		t.Errorf("patch = %+v, want no [cluster] section propagated", got[0])
	}
	if gh, ok := got[0]["github"].(map[string]any); ok {
		if _, present := gh["repositories"]; present {
			t.Errorf("patch.github = %+v, want repositories not propagated", gh)
		}
	}
}

func TestPropagateClusterConfigToleratesUnreadableFile(t *testing.T) {
	remote := newPropagateTarget(t)
	cs := propagateHub(t, remote)
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	// Must not panic on a missing/unreadable config file.
	propagateClusterConfig(context.Background(), filepath.Join(t.TempDir(), "missing.toml"), cs, broker)

	if len(remote.received()) != 0 {
		t.Error("propagation was attempted despite an unreadable config file")
	}
}

func TestEnsureSelfInstanceDefaultsThePort(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	ensureSelfInstance(cfg, "/data")
	if got := cfg.Cluster.Instances["hub-1"].BaseURL; got != "http://127.0.0.1:7842" {
		t.Errorf("base_url = %q, want the default port", got)
	}
}
