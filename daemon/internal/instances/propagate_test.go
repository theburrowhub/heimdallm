package instances

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

func TestIsLocalOnly(t *testing.T) {
	local := []string{
		"cluster", "cluster.routing", "cluster.routing.repos", "cluster.instances.srv-a.token",
		"server.port", "server.bind_addr", "server.max_concurrent_workers",
		"github.token", "github.repositories", "github.non_monitored", "github.first_seen_at",
		"ai.local_dir_base", "ai.local_dirs_detected",
		// Flat keys as they appear in GET /config's map.
		"server_port", "bind_addr", "github_token", "repositories", "non_monitored",
		"local_dir_base", "local_dirs_detected", "first_seen_at", "instance_id", "instance_name",
		// Case must not be a way around the denylist.
		"Server.Port", "GITHUB.TOKEN",
	}
	for _, path := range local {
		if !IsLocalOnly(path) {
			t.Errorf("IsLocalOnly(%q) = false, want true", path)
		}
	}

	shared := []string{
		"", "ai", "ai.primary", "ai.review_mode", "ai.repos", "ai.repos.acme/tools",
		"polling", "polling.tier2_interval", "merge_tracking", "merge_tracking.enabled",
		"merge_tracking.repos", "circuit_breaker.enabled", "autonomous.enabled",
		"retention.max_days", "github.poll_interval", "github.discovery_orgs",
		// Not a prefix match: "servers" must not be caught by "server".
		"servers.something", "clusters.foo",
	}
	for _, path := range shared {
		if IsLocalOnly(path) {
			t.Errorf("IsLocalOnly(%q) = true, want false", path)
		}
	}
}

func TestFilterPropagatableDropsOnlyLocalLeaves(t *testing.T) {
	patch := map[string]any{
		"server": map[string]any{
			"port":                   7842,
			"bind_addr":              "0.0.0.0",
			"max_concurrent_workers": 5,
		},
		"github": map[string]any{
			"token":         "ghp_secret",
			"repositories":  []string{"acme/a"},
			"poll_interval": "5m",
		},
		"ai": map[string]any{
			"review_mode":    "multi",
			"local_dir_base": "/Users/someone/projects",
		},
		"cluster": map[string]any{"role": "hub"},
		"merge_tracking": map[string]any{
			"enabled": true,
			"repos":   map[string]any{"acme/a": map[string]any{"merge": true}},
		},
	}

	filtered, dropped := FilterPropagatable(patch)

	// A whole table must not be lost because part of it was local.
	gh, ok := filtered["github"].(map[string]any)
	if !ok {
		t.Fatalf("github table was dropped entirely: %v", filtered)
	}
	if _, leaked := gh["token"]; leaked {
		t.Error("github.token leaked into the propagated patch")
	}
	if _, leaked := gh["repositories"]; leaked {
		t.Error("github.repositories leaked into the propagated patch")
	}
	if gh["poll_interval"] != "5m" {
		t.Errorf("github.poll_interval = %v, want it preserved", gh["poll_interval"])
	}

	// A table whose every key was local should vanish rather than be written
	// as an empty TOML table.
	if _, present := filtered["server"]; present {
		t.Errorf("server table survived with %v, want it dropped entirely", filtered["server"])
	}
	if _, present := filtered["cluster"]; present {
		t.Error("cluster must never be propagated")
	}

	aiTable := filtered["ai"].(map[string]any)
	if _, leaked := aiTable["local_dir_base"]; leaked {
		t.Error("ai.local_dir_base leaked; it is a path on the hub's filesystem")
	}
	if aiTable["review_mode"] != "multi" {
		t.Error("ai.review_mode should propagate")
	}
	if _, present := filtered["merge_tracking"]; !present {
		t.Error("merge_tracking should propagate wholesale, scoped overrides included")
	}

	for _, want := range []string{"ai.local_dir_base", "cluster", "github.repositories", "github.token", "server.bind_addr", "server.port"} {
		if !containsID(dropped, want) {
			t.Errorf("dropped list %v is missing %q", dropped, want)
		}
	}
}

func TestFilterPropagatableEmptyInput(t *testing.T) {
	filtered, dropped := FilterPropagatable(nil)
	if len(filtered) != 0 || len(dropped) != 0 {
		t.Errorf("FilterPropagatable(nil) = %v, %v; want both empty", filtered, dropped)
	}
}

