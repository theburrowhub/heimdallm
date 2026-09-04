package server_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/config"
	"github.com/heimdallm/daemon/internal/instances"
	"github.com/heimdallm/daemon/internal/lan"
	"github.com/heimdallm/daemon/internal/server"
)

// With cluster.discovery off — the default — the route still answers, so the
// GUI can say "discovery is switched off" instead of showing an empty list that
// looks like "nothing is out there".
func TestDiscoveredIsExplicitlyDisabledWhenOff(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/cluster/discovered"},
		{http.MethodPost, "/cluster/discovered/scan"},
	} {
		rec := f.do(t, tc.method, tc.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s = %d, want 200", tc.method, tc.path, rec.Code)
		}
		body := decode(t, rec)
		if body["enabled"] != false {
			t.Errorf("%s %s: enabled = %v, want false", tc.method, tc.path, body["enabled"])
		}
		peers, ok := body["peers"].([]any)
		if !ok {
			t.Fatalf("%s %s: peers = %#v, want an array", tc.method, tc.path, body["peers"])
		}
		if len(peers) != 0 {
			t.Errorf("%s %s: got %d peers, want none", tc.method, tc.path, len(peers))
		}
		if _, present := body["last_scan"]; present {
			t.Errorf("%s %s: last_scan should be omitted before any scan", tc.method, tc.path)
		}
	}
}

// peers must be [] and never null: a client that has to distinguish the two is
// a client that will get it wrong.
func TestDiscoveredPeersIsAlwaysAnArray(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	rec := f.do(t, http.MethodGet, "/cluster/discovered", "")

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decoding %s: %v", rec.Body.String(), err)
	}
	if string(raw["peers"]) != "[]" {
		t.Fatalf("peers = %s, want []", raw["peers"])
	}
}

// fixedBrowser answers with a set of peers, standing in for the network.
type fixedBrowser struct{ peers []lan.Peer }

func (b fixedBrowser) Browse(context.Context, time.Duration) ([]lan.Peer, error) {
	return b.peers, nil
}

// withDiscovery wires a live Discoverer into the hub, backed by a daemon that
// answers /health, so the enabled path is exercised rather than only the
// switched-off one.
func withDiscovery(t *testing.T, f *hubFixture, instanceID string) *instances.Discoverer {
	t.Helper()

	peer := lan.Peer{
		InstanceID: instanceID,
		Hostname:   instanceID + ".local",
		Port:       7842,
		Scheme:     "http",
		Addrs:      []netip.Addr{netip.MustParseAddr("192.0.2.10")},
	}
	remote := newFakeInstance(t, instanceID, nil)
	remoteURL, err := url.Parse(remote.URL)
	if err != nil {
		t.Fatalf("parsing %q: %v", remote.URL, err)
	}

	// Resolve the advertised .local name to the test server.
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, remoteURL.Host)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}

	d := instances.NewDiscoverer(nil, fixedBrowser{peers: []lan.Peer{peer}}, time.Hour,
		func(inst instances.Instance) *instances.Client {
			return instances.NewClient(inst, client)
		})

	deps := &server.ClusterDeps{
		Snapshot:   f.snapshot,
		Store:      f.store,
		Discoverer: d,
	}
	f.srv.SetCluster(deps)
	return d
}

func TestDiscoveredListsWhatTheHubHasSeen(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	d := withDiscovery(t, f, "srv-a")
	d.Scan(context.Background())

	body := decode(t, f.do(t, http.MethodGet, "/cluster/discovered", ""))
	if body["enabled"] != true {
		t.Fatalf("enabled = %v, want true", body["enabled"])
	}
	if body["last_scan"] == nil {
		t.Error("last_scan should be set once a scan has run")
	}

	peers, _ := body["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("got %d peers, want 1: %v", len(peers), body["peers"])
	}
	peer, _ := peers[0].(map[string]any)
	if peer["instance_id"] != "srv-a" {
		t.Errorf("instance_id = %v, want srv-a", peer["instance_id"])
	}
	if peer["status"] != instances.StatusNew {
		t.Errorf("status = %v, want %q", peer["status"], instances.StatusNew)
	}
	// The address a hub would register: the name, not the IP it answered from.
	if peer["base_url"] != "http://srv-a.local:7842" {
		t.Errorf("base_url = %v, want the hostname form", peer["base_url"])
	}
}

// The scan route browses on demand — the refresh button — rather than serving
// whatever the loop last cached.
func TestScanBrowsesOnDemand(t *testing.T) {
	f := newHub(t, nil, config.RoutingConfig{})
	withDiscovery(t, f, "srv-a")

	// Nothing has scanned yet, so the cache is empty.
	before := decode(t, f.do(t, http.MethodGet, "/cluster/discovered", ""))
	if peers, _ := before["peers"].([]any); len(peers) != 0 {
		t.Fatalf("cache started with %d peers, want 0", len(peers))
	}

	after := decode(t, f.do(t, http.MethodPost, "/cluster/discovered/scan", ""))
	peers, _ := after["peers"].([]any)
	if len(peers) != 1 {
		t.Fatalf("scan returned %d peers, want 1: %v", len(peers), after["peers"])
	}
	if after["last_scan"] == nil {
		t.Error("last_scan should be set by a scan")
	}
}
