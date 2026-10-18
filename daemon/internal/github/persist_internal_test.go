package github

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memBackend struct {
	mu      sync.Mutex
	entries map[string]PersistedETag
	loadErr error
	deleted []string
}

func newMemBackend() *memBackend { return &memBackend{entries: map[string]PersistedETag{}} }

func (b *memBackend) LoadETags() ([]PersistedETag, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []PersistedETag
	for _, e := range b.entries {
		out = append(out, e)
	}
	return out, b.loadErr
}

func (b *memBackend) SaveETag(e PersistedETag) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.entries[e.Key] = e
	return nil
}

func (b *memBackend) DeleteETag(key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.entries, key)
	b.deleted = append(b.deleted, key)
	return nil
}

// etagServer serves body with ETag "v1" and answers 304 to a matching
// If-None-Match, counting full (200) responses.
func etagServer(t *testing.T) (string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	var full, notModified atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("If-None-Match") == `"v1"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full.Add(1)
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`{"login":"bot"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &full, &notModified
}

func TestPersistentETagCache_SurvivesRestart(t *testing.T) {
	url, full, notModified := etagServer(t)
	backend := newMemBackend()

	first := NewClient("tok", WithBaseURL(url))
	if err := first.AttachCacheBackend(backend); err != nil {
		t.Fatal(err)
	}
	resp, err := first.do("GET", "/user", "application/json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(backend.entries) != 1 {
		t.Fatalf("entry not persisted: %v", backend.entries)
	}

	// A new client — a restarted daemon — starts warm from the backend.
	second := NewClient("tok", WithBaseURL(url))
	if err := second.AttachCacheBackend(backend); err != nil {
		t.Fatal(err)
	}
	resp, err = second.do("GET", "/user", "application/json")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if full.Load() != 1 || notModified.Load() != 1 {
		t.Fatalf("full=%d notModified=%d, want the restart served by a 304", full.Load(), notModified.Load())
	}
}

func TestAttachCacheBackend_LoadErrorStillAttaches(t *testing.T) {
	backend := newMemBackend()
	backend.loadErr = errors.New("disk gone")
	c := NewClient("tok")
	if err := c.AttachCacheBackend(backend); err == nil {
		t.Fatal("want load error")
	}
	c.cache.Put("k", "e", []byte("b"))
	if _, ok := backend.entries["k"]; !ok {
		t.Fatal("backend must still receive writes")
	}
	if err := c.AttachCacheBackend(nil); err != nil {
		t.Fatal("nil backend is a no-op")
	}
}

func TestConditionalCache_LoadSkipsInvalidAndKeepsNewest(t *testing.T) {
	cc := NewConditionalCache()
	now := time.Now()
	entries := []PersistedETag{
		{Key: "", ETag: "e", Body: []byte("x"), StoredAt: now},
		{Key: "noetag", Body: []byte("x"), StoredAt: now},
		{Key: "huge", ETag: "e", Body: make([]byte, maxCacheBodyBytes+1), StoredAt: now},
	}
	for i := 0; i < maxCacheEntries+5; i++ {
		entries = append(entries, PersistedETag{
			Key: fmt.Sprintf("k%d", i), ETag: "e", Body: []byte("b"),
			StoredAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	cc.load(entries)
	if cc.Len() != maxCacheEntries {
		t.Fatalf("len = %d", cc.Len())
	}
	if _, _, ok := cc.Get("noetag"); ok {
		t.Fatal("entry without etag loaded")
	}
	newest := entries[len(entries)-1].Key
	if _, _, ok := cc.Get(newest); !ok {
		t.Fatal("newest entry evicted")
	}
}

func TestConditionalCache_EvictsLeastRecentlyUsed(t *testing.T) {
	cc := NewConditionalCache()
	backend := newMemBackend()
	cc.setBackend(backend)
	base := time.Now().Add(-time.Hour)
	cc.mu.Lock()
	for i := 0; i < maxCacheEntries; i++ {
		cc.putLocked(fmt.Sprintf("k%d", i), "e", []byte("b"), base.Add(time.Duration(i)*time.Millisecond))
	}
	cc.mu.Unlock()
	oldest := "k0"
	// Touching the oldest entry makes the second-oldest the eviction victim.
	cc.Get(oldest)
	cc.Put("new", "e", []byte("b"))
	if _, _, ok := cc.Get(oldest); !ok {
		t.Fatal("recently used entry was evicted")
	}
	victim := "k1"
	if _, _, ok := cc.Get(victim); ok {
		t.Fatal("LRU victim still present")
	}
	if len(backend.deleted) != 1 || backend.deleted[0] != victim {
		t.Fatalf("backend deletes = %v", backend.deleted)
	}
}

type memDiffs struct {
	mu   sync.Mutex
	m    map[string]string
	gets int
}

func (d *memDiffs) GetDiff(k string) (string, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.gets++
	v, ok := d.m[k]
	return v, ok
}

func (d *memDiffs) PutDiff(k, v string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.m[k] = v
}

const (
	shaBase = "1111111111111111111111111111111111111111"
	shaHead = "2222222222222222222222222222222222222222"
)

func TestFetchDiffAt_ServesRepeatFromCache(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("diff --git a/x b/x\n"))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	c.SetCacheEnabled(false)
	diffs := &memDiffs{m: map[string]string{}}
	c.AttachDiffCache(diffs)

	for i := 0; i < 3; i++ {
		d, err := c.FetchDiffAt("o/r", 1, shaBase, shaHead)
		if err != nil || !strings.HasPrefix(d, "diff --git") {
			t.Fatalf("d=%q err=%v", d, err)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want 1", hits.Load())
	}
	// A new head is a new key.
	if _, err := c.FetchDiffAt("o/r", 1, shaBase, shaBase); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("requests = %d, want 2", hits.Load())
	}
	// Without valid SHAs the cache is bypassed.
	if _, err := c.FetchDiffAt("o/r", 1, "", shaHead); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Fatalf("requests = %d, want 3", hits.Load())
	}
	c.AttachDiffCache(nil)
	if c.diffCache() != nil {
		t.Fatal("detach")
	}
}

func TestFetchDiffAt_ErrorIsNotCached(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	diffs := &memDiffs{m: map[string]string{}}
	c.AttachDiffCache(diffs)
	if _, err := c.FetchDiffAt("o/r", 1, shaBase, shaHead); err == nil {
		t.Fatal("want error")
	}
	if len(diffs.m) != 0 {
		t.Fatal("error cached")
	}
}

func TestFetchCompareDiff_CachesBothVerdicts(t *testing.T) {
	var hits atomic.Int64
	linear := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if strings.Contains(r.Header.Get("Accept"), "diff") {
			_, _ = w.Write([]byte("diff --git a/y b/y\n"))
			return
		}
		if linear {
			_, _ = w.Write([]byte(`{"status":"ahead","commits":[{"parents":[{"sha":"a"}]}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"diverged","commits":[]}`))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	c.SetCacheEnabled(false)
	c.AttachDiffCache(&memDiffs{m: map[string]string{}})

	for i := 0; i < 2; i++ {
		d, ok, err := c.FetchCompareDiff("o/r", shaBase, shaHead)
		if err != nil || !ok || d == "" {
			t.Fatalf("d=%q ok=%v err=%v", d, ok, err)
		}
	}
	if hits.Load() != 2 { // compare JSON + compare diff, once
		t.Fatalf("requests = %d", hits.Load())
	}

	linear = false
	for i := 0; i < 2; i++ {
		_, ok, err := c.FetchCompareDiff("o/r", shaHead, shaBase)
		if err != nil || ok {
			t.Fatalf("ok=%v err=%v", ok, err)
		}
	}
	if hits.Load() != 3 {
		t.Fatalf("requests = %d, the non-linear verdict must be cached", hits.Load())
	}
}

func TestFetchReposByTopic_MetersEveryPageOnTheSearchGate(t *testing.T) {
	var pages atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := pages.Add(1)
		items := make([]string, 0, 100)
		count := 100
		if n == 2 {
			count = 1
		}
		for i := 0; i < count; i++ {
			items = append(items, fmt.Sprintf(`{"full_name":"org/r%d-%d"}`, n, i))
		}
		_, _ = fmt.Fprintf(w, `{"total_count":101,"items":[%s]}`, strings.Join(items, ","))
	}))
	t.Cleanup(srv.Close)
	c := NewClient("tok", WithBaseURL(srv.URL))
	var gates int
	c.SetSearchGate(func() error { gates++; return nil })
	repos, err := c.FetchReposByTopic("t", []string{"org"})
	if err != nil || len(repos) != 101 {
		t.Fatalf("repos=%d err=%v", len(repos), err)
	}
	if gates != 2 {
		t.Fatalf("search gate acquired %d times, want once per page", gates)
	}
	c.SetSearchGate(func() error { return errors.New("no budget") })
	if _, err := c.FetchReposByTopic("t", []string{"org"}); err == nil {
		t.Fatal("refused gate must fail the org")
	}
}
