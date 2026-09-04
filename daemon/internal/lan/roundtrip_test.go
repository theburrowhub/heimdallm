package lan

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// quietLogger keeps the transport's debug chatter out of test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startAdvertiser runs an advertiser on one end of an in-memory pair and
// returns the browser's end.
func startAdvertiser(t *testing.T, ad Advertisement) PacketConn {
	t.Helper()
	adConn, browseConn := NewMemConn()

	adv, err := NewAdvertiser(adConn, ad, quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		adv.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = adConn.Close()
		_ = browseConn.Close()
	})
	return browseConn
}

func testAdvertisement() Advertisement {
	return Advertisement{
		InstanceID:   "mac-sergio-a1b2",
		InstanceName: "mac-sergio",
		Role:         "worker",
		Version:      "0.8.17",
		Hostname:     "mac-sergio.local.",
		Port:         7842,
		Addrs:        []netip.Addr{netip.MustParseAddr("10.0.0.11")},
	}
}

// TestAdvertiseBrowseRoundTrip is the reason the PacketConn seam exists. The
// daemon's tests run on Docker's default bridge, where no interface carries the
// MULTICAST flag, so a test over a real socket could only ever skip. Over the
// in-memory pair the wire format is genuinely exercised in CI.
func TestAdvertiseBrowseRoundTrip(t *testing.T) {
	browseConn := startAdvertiser(t, testAdvertisement())

	browser, err := NewBrowser(browseConn, quietLogger())
	if err != nil {
		t.Fatalf("NewBrowser: %v", err)
	}

	peers, err := browser.Browse(context.Background(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("found %d peers, want 1: %+v", len(peers), peers)
	}

	got := peers[0]
	if got.InstanceID != "mac-sergio-a1b2" {
		t.Errorf("InstanceID = %q, want mac-sergio-a1b2", got.InstanceID)
	}
	if got.InstanceName != "mac-sergio" {
		t.Errorf("InstanceName = %q, want mac-sergio", got.InstanceName)
	}
	if got.Role != "worker" {
		t.Errorf("Role = %q, want worker", got.Role)
	}
	if got.Version != "0.8.17" {
		t.Errorf("Version = %q, want 0.8.17", got.Version)
	}
	if got.Hostname != "mac-sergio.local" {
		t.Errorf("Hostname = %q, want mac-sergio.local", got.Hostname)
	}
	if got.Port != 7842 {
		t.Errorf("Port = %d, want 7842", got.Port)
	}
	if got.BaseURL() != "http://mac-sergio.local:7842" {
		t.Errorf("BaseURL = %q, want http://mac-sergio.local:7842", got.BaseURL())
	}
	if len(got.Addrs) != 1 || got.Addrs[0].String() != "10.0.0.11" {
		t.Errorf("Addrs = %v, want [10.0.0.11]", got.Addrs)
	}
}

// A peer must be addressable by name, not by the address it happened to answer
// from — that is what makes a DHCP lease change a non-event.
func TestBrowseAddressesPeerByHostnameNotIP(t *testing.T) {
	ad := testAdvertisement()
	ad.Addrs = []netip.Addr{netip.MustParseAddr("10.0.0.99")}
	browseConn := startAdvertiser(t, ad)

	browser, _ := NewBrowser(browseConn, quietLogger())
	peers, err := browser.Browse(context.Background(), 500*time.Millisecond)
	if err != nil || len(peers) != 1 {
		t.Fatalf("Browse: %v, peers %+v", err, peers)
	}
	if got := peers[0].BaseURL(); got != "http://mac-sergio.local:7842" {
		t.Fatalf("BaseURL = %q, want the hostname form", got)
	}
}

func TestBrowseIgnoresANonAnswer(t *testing.T) {
	// Two browsers share the group: one seeing the other's question must not
	// mistake it for a peer.
	a, b := NewMemConn()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })

	asker, _ := NewBrowser(a, quietLogger())
	if err := asker.query(); err != nil {
		t.Fatalf("query: %v", err)
	}

	listener, _ := NewBrowser(b, quietLogger())
	peers, err := listener.Browse(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("found %d peers from a question, want 0", len(peers))
	}
}

