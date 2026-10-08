package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestCLIAgentsClient(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		if r.URL.Query().Get("bad") != "" {
			return
		}
		_, _ = w.Write([]byte(`{"scanned_at":"2026-10-08T10:00:00Z","agents":[{"id":"copilot","name":"GitHub Copilot CLI","installed":true,"models":["gpt-5.5"]}]}`))
	}))
	defer srv.Close()

	c := api.New(srv.URL, "tok")
	cat, err := c.ListCLIAgents()
	if err != nil || len(cat.Agents) != 1 || !cat.Agents[0].Installed || cat.ScannedAt.IsZero() {
		t.Fatalf("ListCLIAgents = %+v, %v", cat, err)
	}
	if _, err := c.RescanCLIAgents(); err != nil {
		t.Fatalf("RescanCLIAgents: %v", err)
	}
	if len(seen) != 2 || seen[0] != "GET /cli-agents" || seen[1] != "POST /cli-agents/rescan" {
		t.Errorf("requests = %v", seen)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{oops`))
	}))
	defer bad.Close()
	if _, err := api.New(bad.URL, "").ListCLIAgents(); err == nil {
		t.Error("malformed catalog must error")
	}
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "x", http.StatusServiceUnavailable)
	}))
	defer down.Close()
	if _, err := api.New(down.URL, "").ListCLIAgents(); err == nil {
		t.Error("503 must error")
	}
	if _, err := api.New(down.URL, "").RescanCLIAgents(); err == nil {
		t.Error("503 must error on rescan")
	}
}

func TestCLIAgentStateLabel(t *testing.T) {
	cases := map[string]api.CLIAgent{
		"API key set":     {Kind: "provider", Installed: true},
		"no API key":      {Kind: "provider"},
		"not installed":   {Kind: "agent"},
		"installed":       {Kind: "agent", Installed: true},
		"installed 1.0.0": {Kind: "agent", Installed: true, Version: "1.0.0"},
	}
	for want, a := range cases {
		if got := a.StateLabel(); got != want {
			t.Errorf("%+v → %q, want %q", a, got, want)
		}
	}
}
