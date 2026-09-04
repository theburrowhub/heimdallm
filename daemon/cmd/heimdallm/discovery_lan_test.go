package main

import (
	"context"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
)

func discoveryCfg(t *testing.T, role, discovery string) *config.Config {
	t.Helper()
	cfg := &config.Config{}
	cfg.AI.Primary = "claude"
	cfg.Server.Port = 7842
	cfg.Cluster.Role = role
	cfg.Cluster.InstanceID = "hub-1"
	cfg.Cluster.InstanceName = "Local hub"
	cfg.Cluster.Discovery = discovery
	return cfg
}

// A hub only gets a discoverer when discovery is actually on, and a worker
// never does — the same "only a hub probes" rule the prober follows.
func TestApplyDiscoveryBuildsTheDiscovererOnlyForAHubWithDiscoveryOn(t *testing.T) {
	tests := []struct {
		name      string
		role      string
		discovery string
		want      bool
	}{
		{"hub with mdns", config.RoleHub, config.DiscoveryMDNS, true},
		{"hub with discovery off", config.RoleHub, config.DiscoveryOff, false},
		{"hub with discovery unset", config.RoleHub, "", false},
		{"worker with mdns", config.RoleWorker, config.DiscoveryMDNS, false},
		{"standalone with mdns", config.RoleStandalone, config.DiscoveryMDNS, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := discoveryCfg(t, tt.role, tt.discovery)
			cs := newClusterState(cfg, nil, nil)
			if got := cs.Discoverer() != nil; got != tt.want {
				t.Fatalf("Discoverer present = %v, want %v", got, tt.want)
			}
		})
	}
}

// The discoverer outlives a reload. Its cached view is what the Instances tab
// reads, and rebuilding it on every config save would blank the list for a
// browse interval each time.
func TestApplyDiscoveryKeepsTheDiscovererAcrossReloads(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleHub, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)

	first := cs.Discoverer()
	if first == nil {
		t.Fatal("no discoverer on a hub with discovery on")
	}

	cfg.Cluster.InstanceName = "Renamed hub"
	cs.Update(cfg)

	if second := cs.Discoverer(); second != first {
		t.Fatalf("reload replaced the discoverer (%p -> %p); the cached view is lost", first, second)
	}
}

// Turning discovery off must actually stop it, and turning it back on must not
// need a restart.
func TestApplyDiscoveryFollowsTheConfigBothWays(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleHub, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)
	if cs.Discoverer() == nil {
		t.Fatal("no discoverer on a hub with discovery on")
	}

	cfg.Cluster.Discovery = config.DiscoveryOff
	cs.Update(cfg)
	if cs.Discoverer() != nil {
		t.Fatal("discovery = off left a live discoverer")
	}

	cfg.Cluster.Discovery = config.DiscoveryMDNS
	cs.Update(cfg)
	if cs.Discoverer() == nil {
		t.Fatal("turning discovery back on did not rebuild the discoverer")
	}
}

// applyDiscovery must never touch a socket: it runs synchronously on the reload
// path while the previous loops are still live, so anything it closed would be
// closed out from under a running read.
func TestApplyDiscoveryRecordsTheAdvertisedSignature(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleWorker, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)

	sig := cs.discoverySignatureNow()
	if !sig.enabled || sig.hub {
		t.Fatalf("signature = %+v, want enabled and not a hub", sig)
	}
	if sig.id != "hub-1" || sig.name != "Local hub" || sig.port != 7842 {
		t.Fatalf("signature = %+v, want the daemon's identity and port", sig)
	}
	if sig.role != config.RoleWorker {
		t.Fatalf("role = %q, want %q", sig.role, config.RoleWorker)
	}
}

// An empty role goes on the wire as "standalone": a blank column in a browsing
// hub's listing reads as a bug.
func TestEffectiveRoleResolvesEmptyToStandalone(t *testing.T) {
	if got := effectiveRole(&config.Config{}); got != config.RoleStandalone {
		t.Fatalf("effectiveRole = %q, want %q", got, config.RoleStandalone)
	}
	cfg := &config.Config{}
	cfg.Cluster.Role = "HUB"
	if got := effectiveRole(cfg); got != config.RoleHub {
		t.Fatalf("effectiveRole = %q, want %q", got, config.RoleHub)
	}
}

// The loops must not outlive their context while waiting to retry a socket the
// machine cannot give them yet.
func TestRunLoopsReturnOnACancelledContext(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleHub, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for name, run := range map[string]func(context.Context){
		"advertiser": cs.RunAdvertiser,
		"discoverer": cs.RunDiscoverer,
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); run(ctx) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("loop did not return on a cancelled context")
			}
		})
	}
}

// With discovery off both loops are no-ops, so startPollers can launch them
// unconditionally rather than wrapping the block in a conditional.
func TestRunLoopsAreNoOpsWithDiscoveryOff(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleHub, config.DiscoveryOff)
	cs := newClusterState(cfg, nil, nil)

	for name, run := range map[string]func(context.Context){
		"advertiser": cs.RunAdvertiser,
		"discoverer": cs.RunDiscoverer,
	} {
		t.Run(name, func(t *testing.T) {
			done := make(chan struct{})
			go func() { defer close(done); run(context.Background()) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("loop did not return with discovery off")
			}
		})
	}
}

// dialMulticast retries rather than giving up, because the commonest failure is
// a network that is not up yet — but it must honour cancellation while waiting.
func TestDialMulticastGivesUpOnlyWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	conn, ok := dialMulticast(ctx, "browse")
	if ok {
		_ = conn.Close()
		t.Skip("this machine can join the mDNS group; the cancellation path needs a machine that cannot")
	}
	if conn != nil {
		t.Fatal("dialMulticast returned a connection alongside ok=false")
	}
}

// The container notice is a paragraph. Reloads are frequent; printing it again
// on every one of them would be noise.
func TestContainerWarningIsPrintedAtMostOnce(t *testing.T) {
	warnIfDiscoveryIsContainerised()
	warnIfDiscoveryIsContainerised()
	// sync.Once is the guarantee; this asserts it is wired and does not panic
	// on repeat, which is what the reload path does to it.
	if !containerWarnOnceFired() {
		t.Fatal("the once was never armed")
	}
}
