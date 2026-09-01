package instances

import (
	"fmt"
	"sync"
	"testing"

	"github.com/heimdallm/daemon/internal/config"
)

// routerWith builds a Router over a registry of the given ids (all usable).
func routerWith(selfID string, ids []string, defaultInstance string, routing config.RoutingConfig) *Router {
	insts := make(map[string]config.InstanceConfig, len(ids))
	for _, id := range ids {
		insts[id] = config.InstanceConfig{BaseURL: "http://" + id + ":7842", Token: "t"}
	}
	cfg := cfgWith(config.RoleHub, selfID, defaultInstance, insts, routing)
	return NewRouter(NewRegistry(cfg), cfg)
}

// The invariant the whole feature rests on: with no [cluster] at all, this
// daemon owns everything, exactly as it did before instances existed.
func TestRouterOwnsEverythingWhenUnconfigured(t *testing.T) {
	cfg := cfgWith("", "", "", nil, config.RoutingConfig{})
	r := NewRouter(NewRegistry(cfg), cfg)

	if r.Enabled() {
		t.Error("Enabled() = true with no cluster config")
	}
	for _, repo := range []string{"acme/tools", "other/thing", "", "weird"} {
		if !r.Owns(repo) {
			t.Errorf("Owns(%q) = false, want true when routing is not configured", repo)
		}
	}
	repos := []string{"acme/a", "acme/b"}
	if got := r.FilterOwned(repos); len(got) != 2 {
		t.Errorf("FilterOwned(%v) = %v, want everything", repos, got)
	}
	if got := r.OwnerFor("acme/tools"); got != "" {
		t.Errorf("OwnerFor() = %q, want empty when routing is off", got)
	}
	if r.RoundRobinsOp(config.OpReview) {
		t.Error("RoundRobinsOp() = true with routing off")
	}
}

// A single registered instance is still a single-daemon install: nothing to
// route between, so nothing should be filtered out.
func TestRouterSingleInstanceOwnsEverything(t *testing.T) {
	r := routerWith("only", []string{"only"}, "", config.RoutingConfig{})
	if r.Enabled() {
		t.Error("Enabled() = true with one instance and no rules")
	}
	if !r.Owns("acme/tools") {
		t.Error("Owns() = false with a single instance")
	}
}

func TestRouterNilRegistryIsSafe(t *testing.T) {
	r := NewRouter(nil, nil)
	if r.Enabled() {
		t.Error("Enabled() = true for a nil registry")
	}
	if !r.Owns("acme/tools") {
		t.Error("Owns() = false for a nil registry; must default to owning everything")
	}
	if got := r.Next(config.OpReview); got != "" {
		t.Errorf("Next() = %q, want empty with no pool", got)
	}
	if id, changed := r.AssignRepo("acme/tools"); id != "" || changed {
		t.Errorf("AssignRepo() = (%q, %v), want (\"\", false) with no pool", id, changed)
	}
}

func TestRouterPrecedenceRepoOverOrgOverFallback(t *testing.T) {
	r := routerWith("a", []string{"a", "b", "c"}, "c", config.RoutingConfig{
		Orgs:  map[string]string{"acme": "b"},
		Repos: map[string]string{"acme/special": "a"},
	})

	tests := []struct {
		repo string
		want string
		why  string
	}{
		{"acme/special", "a", "explicit repo rule wins"},
		{"acme/other", "b", "org rule applies to the rest of the org"},
		{"unrelated/thing", "c", "no rule falls back to default_instance"},
	}
	for _, tt := range tests {
		if got := r.OwnerFor(tt.repo); got != tt.want {
			t.Errorf("OwnerFor(%q) = %q, want %q (%s)", tt.repo, got, tt.want, tt.why)
		}
	}

	// RuleFor must NOT consult the fallback: callers use it to tell an
	// explicit assignment apart from an inherited one.
	if got := r.RuleFor("unrelated/thing"); got != "" {
		t.Errorf("RuleFor(unrelated/thing) = %q, want empty", got)
	}
	if got := r.RuleFor("acme/other"); got != "b" {
		t.Errorf("RuleFor(acme/other) = %q, want b", got)
	}
}

func TestRouterOrgRuleIsCaseInsensitive(t *testing.T) {
	// GitHub owner slugs are case-insensitive, so a config that says "AcmeCorp"
	// must still match a repo reported as "acmecorp/tools".
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Orgs: map[string]string{"AcmeCorp": "b"},
	})
	if got := r.OwnerFor("acmecorp/tools"); got != "b" {
		t.Errorf("OwnerFor(acmecorp/tools) = %q, want b", got)
	}
}

