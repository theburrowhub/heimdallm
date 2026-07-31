package github_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

// ── GraphQL test helpers ──────────────────────────────────────────────────────

// gqlIssueNode builds a GraphQL Issue node matching the search query fields.
// author is the login of the issue author (maps to Issue.User).
func gqlIssueNode(id int64, number int, repo, title, state string, labels, assignees []string) map[string]any {
	return gqlIssueNodeWithAuthor(id, number, repo, title, state, "", labels, assignees)
}

// gqlIssueNodeWithAuthor is like gqlIssueNode but also sets the author login.
func gqlIssueNodeWithAuthor(id int64, number int, repo, title, state, author string, labels, assignees []string) map[string]any {
	labelNodes := make([]map[string]string, len(labels))
	for i, l := range labels {
		labelNodes[i] = map[string]string{"name": l}
	}
	assigneeNodes := make([]map[string]string, len(assignees))
	for i, a := range assignees {
		assigneeNodes[i] = map[string]string{"login": a}
	}
	return map[string]any{
		"databaseId": id,
		"number":     number,
		"title":      title,
		"body":       "issue body",
		"state":      state,
		"url":        fmt.Sprintf("https://github.com/%s/issues/%d", repo, number),
		"createdAt":  time.Now().UTC().Format(time.RFC3339),
		"updatedAt":  time.Now().UTC().Format(time.RFC3339),
		"author":     map[string]string{"login": author},
		"repository": map[string]string{"nameWithOwner": repo},
		"assignees":  map[string]any{"nodes": assigneeNodes},
		"labels":     map[string]any{"nodes": labelNodes},
	}
}

// gqlSearchEnvelope builds the full GraphQL response envelope for a search result.
// hasNextPage and endCursor control pagination metadata.
func gqlSearchEnvelope(nodes []map[string]any, hasNextPage bool, endCursor string) []byte {
	pageInfo := map[string]any{
		"hasNextPage": hasNextPage,
		"endCursor":   endCursor,
	}
	data := map[string]any{
		"search": map[string]any{
			"pageInfo": pageInfo,
			"nodes":    nodes,
		},
	}
	b, _ := json.Marshal(map[string]any{"data": data})
	return b
}

// gqlErrorEnvelope builds a GraphQL response with top-level errors.
func gqlErrorEnvelope(messages ...string) []byte {
	errs := make([]map[string]string, len(messages))
	for i, m := range messages {
		errs[i] = map[string]string{"message": m}
	}
	b, _ := json.Marshal(map[string]any{"errors": errs})
	return b
}

// ── SearchIssuesGraphQL tests ─────────────────────────────────────────────────