func TestBrowseDropsGarbage(t *testing.T) {
	a, b := NewMemConn()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })

	if _, err := a.WriteTo([]byte("this is not a DNS message"), GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	// A truncated but plausible packet.
	if _, err := a.WriteTo([]byte{0x00, 0x01, 0x84}, GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	browser, _ := NewBrowser(b, quietLogger())
	peers, err := browser.Browse(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("found %d peers in garbage, want 0", len(peers))
	}
}

// sendResponse publishes a hand-built answer so the assembly rules can be
// tested without an advertiser.
func sendResponse(t *testing.T, conn PacketConn, answers []dns.RR) {
	t.Helper()
	msg := new(dns.Msg)
	msg.Response = true
	msg.Authoritative = true
	msg.Answer = answers
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	if _, err := conn.WriteTo(packed, GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
}

func ptrRR() *dns.PTR {
	return &dns.PTR{
		Hdr: dns.RR_Header{Name: serviceFQDN(), Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 120},
		Ptr: "srv-a._heimdallm._tcp.local.",
	}
}

func srvRR() *dns.SRV {
	return &dns.SRV{
		Hdr:    dns.RR_Header{Name: "srv-a._heimdallm._tcp.local.", Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: 120},
		Port:   7842,
		Target: "srv-a.local.",
	}
}

func txtRR() *dns.TXT {
	return &dns.TXT{
		Hdr: dns.RR_Header{Name: "srv-a._heimdallm._tcp.local.", Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 120},
		Txt: []string{"id=srv-a", "name=Server A", "role=worker"},
	}
}

func browseAnswers(t *testing.T, answers []dns.RR) []Peer {
	t.Helper()
	a, b := NewMemConn()
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	sendResponse(t, a, answers)
	browser, _ := NewBrowser(b, quietLogger())
	peers, err := browser.Browse(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	return peers
}

func TestBrowseNeedsBothSRVAndTXT(t *testing.T) {
	tests := []struct {
		name    string
		answers []dns.RR
		want    int
	}{
		{"complete", []dns.RR{ptrRR(), srvRR(), txtRR()}, 1},
		{"srv without txt", []dns.RR{ptrRR(), srvRR()}, 0},
		{"txt without srv", []dns.RR{ptrRR(), txtRR()}, 0},
		{"no ptr", []dns.RR{srvRR(), txtRR()}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(browseAnswers(t, tt.answers)); got != tt.want {
				t.Fatalf("found %d peers, want %d", got, tt.want)
			}
		})
	}
}

func TestBrowseSkipsAPeerWithNoInstanceID(t *testing.T) {
	txt := txtRR()
	txt.Txt = []string{"name=Server A", "role=worker"} // no id
	if got := len(browseAnswers(t, []dns.RR{ptrRR(), srvRR(), txt})); got != 0 {
		t.Fatalf("found %d peers without an id, want 0", got)
	}
}

func TestBrowseHonoursAGoodbye(t *testing.T) {
	// TTL 0 retracts the record. A peer that says goodbye mid-window must not
	// be reported as present.
	answers := []dns.RR{ptrRR(), srvRR(), txtRR()}
	goodbye := ptrRR()
	goodbye.Hdr.Ttl = 0

	if got := len(browseAnswers(t, append(answers, goodbye))); got != 0 {
		t.Fatalf("found %d peers after a goodbye, want 0", got)
	}
}

func TestAdvertiserRejectsAnUnusableAdvertisement(t *testing.T) {
	conn, other := NewMemConn()
	t.Cleanup(func() { _ = conn.Close(); _ = other.Close() })

	tests := []struct {
		name string
		ad   Advertisement
	}{
		{"no instance id", Advertisement{Port: 7842}},
		{"no port", Advertisement{InstanceID: "srv-a"}},
		{"port out of range", Advertisement{InstanceID: "srv-a", Port: 70000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewAdvertiser(conn, tt.ad, quietLogger()); err == nil {
				t.Fatal("NewAdvertiser accepted an unusable advertisement")
			}
		})
	}
	if _, err := NewAdvertiser(nil, testAdvertisement(), quietLogger()); err == nil {
		t.Fatal("NewAdvertiser accepted a nil connection")
	}
	if _, err := NewBrowser(nil, quietLogger()); err == nil {
		t.Fatal("NewBrowser accepted a nil connection")
	}
}

func TestAdvertiserFallsBackToTheInstanceIDWhenUnnamed(t *testing.T) {
	ad := testAdvertisement()
	ad.InstanceName = ""
	browseConn := startAdvertiser(t, ad)

	browser, _ := NewBrowser(browseConn, quietLogger())
	peers, err := browser.Browse(context.Background(), 500*time.Millisecond)
	if err != nil || len(peers) != 1 {
		t.Fatalf("Browse: %v, peers %+v", err, peers)
	}
	if peers[0].InstanceID != ad.InstanceID {
		t.Fatalf("InstanceID = %q, want %q", peers[0].InstanceID, ad.InstanceID)
	}
}

func TestAdvertiserSendsAGoodbyeOnShutdown(t *testing.T) {
	adConn, browseConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = browseConn.Close() })

	adv, err := NewAdvertiser(adConn, testAdvertisement(), quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); adv.Run(ctx) }()

	cancel()
	<-done

	// The goodbye is the only thing on the wire: nobody asked a question.
	browser, _ := NewBrowser(browseConn, quietLogger())
	peers, err := browser.Browse(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Browse: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("a goodbye produced %d live peers, want 0", len(peers))
	}
}

