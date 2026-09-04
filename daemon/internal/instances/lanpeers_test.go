package instances

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/lan"
)

// fakeBrowser answers with a fixed set of peers, so classification and
// verification can be tested without a socket.
type fakeBrowser struct {
	peers []lan.Peer
	err   error
	calls int
}

func (b *fakeBrowser) Browse(context.Context, time.Duration) ([]lan.Peer, error) {
	b.calls++
	return b.peers, b.err
}

// loopbackFactory builds clients that resolve any .local name to loopback on
// the port it was advertised with — which is what an mDNS resolver does on a
// real network, and lets these tests use the hostnames the code now requires
// while still reaching an httptest server.
func loopbackFactory() ClientFactory {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			_, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			var d net.Dialer
			return d.DialContext(ctx, network, net.JoinHostPort("127.0.0.1", port))
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	return func(inst Instance) *Client { return NewClient(inst, client) }
}

// daemonAt stands up something that answers /health like a daemon, and returns
// the peer that advertises it — with a .local hostname, as a real one would.
func daemonAt(t *testing.T, instanceID, name, role string) (*httptest.Server, lan.Peer) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"0.8.17","instance_id":"` +
			instanceID + `","instance_name":"` + name + `","role":"` + role + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, peerFor(t, srv.URL, instanceID)
}

// peerFor builds the advertisement a daemon at url would publish. The hostname
// is derived from the id so it is a legal single-label .local name; the port is
// the test server's, so loopbackFactory reaches it.
func peerFor(t *testing.T, rawURL, instanceID string) lan.Peer {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parsing %q: %v", rawURL, err)
	}
	port := 80
	if p := u.Port(); p != "" {
		parsed, err := strconv.Atoi(p)
		if err != nil {
			t.Fatalf("parsing port %q: %v", p, err)
		}
		port = parsed
	}
	return lan.Peer{
		InstanceID: instanceID,
		Hostname:   strings.ToLower(instanceID) + ".local",
		Port:       port,
		Scheme:     "http",
		Addrs:      []netip.Addr{netip.MustParseAddr("127.0.0.1")},
	}
}

// errBrowseFailed stands in for whatever a real transport failure would be.
var errBrowseFailed = errors.New("browse failed")

func registryWith(t *testing.T, entries map[string]string) *Registry {
	return registryWithSelf(t, entries, "")
}

func registryWithSelf(t *testing.T, entries map[string]string, selfID string) *Registry {
	t.Helper()
	cfg := &config.Config{Cluster: config.ClusterConfig{
		Role:       config.RoleHub,
		InstanceID: selfID,
		Instances:  map[string]config.InstanceConfig{},
	}}
	for id, baseURL := range entries {
		cfg.Cluster.Instances[id] = config.InstanceConfig{
			Name: id, BaseURL: baseURL, Token: "t",
		}
	}
	return NewRegistry(cfg)
}

func TestDiscovererClassifiesAgainstTheRegistry(t *testing.T) {
	srv, peer := daemonAt(t, "srv-a", "Server A", "worker")

	tests := []struct {
		name       string
		registry   map[string]string
		wantStatus string
		wantRegURL string
		wantRegID  string
	}{
		{
			name:       "unknown instance is offered",
			registry:   map[string]string{},
			wantStatus: StatusNew,
		},
		{
			name:       "known at the same address is not actionable",
			registry:   map[string]string{"srv-a": srvLocalURL(srv, "srv-a")},
			wantStatus: StatusRegistered,
			wantRegID:  "srv-a",
			wantRegURL: srvLocalURL(srv, "srv-a"),
		},
		{
			name:       "known at a different address needs repair",
			registry:   map[string]string{"srv-a": "http://10.0.0.11:7842"},
			wantStatus: StatusAddressChanged,
			wantRegID:  "srv-a",
			wantRegURL: "http://10.0.0.11:7842",
		},
		{
			name:       "a different instance at that address is still new",
			registry:   map[string]string{"srv-b": srvLocalURL(srv, "srv-b")},
			wantStatus: StatusNew,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDiscoverer(registryWith(t, tt.registry), &fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())
			got := d.Scan(context.Background())
			if len(got) != 1 {
				t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
			}
			if got[0].Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got[0].Status, tt.wantStatus)
			}
			if got[0].RegisteredID != tt.wantRegID {
				t.Errorf("RegisteredID = %q, want %q", got[0].RegisteredID, tt.wantRegID)
			}
			if got[0].RegisteredBaseURL != tt.wantRegURL {
				t.Errorf("RegisteredBaseURL = %q, want %q", got[0].RegisteredBaseURL, tt.wantRegURL)
			}
		})
	}
}

