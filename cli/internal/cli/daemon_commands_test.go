package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeDaemon answers the read endpoints status, stats and agents use.
func fakeDaemon(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /health":
			_, _ = w.Write([]byte(`{"status":"ok","version":"1.2.3"}`))
		case "GET /config":
			_, _ = w.Write([]byte(`{"repositories":["acme/api"],"ai_primary":"claude"}`))
		case "GET /stats":
			_, _ = w.Write([]byte(`{"total_reviews":3,"tokens_last_7_days":{"reviews":2,"input_tokens":1000,"output_tokens":100,"avg_prompt_bytes":2048}}`))
		case "GET /review-limits":
			_, _ = w.Write([]byte(`[{"kind":"global","windows":[{"window":"hour","used":1,"limit":5}]}]`))
		case "GET /cli-agents", "POST /cli-agents/rescan":
			_, _ = w.Write([]byte(`{"agents":[{"id":"copilot","name":"GitHub Copilot CLI","installed":true,"version":"1.0.88"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStatusCmdPrintsReviewLimits(t *testing.T) {
	out, err := runCmd(t, fakeDaemon(t), "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "Review limits:") || !strings.Contains(out, "all reviews: 1/5 per hour") {
		t.Errorf("status output:\n%s", out)
	}
}

func TestStatsCmdPrintsTokens(t *testing.T) {
	out, err := runCmd(t, fakeDaemon(t), "stats")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !strings.Contains(out, "Tokens (last 7 days):") || !strings.Contains(out, "Per review:   550") {
		t.Errorf("stats output:\n%s", out)
	}
}

func TestAgentsCmd(t *testing.T) {
	srv := fakeDaemon(t)
	for _, args := range [][]string{{"agents"}, {"agents", "--rescan"}} {
		out, err := runCmd(t, srv, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if !strings.Contains(out, "GitHub Copilot CLI") || !strings.Contains(out, "installed 1.0.88") {
			t.Errorf("%v output:\n%s", args, out)
		}
	}
	down := httptest.NewServer(http.NotFoundHandler())
	defer down.Close()
	if _, err := runCmd(t, down, "agents"); err == nil {
		t.Error("an unreachable catalog must fail the command")
	}
}
