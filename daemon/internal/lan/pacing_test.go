package lan

import (
	"context"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/miekg/dns"
)

// recordingConn is a memConn that also remembers where each write was
// addressed, which the in-memory pair otherwise discards.
type recordingConn struct {
	PacketConn
	mu    sync.Mutex
	dests []net.Addr
}

func (c *recordingConn) WriteTo(b []byte, to net.Addr) (int, error) {
	c.mu.Lock()
	c.dests = append(c.dests, to)
	c.mu.Unlock()
	return c.PacketConn.WriteTo(b, to)
}

func (c *recordingConn) destinations() []net.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]net.Addr(nil), c.dests...)
}

// sourcedConn makes reads appear to come from a chosen address. The in-memory
// pair reports only which end wrote, so without this the advertiser cannot see
// a source port and every query looks like it came from a full participant.
type sourcedConn struct {
	PacketConn
	src net.Addr
}

func (c *sourcedConn) ReadFrom(b []byte) (int, net.Addr, int, error) {
	n, _, iface, err := c.PacketConn.ReadFrom(b)
	if err != nil {
		return n, nil, iface, err
	}
	return n, c.src, iface, nil
}

// runAdvertiser starts one on the given connection and stops it on cleanup.
func runAdvertiser(t *testing.T, conn PacketConn, ad Advertisement) *Advertiser {
	t.Helper()
	adv, err := NewAdvertiser(conn, ad, quietLogger())
	if err != nil {
		t.Fatalf("NewAdvertiser: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); adv.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	return adv
}

// queryFrom sends a PTR query for the service. The apparent source is whatever
// the advertiser's side was wrapped with; see sourcedConn.
func queryFrom(t *testing.T, conn PacketConn, id uint16) {
	t.Helper()
	msg := new(dns.Msg)
	msg.SetQuestion(serviceFQDN(), dns.TypePTR)
	msg.Id = id
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("packing: %v", err)
	}
	if _, err := conn.WriteTo(packed, GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
}

// participant is a source that looks like a full mDNS participant.
func participant() net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("192.168.1.9"), Port: mdnsPort}
}

// legacyQuerier is a source that looks like a one-shot resolver.
func legacyQuerier(port int) net.Addr {
	return &net.UDPAddr{IP: net.ParseIP("192.168.1.9"), Port: port}
}

// A query arriving inside the rate-limit interval is answered late, not
// dropped.
//
// Browse sends exactly one query per window, so a dropped answer is not a
// retry away: two hubs browsing the same link within the interval meant the
// second was answered with silence and saw this daemon as absent for its
// whole window. RFC 6762 §6 says to defer, not to discard.
func TestASecondQueryInsideTheIntervalIsDeferredNotDropped(t *testing.T) {
	realInterval, realDelay := minResponseInterval, responseDelay
	minResponseInterval = 150 * time.Millisecond
	responseDelay = func() time.Duration { return 0 }
	t.Cleanup(func() { minResponseInterval, responseDelay = realInterval, realDelay })

	adConn, peerConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = peerConn.Close() })
	runAdvertiser(t, &sourcedConn{adConn, participant()}, testAdvertisement())

	// First query: answered promptly.
	queryFrom(t, peerConn, 1)
	first := readReply(t, peerConn)
	if len(first.Answer) == 0 {
		t.Fatal("the first query went unanswered")
	}

	// Second, immediately: inside the interval, so it must wait rather than
	// vanish.
	queryFrom(t, peerConn, 2)
	second := readReply(t, peerConn)
	if len(second.Answer) == 0 {
		t.Fatal("a query inside the rate-limit interval was dropped instead of deferred")
	}
}

// The jitter must not stop the socket being drained.
//
// It used to be a time.Sleep on the read loop's own goroutine, so for its
// duration nothing was read and the kernel receive buffer could discard other
// queries. With a long jitter and a burst of queries, every one has to be
// consumed.
func TestTheResponseDelayDoesNotBlockTheReadLoop(t *testing.T) {
	realInterval, realDelay := minResponseInterval, responseDelay
	minResponseInterval = 0
	responseDelay = func() time.Duration { return 300 * time.Millisecond }
	t.Cleanup(func() { minResponseInterval, responseDelay = realInterval, realDelay })

	adConn, peerConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = peerConn.Close() })

	var reads atomic.Int32
	counting := &countingConn{PacketConn: &sourcedConn{adConn, participant()}, reads: &reads}
	runAdvertiser(t, counting, testAdvertisement())

	const burst = 8
	for i := range burst {
		queryFrom(t, peerConn, uint16(i+1))
	}

	// Well inside the 300ms jitter: if the loop slept through it, it would
	// have consumed one packet, not the burst.
	deadline := time.Now().Add(150 * time.Millisecond)
	for time.Now().Before(deadline) && reads.Load() < burst {
		time.Sleep(5 * time.Millisecond)
	}
	if got := reads.Load(); got < burst {
		t.Fatalf("read %d of %d queries while a response was pending; the "+
			"delay is blocking the read loop", got, burst)
	}
}

// countingConn counts successful reads.
type countingConn struct {
	PacketConn
	reads *atomic.Int32
}

