package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/lan"
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
func TestApplyDiscoveryRecordsTheAdvertisedIdentity(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleWorker, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)

	sig := cs.discoverySignatureNow()
	if !sig.enabled || sig.hub {
		t.Fatalf("signature = %+v, want enabled and not a hub", sig)
	}
	if sig.id != "hub-1" || sig.name != "Local hub" {
		t.Fatalf("signature = %+v, want the daemon's identity", sig)
	}
	if sig.role != config.RoleWorker {
		t.Fatalf("role = %q, want %q", sig.role, config.RoleWorker)
	}
}

// The advertised port comes from the bound listener, never from config.toml.
// The listener is bound once at startup and no reload rebinds it, so a live
// edit of server.port would otherwise have the daemon advertising a port
// nothing is listening on.
func TestAdvertisedPortComesFromTheListenerNotTheConfig(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleWorker, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)
	cs.SetServedAddr(&net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 7842})

	// The operator edits the port; the listener does not move.
	cfg.Server.Port = 9000
	cs.Update(cfg)

	if got := cs.servedAddr(); got == nil || got.Port != 7842 {
		t.Fatalf("served addr = %v, want the bound port 7842", got)
	}
}

// A daemon that only listens on loopback cannot be reached by any peer, and
// bind_addr defaults to 127.0.0.1. Advertising anyway is worse than staying
// quiet: the hub only offers peers it has already reached over HTTP, so the
// machine would simply never appear, with nothing to explain why.
func TestServedReachableFromLAN(t *testing.T) {
	tests := []struct {
		name string
		addr *net.TCPAddr
		want bool
	}{
		{"the default bind", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7842}, false},
		{"ipv6 loopback", &net.TCPAddr{IP: net.ParseIP("::1"), Port: 7842}, false},
		{"a LAN address", &net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 7842}, true},
		{"all interfaces", &net.TCPAddr{IP: net.ParseIP("0.0.0.0"), Port: 7842}, true},
		{"all interfaces v6", &net.TCPAddr{IP: net.ParseIP("::"), Port: 7842}, true},
		{"no address at all", &net.TCPAddr{Port: 7842}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := servedReachableFromLAN(tt.addr); got != tt.want {
				t.Fatalf("servedReachableFromLAN(%v) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

// Same rule, end to end: the advertiser declines rather than publishing an
// address the server will refuse.
func TestRunAdvertiserDeclinesALoopbackOnlyDaemon(t *testing.T) {
	policy, calls := testPolicy(func() (lan.PacketConn, error) {
		t.Error("dialled the multicast group despite a loopback-only bind")
		return nil, errors.New("should not be reached")
	})

	cfg := discoveryCfg(t, config.RoleWorker, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)
	cs.SetServedAddr(&net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 7842})

	done := make(chan struct{})
	go func() { defer close(done); cs.runAdvertiser(context.Background(), policy) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunAdvertiser did not return on a loopback-only bind")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("dialled %d times, want 0", got)
	}
}

// And it declines before the listener's address is known, rather than guessing.
func TestRunAdvertiserWaitsForTheServedAddress(t *testing.T) {
	policy, calls := testPolicy(func() (lan.PacketConn, error) {
		t.Error("dialled the multicast group with no served address")
		return nil, errors.New("should not be reached")
	})

	cfg := discoveryCfg(t, config.RoleWorker, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)

	done := make(chan struct{})
	go func() { defer close(done); cs.runAdvertiser(context.Background(), policy) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunAdvertiser did not return without a served address")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Fatalf("dialled %d times, want 0", got)
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
	cs.SetServedAddr(&net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 7842})

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

// testPolicy builds a retry policy with a stub dialler and short bounds.
// Nothing package-level is mutated, so these tests are safe to run in parallel
// and a future t.Parallel() cannot turn into a data race on shared backoff
// variables.
func testPolicy(dial func() (lan.PacketConn, error)) (retryPolicy, *int32) {
	var calls int32
	return retryPolicy{
		dial: func() (lan.PacketConn, error) {
			atomic.AddInt32(&calls, 1)
			return dial()
		},
		min:         time.Millisecond,
		max:         200 * time.Millisecond,
		established: time.Hour, // no connection counts as lasting unless asked
	}, &calls
}

// deadConn returns a connection that is closed the moment it is handed over,
// standing in for a socket that joined the group but cannot be used.
func deadConn() (lan.PacketConn, error) {
	conn, other := lan.NewMemConn()
	_ = other.Close()
	return conn, nil
}

// A network that is not up yet is the commonest failure, and it fixes itself
// seconds later — so a single attempt would leave discovery dead for the life
// of the process with nothing in the config to prompt another try.
func TestRunWithMulticastRetriesAFailingDial(t *testing.T) {
	policy, calls := testPolicy(func() (lan.PacketConn, error) {
		return nil, errors.New("network is down")
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWithMulticast(ctx, policy, "browse", func(lan.PacketConn) error { return nil })
	}()

	waitForCalls(t, calls, 3, cancel, done, "a failing dial should be retried")
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("cancelling during the backoff wait did not stop the loop")
	}
}

// The mirror image of a failed join, and at least as common: the socket dies
// after it was obtained. Suspend and resume drops group membership with the
// interface, and a laptop is exactly the machine this feature exists for.
func TestRunWithMulticastRedialsAfterTheSocketDies(t *testing.T) {
	policy, calls := testPolicy(deadConn)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWithMulticast(ctx, policy, "advertise on", func(conn lan.PacketConn) error {
			return conn.Close()
		})
	}()

	waitForCalls(t, calls, 3, cancel, done, "the loop should redial when the socket dies")
	cancel()
	<-done
}

// A malformed advertisement is not a network problem. Retrying it would log the
// same complaint every few seconds forever.
func TestRunWithMulticastStopsOnANonRetryableFailure(t *testing.T) {
	policy, calls := testPolicy(deadConn)

	done := make(chan struct{})
	go func() {
		defer close(done)
		runWithMulticast(context.Background(), policy, "advertise on",
			func(conn lan.PacketConn) error {
				_ = conn.Close()
				return errNotRetryable
			})
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("a non-retryable failure did not stop the loop")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("dialled %d times, want exactly 1", got)
	}
}

// waitForCalls blocks until the dialler has been called n times.
func waitForCalls(t *testing.T, calls *int32, n int32, cancel context.CancelFunc,
	done chan struct{}, what string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for atomic.LoadInt32(calls) < n {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatalf("dialled %d times, wanted %d: %s", atomic.LoadInt32(calls), n, what)
		case <-time.After(time.Millisecond):
		}
	}
}

// waitRecorder times the gap between one use() returning and the next one
// starting — which is the backoff and nothing else. Timing across use() itself
// would fold in whatever the body does and drown the signal; that is what made
// the first version of this test unable to fail.
type waitRecorder struct {
	mu       sync.Mutex
	lastExit time.Time
	waits    []time.Duration
}

func (w *waitRecorder) enter() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.lastExit.IsZero() {
		w.waits = append(w.waits, time.Since(w.lastExit))
	}
}

