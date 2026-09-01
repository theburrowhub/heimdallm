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

	events := newClusterEvents(broker)
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
	if got := newClusterEvents(nil); got != nil {
		t.Errorf("newClusterEvents(nil) = %v, want nil", got)
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

	n, err := a.ProcessRepo(t.Context(), "acme/not-mine")
	if err != nil {
		t.Fatalf("ProcessRepo: %v", err)
	}
	if n != 0 {
		t.Errorf("processed %d issues on an unowned repo, want 0", n)
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

func TestEnsureSelfInstanceDefaultsThePort(t *testing.T) {
	cfg := &config.Config{}
	cfg.Cluster.Role = config.RoleHub
	cfg.Cluster.InstanceID = "hub-1"
	ensureSelfInstance(cfg, "/data")
	if got := cfg.Cluster.Instances["hub-1"].BaseURL; got != "http://127.0.0.1:7842" {
		t.Errorf("base_url = %q, want the default port", got)
	}
}
