package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newReposTestServer(t *testing.T, cfg map[string]any, prs []map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/config":
			_ = json.NewEncoder(w).Encode(cfg)
		case "/prs":
			_ = json.NewEncoder(w).Encode(prs)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReposCmd_ListsLocalDirSourceAndOpenPRCount(t *testing.T) {
	srv := newReposTestServer(t,
		map[string]any{
			"repositories": []any{"acme/api", "acme/web", "acme/docs"},
			"repo_overrides": map[string]any{
				"acme/api": map[string]any{"local_dir": "/src/api"},
			},
			"local_dirs_detected": map[string]any{"acme/web": "/repos/web"},
		},
		[]map[string]any{
			{"id": 1, "repo": "acme/api", "number": 1, "state": "open"},
			{"id": 2, "repo": "acme/api", "number": 2, "state": "open"},
			{"id": 3, "repo": "acme/api", "number": 3, "state": "closed"},
			{"id": 4, "repo": "acme/web", "number": 4, "state": "open"},
		},
	)

	out, err := runCmd(t, srv, "repos")
	if err != nil {
		t.Fatalf("repos: %v", err)
	}
	if strings.Contains(out, "ISSUES") {
		t.Errorf("the issue column must be gone:\n%s", out)
	}
	for _, want := range []string{
		"REPO", "LOCAL_DIR", "PRS",
		"3 repositories monitored.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	rows := map[string][]string{}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) == 3 && strings.HasPrefix(f[0], "acme/") {
			rows[f[0]] = f[1:]
		}
	}
	want := map[string][]string{
		"acme/api":  {"yes", "2"},
		"acme/web":  {"auto", "1"},
		"acme/docs": {"no", "0"},
	}
	for repo, cols := range want {
		got := rows[repo]
		if len(got) != 2 || got[0] != cols[0] || got[1] != cols[1] {
			t.Errorf("%s row = %v, want %v\n%s", repo, got, cols, out)
		}
	}
}

func TestReposCmd_NoRepositories(t *testing.T) {
	srv := newReposTestServer(t, map[string]any{}, nil)

	out, err := runCmd(t, srv, "repos")
	if err != nil {
		t.Fatalf("repos: %v", err)
	}
	if !strings.Contains(out, "No monitored repositories.") {
		t.Errorf("got %q", out)
	}
}