func (w *waitRecorder) exit() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.lastExit = time.Now()
}

func (w *waitRecorder) observed() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]time.Duration(nil), w.waits...)
}

// runUntilReconnects drives runWithMulticast until it has redialled n times.
func runUntilReconnects(t *testing.T, policy retryPolicy, calls *int32, n int32,
	hold time.Duration) []time.Duration {
	t.Helper()
	rec := &waitRecorder{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWithMulticast(ctx, policy, "browse", func(lan.PacketConn) error {
			rec.enter()
			if hold > 0 {
				time.Sleep(hold)
			}
			rec.exit()
			return nil
		})
	}()

	waitForCalls(t, calls, n, cancel, done, "not enough reconnects observed")
	cancel()
	<-done
	return rec.observed()
}

// A socket that dies the instant it is created — an interface with no
// multicast route, a bridged container, a firewalled group — must not retry at
// the floor forever. The ceiling exists for exactly that case.
func TestRunWithMulticastBacksOffWhenTheSocketNeverWorks(t *testing.T) {
	policy, calls := testPolicy(deadConn)
	waits := runUntilReconnects(t, policy, calls, 6, 0)

	if len(waits) < 4 {
		t.Fatalf("only %d waits observed", len(waits))
	}
	if last := waits[len(waits)-1]; last < 8*policy.min {
		t.Fatalf("waits stayed at the floor (%v); a socket that never works "+
			"is retrying forever without backing off", waits)
	}
}

