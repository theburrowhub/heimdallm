package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/store"
)

func TestPRDismissAndUndismissLifecycle(t *testing.T) {
	srv, st := setupServer(t)
	now := time.Now().UTC()
	id, err := st.UpsertPR(&store.PR{
		GithubID:  7001,
		Repo:      "acme/widgets",
		Number:    17,
		Title:     "Preserve operator dismissal",
		Author:    "alice",
		URL:       "https://example.test/acme/widgets/pull/17",
		State:     "open",
		UpdatedAt: now,
		FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert PR: %v", err)
	}

	for _, path := range []string{"/prs/not-an-id/dismiss", "/prs/not-an-id/undismiss"} {
		recorder := httptest.NewRecorder()
		srv.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, nil))
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("POST %s status = %d, want 400", path, recorder.Code)
		}
	}

	dismissPath := "/prs/" + itoa(id) + "/dismiss"
	recorder := httptest.NewRecorder()
	srv.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, dismissPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("dismiss status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if pr, err := st.GetPR(id); err != nil || !pr.Dismissed {
		t.Fatalf("dismissed PR = (%+v, %v), want dismissed", pr, err)
	}
	if visible, err := st.ListPRs(); err != nil || len(visible) != 0 {
		t.Fatalf("visible PRs after dismiss = (%+v, %v), want none", visible, err)
	}

	undismissPath := "/prs/" + itoa(id) + "/undismiss"
	recorder = httptest.NewRecorder()
	srv.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, undismissPath, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("undismiss status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if pr, err := st.GetPR(id); err != nil || pr.Dismissed {
		t.Fatalf("undismissed PR = (%+v, %v), want visible", pr, err)
	}
	if visible, err := st.ListPRs(); err != nil || len(visible) != 1 || visible[0].ID != id {
		t.Fatalf("visible PRs after undismiss = (%+v, %v), want PR %d", visible, err, id)
	}
}

func TestAgentListAndDeleteLifecycle(t *testing.T) {
	srv, st := setupServer(t)

	listAgents := func() []*store.Agent {
		t.Helper()
		recorder := httptest.NewRecorder()
		srv.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agents", nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET /agents status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var agents []*store.Agent
		if err := json.NewDecoder(recorder.Body).Decode(&agents); err != nil {
			t.Fatalf("decode agents: %v", err)
		}
		return agents
	}

	if agents := listAgents(); len(agents) != 0 {
		t.Fatalf("initial agents = %+v, want []", agents)
	}
	if err := st.UpsertAgent(&store.Agent{ID: "review-safe", Name: "Review safe", CLI: "codex"}); err != nil {
		t.Fatalf("upsert agent: %v", err)
	}
	if agents := listAgents(); len(agents) != 1 || agents[0].ID != "review-safe" {
		t.Fatalf("agents after insert = %+v, want review-safe", agents)
	}

	recorder := httptest.NewRecorder()
	srv.Router().ServeHTTP(
		recorder,
		httptest.NewRequest(http.MethodDelete, "/agents/review-safe", nil),
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("DELETE /agents/review-safe status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if agents := listAgents(); len(agents) != 0 {
		t.Fatalf("agents after delete = %+v, want []", agents)
	}
}
