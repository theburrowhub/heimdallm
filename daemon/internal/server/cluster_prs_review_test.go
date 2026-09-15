package server_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/server"
	"github.com/heimdallm/daemon/internal/sse"
	"github.com/heimdallm/daemon/internal/store"
)

// These tests cover POST /cluster/prs/review, the endpoint a cluster peer
// dispatches a review to by the PR's stable identity (github_id, repo+number)
// instead of a store row ID — see theburrowhub/heimdallm#799, where a rowid
// minted by the dispatching daemon addressed nothing (or the wrong PR) on the
// receiving one.

// TestHandleClusterTriggerPRReview_ResolvesByGithubID is the core regression
// guard: the local row id resolved from github_id must be used to trigger the
// review, not the id the caller happened to send (there is none here — this
// endpoint never takes one).
func TestHandleClusterTriggerPRReview_ResolvesByGithubID(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	// The local rowid deliberately does not match github_id, the way two
	// independent daemons' autoincrement sequences never agree in practice.
	id, err := s.UpsertPR(&store.PR{
		GithubID: 555, Repo: "acme/tools", Number: 7, Title: "t",
		Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var reviewed int64
	done := make(chan struct{})
	srv.SetTriggerReviewFn(func(prID int64) error {
		reviewed = prID
		close(done)
		return nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review",
		strings.NewReader(`{"github_id":555,"repo":"acme/tools","number":7}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerReviewFn was not called")
	}
	if reviewed != id {
		t.Errorf("triggered review for local id %d, want the resolved row %d", reviewed, id)
	}
}

// TestHandleClusterTriggerPRReview_AdoptsUnknownPR covers the instance that
// has never seen this PR before: it must fetch and store it (the same path
// POST /prs/add uses) before triggering the review, in one request.
func TestHandleClusterTriggerPRReview_AdoptsUnknownPR(t *testing.T) {
	srv, s := setupServer(t)

	var addCalls int
	srv.SetAddPRFn(func(repo string, number int) (*store.PR, error) {
		addCalls++
		if repo != "acme/tools" || number != 9 {
			t.Errorf("addPRFn got %s#%d, want acme/tools#9", repo, number)
		}
		id, err := s.UpsertPR(&store.PR{
			GithubID: 999, Repo: repo, Number: number, Title: "t",
			Author: "a", URL: "u", State: "open", UpdatedAt: time.Now(), FetchedAt: time.Now(),
		})
		if err != nil {
			return nil, err
		}
		return &store.PR{ID: id, Repo: repo, Number: number}, nil
	})

	var reviewed int64
	done := make(chan struct{})
	srv.SetTriggerReviewFn(func(prID int64) error {
		reviewed = prID
		close(done)
		return nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(
		`{"github_id":999,"repo":"acme/tools","number":9,"pr_url":"https://github.com/acme/tools/pull/9"}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202, body=%s", rec.Code, rec.Body.String())
	}
	if addCalls != 1 {
		t.Errorf("addPRFn called %d times, want 1", addCalls)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerReviewFn was not called")
	}
	if reviewed == 0 {
		t.Error("triggerReviewFn called with zero prID")
	}
}

// TestHandleClusterTriggerPRReview_MismatchIs409 is the barrier against
// reviewing the wrong PR: a resolved row whose repo/number does not match
// what was asked for must never trigger a review under that identity.
func TestHandleClusterTriggerPRReview_MismatchIs409(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	if _, err := s.UpsertPR(&store.PR{
		GithubID: 555, Repo: "acme/tools", Number: 7, Title: "t",
		Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called on a mismatch")
		return nil
	})

	rec := httptest.NewRecorder()
	// github_id 555 resolves to acme/tools#7, but the request claims #8.
	req := httptest.NewRequest("POST", "/cluster/prs/review",
		strings.NewReader(`{"github_id":555,"repo":"acme/tools","number":8}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status: got %d want 409, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_UnresolvableIs502 covers a PR this
// instance neither knows nor can adopt (no addPRFn wired, e.g. the GitHub
// client isn't available): the caller must see a failure, not a silent no-op.
func TestHandleClusterTriggerPRReview_UnresolvableIs502(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetTriggerReviewFn(func(int64) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review",
		strings.NewReader(`{"github_id":123,"repo":"acme/tools","number":1}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_RejectsMissingIdentity guards the input
// contract: at least github_id, or repo+number, is required.
func TestHandleClusterTriggerPRReview_RejectsMissingIdentity(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetTriggerReviewFn(func(int64) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(`{}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_RejectsMalformedJSON covers the decode
// failure, distinct from a well-formed body that merely lacks an identity.
func TestHandleClusterTriggerPRReview_RejectsMalformedJSON(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetTriggerReviewFn(func(int64) error { return nil })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(`{"github_id":`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d want 400, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_NoTriggerFnIs503 mirrors the same guard
// POST /prs/{id}/review and POST /prs/add already have.
func TestHandleClusterTriggerPRReview_NoTriggerFnIs503(t *testing.T) {
	srv, _ := setupServer(t)
	// triggerReviewFn deliberately left unset.

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review",
		strings.NewReader(`{"github_id":1,"repo":"acme/tools","number":1}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status: got %d want 503, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_ResolvesByRepoNumberWhenGithubIDMissing
// covers the repo+number lookup succeeding on its own — the shape the GUI's
// "run this here" dispatch sends today, which carries no github_id at all.
func TestHandleClusterTriggerPRReview_ResolvesByRepoNumberWhenGithubIDMissing(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	id, err := s.UpsertPR(&store.PR{
		GithubID: 42, Repo: "acme/tools", Number: 3, Title: "t",
		Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}

	var reviewed int64
	done := make(chan struct{})
	srv.SetTriggerReviewFn(func(prID int64) error {
		reviewed = prID
		close(done)
		return nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review",
		strings.NewReader(`{"repo":"acme/tools","number":3}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerReviewFn was not called")
	}
	if reviewed != id {
		t.Errorf("triggered review for %d, want the resolved row %d", reviewed, id)
	}
}

// TestHandleClusterTriggerPRReview_GithubIDOnlyMissIs502 covers the case
// where github_id resolves to nothing and there is no repo+number to adopt
// the PR by — the request simply cannot be satisfied.
func TestHandleClusterTriggerPRReview_GithubIDOnlyMissIs502(t *testing.T) {
	srv, _ := setupServer(t)
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called")
		return nil
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(`{"github_id":777}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_StoreErrorOnGithubIDLookupIs502 and
// TestHandleClusterTriggerPRReview_StoreErrorOnRepoNumberLookupIs502 cover
// resolveOrAdoptPR's two non-ErrPRNotFound branches: a store failure other
// than "no such row" must be reported, not silently treated as "not found
// yet, try adopting it."
func TestHandleClusterTriggerPRReview_StoreErrorOnGithubIDLookupIs502(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{})
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called")
		return nil
	})
	s.Close() // github_id-only request never reaches the repo/number branch

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(`{"github_id":1}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502, body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleClusterTriggerPRReview_StoreErrorOnRepoNumberLookupIs502(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{})
	srv.SetTriggerReviewFn(func(int64) error {
		t.Fatal("triggerReviewFn must not be called")
		return nil
	})
	s.Close() // no github_id in the request, so only the repo/number branch runs

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review",
		strings.NewReader(`{"repo":"acme/tools","number":1}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status: got %d want 502, body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleClusterTriggerPRReview_LogsAsyncTriggerFailure exercises the
// goroutine's own error path: the HTTP response is already 202 by the time
// triggerReviewFn runs, so a failure there can only be logged, not surfaced
// to the caller — this pins that it does not panic or hang.
func TestHandleClusterTriggerPRReview_LogsAsyncTriggerFailure(t *testing.T) {
	srv, s := setupServer(t)
	now := time.Now()
	if _, err := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "acme/tools", Number: 1, Title: "t",
		Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	done := make(chan struct{})
	srv.SetTriggerReviewFn(func(int64) error {
		close(done)
		return errors.New("boom")
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(`{"github_id":1}`))
	srv.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status: got %d want 202, body=%s", rec.Code, rec.Body.String())
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("triggerReviewFn was not called")
	}
}

// TestHandleClusterTriggerPRReview_SemaphoreFullIs429 confirms the new
// endpoint respects the same shared review concurrency limit as
// POST /prs/{id}/review and POST /prs/add.
func TestHandleClusterTriggerPRReview_SemaphoreFullIs429(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	broker := sse.NewBroker()
	broker.Start()
	defer broker.Stop()
	srv := server.NewWithOptions(s, broker, nil, "", server.Options{MaxConcurrentReviews: 1})

	now := time.Now()
	if _, err := s.UpsertPR(&store.PR{
		GithubID: 1, Repo: "acme/tools", Number: 1, Title: "t",
		Author: "a", URL: "u", State: "open", UpdatedAt: now, FetchedAt: now,
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	gate := make(chan struct{})
	srv.SetTriggerReviewFn(func(int64) error {
		<-gate
		return nil
	})

	body := `{"github_id":1,"repo":"acme/tools","number":1}`
	rec1 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec1, httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(body)))
	if rec1.Code != http.StatusAccepted {
		t.Fatalf("first dispatch = %d: %s", rec1.Code, rec1.Body.String())
	}
	time.Sleep(10 * time.Millisecond)

	rec2 := httptest.NewRecorder()
	srv.Router().ServeHTTP(rec2, httptest.NewRequest("POST", "/cluster/prs/review", strings.NewReader(body)))
	if rec2.Code != http.StatusTooManyRequests {
		t.Errorf("second dispatch = %d, want 429", rec2.Code)
	}
	close(gate)
}