func TestRouterOwnsPartitions(t *testing.T) {
	routing := config.RoutingConfig{
		Orgs:  map[string]string{"acme": "b"},
		Repos: map[string]string{"acme/mine": "a"},
	}
	ra := routerWith("a", []string{"a", "b"}, "a", routing)
	rb := routerWith("b", []string{"a", "b"}, "a", routing)

	cases := map[string][2]bool{ // repo -> {a owns, b owns}
		"acme/mine":  {true, false}, // explicit repo rule to a
		"acme/other": {false, true}, // org rule to b
		"third/x":    {true, false}, // fallback default_instance = a
	}
	for repo, want := range cases {
		if got := ra.Owns(repo); got != want[0] {
			t.Errorf("a.Owns(%q) = %v, want %v", repo, got, want[0])
		}
		if got := rb.Owns(repo); got != want[1] {
			t.Errorf("b.Owns(%q) = %v, want %v", repo, got, want[1])
		}
		// The point of partitioning: exactly one daemon acts on each repo.
		if ra.Owns(repo) == rb.Owns(repo) {
			t.Errorf("%q: both daemons agree (%v); ownership must be exclusive", repo, ra.Owns(repo))
		}
	}
}

func TestRouterFilterOwned(t *testing.T) {
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Orgs: map[string]string{"theirs": "b"},
	})
	in := []string{"mine/one", "theirs/two", "mine/three", "theirs/four"}
	got := r.FilterOwned(in)
	want := []string{"mine/one", "mine/three"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("FilterOwned(%v) = %v, want %v (order preserved)", in, got, want)
	}
	if out := r.FilterOwned(nil); out != nil {
		t.Errorf("FilterOwned(nil) = %v, want nil", out)
	}
}

// A daemon with no identity of its own cannot know what it owns, so it must
// keep working on everything rather than going silent.
func TestRouterWithoutSelfIDOwnsEverything(t *testing.T) {
	r := routerWith("", []string{"a", "b"}, "a", config.RoutingConfig{
		Orgs: map[string]string{"acme": "b"},
	})
	if !r.Owns("acme/tools") {
		t.Error("Owns() = false without an instance_id; must default to owning everything")
	}
}

// Routing configured but no owner resolvable anywhere: still permissive, so a
// repo never goes unreviewed with nothing to explain why.
func TestRouterOwnsWhenNoFallbackResolvable(t *testing.T) {
	cfg := cfgWith(config.RoleWorker, "self", "", nil, config.RoutingConfig{
		Orgs: map[string]string{"other": "somewhere"},
	})
	r := NewRouter(NewRegistry(cfg), cfg)
	if !r.Enabled() {
		t.Fatal("Enabled() = false; rules are configured")
	}
	if !r.Owns("unmatched/repo") {
		t.Error("Owns() = false with no resolvable owner; must default to true")
	}
}

func TestRouterRoundRobinFairness(t *testing.T) {
	r := routerWith("a", []string{"a", "b", "c"}, "a", config.RoutingConfig{
		RoundRobinPool: []string{"a", "b", "c"},
	})
	counts := map[string]int{}
	const rounds = 30
	for i := 0; i < rounds; i++ {
		counts[r.Next(config.OpReview)]++
	}
	for _, id := range []string{"a", "b", "c"} {
		if counts[id] != rounds/3 {
			t.Errorf("instance %q got %d of %d picks, want an even %d", id, counts[id], rounds, rounds/3)
		}
	}
}

func TestRouterRoundRobinOrderFollowsPool(t *testing.T) {
	// Configured pool order is the rotation order, so an operator can reason
	// about which instance takes the next operation.
	r := routerWith("a", []string{"a", "b", "c"}, "a", config.RoutingConfig{
		RoundRobinPool: []string{"c", "a", "b"},
	})
	want := []string{"c", "a", "b", "c", "a", "b"}
	for i, w := range want {
		if got := r.Next(config.OpReview); got != w {
			t.Errorf("pick %d = %q, want %q", i, got, w)
		}
	}
}

func TestRouterRoundRobinCountersAreIndependentPerOp(t *testing.T) {
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		RoundRobinPool: []string{"a", "b"},
	})
	// Interleaving two ops must not make either of them skew: each keeps its
	// own counter, so both start at the head of the rotation.
	if got := r.Next(config.OpReview); got != "a" {
		t.Errorf("first review = %q, want a", got)
	}
	if got := r.Next(config.OpMerge); got != "a" {
		t.Errorf("first merge = %q, want a (independent counter)", got)
	}
	if got := r.Next(config.OpReview); got != "b" {
		t.Errorf("second review = %q, want b", got)
	}
	if got := r.Next(config.OpMerge); got != "b" {
		t.Errorf("second merge = %q, want b", got)
	}
}