// The other half of the same rule: a connection that genuinely worked clears
// the previous failure, so a laptop resuming after hours asleep reconnects at
// the floor rather than at the ceiling.
func TestRunWithMulticastResetsBackoffAfterAConnectionThatLasted(t *testing.T) {
	policy, calls := testPolicy(deadConn)
	policy.established = 5 * time.Millisecond

	// Each connection outlasts the threshold, so every reconnect counts as
	// "it worked" and the wait must go back to the floor every time.
	waits := runUntilReconnects(t, policy, calls, 6, 10*time.Millisecond)

	if len(waits) < 4 {
		t.Fatalf("only %d waits observed", len(waits))
	}
	for i, w := range waits {
		if w > 8*policy.min {
			t.Fatalf("wait %d was %v, far above the floor %v: the backoff was "+
				"not reset after a connection that lasted (all: %v)",
				i, w, policy.min, waits)
		}
	}
}

// The container notice explains a silent no-op, so the test asserts the line
// was actually emitted — not merely that the once-block ran, which a mutation
// deleting the log call would still satisfy.
func TestContainerWarningIsEmittedOnceInAContainer(t *testing.T) {
	var buf bytes.Buffer
	realLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(realLogger) })

	realDetect, realOnce := inContainer, containerWarnOnce
	inContainer = func() bool { return true }
	containerWarnOnce = new(sync.Once)
	t.Cleanup(func() { inContainer, containerWarnOnce = realDetect, realOnce })

	warnIfDiscoveryIsContainerised()
	warnIfDiscoveryIsContainerised()
	warnIfDiscoveryIsContainerised()

	// The message names the escape hatch, which is the whole reason it exists.
	if !strings.Contains(buf.String(), "HEIMDALLM_CLUSTER_DISCOVERY=off") {
		t.Fatalf("the warning was not emitted, or lost its advice: %q", buf.String())
	}
	if got := strings.Count(buf.String(), "does not cross Docker"); got != 1 {
		t.Fatalf("the warning was emitted %d times, want exactly 1", got)
	}
}

// And it stays quiet on a machine that is not a container, so a desktop
// install is not told about a limitation it does not have.
func TestContainerWarningIsSilentOutsideAContainer(t *testing.T) {
	var buf bytes.Buffer
	realLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(realLogger) })

	realDetect, realOnce := inContainer, containerWarnOnce
	inContainer = func() bool { return false }
	containerWarnOnce = new(sync.Once)
	t.Cleanup(func() { inContainer, containerWarnOnce = realDetect, realOnce })

	warnIfDiscoveryIsContainerised()

	if strings.Contains(buf.String(), "does not cross Docker") {
		t.Fatalf("warned outside a container: %q", buf.String())
	}
}

// The container notice explains a silent no-op, and browsing is just as broken
// on a bridged container as advertising — so a hub that only browses has to see
// it too.
func TestContainerWarningIsReachableFromBothLoops(t *testing.T) {
	body, err := os.ReadFile("discovery_lan.go")
	if err != nil {
		t.Fatalf("reading discovery_lan.go: %v", err)
	}
	src := string(body)

	for _, fn := range []string{"runAdvertiser", "runDiscoverer"} {
		start := strings.Index(src, "func (cs *clusterState) "+fn+"(")
		if start < 0 {
			t.Fatalf("%s not found", fn)
		}
		end := strings.Index(src[start:], "\n}\n")
		if end < 0 {
			end = len(src) - start
		}
		if !strings.Contains(src[start:start+end], "warnIfDiscoveryIsContainerised()") {
			t.Errorf("%s does not call warnIfDiscoveryIsContainerised; a container "+
				"running only that loop gets no explanation for the silence", fn)
		}
	}
}

// A specific bind must advertise only that address. Publishing the whole
// machine's enumeration repeats the bug that made the port issue serious: with
// bind_addr = 192.168.1.20 on a host that also has a VPN address, a hub
// resolving the name could pick the VPN one, be refused, and the peer would
// silently never appear.
func TestAdvertisableAddrsFollowsTheBind(t *testing.T) {
	specific := advertisableAddrs(&net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 7842})()
	if len(specific) != 1 || specific[0].String() != "192.168.1.20" {
		t.Fatalf("a specific bind advertised %v, want just [192.168.1.20]", specific)
	}

	// A wildcard bind really does answer on every interface, so the full
	// enumeration is correct there.
	for _, wildcard := range []net.IP{net.ParseIP("0.0.0.0"), net.ParseIP("::"), nil} {
		got := advertisableAddrs(&net.TCPAddr{IP: wildcard, Port: 7842})
		if reflect.ValueOf(got).Pointer() != reflect.ValueOf(localAddresses).Pointer() {
			t.Errorf("wildcard bind %v did not fall back to the full enumeration", wildcard)
		}
	}
}

