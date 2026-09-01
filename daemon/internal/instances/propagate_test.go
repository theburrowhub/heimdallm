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