func TestRouterPoolExcludesUnknownAndDisabled(t *testing.T) {
	insts := map[string]config.InstanceConfig{
		"a": {BaseURL: "http://a:7842", Token: "t"},
		"b": {BaseURL: "http://b:7842", Token: "t", Enabled: boolPtr(false)},
	}
	cfg := cfgWith(config.RoleHub, "a", "a", insts, config.RoutingConfig{
		// "ghost" is not registered and "b" is disabled; both must drop out
		// so round robin never targets an instance that cannot serve.
		RoundRobinPool: []string{"a", "b", "ghost", "a"},
	})
	r := NewRouter(NewRegistry(cfg), cfg)
	if got := fmt.Sprint(r.Pool()); got != "[a]" {
		t.Errorf("Pool() = %v, want only [a]", got)
	}
}

func TestRouterPoolDefaultsToEveryUsableInstance(t *testing.T) {
	r := routerWith("a", []string{"b", "a", "c"}, "a", config.RoutingConfig{})
	// Empty configured pool means "everything usable", sorted for determinism.
	if got := fmt.Sprint(r.Pool()); got != "[a b c]" {
		t.Errorf("Pool() = %v, want sorted [a b c]", got)
	}
}

func TestRouterNextAmongRestrictsToHealthy(t *testing.T) {
	r := routerWith("a", []string{"a", "b", "c"}, "a", config.RoutingConfig{})
	for i := 0; i < 6; i++ {
		got := r.NextAmong(config.OpReview, []string{"b", "c"})
		if got != "b" && got != "c" {
			t.Fatalf("NextAmong picked %q, want one of the healthy candidates", got)
		}
	}
	// No overlap between candidates and pool: falling back to the full pool
	// beats dropping the operation entirely.
	if got := r.NextAmong(config.OpReview, []string{"nobody"}); got == "" {
		t.Error("NextAmong with no overlap returned empty; want a full-pool fallback")
	}
}

func TestRouterFallbackWhenDefaultInstanceIsUnusable(t *testing.T) {
	insts := map[string]config.InstanceConfig{
		"a": {BaseURL: "http://a:7842", Token: "t"},
		"z": {BaseURL: "http://z:7842", Token: "t", Enabled: boolPtr(false)},
	}
	// default_instance points at a disabled instance, which would orphan every
	// unrouted repo. The first pool member takes over instead.
	cfg := cfgWith(config.RoleHub, "a", "z", insts, config.RoutingConfig{
		Orgs: map[string]string{"acme": "a"},
	})
	r := NewRouter(NewRegistry(cfg), cfg)
	if got := r.Fallback(); got != "a" {
		t.Errorf("Fallback() = %q, want a", got)
	}
	if !r.Owns("unrouted/repo") {
		t.Error("a should own the unrouted repo once it becomes the fallback")
	}
}

func TestRouterAssignRepo(t *testing.T) {
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Repos: map[string]string{"acme/pinned": "b"},
	})

	// An existing rule is authoritative and must never be reassigned.
	if id, changed := r.AssignRepo("acme/pinned"); id != "b" || changed {
		t.Errorf("AssignRepo(pinned) = (%q, %v), want (b, false)", id, changed)
	}
	// New repos rotate through the pool.
	first, changed := r.AssignRepo("acme/new-one")
	if !changed {
		t.Error("AssignRepo on a new repo should report changed")
	}
	second, _ := r.AssignRepo("acme/new-two")
	if first == second {
		t.Errorf("consecutive assignments both went to %q; want them spread", first)
	}
	if id, changed := r.AssignRepo(""); id != "" || changed {
		t.Errorf("AssignRepo(\"\") = (%q, %v), want (\"\", false)", id, changed)
	}
}

func TestRouterRoundRobinsOpRequiresDispatchMode(t *testing.T) {
	assignment := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Mode:          config.ModeAssignment,
		RoundRobinOps: []string{config.OpReview},
	})
	if assignment.RoundRobinsOp(config.OpReview) {
		t.Error("RoundRobinsOp() = true in assignment mode; per-operation spread is dispatch-only")
	}

	dispatch := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Mode:          config.ModeDispatch,
		RoundRobinOps: []string{config.OpReview},
	})
	if !dispatch.RoundRobinsOp(config.OpReview) {
		t.Error("RoundRobinsOp(review) = false in dispatch mode with review listed")
	}
	if dispatch.RoundRobinsOp(config.OpMerge) {
		t.Error("RoundRobinsOp(merge) = true but merge is not listed")
	}
}