// propagatorFixture wires a Propagator over N fake daemons.
type propagatorFixture struct {
	prop    *Propagator
	daemons map[string]*fakeDaemon
}

func newPropagatorFixture(t *testing.T, selfID string, ids []string, handler func(id string) func(http.ResponseWriter, *http.Request)) *propagatorFixture {
	t.Helper()
	daemons := make(map[string]*fakeDaemon, len(ids))
	insts := make(map[string]config.InstanceConfig, len(ids))
	for _, id := range ids {
		d := newFakeDaemon(t, handler(id))
		daemons[id] = d
		insts[id] = config.InstanceConfig{Name: id, BaseURL: d.URL, Token: "secret"}
	}
	cfg := cfgWith(config.RoleHub, selfID, selfID, insts, config.RoutingConfig{})
	reg := NewRegistry(cfg)
	prop := NewPropagator(reg, func(inst Instance) *Client {
		return NewClient(inst, daemons[inst.ID].Client())
	})
	return &propagatorFixture{prop: prop, daemons: daemons}
}

func okHandler(string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

func TestPropagateSkipsSelfAndPushesToOthers(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a", "srv-b"}, okHandler)

	results := f.prop.Propagate(context.Background(), map[string]any{
		"ai": map[string]any{"review_mode": "multi"},
	}, nil)

	if len(results) != 3 {
		t.Fatalf("got %d results, want one per instance", len(results))
	}
	byID := map[string]Result{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}

	// The hub is the source of truth; pushing its own config back to itself
	// would be a no-op at best and a write loop at worst.
	if !byID["hub"].Skipped || !byID["hub"].OK {
		t.Errorf("hub result = %+v, want skipped and OK", byID["hub"])
	}
	if len(f.daemons["hub"].seen()) != 0 {
		t.Errorf("hub received %d requests, want none", len(f.daemons["hub"].seen()))
	}

	for _, id := range []string{"srv-a", "srv-b"} {
		if !byID[id].OK || byID[id].Skipped {
			t.Errorf("%s result = %+v, want applied", id, byID[id])
		}
		if !containsID(byID[id].AppliedKey, "ai.review_mode") {
			t.Errorf("%s applied keys = %v, want ai.review_mode", id, byID[id].AppliedKey)
		}
		seen := f.daemons[id].seen()
		if len(seen) != 1 || seen[0].Method != http.MethodPatch || seen[0].Path != "/config" {
			t.Errorf("%s requests = %+v, want one PATCH /config", id, seen)
		}
	}
}

// The whole point of the denylist: a local key must not reach the wire even if
// the caller hands it to Propagate.
func TestPropagateNeverSendsLocalKeys(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a"}, okHandler)

	f.prop.Propagate(context.Background(), map[string]any{
		"server": map[string]any{"port": 9999, "bind_addr": "0.0.0.0"},
		"github": map[string]any{"token": "ghp_leaked", "poll_interval": "3m"},
		"cluster": map[string]any{
			"instances": map[string]any{"srv-a": map[string]any{"token": "other"}},
		},
		"ai": map[string]any{"review_mode": "multi"},
	}, nil)

	seen := f.daemons["srv-a"].seen()
	if len(seen) != 1 {
		t.Fatalf("got %d requests, want 1", len(seen))
	}
	body := seen[0].Body
	for _, forbidden := range []string{"ghp_leaked", "9999", "0.0.0.0", "bind_addr", "cluster", "instances"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("propagated body leaked %q: %s", forbidden, body)
		}
	}
	for _, wanted := range []string{"review_mode", "poll_interval"} {
		if !strings.Contains(body, wanted) {
			t.Errorf("propagated body is missing %q: %s", wanted, body)
		}
	}
}

func TestPropagateNothingLeftAfterFiltering(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a"}, okHandler)
	results := f.prop.Propagate(context.Background(), map[string]any{
		"server": map[string]any{"port": 1234},
	}, nil)

	for _, r := range results {
		if r.InstanceID != "srv-a" {
			continue
		}
		if !r.Skipped || !r.OK {
			t.Errorf("srv-a result = %+v, want skipped-OK when nothing is left to send", r)
		}
	}
	if got := len(f.daemons["srv-a"].seen()); got != 0 {
		t.Errorf("srv-a received %d requests, want none for an empty patch", got)
	}
}