// The advertiser gives up permanently when the served address is unknown, which
// is safe only because SetServedAddr runs first. That ordering is invisible in
// the code, so it gets an assertion rather than a comment: a future reordering
// should fail here, not silently stop advertising in production.
func TestServedAddrIsSetBeforeThePollersStart(t *testing.T) {
	body, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("reading main.go: %v", err)
	}
	src := string(body)

	setServed := strings.Index(src, "clusterSt.SetServedAddr(")
	if setServed < 0 {
		t.Fatal("main.go no longer calls SetServedAddr; RunAdvertiser will never " +
			"know where the server listens and will refuse to advertise")
	}
	startPollers := strings.Index(src, "startPollers(runtimeCtx,")
	if startPollers < 0 {
		t.Skip("startPollers call site moved; update this guard")
	}
	if setServed > startPollers {
		t.Fatalf("SetServedAddr (offset %d) now runs after startPollers (offset %d): "+
			"the advertiser will start before it knows the served address and "+
			"will refuse to advertise for the life of the process",
			setServed, startPollers)
	}
}

// runningInContainer is a heuristic, so what matters is that it answers rather
// than what it answers here — the daemon's own tests run inside a container, so
// this asserts the shape rather than a value that depends on the host.
func TestRunningInContainerAnswers(t *testing.T) {
	_ = runningInContainer()
}

// The enumeration must never offer a peer an address it cannot use.
func TestLocalAddressesSkipsWhatAPeerCannotReach(t *testing.T) {
	for _, addr := range localAddresses() {
		if addr.IsLoopback() {
			t.Errorf("advertised loopback %s: no peer can reach us there", addr)
		}
		if addr.IsLinkLocalUnicast() {
			t.Errorf("advertised link-local %s: not dialable from a record", addr)
		}
		if addr.IsUnspecified() {
			t.Errorf("advertised the unspecified address %s", addr)
		}
	}
}

// The advertiser refuses an advertisement it cannot build and does not retry:
// waiting will not make an invalid instance id valid.
func TestRunAdvertiserStopsOnAnUnbuildableAdvertisement(t *testing.T) {
	policy, calls := testPolicy(deadConn)

	cfg := discoveryCfg(t, config.RoleWorker, config.DiscoveryMDNS)
	cfg.Cluster.InstanceID = "" // NewAdvertiser refuses this
	cs := newClusterState(cfg, nil, nil)
	cs.discoverySig.enabled = true
	cs.SetServedAddr(&net.TCPAddr{IP: net.ParseIP("192.168.1.20"), Port: 7842})

	done := make(chan struct{})
	go func() { defer close(done); cs.runAdvertiser(context.Background(), policy) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop retried an advertisement that can never be valid")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Fatalf("dialled %d times, want exactly 1", got)
	}
}

// A dialler that reports neither a connection nor a failure must be treated as
// a failure, not dereferenced. Nothing in production does it; a panic in a
// background loop is too expensive to leave to trust.
func TestRunWithMulticastSurvivesADiallerReturningNothing(t *testing.T) {
	policy, calls := testPolicy(func() (lan.PacketConn, error) { return nil, nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runWithMulticast(ctx, policy, "browse", func(lan.PacketConn) error {
			t.Error("use was called with no connection")
			return nil
		})
	}()

	waitForCalls(t, calls, 2, cancel, done, "a nil connection should be retried, not dereferenced")
	cancel()
	<-done
}

// A hub with discovery on lends its Discoverer a browser for the life of the
// loop and takes it back, so the cached view survives a poller restart while
// the socket does not.
func TestRunDiscovererLendsAndReclaimsTheBrowser(t *testing.T) {
	cfg := discoveryCfg(t, config.RoleHub, config.DiscoveryMDNS)
	cs := newClusterState(cfg, nil, nil)
	d := cs.Discoverer()
	if d == nil {
		t.Fatal("no discoverer on a hub with discovery on")
	}

	policy, _ := testPolicy(func() (lan.PacketConn, error) {
		conn, other := lan.NewMemConn()
		_ = other.Close()
		return conn, nil
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); cs.runDiscoverer(ctx, policy) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	// Reclaimed: a scan after the loop exits must not reach a closed socket.
	if got := d.Scan(context.Background()); len(got) != 0 {
		t.Fatalf("scanned through a reclaimed browser: %+v", got)
	}
}