// A reload must not reset the rotation: doing so would send a burst of work
// back to the first instance every time the config changed.
func TestRouterUpdatePreservesRotation(t *testing.T) {
	routing := config.RoutingConfig{RoundRobinPool: []string{"a", "b"}}
	r := routerWith("a", []string{"a", "b"}, "a", routing)
	if got := r.Next(config.OpReview); got != "a" {
		t.Fatalf("first pick = %q, want a", got)
	}

	insts := map[string]config.InstanceConfig{
		"a": {BaseURL: "http://a:7842", Token: "t"},
		"b": {BaseURL: "http://b:7842", Token: "t"},
	}
	cfg := cfgWith(config.RoleHub, "a", "a", insts, routing)
	r.Update(NewRegistry(cfg), cfg)

	if got := r.Next(config.OpReview); got != "b" {
		t.Errorf("pick after Update = %q, want b (rotation must survive a reload)", got)
	}
}

func TestRouterUpdateSwapsRules(t *testing.T) {
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Repos: map[string]string{"acme/x": "a"},
	})
	if !r.Owns("acme/x") {
		t.Fatal("precondition: a should own acme/x")
	}

	insts := map[string]config.InstanceConfig{
		"a": {BaseURL: "http://a:7842", Token: "t"},
		"b": {BaseURL: "http://b:7842", Token: "t"},
	}
	cfg := cfgWith(config.RoleHub, "a", "a", insts, config.RoutingConfig{
		Repos: map[string]string{"acme/x": "b"},
	})
	r.Update(NewRegistry(cfg), cfg)
	if r.Owns("acme/x") {
		t.Error("a still owns acme/x after it was routed to b")
	}
}

// The router is read on every Tier 2 PR and written on every reload, so a data
// race here would be a real production crash under -race.
func TestRouterConcurrentAccess(t *testing.T) {
	insts := map[string]config.InstanceConfig{
		"a": {BaseURL: "http://a:7842", Token: "t"},
		"b": {BaseURL: "http://b:7842", Token: "t"},
	}
	cfg := cfgWith(config.RoleHub, "a", "a", insts, config.RoutingConfig{
		Mode:  config.ModeDispatch,
		Repos: map[string]string{"acme/x": "a"},
	})
	r := NewRouter(NewRegistry(cfg), cfg)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				r.Owns("acme/x")
				r.Next(config.OpReview)
				r.OwnerFor("other/y")
				r.Pool()
				r.RoundRobinsOp(config.OpReview)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Update(NewRegistry(cfg), cfg)
			}
		}()
	}
	wg.Wait()
}

func TestNormalizeOpAndRepoOrg(t *testing.T) {
	for in, want := range map[string]string{
		"":        "default",
		"  ":      "default",
		"Review":  "review",
		" merge ": "merge",
	} {
		if got := normalizeOp(in); got != want {
			t.Errorf("normalizeOp(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"acme/tools": "acme",
		"noslash":    "",
		"":           "",
		"/leading":   "",
	} {
		if got := repoOrg(in); got != want {
			t.Errorf("repoOrg(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRouterRuleForEmptyRepo(t *testing.T) {
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Orgs: map[string]string{"acme": "b"},
	})
	if got := r.RuleFor(""); got != "" {
		t.Errorf("RuleFor(\"\") = %q, want empty", got)
	}
	// An org rule pointing at nothing must not be treated as a match.
	empty := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Orgs:  map[string]string{"acme": ""},
		Repos: map[string]string{"acme/x": ""},
	})
	if got := empty.RuleFor("acme/x"); got != "" {
		t.Errorf("RuleFor with empty targets = %q, want empty", got)
	}
}

func TestRulesSnapshotIsACopy(t *testing.T) {
	r := routerWith("a", []string{"a", "b"}, "a", config.RoutingConfig{
		Orgs:  map[string]string{"acme": "b"},
		Repos: map[string]string{"acme/x": "a"},
	})
	snap := r.RulesSnapshot()
	// Callers render and serialise this while a reload may be swapping the
	// live rules underneath, so mutating the copy must not reach the router.
	snap.Orgs["acme"] = "hijacked"
	snap.Repos["acme/x"] = "hijacked"
	if r.OwnerFor("acme/y") != "b" {
		t.Error("mutating the snapshot changed the live org rule")
	}
	if r.OwnerFor("acme/x") != "a" {
		t.Error("mutating the snapshot changed the live repo rule")
	}

	empty := NewRouter(nil, nil).RulesSnapshot()
	if empty.Orgs != nil || empty.Repos != nil {
		t.Errorf("empty snapshot = %+v, want nil maps", empty)
	}
}