func (c *countingConn) ReadFrom(b []byte) (int, net.Addr, int, error) {
	n, from, iface, err := c.PacketConn.ReadFrom(b)
	if err == nil {
		c.reads.Add(1)
	}
	return n, from, iface, err
}

// A question that is not about us must not build the record set.
//
// Building invokes the Addrs() callback and formats every record to key the
// dedup map, and the rate limit cannot help: it used to be applied after the
// build, so a flooder paid us for the full construction of an answer that was
// then thrown away.
func TestAQueryThatIsNotOursBuildsNothing(t *testing.T) {
	unpaced(t)
	adConn, peerConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = peerConn.Close() })

	var builds atomic.Int32
	ad := testAdvertisement()
	ad.Addrs = func() []netip.Addr {
		builds.Add(1)
		return []netip.Addr{netip.MustParseAddr("10.0.0.11")}
	}
	runAdvertiser(t, &sourcedConn{adConn, participant()}, ad)

	msg := new(dns.Msg)
	msg.SetQuestion("_printer._tcp.local.", dns.TypePTR)
	packed, err := msg.Pack()
	if err != nil {
		t.Fatalf("packing: %v", err)
	}
	if _, err := peerConn.WriteTo(packed, GroupAddr()); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}

	// Long enough that a response would have gone out.
	time.Sleep(200 * time.Millisecond)
	if got := builds.Load(); got != 0 {
		t.Fatalf("built the record set %d times for a question about another "+
			"service; a non-matching query must cost a name comparison", got)
	}
}

// A one-shot resolver is not in the group, so it gets its own answer
// (RFC 6762 §6.7): unicast to the asker, echoing its id and question, with a
// short TTL and no cache-flush bit.
func TestALegacyQuerierGetsAUnicastAnswer(t *testing.T) {
	unpaced(t)
	adConn, peerConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = peerConn.Close() })

	const legacyPort = 54321
	recorder := &recordingConn{PacketConn: &sourcedConn{adConn, legacyQuerier(legacyPort)}}
	runAdvertiser(t, recorder, testAdvertisement())

	queryFrom(t, peerConn, 0x4d2)

	reply := readReply(t, peerConn)
	if reply.Id != 0x4d2 {
		t.Fatalf("legacy reply id = %#x, want the query's own %#x — a one-shot "+
			"resolver matches on it", reply.Id, 0x4d2)
	}
	if len(reply.Question) != 1 {
		t.Fatalf("legacy reply carried %d questions, want the query repeated back "+
			"(RFC 6762 §6.7)", len(reply.Question))
	}
	if len(reply.Answer) == 0 {
		t.Fatal("legacy reply carried no answer")
	}
	for _, rr := range reply.Answer {
		if rr.Header().Ttl > legacyResponseTTL {
			t.Fatalf("legacy record %s has TTL %d, want at most %d: the querier "+
				"never hears a goodbye", rr.Header().Name, rr.Header().Ttl, legacyResponseTTL)
		}
		if rr.Header().Class&cacheFlush != 0 {
			t.Fatalf("legacy record %s carries the cache-flush bit, which a "+
				"one-shot resolver reads as an unknown class", rr.Header().Name)
		}
	}

	dests := recorder.destinations()
	if len(dests) == 0 {
		t.Fatal("nothing was sent")
	}
	last := dests[len(dests)-1]
	udp, ok := last.(*net.UDPAddr)
	if !ok || udp.Port != legacyPort {
		t.Fatalf("legacy answer went to %v, want the querier at port %d", last, legacyPort)
	}
}

// Clamping the legacy TTL must not shorten the records the group gets.
func TestALegacyAnswerDoesNotShortenTheMulticastRecords(t *testing.T) {
	unpaced(t)
	adConn, peerConn := NewMemConn()
	t.Cleanup(func() { _ = adConn.Close(); _ = peerConn.Close() })
	// One advertiser, two apparent sources: the legacy query first, then a
	// participant, so a TTL clamp that leaked into the shared records shows up
	// in the second answer.
	source := &switchableConn{PacketConn: adConn, src: legacyQuerier(54321)}
	runAdvertiser(t, source, testAdvertisement())

	queryFrom(t, peerConn, 1)
	_ = readReply(t, peerConn)

	source.use(participant())
	queryFrom(t, peerConn, 2)
	multicast := readReply(t, peerConn)
	if len(multicast.Answer) == 0 {
		t.Fatal("the participant's query went unanswered")
	}
	for _, rr := range multicast.Answer {
		if rr.Header().Ttl <= legacyResponseTTL {
			t.Fatalf("record %s came back with TTL %d after a legacy answer; the "+
				"clamp leaked into the shared records", rr.Header().Name, rr.Header().Ttl)
		}
	}
}

// switchableConn is sourcedConn with a source a test can change mid-run.
type switchableConn struct {
	PacketConn
	mu  sync.Mutex
	src net.Addr
}

func (c *switchableConn) use(src net.Addr) {
	c.mu.Lock()
	c.src = src
	c.mu.Unlock()
}

func (c *switchableConn) ReadFrom(b []byte) (int, net.Addr, int, error) {
	n, _, iface, err := c.PacketConn.ReadFrom(b)
	if err != nil {
		return n, nil, iface, err
	}
	c.mu.Lock()
	src := c.src
	c.mu.Unlock()
	return n, src, iface, nil
}