func TestSearchIssuesGraphQL_ParsesPayloadAndMapsFields(t *testing.T) {
	node := gqlIssueNodeWithAuthor(42, 7, "org/repo", "GraphQL issue", "OPEN", "bob", []string{"bug"}, []string{"alice"})
	body := gqlSearchEnvelope([]map[string]any{node}, false, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		if r.Method != "POST" {
			http.Error(w, "expected POST", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake-token", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssuesGraphQL("is:issue is:open assignee:alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue, got %d", len(issues))
	}
	got := issues[0]
	if got.ID != 42 {
		t.Errorf("ID: want 42, got %d", got.ID)
	}
	if got.Number != 7 {
		t.Errorf("Number: want 7, got %d", got.Number)
	}
	if got.Title != "GraphQL issue" {
		t.Errorf("Title: want %q, got %q", "GraphQL issue", got.Title)
	}
	if got.Repo != "org/repo" {
		t.Errorf("Repo: want %q, got %q", "org/repo", got.Repo)
	}
	if got.State != "open" {
		t.Errorf("State: want %q, got %q", "open", got.State)
	}
	if got.User.Login != "bob" {
		t.Errorf("User.Login: want %q, got %q", "bob", got.User.Login)
	}
	if got.HTMLURL != "https://github.com/org/repo/issues/7" {
		t.Errorf("HTMLURL: want %q, got %q", "https://github.com/org/repo/issues/7", got.HTMLURL)
	}
	if len(got.Labels) != 1 || got.Labels[0].Name != "bug" {
		t.Errorf("Labels: want [{bug}], got %v", got.Labels)
	}
	if len(got.Assignees) != 1 || got.Assignees[0].Login != "alice" {
		t.Errorf("Assignees: want [{alice}], got %v", got.Assignees)
	}
	if got.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
	if got.UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

func TestSearchIssuesGraphQL_PaginatesTwoPages(t *testing.T) {
	// Page 1: 100 nodes, hasNextPage=true, endCursor="cursor1"
	page1Nodes := make([]map[string]any, 100)
	for i := range page1Nodes {
		page1Nodes[i] = gqlIssueNode(int64(i+1), i+1, "org/repo", fmt.Sprintf("issue %d", i+1), "OPEN", nil, nil)
	}
	page1Body := gqlSearchEnvelope(page1Nodes, true, "cursor1")

	// Page 2: 3 nodes, hasNextPage=false
	page2Nodes := []map[string]any{
		gqlIssueNode(101, 101, "org/repo", "issue 101", "OPEN", nil, nil),
		gqlIssueNode(102, 102, "org/repo", "issue 102", "OPEN", nil, nil),
		gqlIssueNode(103, 103, "org/repo", "issue 103", "OPEN", nil, nil),
	}
	page2Body := gqlSearchEnvelope(page2Nodes, false, "")

	pageCount := 0
	var receivedCursors []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		pageCount++
		// Extract the cursor variable from the request body.
		var reqBody struct {
			Variables map[string]any `json:"variables"`
		}
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		if c, ok := reqBody.Variables["cursor"]; ok && c != nil {
			receivedCursors = append(receivedCursors, fmt.Sprintf("%v", c))
		}
		w.Header().Set("Content-Type", "application/json")
		switch pageCount {
		case 1:
			w.Write(page1Body)
		case 2:
			w.Write(page2Body)
		default:
			w.Write(gqlSearchEnvelope(nil, false, ""))
		}
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssuesGraphQL("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 103 {
		t.Errorf("expected 103 issues, got %d", len(issues))
	}
	if pageCount != 2 {
		t.Errorf("expected 2 page requests, got %d", pageCount)
	}
	// The second request should carry cursor="cursor1".
	if len(receivedCursors) != 1 || receivedCursors[0] != "cursor1" {
		t.Errorf("expected cursor1 on page 2, got %v", receivedCursors)
	}
}

func TestSearchIssuesGraphQL_StopsAtPageCap(t *testing.T) {
	// Always return 100 nodes and hasNextPage=true so the paginator would loop
	// forever without the cap.
	pageCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		pageCount++
		nodes := make([]map[string]any, 100)
		for i := range nodes {
			id := int64(pageCount*100 + i + 1)
			nodes[i] = gqlIssueNode(id, int(id), "org/repo", "issue", "OPEN", nil, nil)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(gqlSearchEnvelope(nodes, true, fmt.Sprintf("cursor%d", pageCount)))
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssuesGraphQL("is:issue is:open")
	// Same contract as the REST path: report the cap alongside the partial
	// results so callers can distinguish "all matches" from "first 1000".
	if !errors.Is(err, gh.ErrSearchTruncated) {
		t.Fatalf("cap should surface ErrSearchTruncated, got: %v", err)
	}
	// Should stop at maxSearchIssuePages (10) × 100 = 1000 issues.
	if len(issues) != 1000 {
		t.Errorf("expected 1000 issues at cap, got %d", len(issues))
	}
	if pageCount != 10 {
		t.Errorf("expected exactly 10 page requests (cap), got %d", pageCount)
	}
}

func TestSearchIssuesGraphQL_ErrorsEnvelopeReturnsError(t *testing.T) {
	body := gqlErrorEnvelope("NOT_FOUND: resource not found", "FORBIDDEN: insufficient scope")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := client.SearchIssuesGraphQL("is:issue is:open")
	if err == nil {
		t.Fatal("expected error from GraphQL errors envelope, got nil")
	}
	if !strings.Contains(err.Error(), "NOT_FOUND") {
		t.Errorf("error should contain first error message, got: %v", err)
	}
	if !strings.Contains(err.Error(), "FORBIDDEN") {
		t.Errorf("error should contain second error message, got: %v", err)
	}
}

func TestSearchIssuesGraphQL_DropsNonIssueNodes(t *testing.T) {
	// A non-Issue node (e.g. a PR) has databaseId=0 in the GraphQL union type
	// when the ... on Issue fragment doesn't match.
	issueNode := gqlIssueNode(10, 1, "org/repo", "real issue", "OPEN", nil, nil)
	// PR node: zero databaseId (fragment doesn't apply).
	prNode := map[string]any{
		"databaseId": 0,
	}
	body := gqlSearchEnvelope([]map[string]any{issueNode, prNode}, false, "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	issues, err := client.SearchIssuesGraphQL("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue (PR node dropped), got %d", len(issues))
	}
	if issues[0].ID != 10 {
		t.Errorf("wrong issue kept: ID=%d", issues[0].ID)
	}
}

// ── graphQL() low-level tests ─────────────────────────────────────────────────

func TestGraphQLLowLevel_HTTPErrorReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"bad credentials"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	client := gh.NewClient("bad-token", gh.WithBaseURL(srv.URL))
	// Call SearchIssuesGraphQL which internally calls graphQL().
	_, err := client.SearchIssuesGraphQL("is:issue")
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should contain status 401, got: %v", err)
	}
}

func TestGraphQLLowLevel_RateObserverNotified(t *testing.T) {
	// Assert that the rate observer is called on GraphQL responses.
	body := gqlSearchEnvelope(nil, false, "")

	observedCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4500")
		w.Header().Set("X-RateLimit-Resource", "graphql")
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetRateObserver(gh.RateLimitObserverFunc(func(resp *http.Response) {
		observedCount++
	}))

	_, err := client.SearchIssuesGraphQL("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if observedCount != 1 {
		t.Errorf("rate observer should have been called once, got %d", observedCount)
	}
}

// ── Dispatch / fallback tests ─────────────────────────────────────────────────

// TestSearchIssues_GraphQLDisabledNeverCallsGraphQLEndpoint asserts that when
// useGraphQL is false (the default), /graphql is never hit.
func TestSearchIssues_GraphQLDisabledNeverCallsGraphQLEndpoint(t *testing.T) {
	restItem := searchIssueItem(1, 42, "org/repo", "rest issue", []string{}, []string{}, false)

	graphqlHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/graphql") {
			graphqlHit = true
			http.Error(w, "should not be called", http.StatusInternalServerError)
			return
		}
		// REST search path.
		w.Header().Set("Content-Type", "application/json")
		w.Write(searchEnvelope(1, []map[string]any{restItem}))
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	// GraphQL is off by default — do not call SetGraphQLEnabled.
	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("expected 1 issue from REST, got %d", len(issues))
	}
	if graphqlHit {
		t.Error("/graphql should never be called when GraphQL is disabled")
	}
}