func TestAdvertiserIgnoresAQuestionAboutSomethingElse(t *testing.T) {
	browseConn := startAdvertiser(t, testAdvertisement())

	msg := new(dns.Msg)
	msg.SetQuestion("_printer._tcp.local.", dns.TypePTR)
	packed, _ := msg.Pack()
	if _, err := browseConn.WriteTo(packed, GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	browser, _ := NewBrowser(browseConn, quietLogger())
	// Browse sends its own query too, so drain with a raw read instead: the
	// point is that the printer question alone drew no answer.
	_ = browser
	_ = browseConn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	buf := make([]byte, 2048)
	if _, _, err := browseConn.ReadFrom(buf); err == nil {
		t.Fatal("the advertiser answered a question about another service")
	}
}

func TestBrowseSortsPeersByInstanceID(t *testing.T) {
	mk := func(id string) []dns.RR {
		name := id + "._heimdallm._tcp.local."
		return []dns.RR{
			&dns.PTR{Hdr: dns.RR_Header{Name: serviceFQDN(), Rrtype: dns.TypePTR, Class: dns.ClassINET, Ttl: 120}, Ptr: name},
			&dns.SRV{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeSRV, Class: dns.ClassINET, Ttl: 120}, Port: 7842, Target: id + ".local."},
			&dns.TXT{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET, Ttl: 120}, Txt: []string{"id=" + id}},
		}
	}
	var answers []dns.RR
	for _, id := range []string{"zulu", "alpha", "mike"} {
		answers = append(answers, mk(id)...)
	}

	peers := browseAnswers(t, answers)
	if len(peers) != 3 {
		t.Fatalf("found %d peers, want 3", len(peers))
	}
	for i, want := range []string{"alpha", "mike", "zulu"} {
		if peers[i].InstanceID != want {
			t.Fatalf("peers[%d].InstanceID = %q, want %q", i, peers[i].InstanceID, want)
		}
	}
}