func TestDiscovererTrustsHealthOverTheAdvertisement(t *testing.T) {
	// mDNS is unauthenticated: anything can claim any id in a TXT record. What
	// ends up in the candidate must be what the daemon says about itself.
	_, peer := daemonAt(t, "the-real-id", "Real Name", "hub")
	peer.InstanceID = "i-am-the-hub"
	peer.InstanceName = "Definitely The Hub"
	peer.Role = "hub"
	peer.Version = "9.9.9"

	d := NewDiscoverer(registryWith(t, nil), &fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())
	got := d.Scan(context.Background())
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if got[0].InstanceID != "the-real-id" {
		t.Errorf("InstanceID = %q; the TXT claim should have been discarded", got[0].InstanceID)
	}
	if got[0].InstanceName != "Real Name" {
		t.Errorf("InstanceName = %q, want the name /health reported", got[0].InstanceName)
	}
	if got[0].Version != "0.8.17" {
		t.Errorf("Version = %q, want the version /health reported", got[0].Version)
	}
}

func TestDiscovererDropsWhatItCannotVerify(t *testing.T) {
	unreachable := lan.Peer{InstanceID: "ghost", Hostname: "127.0.0.1", Port: 1, Scheme: "http"}

	notADaemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>hello</html>`))
	}))
	t.Cleanup(notADaemon.Close)

	anonymous := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// A valid health payload, but it names nobody — nothing to register.
		_, _ = w.Write([]byte(`{"status":"ok","version":"0.8.17"}`))
	}))
	t.Cleanup(anonymous.Close)

	tests := []struct {
		name string
		peer lan.Peer
	}{
		{"nothing listening", unreachable},
		{"not a daemon", peerFor(t, notADaemon.URL, "whatever")},
		{"daemon that names nobody", peerFor(t, anonymous.URL, "whatever")},
		{"no addressable host", lan.Peer{InstanceID: "x", Port: 7842}},
		{"no port", lan.Peer{InstanceID: "x", Hostname: "srv-a.local"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDiscoverer(registryWith(t, nil), &fakeBrowser{peers: []lan.Peer{tt.peer}}, time.Minute, loopbackFactory())
			if got := d.Scan(context.Background()); len(got) != 0 {
				t.Fatalf("got %d candidates, want 0: %+v", len(got), got)
			}
		})
	}
}

func TestDiscovererDeduplicatesByIdentity(t *testing.T) {
	// Two live daemons that both claim to be srv-a — one machine advertising
	// on two interfaces, say. Both must verify, so that dedup is what collapses
	// them rather than one of them simply failing to answer.
	_, first := daemonAt(t, "srv-a", "Server A", "worker")
	_, second := daemonAt(t, "srv-a", "Server A", "worker")
	// One machine advertising on two interfaces: same identity, two names.
	second.Hostname = "srv-a-alt.local"
	if first.BaseURL() == second.BaseURL() {
		t.Fatal("the two fake daemons need different addresses to be a real test")
	}

	d := NewDiscoverer(registryWith(t, nil),
		&fakeBrowser{peers: []lan.Peer{first, second}}, time.Minute, loopbackFactory())

	got := d.Scan(context.Background())
	if len(got) != 1 {
		t.Fatalf("got %d candidates for one instance, want 1: %+v", len(got), got)
	}
}

// A scan must stay bounded no matter what is on the network: an advertised
// address that accepts a connection and then never replies is trivial for
// anyone on the LAN to publish.
func TestDiscovererScanIsBoundedByASilentPeer(t *testing.T) {
	// The handler must release when the client gives up: httptest's Close
	// waits for in-flight handlers, so one that blocks unconditionally would
	// hang the test rather than exercise the timeout.
	silent := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(silent.Close)

	_, good := daemonAt(t, "srv-a", "Server A", "worker")
	peers := []lan.Peer{peerFor(t, silent.URL, "black-hole"), good}

	d := NewDiscoverer(registryWith(t, nil), &fakeBrowser{peers: peers}, time.Minute, loopbackFactory())

	start := time.Now()
	got := d.Scan(context.Background())
	elapsed := time.Since(start)

	// One verify timeout, not one per peer and not the client default.
	if elapsed > 2*discoveryVerifyTimeout {
		t.Errorf("scan took %s; a silent peer should not stall it", elapsed)
	}
	if len(got) != 1 || got[0].InstanceID != "srv-a" {
		t.Fatalf("got %+v, want only the daemon that answered", got)
	}
}

func TestDiscovererIgnoresATrailingSlashAndCase(t *testing.T) {
	// An operator who typed the address with a trailing slash or in mixed case
	// must not be told the address changed.
	srv, peer := daemonAt(t, "srv-a", "Server A", "worker")

	d := NewDiscoverer(registryWith(t, map[string]string{"srv-a": srvLocalURL(srv, "srv-a") + "/"}),
		&fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())
	got := d.Scan(context.Background())
	if len(got) != 1 || got[0].Status != StatusRegistered {
		t.Fatalf("status = %+v, want registered", got)
	}
}

func TestDiscovererBaseURLUsesTheHostname(t *testing.T) {
	// The whole point: address the peer by a name that re-resolves, not by the
	// IP it happened to answer from.
	srv, peer := daemonAt(t, "srv-a", "Server A", "worker")
	d := NewDiscoverer(registryWith(t, nil), &fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())

	got := d.Scan(context.Background())
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1", len(got))
	}
	if want := srvLocalURL(srv, "srv-a"); got[0].BaseURL != want {
		t.Errorf("BaseURL = %q, want %q", got[0].BaseURL, want)
	}
	if got[0].Hostname != "srv-a.local" {
		t.Errorf("Hostname = %q, want the SRV target", got[0].Hostname)
	}
}

// Registering an instance should stop it being offered immediately, not a
// browse interval later.
func TestDiscovererUpdateReclassifiesTheCache(t *testing.T) {
	srv, peer := daemonAt(t, "srv-a", "Server A", "worker")

	d := NewDiscoverer(registryWith(t, nil), &fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())
	if got := d.Scan(context.Background()); got[0].Status != StatusNew {
		t.Fatalf("status = %q, want new", got[0].Status)
	}

	d.Update(registryWith(t, map[string]string{"srv-a": srvLocalURL(srv, "srv-a")}), 0)

	got := d.Candidates()
	if len(got) != 1 || got[0].Status != StatusRegistered {
		t.Fatalf("after registering, status = %+v, want registered", got)
	}
}

func TestDiscovererIsInertWithoutABrowser(t *testing.T) {
	d := NewDiscoverer(registryWith(t, nil), nil, time.Minute, loopbackFactory())
	// Empty, not nil: a caller that has to distinguish the two is a caller
	// that will eventually get it wrong.
	if got := d.Scan(context.Background()); len(got) != 0 {
		t.Fatalf("Scan returned %+v with no browser, want empty", got)
	}
	if got := d.Candidates(); len(got) != 0 {
		t.Fatalf("Candidates returned %+v, want empty", got)
	}
	if !d.LastScan().IsZero() {
		t.Fatal("LastScan should stay zero when discovery is off")
	}

	// Run must return rather than spin on a ticker forever.
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(context.Background()) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return with discovery off")
	}
}

func TestDiscovererKeepsTheCacheWhenABrowseFails(t *testing.T) {
	_, peer := daemonAt(t, "srv-a", "Server A", "worker")
	browser := &fakeBrowser{peers: []lan.Peer{peer}}
	d := NewDiscoverer(registryWith(t, nil), browser, time.Minute, loopbackFactory())

	if got := d.Scan(context.Background()); len(got) != 1 {
		t.Fatalf("first scan found %d, want 1", len(got))
	}

	// A transient failure should not blank the list the operator is looking at.
	browser.err = errBrowseFailed
	if got := d.Scan(context.Background()); len(got) != 1 {
		t.Fatalf("after a failed browse got %d candidates, want the cached 1", len(got))
	}
}

func TestDiscovererRecordsWhenItLastLooked(t *testing.T) {
	_, peer := daemonAt(t, "srv-a", "Server A", "worker")
	d := NewDiscoverer(registryWith(t, nil), &fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())

	if !d.LastScan().IsZero() {
		t.Fatal("LastScan should be zero before the first scan")
	}
	d.Scan(context.Background())
	if d.LastScan().IsZero() {
		t.Fatal("LastScan should be set after a scan")
	}
}

func TestDiscovererRunScansImmediately(t *testing.T) {
	// The Instances tab should not show an empty section for a full interval
	// after the daemon starts.
	_, peer := daemonAt(t, "srv-a", "Server A", "worker")
	browser := &fakeBrowser{peers: []lan.Peer{peer}}
	d := NewDiscoverer(registryWith(t, nil), browser, time.Hour, loopbackFactory())

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(ctx) }()

	deadline := time.After(3 * time.Second)
	for len(d.Candidates()) == 0 {
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("Run did not scan before the first tick")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// cluster.discovery describes one machine, so it must never be pushed to the
// others. The blanket "cluster" entry covers it, but the guarantee is worth
// pinning rather than assuming.
func TestDiscoveryKeyIsNeverPropagated(t *testing.T) {
	for _, key := range []string{"cluster.discovery", "cluster"} {
		if !IsLocalOnly(key) {
			t.Errorf("IsLocalOnly(%q) = false, want true", key)
		}
	}
}

// The advertiser and the browser share the multicast group, so a hub hears its
// own advertisement. It must not appear in its own listing — and in particular
// must not be reported as having moved: the hub's registry entry for itself is
// deliberately loopback, so "correcting" it to the hostname the network
// reported would replace a working entry with one only the hub can resolve.
func TestDiscovererNeverOffersTheHubItself(t *testing.T) {
	srv, peer := daemonAt(t, "hub-1", "Local hub", "hub")

	d := NewDiscoverer(
		registryWithSelf(t, map[string]string{"hub-1": "http://127.0.0.1:7842"}, "hub-1"),
		&fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())

	if got := d.Scan(context.Background()); len(got) != 0 {
		t.Fatalf("the hub offered itself: %+v", got)
	}
	_ = srv
}

// Skipping self must not become "skip anything that looks like the hub": a
// genuine second daemon still has to show up.
func TestDiscovererStillOffersOtherDaemonsToAHub(t *testing.T) {
	_, peer := daemonAt(t, "srv-a", "Server A", "worker")

	d := NewDiscoverer(
		registryWithSelf(t, map[string]string{"hub-1": "http://127.0.0.1:7842"}, "hub-1"),
		&fakeBrowser{peers: []lan.Peer{peer}}, time.Minute, loopbackFactory())

	got := d.Scan(context.Background())
	if len(got) != 1 || got[0].InstanceID != "srv-a" {
		t.Fatalf("got %+v, want the one peer that is not the hub", got)
	}
	if got[0].Status != StatusNew {
		t.Fatalf("status = %q, want new", got[0].Status)
	}
}

// srvLocalURL is the base URL a peer advertising id would produce for srv.
func srvLocalURL(srv *httptest.Server, id string) string {
	u, err := url.Parse(srv.URL)
	if err != nil {
		panic(err)
	}
	return "http://" + strings.ToLower(id) + ".local:" + u.Port()
}
