package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

// TestFetchCollaborators_MultiPage verifies that FetchCollaborators follows
// the GitHub Link: rel="next" header across multiple pages and returns the
// concatenated logins from every page in order.
func TestFetchCollaborators_MultiPage(t *testing.T) {
	pages := [][]string{
		{"alice", "bob"},
		{"carol", "dave"},
		{"eve"},
	}

	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/repos/org/repo/collaborators", func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if page < 1 || page > len(pages) {
			http.NotFound(w, r)
			return
		}
		if page < len(pages) {
			next := fmt.Sprintf(`<%s/repos/org/repo/collaborators?per_page=100&page=%d>; rel="next"`,
				server.URL, page+1)
			w.Header().Set("Link", next)
		}
		var raw []map[string]string
		for _, login := range pages[page-1] {
			raw = append(raw, map[string]string{"login": login})
		}
		_ = json.NewEncoder(w).Encode(raw)
	})

	client := gh.NewClient("fake", gh.WithBaseURL(server.URL))
	got, err := client.FetchCollaborators("org/repo")
	if err != nil {
		t.Fatalf("FetchCollaborators: unexpected error: %v", err)
	}

	want := []string{"alice", "bob", "carol", "dave", "eve"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("logins mismatch:\n got=%v\nwant=%v", got, want)
	}
}

// TestFetchCollaborators_SinglePage verifies that when the response has no
// Link header (small repo, fits in one page) FetchCollaborators returns the
// page's logins and stops after a single request.
func TestFetchCollaborators_SinglePage(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if r.URL.Path != "/repos/org/repo/collaborators" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode([]map[string]string{
			{"login": "alice"},
			{"login": "bob"},
		})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.FetchCollaborators("org/repo")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"alice", "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("logins mismatch: got=%v, want=%v", got, want)
	}
	if c := atomic.LoadInt32(&calls); c != 1 {
		t.Errorf("expected 1 HTTP call, got %d", c)
	}
}
