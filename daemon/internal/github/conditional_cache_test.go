package github_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// ── ConditionalCache unit tests ───────────────────────────────────────────────

func TestConditionalCache_GetPutRoundTrip(t *testing.T) {
	c := gh.NewConditionalCache()

	// Empty cache returns ok=false.
	if _, _, ok := c.Get("key1"); ok {
		t.Fatal("expected ok=false on empty cache")
	}

	c.Put("key1", `"v1"`, []byte("hello"))
	etag, body, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected ok=true after Put")
	}
	if etag != `"v1"` {
		t.Errorf("etag = %q, want %q", etag, `"v1"`)
	}
	if string(body) != "hello" {
		t.Errorf("body = %q, want %q", body, "hello")
	}
}

func TestConditionalCache_PutUpdatesExistingEntry(t *testing.T) {
	c := gh.NewConditionalCache()
	c.Put("key1", `"v1"`, []byte("old"))
	c.Put("key1", `"v2"`, []byte("new"))

	etag, body, ok := c.Get("key1")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if etag != `"v2"` || string(body) != "new" {
		t.Errorf("got etag=%q body=%q, want v2/new", etag, body)
	}
}

func TestConditionalCache_EvictsOldestWhenAtCap(t *testing.T) {
	c := gh.NewConditionalCache()

	// Fill the cache to capacity with keys key-0000 … key-1999.
	for i := 0; i < 2000; i++ {
		k := fmt.Sprintf("key-%04d", i)
		c.Put(k, `"etag"`, []byte(k))
	}

	// Verify all entries exist.
	for i := 0; i < 2000; i++ {
		k := fmt.Sprintf("key-%04d", i)
		if _, _, ok := c.Get(k); !ok {
			t.Fatalf("expected entry %s before eviction", k)
		}
	}

	// Adding a 2001st key should evict exactly one entry.
	c.Put("new-key", `"etag"`, []byte("data"))

	// The new key must be present.
	if _, _, ok := c.Get("new-key"); !ok {
		t.Fatal("new-key missing after insert")
	}

	// Total entries in cache must be exactly 2000 (one was evicted).
	// We verify by counting how many of the original keys are still present.
	present := 0
	for i := 0; i < 2000; i++ {
		k := fmt.Sprintf("key-%04d", i)
		if _, _, ok := c.Get(k); ok {
			present++
		}
	}
	// 1999 original keys + 1 new key = 2000 total.
	if present != 1999 {
		t.Errorf("expected 1999 original keys after eviction, got %d", present)
	}
}

func TestConditionalCache_ConcurrencyNoPanic(t *testing.T) {
	c := gh.NewConditionalCache()
	var wg sync.WaitGroup
	const goroutines = 50
	const opsPerGoroutine = 200

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				k := fmt.Sprintf("key-%d-%d", id, j%10)
				c.Put(k, `"etag"`, []byte("data"))
				c.Get(k)
			}
		}(i)
	}
	wg.Wait()
}

// ── Integration tests via httptest ────────────────────────────────────────────

// serverCallCount keeps a per-path counter so tests can assert the server was
// only hit a specific number of times.
type callCounter struct {
	mu     sync.Mutex
	counts map[string]int
}

func newCallCounter() *callCounter { return &callCounter{counts: map[string]int{}} }
func (cc *callCounter) inc(path string) {
	cc.mu.Lock()
	cc.counts[path]++
	cc.mu.Unlock()
}
func (cc *callCounter) get(path string) int {
	cc.mu.Lock()
	defer cc.mu.Unlock()
	return cc.counts[path]
}

// TestETagCaching_SecondCallSendsIfNoneMatch verifies that:
//  1. First GET → 200 + ETag "v1" + body B1.
//  2. Client stores the ETag and body.
//  3. Second GET → server receives If-None-Match "v1" → replies 304 (empty body).
//  4. Caller still observes status 200 and can read B1 — transparent swap.
func TestETagCaching_SecondCallSendsIfNoneMatch(t *testing.T) {
	const body1 = `{"state":"open"}`
	cc := newCallCounter()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/org/repo/issues/1" {
			http.NotFound(w, r)
			return
		}
		cc.inc(r.URL.Path)
		inm := r.Header.Get("If-None-Match")
		if inm == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, body1)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	// First call — should hit the server and cache the response.
	resp1, err := client.DoGETForTest("/repos/org/repo/issues/1", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	data1, _ := io.ReadAll(resp1.Body)
	resp1.Body.Close()
	if resp1.StatusCode != http.StatusOK {
		t.Errorf("first call status = %d, want 200", resp1.StatusCode)
	}
	if string(data1) != body1 {
		t.Errorf("first call body = %q, want %q", data1, body1)
	}
	if cc.get("/repos/org/repo/issues/1") != 1 {
		t.Errorf("server call count after first request = %d, want 1", cc.get("/repos/org/repo/issues/1"))
	}

	// Second call — server receives If-None-Match "v1" and replies 304.
	// Client must transparently return 200 + cached body.
	resp2, err := client.DoGETForTest("/repos/org/repo/issues/1", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	data2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("second call status = %d, want 200", resp2.StatusCode)
	}
	if string(data2) != body1 {
		t.Errorf("second call body = %q, want %q", data2, body1)
	}
	if cc.get("/repos/org/repo/issues/1") != 2 {
		t.Errorf("server call count after second request = %d, want 2", cc.get("/repos/org/repo/issues/1"))
	}

	hits, _ := client.CacheStats()
	if hits != 1 {
		t.Errorf("cache hits = %d, want 1", hits)
	}
}