// TestSearchIssues_GraphQLEnabledUsesGraphQLPath asserts that when GraphQL is
// enabled, SearchIssues calls the GraphQL endpoint (not REST search).
func TestSearchIssues_GraphQLEnabledUsesGraphQLPath(t *testing.T) {
	gqlNode := gqlIssueNode(77, 3, "org/repo", "graphql issue", "OPEN", nil, nil)
	gqlBody := gqlSearchEnvelope([]map[string]any{gqlNode}, false, "")

	restHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/graphql") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(gqlBody)
			return
		}
		if strings.Contains(r.URL.Path, "/search/issues") {
			restHit = true
			http.Error(w, "should not call REST", http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetGraphQLEnabled(true)

	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue from GraphQL, got %d", len(issues))
	}
	if issues[0].ID != 77 {
		t.Errorf("Issue ID: want 77, got %d", issues[0].ID)
	}
	if restHit {
		t.Error("REST /search/issues should not be called when GraphQL succeeds")
	}
}

// TestSearchIssues_GraphQLEnabledFallsBackOnError asserts that when GraphQL
// errors, SearchIssues falls back to the REST search path transparently.
func TestSearchIssues_GraphQLEnabledFallsBackOnError(t *testing.T) {
	restItem := searchIssueItem(99, 55, "org/repo", "fallback rest issue", []string{}, []string{}, false)

	fallbackHit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/graphql") {
			// Return a GraphQL errors envelope to trigger fallback.
			w.Header().Set("Content-Type", "application/json")
			w.Write(gqlErrorEnvelope("internal server error"))
			return
		}
		if strings.Contains(r.URL.Path, "/search/issues") {
			fallbackHit = true
			w.Header().Set("Content-Type", "application/json")
			w.Write(searchEnvelope(1, []map[string]any{restItem}))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetGraphQLEnabled(true)

	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("fallback should succeed, got error: %v", err)
	}
	if !fallbackHit {
		t.Error("REST fallback path should have been called when GraphQL errors")
	}
	if len(issues) != 1 {
		t.Fatalf("expected 1 issue from REST fallback, got %d", len(issues))
	}
	if issues[0].Number != 55 {
		t.Errorf("Issue.Number: want 55, got %d", issues[0].Number)
	}
}

// TestSearchIssues_GraphQLHTTPErrorFallsBackToREST asserts that an HTTP-level
// error (non-200 from /graphql) also triggers the REST fallback.
func TestSearchIssues_GraphQLHTTPErrorFallsBackToREST(t *testing.T) {
	restItem := searchIssueItem(1, 1, "org/repo", "rest fallback", []string{}, []string{}, false)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/graphql") {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		if strings.Contains(r.URL.Path, "/search/issues") {
			w.Header().Set("Content-Type", "application/json")
			w.Write(searchEnvelope(1, []map[string]any{restItem}))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetGraphQLEnabled(true)

	issues, err := client.SearchIssues("is:issue is:open")
	if err != nil {
		t.Fatalf("expected fallback to succeed, got: %v", err)
	}
	if len(issues) != 1 {
		t.Errorf("expected 1 issue from REST fallback, got %d", len(issues))
	}
}
