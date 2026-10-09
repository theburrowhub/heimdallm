package github_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)

func compareServer(t *testing.T, status int, jsonBody, diffBody string) *gh.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/repos/org/repo/compare/") {
			http.NotFound(w, r)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "diff") {
			_, _ = w.Write([]byte(diffBody))
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(jsonBody))
	}))
	t.Cleanup(srv.Close)
	return gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
}

const (
	shaA = "aaaaaaa1111111"
	shaB = "bbbbbbb2222222"
)

func TestFetchCompareDiff_LinearRange(t *testing.T) {
	c := compareServer(t, 200, `{"status":"ahead","commits":[{"parents":[{"sha":"x"}]}]}`, "diff --git a/f b/f\n+x\n")
	diff, ok, err := c.FetchCompareDiff("org/repo", shaA, shaB)
	if err != nil || !ok || !strings.Contains(diff, "+x") {
		t.Fatalf("diff=%q ok=%v err=%v", diff, ok, err)
	}
}

func TestFetchCompareDiff_FallsBack(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		diff   string
		base   string
	}{
		"force-pushed (diverged)": {200, `{"status":"diverged","commits":[{"parents":[{}]}]}`, "d", shaA},
		"merge commit in range":   {200, `{"status":"ahead","commits":[{"parents":[{},{}]}]}`, "d", shaA},
		"empty range":             {200, `{"status":"ahead","commits":[]}`, "d", shaA},
		"commit gone":             {404, `{}`, "d", shaA},
		"malformed json":          {200, `{"status":`, "d", shaA},
		"empty diff":              {200, `{"status":"ahead","commits":[{"parents":[{}]}]}`, "", shaA},
		"not a sha":               {200, `{"status":"ahead","commits":[{"parents":[{}]}]}`, "d", "../../etc"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := compareServer(t, tc.status, tc.body, tc.diff)
			if _, ok, err := c.FetchCompareDiff("org/repo", tc.base, shaB); ok || err != nil {
				t.Fatalf("ok=%v err=%v, want a silent fallback", ok, err)
			}
		})
	}
}

func TestFetchCompareDiff_ServerErrorIsAnError(t *testing.T) {
	c := compareServer(t, 500, `boom`, "")
	if _, ok, err := c.FetchCompareDiff("org/repo", shaA, shaB); ok || err == nil {
		t.Fatalf("ok=%v err=%v, want an error", ok, err)
	}
}
