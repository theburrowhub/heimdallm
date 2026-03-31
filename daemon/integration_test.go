//go:build integration

package main_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/heimdallr/daemon/internal/executor"
	gh "github.com/heimdallr/daemon/internal/github"
	"github.com/heimdallr/daemon/internal/pipeline"
	"github.com/heimdallr/daemon/internal/server"
	"github.com/heimdallr/daemon/internal/sse"
	"github.com/heimdallr/daemon/internal/store"
)

func fakeGHServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/search/issues" {
			prs := struct {
				Items []*gh.PullRequest `json:"items"`
			}{Items: []*gh.PullRequest{
				{ID: 999, Number: 7, Title: "Add feature", HTMLURL: "https://github.com/org/repo/pull/7",
					User: gh.User{Login: "bob"}, State: "open", UpdatedAt: time.Now(),
					Head: gh.Branch{Repo: gh.Repo{FullName: "org/repo"}}},
			}}
			json.NewEncoder(w).Encode(prs)
		} else {
			w.Write([]byte("+new feature line\n"))
		}
	}))
}

type noopNotifier struct{}

func (n *noopNotifier) Notify(title, message string) {}

func TestIntegration_FullPipeline(t *testing.T) {
	ghSrv := fakeGHServer(t)
	defer ghSrv.Close()

	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()

	ghClient := gh.NewClient("fake-token", gh.WithBaseURL(ghSrv.URL))
	exec := executor.New()

	if _, err := exec.Detect("claude", "gemini"); err != nil {
		t.Skip("no AI CLI available, skipping integration test")
	}

	p := pipeline.New(s, ghClient, exec, &noopNotifier{})
	srv := server.New(s, broker, p)

	prs, err := ghClient.FetchPRs([]string{"org/repo"})
	if err != nil {
		t.Fatalf("fetch prs: %v", err)
	}
	if len(prs) == 0 {
		t.Fatal("expected at least 1 PR from fake server")
	}

	pr := prs[0]
	pr.Repo = pr.Head.Repo.FullName
	rev, err := p.Run(pr, "claude", "gemini")
	if err != nil {
		t.Fatalf("pipeline run: %v", err)
	}
	if rev.Summary == "" {
		t.Error("expected non-empty summary")
	}

	testSrv := httptest.NewServer(srv.Router())
	defer testSrv.Close()

	resp, err := http.Get(testSrv.URL + "/prs")
	if err != nil {
		t.Fatalf("GET /prs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /prs: status %d", resp.StatusCode)
	}
}
