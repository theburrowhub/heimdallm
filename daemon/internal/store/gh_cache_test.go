package store

import (
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
)

func openCacheStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestETagCache_RoundTripAndNamespaces(t *testing.T) {
	s := openCacheStore(t)
	a := s.ETagCache("tokA")
	at := time.Now().UTC().Truncate(time.Second)
	if err := a.SaveETag(github.PersistedETag{Key: "k1", ETag: `"e1"`, Body: []byte("b1"), StoredAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveETag(github.PersistedETag{Key: "k1", ETag: `"e2"`, Body: []byte("b2"), StoredAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := a.SaveETag(github.PersistedETag{Key: "k2", ETag: `"e"`, Body: []byte("x"), StoredAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteETag("k2"); err != nil {
		t.Fatal(err)
	}
	got, err := a.LoadETags()
	if err != nil || len(got) != 1 {
		t.Fatalf("got %v err %v", got, err)
	}
	if got[0].ETag != `"e2"` || string(got[0].Body) != "b2" || !got[0].StoredAt.Equal(at) {
		t.Fatalf("entry = %+v", got[0])
	}

	// Another token's namespace purges and never sees tokA's entries.
	b := s.ETagCache("tokB")
	if got, _ := b.LoadETags(); len(got) != 0 {
		t.Fatalf("foreign entries leaked: %v", got)
	}
	if got, _ := a.LoadETags(); len(got) != 0 {
		t.Fatalf("tokA entries should be purged: %v", got)
	}
}

func TestDiffCache_GetPutAndNamespaces(t *testing.T) {
	s := openCacheStore(t)
	d := s.DiffCache("ns1")
	if _, ok := d.GetDiff("k"); ok {
		t.Fatal("empty cache hit")
	}
	d.PutDiff("k", "diff body")
	d.PutDiff("empty", "")
	if v, ok := d.GetDiff("k"); !ok || v != "diff body" {
		t.Fatalf("v=%q ok=%v", v, ok)
	}
	if v, ok := d.GetDiff("empty"); !ok || v != "" {
		t.Fatal("empty body must be a hit")
	}
	other := s.DiffCache("ns2")
	if _, ok := other.GetDiff("k"); ok {
		t.Fatal("namespace leak")
	}
	if _, ok := d.GetDiff("k"); ok {
		t.Fatal("old namespace should be purged")
	}
}

func TestDiffCache_PutTriggersPrune(t *testing.T) {
	s := openCacheStore(t)
	d := s.DiffCache("ns")
	d.maxBytes = 5
	d.PutDiff("big", "0123456789") // first write prunes: over budget, dropped
	if _, ok := d.GetDiff("big"); ok {
		t.Fatal("prune on first write should enforce the budget")
	}
}

func TestDiffCache_PruneByAgeAndSize(t *testing.T) {
	s := openCacheStore(t)
	d := s.DiffCache("ns")
	old := time.Now().Add(-30 * 24 * time.Hour).UTC().Format(sqliteTimeFormat)
	if _, err := s.db.Exec(`INSERT INTO gh_diff_cache (ns, key, body, stored_at) VALUES ('ns','old',?,?)`, []byte("x"), old); err != nil {
		t.Fatal(err)
	}
	d.maxBytes = 25
	for i, k := range []string{"a", "b", "c"} {
		at := time.Now().Add(time.Duration(i-10) * time.Minute).UTC().Format(sqliteTimeFormat)
		if _, err := s.db.Exec(`INSERT INTO gh_diff_cache (ns, key, body, stored_at) VALUES ('ns',?,?,?)`, k, []byte(strings.Repeat(k, 10)), at); err != nil {
			t.Fatal(err)
		}
	}
	d.Prune()
	if _, ok := d.GetDiff("old"); ok {
		t.Fatal("aged entry survived")
	}
	if _, ok := d.GetDiff("a"); ok {
		t.Fatal("oldest entry should be dropped for size")
	}
	for _, k := range []string{"b", "c"} {
		if _, ok := d.GetDiff(k); !ok {
			t.Fatalf("%s dropped", k)
		}
	}
}

func TestGitHubCaches_DatabaseErrorsAreReported(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	e := s.ETagCache("ns")
	if _, err := e.LoadETags(); err == nil {
		t.Error("LoadETags on a closed db must fail")
	}
	if err := e.SaveETag(github.PersistedETag{Key: "k", ETag: "e"}); err == nil {
		t.Error("SaveETag on a closed db must fail")
	}
	if err := e.DeleteETag("k"); err == nil {
		t.Error("DeleteETag on a closed db must fail")
	}
	d := s.DiffCache("ns")
	d.PutDiff("k", "v") // logged, not fatal
	if _, ok := d.GetDiff("k"); ok {
		t.Error("closed db must miss")
	}
	d.Prune()
}