// One machine rebooting must not stop the others from being updated: partial
// success is the normal outcome and the operator needs to know exactly which
// instances to retry.
func TestPropagatePartialFailure(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "good", "bad"}, func(id string) func(http.ResponseWriter, *http.Request) {
		if id == "bad" {
			return func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"status":"starting"}`))
			}
		}
		return okHandler(id)
	})

	results := f.prop.Propagate(context.Background(), map[string]any{
		"ai": map[string]any{"review_mode": "multi"},
	}, nil)

	byID := map[string]Result{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	if !byID["good"].OK {
		t.Errorf("good = %+v, want OK despite bad failing", byID["good"])
	}
	if byID["bad"].OK || byID["bad"].Error == "" {
		t.Errorf("bad = %+v, want a failure carrying the reason", byID["bad"])
	}
	if len(f.daemons["good"].seen()) != 1 {
		t.Error("the healthy instance should still have been patched")
	}
}

func TestPropagateRespectsTargets(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a", "srv-b"}, okHandler)
	results := f.prop.Propagate(context.Background(), map[string]any{
		"ai": map[string]any{"review_mode": "multi"},
	}, []string{"srv-b"})

	if len(results) != 1 || results[0].InstanceID != "srv-b" {
		t.Fatalf("results = %+v, want only srv-b", results)
	}
	if len(f.daemons["srv-a"].seen()) != 0 {
		t.Error("srv-a was patched despite not being targeted")
	}
}

func TestPropagateSkipsDisabledAndTokenlessInstances(t *testing.T) {
	good := newFakeDaemon(t, okHandler("good"))
	off := newFakeDaemon(t, okHandler("off"))

	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub":   {BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"good":  {BaseURL: good.URL, Token: "secret"},
		"off":   {BaseURL: off.URL, Token: "secret", Enabled: boolPtr(false)},
		"notok": {BaseURL: "http://127.0.0.1:1", TokenEnv: "HEIMDALLM_TEST_UNSET_PROP"},
	}, config.RoutingConfig{})
	reg := NewRegistry(cfg)
	prop := NewPropagator(reg, func(inst Instance) *Client {
		switch inst.ID {
		case "good":
			return NewClient(inst, good.Client())
		case "off":
			return NewClient(inst, off.Client())
		default:
			return NewClient(inst, nil)
		}
	})

	results := prop.Propagate(context.Background(), map[string]any{
		"ai": map[string]any{"review_mode": "multi"},
	}, nil)
	byID := map[string]Result{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	if !byID["off"].Skipped || byID["off"].OK {
		t.Errorf("off = %+v, want skipped and not OK", byID["off"])
	}
	if len(off.seen()) != 0 {
		t.Error("a disabled instance must not be contacted")
	}
	if byID["notok"].OK || byID["notok"].Error == "" {
		t.Errorf("notok = %+v, want the token error reported", byID["notok"])
	}
	if !byID["good"].OK {
		t.Errorf("good = %+v, want OK", byID["good"])
	}
}

// ------------------------------------------------------- PropagatePartition

func TestPartitionPatchCarriesOnlyTheAssignedSubset(t *testing.T) {
	patch := PartitionPatch("srv-a", PartitionRules{
		DefaultInstance: "hub-1",
		Orgs:            map[string]string{"acme": "srv-a"},
		Repos:           map[string]string{"acme/tools": "hub-1"},
	})
	cluster, ok := patch["cluster"].(map[string]any)
	if !ok {
		t.Fatalf("patch = %v, want a cluster table", patch)
	}
	if cluster["instance_id"] != "srv-a" {
		t.Errorf("instance_id = %v, want srv-a", cluster["instance_id"])
	}
	if cluster["default_instance"] != "hub-1" {
		t.Errorf("default_instance = %v, want hub-1", cluster["default_instance"])
	}
	for _, forbidden := range []string{"role", "instances", "token", "probe_interval", "round_robin_pool"} {
		if _, present := cluster[forbidden]; present {
			t.Errorf("patch.cluster leaked %q; only the partition subset must travel", forbidden)
		}
	}
	routing, ok := cluster["routing"].(map[string]any)
	if !ok {
		t.Fatalf("cluster.routing missing: %v", cluster)
	}
	if got := routing["orgs"].(map[string]any)["acme"]; got != "srv-a" {
		t.Errorf("routing.orgs = %v, want acme->srv-a", routing["orgs"])
	}
	if got := routing["repos"].(map[string]any)["acme/tools"]; got != "hub-1" {
		t.Errorf("routing.repos = %v, want acme/tools->hub-1", routing["repos"])
	}
}

// partitionRemote is a fake instance whose /health, /cluster/partition and
// /config behave independently, so the PropagatePartition tests can exercise
// the modern path, the legacy fallback and the identity-mismatch case.
type partitionRemote struct {
	healthID       string // what /health reports as instance_id; "" omits it
	partitionCode  int    // status PUT /cluster/partition answers with; 0 means 200
	patchCalls     *int
	partitionCalls *int
}

func (r partitionRemote) handler(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.URL.Path == "/health":
		body := map[string]any{"status": "ok", "role": "worker"}
		if r.healthID != "" {
			body["instance_id"] = r.healthID
		}
		_ = json.NewEncoder(w).Encode(body)
	case req.URL.Path == "/cluster/partition":
		if r.partitionCalls != nil {
			*r.partitionCalls++
		}
		code := r.partitionCode
		if code == 0 {
			code = http.StatusOK
		}
		w.WriteHeader(code)
		if code == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]any{"changed": true})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "nope"})
		}
	case req.URL.Path == "/config" && req.Method == http.MethodPatch:
		if r.patchCalls != nil {
			*r.patchCalls++
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	default:
		http.NotFound(w, req)
	}
}

func TestPropagatePartitionPushesPerInstanceIdentity(t *testing.T) {
	partitionCallsA, partitionCallsB := 0, 0
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a", "srv-b"}, func(id string) func(http.ResponseWriter, *http.Request) {
		switch id {
		case "srv-a":
			return (partitionRemote{healthID: "srv-a", partitionCalls: &partitionCallsA}).handler
		case "srv-b":
			return (partitionRemote{healthID: "srv-b", partitionCalls: &partitionCallsB}).handler
		default:
			return okHandler(id)
		}
	})

	results := f.prop.PropagatePartition(context.Background(), PartitionRules{
		DefaultInstance: "hub",
		Orgs:            map[string]string{"acme": "srv-a"},
	}, nil)

	byID := map[string]PartitionResult{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	if !byID["hub"].Skipped || !byID["hub"].OK {
		t.Errorf("hub result = %+v, want skipped and OK (self)", byID["hub"])
	}
	if !byID["srv-a"].OK || byID["srv-a"].Legacy {
		t.Errorf("srv-a result = %+v, want OK via the modern path", byID["srv-a"])
	}
	if !byID["srv-b"].OK {
		t.Errorf("srv-b result = %+v, want OK", byID["srv-b"])
	}
	if partitionCallsA != 1 || partitionCallsB != 1 {
		t.Errorf("PUT /cluster/partition calls = a:%d b:%d, want 1 each", partitionCallsA, partitionCallsB)
	}
	body := f.daemons["srv-a"].seen()
	last := body[len(body)-1]
	if !strings.Contains(last.Body, `"instance_id":"srv-a"`) {
		t.Errorf("srv-a's push body = %q, want its own id, not a shared one", last.Body)
	}
}

func TestPropagatePartitionFallsBackToPatchOnLegacy404(t *testing.T) {
	patchCalls := 0
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a"}, func(id string) func(http.ResponseWriter, *http.Request) {
		if id == "srv-a" {
			return (partitionRemote{healthID: "srv-a", partitionCode: http.StatusNotFound, patchCalls: &patchCalls}).handler
		}
		return okHandler(id)
	})

	results := f.prop.PropagatePartition(context.Background(), PartitionRules{Orgs: map[string]string{"acme": "srv-a"}}, nil)
	byID := map[string]PartitionResult{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	if !byID["srv-a"].OK || !byID["srv-a"].Legacy {
		t.Errorf("srv-a result = %+v, want OK and Legacy=true", byID["srv-a"])
	}
	if patchCalls != 1 {
		t.Errorf("PATCH /config calls = %d, want 1 (the legacy fallback)", patchCalls)
	}
}

// The Friday case, live: registered under one id, reporting itself as
// another. A legacy instance cannot adopt a new identity through PATCH
// /config (its resolveReloadInstanceID does not exist yet), so pushing rules
// under an id it does not recognise as itself would leave it filtering
// everything out — the rules must be withheld, not silently misapplied.
func TestPropagatePartitionWithholdsRulesFromALegacyMismatchedInstance(t *testing.T) {
	patchCalls := 0
	f := newPropagatorFixture(t, "hub", []string{"hub", "192-168-1-100-3000"}, func(id string) func(http.ResponseWriter, *http.Request) {
		if id == "192-168-1-100-3000" {
			return (partitionRemote{healthID: "friday", partitionCode: http.StatusNotFound, patchCalls: &patchCalls}).handler
		}
		return okHandler(id)
	})

	results := f.prop.PropagatePartition(context.Background(), PartitionRules{Orgs: map[string]string{"Muriano": "192-168-1-100-3000"}}, nil)
	byID := map[string]PartitionResult{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	res := byID["192-168-1-100-3000"]
	if res.OK {
		t.Errorf("result = %+v, want OK=false — rules must not be applied under a mismatched identity", res)
	}
	if !res.IdentityMismatch {
		t.Errorf("result = %+v, want IdentityMismatch=true", res)
	}
	if res.Error == "" {
		t.Error("result carries no error explaining the withheld push")
	}
	if patchCalls != 0 {
		t.Errorf("PATCH /config calls = %d, want 0 (rules withheld, not applied)", patchCalls)
	}
}

func TestPropagatePartitionSkipsSelfDisabledAndUnreachable(t *testing.T) {
	off := newFakeDaemon(t, okHandler("off"))
	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub":  {BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"off":  {BaseURL: off.URL, Token: "secret", Enabled: boolPtr(false)},
		"dead": {BaseURL: "http://127.0.0.1:1", Token: "secret"},
	}, config.RoutingConfig{})
	reg := NewRegistry(cfg)
	prop := NewPropagator(reg, func(inst Instance) *Client {
		if inst.ID == "off" {
			return NewClient(inst, off.Client())
		}
		return NewClient(inst, nil)
	})

	results := prop.PropagatePartition(context.Background(), PartitionRules{}, nil)
	byID := map[string]PartitionResult{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	if !byID["hub"].Skipped || !byID["hub"].OK {
		t.Errorf("hub = %+v, want skipped and OK", byID["hub"])
	}
	if !byID["off"].Skipped || byID["off"].OK {
		t.Errorf("off = %+v, want skipped and not OK", byID["off"])
	}
	if len(off.seen()) != 0 {
		t.Error("a disabled instance must not be contacted")
	}
	if byID["dead"].OK || byID["dead"].Error == "" {
		t.Errorf("dead = %+v, want a failure carrying the reason", byID["dead"])
	}
}

// A 503 (still starting) must not be read as "this instance predates the
// endpoint" — it will accept the same request once it finishes booting, so it
// is reported as a plain failure to retry, not permanently downgraded to the
// legacy PATCH path.
func TestPropagatePartitionTreats503AsRetryNotLegacy(t *testing.T) {
	patchCalls := 0
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a"}, func(id string) func(http.ResponseWriter, *http.Request) {
		if id == "srv-a" {
			return (partitionRemote{healthID: "srv-a", partitionCode: http.StatusServiceUnavailable, patchCalls: &patchCalls}).handler
		}
		return okHandler(id)
	})

	results := f.prop.PropagatePartition(context.Background(), PartitionRules{}, nil)
	byID := map[string]PartitionResult{}
	for _, r := range results {
		byID[r.InstanceID] = r
	}
	res := byID["srv-a"]
	if res.OK || res.Legacy {
		t.Errorf("result = %+v, want OK=false Legacy=false on a 503", res)
	}
	if patchCalls != 0 {
		t.Errorf("PATCH /config calls = %d, want 0 — a 503 must not trigger the legacy fallback", patchCalls)
	}
}

func TestDetectDrift(t *testing.T) {
	remote := map[string]any{
		"review_mode":   "single",    // differs
		"poll_interval": "5m",        // matches
		"max_days":      float64(90), // matches after a JSON round trip
		"server_port":   7843,        // local: must not be reported
	}
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a"}, func(id string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(remote)
		}
	})

	hubConfig := map[string]any{
		"review_mode":   "multi",
		"poll_interval": "5m",
		"max_days":      90, // int on the hub, float64 on the wire
		"server_port":   7842,
		"github_token":  "ghp_x",
		"tier3_enabled": true, // absent on the remote
	}

	drifts := f.prop.DetectDrift(context.Background(), hubConfig, nil)
	byID := map[string]InstanceDrift{}
	for _, d := range drifts {
		byID[d.InstanceID] = d
	}
	if !byID["hub"].Skipped {
		t.Errorf("hub = %+v, want skipped", byID["hub"])
	}

	srv := byID["srv-a"]
	if !srv.OK {
		t.Fatalf("srv-a = %+v, want OK", srv)
	}
	keys := map[string]Drift{}
	for _, d := range srv.Drifts {
		keys[d.Key] = d
	}
	if _, ok := keys["review_mode"]; !ok {
		t.Error("review_mode differs but was not reported")
	}
	if _, ok := keys["poll_interval"]; ok {
		t.Error("poll_interval matches but was reported as drift")
	}
	// An int on the hub vs a float64 off the wire is the same number; treating
	// it as drift would make every instance look permanently out of sync.
	if _, ok := keys["max_days"]; ok {
		t.Error("max_days 90 vs 90.0 must not count as drift")
	}
	// Machine-specific keys are legitimately different everywhere.
	if _, ok := keys["server_port"]; ok {
		t.Error("server_port is machine-specific and must not be reported as drift")
	}
	if _, ok := keys["github_token"]; ok {
		t.Error("github_token must never be compared or echoed")
	}
	if d, ok := keys["tier3_enabled"]; !ok || !d.Missing {
		t.Errorf("tier3_enabled = %+v, want it reported as missing on the instance", d)
	}
}

func TestDetectDriftReportsFetchFailure(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a"}, func(id string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	drifts := f.prop.DetectDrift(context.Background(), map[string]any{"review_mode": "multi"}, nil)
	for _, d := range drifts {
		if d.InstanceID != "srv-a" {
			continue
		}
		if d.OK || d.Error == "" {
			t.Errorf("srv-a = %+v, want the fetch failure reported instead of an empty diff", d)
		}
	}
}

func TestErrNoTargets(t *testing.T) {
	err := ErrNoTargets([]string{"a", "b"})
	if err == nil || !strings.Contains(err.Error(), "a, b") {
		t.Errorf("ErrNoTargets() = %v, want it to name the targets", err)
	}
}

func TestFlattenKeys(t *testing.T) {
	got := flattenKeys(map[string]any{
		"b": 1,
		"a": map[string]any{"y": 2, "x": 3},
		"c": map[string]any{}, // empty table counts as a leaf, not skipped
	})
	if fmt.Sprint(got) != "[a.x a.y b c]" {
		t.Errorf("flattenKeys() = %v, want sorted dotted leaves", got)
	}
}

func TestNewPropagatorDefaultFactory(t *testing.T) {
	// A nil factory must produce a working real-HTTP propagator rather than
	// panicking at the first push.
	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub":  {BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"down": {BaseURL: "http://127.0.0.1:1", Token: "t"},
	}, config.RoutingConfig{})
	prop := NewPropagator(NewRegistry(cfg), nil)

	results := prop.Propagate(context.Background(), map[string]any{
		"ai": map[string]any{"review_mode": "multi"},
	}, nil)

	var down Result
	for _, r := range results {
		if r.InstanceID == "down" {
			down = r
		}
	}
	if down.OK || down.Error == "" {
		t.Errorf("unreachable instance = %+v, want a reported failure", down)
	}
}

func TestEqualConfigValues(t *testing.T) {
	// An int on the hub and a float64 off the wire are the same number;
	// reporting that as drift would make every instance look out of sync.
	if !equalConfigValues(90, 90.0) {
		t.Error("90 and 90.0 should compare equal")
	}
	if !equalConfigValues(int64(5), float32(5)) {
		t.Error("int64 5 and float32 5 should compare equal")
	}
	if equalConfigValues(90, 91.0) {
		t.Error("different numbers should not compare equal")
	}
	if equalConfigValues(90, "90") {
		t.Error("a number and a string are not equal")
	}
	if !equalConfigValues([]any{"a"}, []any{"a"}) {
		t.Error("equal slices should compare equal")
	}
	if equalConfigValues("a", "b") {
		t.Error("different strings should not compare equal")
	}
}

func TestDetectDriftSkipsDisabledAndTokenlessInstances(t *testing.T) {
	cfg := cfgWith(config.RoleHub, "hub", "hub", map[string]config.InstanceConfig{
		"hub":   {BaseURL: "http://127.0.0.1:7842", Token: "t"},
		"off":   {BaseURL: "http://127.0.0.1:7843", Token: "t", Enabled: boolPtr(false)},
		"notok": {BaseURL: "http://127.0.0.1:7844", TokenEnv: "HEIMDALLM_TEST_UNSET_DRIFT"},
	}, config.RoutingConfig{})
	prop := NewPropagator(NewRegistry(cfg), func(inst Instance) *Client {
		return NewClient(inst, nil)
	})

	byID := map[string]InstanceDrift{}
	for _, d := range prop.DetectDrift(context.Background(), map[string]any{"x": 1}, nil) {
		byID[d.InstanceID] = d
	}
	if !byID["off"].Skipped {
		t.Errorf("off = %+v, want skipped", byID["off"])
	}
	if byID["notok"].Error == "" {
		t.Errorf("notok = %+v, want the token error reported", byID["notok"])
	}
}

func TestDetectDriftRespectsTargets(t *testing.T) {
	f := newPropagatorFixture(t, "hub", []string{"hub", "srv-a", "srv-b"}, func(id string) func(http.ResponseWriter, *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"x": 1})
		}
	})
	drifts := f.prop.DetectDrift(context.Background(), map[string]any{"x": 1}, []string{"srv-b"})
	if len(drifts) != 1 || drifts[0].InstanceID != "srv-b" {
		t.Errorf("drifts = %+v, want only srv-b", drifts)
	}
}

// The hub builds its config from typed Go values; an instance's arrives decoded
// from JSON. Comparing those with reflect.DeepEqual alone reported every nested
// section as drift, which is enough noise to make the whole view useless.
func TestEqualConfigValuesCanonicalisesThroughJSON(t *testing.T) {
	type typedSection struct {
		Enabled bool   `json:"enabled"`
		Method  string `json:"merge_method"`
	}

	cases := []struct {
		name string
		hub  any
		wire any
		want bool
	}{
		{"empty typed map vs empty generic map", map[string]typedSection{}, map[string]any{}, true},
		{
			name: "typed struct vs decoded object",
			hub:  typedSection{Enabled: true, Method: "squash"},
			wire: map[string]any{"enabled": true, "merge_method": "squash"},
			want: true,
		},
		{
			name: "key order does not matter",
			hub:  map[string]any{"a": 1, "b": 2},
			wire: map[string]any{"b": 2.0, "a": 1.0},
			want: true,
		},
		{
			name: "a real difference is still reported",
			hub:  typedSection{Enabled: true, Method: "squash"},
			wire: map[string]any{"enabled": false, "merge_method": "squash"},
			want: false,
		},
		{
			name: "typed slice vs decoded array",
			hub:  []string{"a", "b"},
			wire: []any{"a", "b"},
			want: true,
		},
		{
			name: "different slice contents differ",
			hub:  []string{"a"},
			wire: []any{"b"},
			want: false,
		},
	}
	for _, tc := range cases {
		if got := equalConfigValues(tc.hub, tc.wire); got != tc.want {
			t.Errorf("%s: equalConfigValues = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestEqualConfigValuesUnmarshalable(t *testing.T) {
	// A value JSON cannot represent must not be silently declared equal.
	if equalConfigValues(make(chan int), map[string]any{}) {
		t.Error("an unmarshalable value compared equal")
	}
}

// Marshalling a struct emits fields in declaration order while marshalling a
// map sorts the keys, so identical content on the two sides produced different
// bytes and every nested section read as drift.
func TestEqualConfigValuesIgnoresStructFieldOrder(t *testing.T) {
	type section struct {
		Zulu  string `json:"zulu"`
		Alpha string `json:"alpha"`
	}
	hub := section{Zulu: "z", Alpha: "a"}
	wire := map[string]any{"alpha": "a", "zulu": "z"}

	if !equalConfigValues(hub, wire) {
		t.Error("a struct and an equivalent decoded map compared unequal")
	}
	if equalConfigValues(hub, map[string]any{"alpha": "a", "zulu": "different"}) {
		t.Error("a real difference was missed")
	}
}

func TestCanonicalJSON(t *testing.T) {
	if _, ok := canonicalJSON(make(chan int)); ok {
		t.Error("canonicalJSON accepted an unmarshalable value")
	}
	a, aok := canonicalJSON(map[string]any{"b": 1, "a": 2})
	b, bok := canonicalJSON(map[string]any{"a": 2, "b": 1})
	if !aok || !bok || string(a) != string(b) {
		t.Errorf("canonicalJSON is not order-independent: %q vs %q", a, b)
	}
}
