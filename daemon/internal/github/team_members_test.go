package github_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestIsTeamMember(t *testing.T) {
	var membershipCalls, teamCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/orgs/acme/teams/core/memberships/alice":
			membershipCalls.Add(1)
			_, _ = w.Write([]byte(`{"state":"active"}`))
		case "/orgs/acme/teams/core/memberships/invited":
			_, _ = w.Write([]byte(`{"state":"pending"}`))
		case "/orgs/acme/teams/core/memberships/mallory", "/orgs/acme/teams/typo/memberships/alice":
			w.WriteHeader(http.StatusNotFound)
		case "/orgs/acme/teams/core", "/orgs/acme/teams/typo":
			teamCalls.Add(1)
			if strings.HasSuffix(r.URL.Path, "/typo") {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = w.Write([]byte(`{"slug":"core"}`))
		case "/orgs/acme/teams/core/memberships/broken":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	for _, tc := range []struct {
		team, login string
		want        bool
		wantErr     bool
	}{
		{"core", "alice", true, false},
		{"core", "invited", false, false}, // a pending invitation is not membership
		{"core", "mallory", false, false},
		{"typo", "alice", false, false}, // unknown team: not a member, logged
		{"core", "broken", false, true},
	} {
		got, err := c.IsTeamMember("acme", tc.team, tc.login)
		if got != tc.want || (err != nil) != tc.wantErr {
			t.Errorf("IsTeamMember(acme/%s, %s) = %v, %v; want %v, err=%v", tc.team, tc.login, got, err, tc.want, tc.wantErr)
		}
	}
	if teamCalls.Load() != 2 {
		t.Errorf("team visibility checks = %d, want 2 (one per 404)", teamCalls.Load())
	}

	// Cached: a second lookup does not hit GitHub.
	if ok, _ := c.IsTeamMember("ACME", "Core", "Alice"); !ok || membershipCalls.Load() != 1 {
		t.Errorf("cached lookup = %v with %d calls, want true with 1", ok, membershipCalls.Load())
	}
}

func TestOrgPrincipals(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/orgs/acme/teams":
			_, _ = w.Write([]byte(`[{"slug":"core"},{"slug":"ops"}]`))
		case "/orgs/acme/members":
			if r.URL.Query().Get("page") == "1" {
				var b strings.Builder
				b.WriteString("[")
				for i := 0; i < 100; i++ {
					if i > 0 {
						b.WriteString(",")
					}
					fmt.Fprintf(&b, `{"login":"user%03d"}`, i)
				}
				b.WriteString("]")
				_, _ = w.Write([]byte(b.String()))
				return
			}
			_, _ = w.Write([]byte(`[{"login":"Zed"}]`))
		case "/orgs/someone/teams", "/orgs/someone/members":
			w.WriteHeader(http.StatusNotFound) // personal account
		case "/orgs/down/teams":
			w.WriteHeader(http.StatusBadGateway)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := gh.NewClient("fake", gh.WithBaseURL(srv.URL))

	got, err := c.OrgPrincipals("acme")
	if err != nil {
		t.Fatalf("OrgPrincipals: %v", err)
	}
	if len(got) != 103 {
		t.Fatalf("got %d principals, want 2 teams + 101 members", len(got))
	}
	if got[0] != (gh.Principal{Value: "acme/core", Kind: "team"}) || got[len(got)-1] != (gh.Principal{Value: "Zed", Kind: "user"}) {
		t.Errorf("first/last = %v / %v, want acme/core team first and Zed last (case-insensitive sort)", got[0], got[len(got)-1])
	}

	before := calls.Load()
	if _, err := c.OrgPrincipals("acme"); err != nil || calls.Load() != before {
		t.Errorf("second call: err=%v, calls %d -> %d, want cached", err, before, calls.Load())
	}

	if got, err := c.OrgPrincipals("someone"); err != nil || len(got) != 0 {
		t.Errorf("personal account = %v, %v; want empty, nil", got, err)
	}

	// A failing org returns the error, and the failure is cached too.
	if _, err := c.OrgPrincipals("down"); err == nil {
		t.Error("502 listing: want an error")
	}
	before = calls.Load()
	if _, err := c.OrgPrincipals("down"); err == nil || calls.Load() != before {
		t.Errorf("repeat after failure: err=%v, calls %d -> %d; want the cached error", err, before, calls.Load())
	}
}