// TestETagCaching_UpdatedETagReplacesCachedBody verifies that when the server
// returns a new ETag (v2) + new body (B2), the cache is updated and the caller
// sees B2 transparently.
func TestETagCaching_UpdatedETagReplacesCachedBody(t *testing.T) {
	const body1 = `{"state":"open"}`
	const body2 = `{"state":"closed"}`
	call := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		switch call {
		case 1:
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, body1)
		case 2:
			// New ETag — resource changed.
			w.Header().Set("ETag", `"v2"`)
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, body2)
		default:
			// Third call: If-None-Match "v2" → 304.
			if r.Header.Get("If-None-Match") != `"v2"` {
				http.Error(w, "unexpected If-None-Match", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNotModified)
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	path := "/repos/org/repo/issues/42"
	accept := "application/vnd.github+json"

	// Call 1 → 200 + ETag v1 + body1.
	r1, err := client.DoGETForTest(path, accept)
	if err != nil {
		t.Fatalf("call1: %v", err)
	}
	d1, _ := io.ReadAll(r1.Body)
	r1.Body.Close()
	if string(d1) != body1 {
		t.Errorf("call1 body = %q, want %q", d1, body1)
	}

	// Call 2 → server replies with new ETag v2 + body2.
	r2, err := client.DoGETForTest(path, accept)
	if err != nil {
		t.Fatalf("call2: %v", err)
	}
	d2, _ := io.ReadAll(r2.Body)
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Errorf("call2 status = %d, want 200", r2.StatusCode)
	}
	if string(d2) != body2 {
		t.Errorf("call2 body = %q, want %q", d2, body2)
	}

	// Call 3 → 304 served from cache (v2 body).
	r3, err := client.DoGETForTest(path, accept)
	if err != nil {
		t.Fatalf("call3: %v", err)
	}
	d3, _ := io.ReadAll(r3.Body)
	r3.Body.Close()
	if r3.StatusCode != http.StatusOK {
		t.Errorf("call3 status = %d, want 200", r3.StatusCode)
	}
	if string(d3) != body2 {
		t.Errorf("call3 body = %q, want %q (expected v2 body from cache)", d3, body2)
	}

	hits, misses := client.CacheStats()
	if hits != 1 {
		t.Errorf("cache hits = %d, want 1", hits)
	}
	if misses != 2 {
		t.Errorf("cache misses (stores) = %d, want 2", misses)
	}
}

// TestETagCaching_NoETagResponseIsNotCached verifies that responses without an
// ETag header are not cached: no If-None-Match is sent on the next request.
func TestETagCaching_NoETagResponseIsNotCached(t *testing.T) {
	cc := newCallCounter()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc.inc(r.URL.Path)
		// Deliberately no ETag header.
		if r.Header.Get("If-None-Match") != "" {
			http.Error(w, "unexpected If-None-Match", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"id":1}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	path := "/repos/org/repo/issues/99"

	for i := 0; i < 3; i++ {
		resp, err := client.DoGETForTest(path, "application/vnd.github+json")
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		resp.Body.Close()
	}
	if cc.get(path) != 3 {
		t.Errorf("server call count = %d, want 3 (no caching without ETag)", cc.get(path))
	}
	_, misses := client.CacheStats()
	if misses != 0 {
		t.Errorf("cache misses (stores) = %d, want 0 (no ETag → nothing stored)", misses)
	}
}

// TestETagCaching_NonGETNotCached confirms that DELETE (and other non-GET
// methods) are never intercepted by the cache layer.
func TestETagCaching_NonGETNotCached(t *testing.T) {
	cc := newCallCounter()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cc.inc(r.Method)
		if r.Header.Get("If-None-Match") != "" {
			http.Error(w, "unexpected If-None-Match on DELETE", http.StatusInternalServerError)
			return
		}
		// ETag on the response should also be ignored for non-GETs.
		w.Header().Set("ETag", `"v1"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	// Issue two DELETE calls; the cache must not interfere.
	for i := 0; i < 2; i++ {
		resp, err := client.DoDELETEForTest("/repos/org/repo/issues/1/labels/foo",
			"application/vnd.github+json")
		if err != nil {
			t.Fatalf("DELETE call %d: %v", i+1, err)
		}
		resp.Body.Close()
	}
	if cc.get("DELETE") != 2 {
		t.Errorf("server DELETE count = %d, want 2", cc.get("DELETE"))
	}
	hits, misses := client.CacheStats()
	if hits != 0 || misses != 0 {
		t.Errorf("cache hits=%d misses=%d, want 0/0 for non-GET requests", hits, misses)
	}
}
