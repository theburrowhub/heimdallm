# Collaborators Pagination Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `daemon/internal/github.Client.FetchCollaborators` return the full collaborator list by paginating through GitHub's `Link: rel="next"` header instead of stopping after one page of 100.

**Architecture:** Two-piece change in a single file. (1) A new private helper `parseNextLink` that extracts the `rel="next"` URL from a GitHub `Link` header; (2) `FetchCollaborators` becomes a loop that keeps fetching until the `Link` header has no `rel="next"`, capped at 100 pages so a malformed header from GitHub can never hang the daemon. Tests are black-box (matching the package's existing `github_test` convention) and exercise pagination behavior through the public function with an `httptest` server.

**Tech Stack:** Go (1.x, whatever the repo currently uses), standard library only (`net/http`, `net/http/httptest`, `net/url`, `strings`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-05-08-collaborators-pagination-design.md`

---

## File Structure

- **Modify:** `daemon/internal/github/client.go`
  - Replace the body of `FetchCollaborators` (currently lines 947-971) with a paginating loop.
  - Add a new unexported helper `parseNextLink(string) string` adjacent to `FetchCollaborators`.
- **Create:** `daemon/internal/github/collaborators_test.go`
  - New black-box test file (`package github_test`), matching the convention used by `repos_test.go`, `fetch_issues_test.go`, etc.
  - Holds all new tests for `FetchCollaborators` pagination behavior. Kept separate from `repos_test.go` so the diff is reviewable in isolation.

## Implementation Notes

- `c.do(method, path, accept)` prepends `c.baseURL` to `path`, so `path` must be a relative URI (path + query). GitHub's `Link` header gives an absolute URL like `https://api.github.com/repositories/123/collaborators?per_page=100&page=2`. We extract its path + query with `net/url.Parse(...).RequestURI()` and pass that to `c.do`. This works against both real GitHub and `httptest` servers configured with `WithBaseURL`, because the test server's `Link` header points back to its own URL, whose `RequestURI()` is what `c.baseURL+path` reconstructs.
- The current implementation reads the body via `io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))`. We keep that per-page; no need to grow `maxBodyBytes` since each page is at most ~100 collaborators.
- Each page's response body must be closed before moving on, even when status != 200.
- We do NOT introduce white-box tests (no `package github` test file) — the package is uniformly black-box today, and `parseNextLink` is fully covered by the integration tests through `FetchCollaborators`.

---

## Task 1: Failing test for multi-page traversal

**Why first:** This is the core behavior the bug requires. Writing it before the implementation keeps us honest — the implementation must satisfy this test, not the other way around.

**Files:**
- Create: `daemon/internal/github/collaborators_test.go`

- [ ] **Step 1: Write the failing test**

Create `daemon/internal/github/collaborators_test.go` with this exact content:

```go
package github_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
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
```

Tasks 3-6 will append more tests and incrementally introduce additional imports (`sync/atomic`, `strings`). For now this minimal import set is sufficient.

- [ ] **Step 2: Run the test to verify it fails**

Run from the repo root:
```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -run TestFetchCollaborators_MultiPage -v
```

Expected: **FAIL** with a logins mismatch — `got=[alice bob]`, `want=[alice bob carol dave eve]`. This is because `FetchCollaborators` today only fetches page 1 and ignores the `Link` header.

If the test fails for a different reason (compile error, etc.), stop and fix that before continuing.

- [ ] **Step 3: Commit the failing test**

```bash
cd .worktrees/collaborators-pagination
git add daemon/internal/github/collaborators_test.go
git commit -m "test: add failing multi-page test for FetchCollaborators"
```

---

## Task 2: Implement pagination

**Files:**
- Modify: `daemon/internal/github/client.go` (replace body of `FetchCollaborators`, currently lines 946-971; add new helper `parseNextLink` adjacent to it).

- [ ] **Step 1: Add the `parseNextLink` helper**

In `daemon/internal/github/client.go`, immediately above the existing `FetchCollaborators` function (around line 946), insert:

```go
// parseNextLink extracts the URL whose rel parameter is "next" from a GitHub
// Link header. Returns "" when no such URL exists. The header format is:
//
//	<https://api.github.com/...&page=2>; rel="next", <...>; rel="last"
//
// We do not pull in a parser dependency; a small string scan is sufficient.
func parseNextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		segs := strings.Split(strings.TrimSpace(part), ";")
		if len(segs) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		linkURL := urlPart[1 : len(urlPart)-1]
		for _, s := range segs[1:] {
			s = strings.TrimSpace(s)
			if s == `rel="next"` || s == "rel=next" {
				return linkURL
			}
		}
	}
	return ""
}
```

- [ ] **Step 2: Replace the body of `FetchCollaborators`**

Replace the existing function body (lines 947-971 in `client.go`):

```go
// FetchCollaborators returns the login names of repository collaborators.
func (c *Client) FetchCollaborators(repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	resp, err := c.do("GET", fmt.Sprintf("/repos/%s/collaborators?per_page=100", repo), "application/vnd.github+json")
	if err != nil {
		return nil, fmt.Errorf("github: fetch collaborators: %w", err)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github: fetch collaborators %s: status %d", repo, resp.StatusCode)
	}
	var raw []struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("github: decode collaborators: %w", err)
	}
	logins := make([]string, len(raw))
	for i, u := range raw {
		logins[i] = u.Login
	}
	return logins, nil
}
```

with the paginating version:

```go
// FetchCollaborators returns the login names of repository collaborators,
// following GitHub's Link: rel="next" header to walk every page.
func (c *Client) FetchCollaborators(repo string) ([]string, error) {
	if repo == "" {
		return nil, nil
	}
	const maxPages = 100
	path := fmt.Sprintf("/repos/%s/collaborators?per_page=100", repo)
	var logins []string
	for page := 0; page < maxPages; page++ {
		resp, err := c.do("GET", path, "application/vnd.github+json")
		if err != nil {
			return nil, fmt.Errorf("github: fetch collaborators: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
		linkHeader := resp.Header.Get("Link")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("github: fetch collaborators %s: status %d", repo, resp.StatusCode)
		}
		var raw []struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			return nil, fmt.Errorf("github: decode collaborators: %w", err)
		}
		for _, u := range raw {
			logins = append(logins, u.Login)
		}
		next := parseNextLink(linkHeader)
		if next == "" {
			return logins, nil
		}
		nextURL, err := url.Parse(next)
		if err != nil {
			return nil, fmt.Errorf("github: parse next link %q: %w", next, err)
		}
		path = nextURL.RequestURI()
	}
	return nil, fmt.Errorf("github: fetch collaborators %s: pagination exceeded %d pages", repo, maxPages)
}
```

Note: `url` is already imported (`net/url`, line 10) and `strings` is already imported (line 14). No new imports needed.

- [ ] **Step 3: Run the multi-page test to verify it passes**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -run TestFetchCollaborators_MultiPage -v
```

Expected: **PASS**.

- [ ] **Step 4: Run the full package's tests to confirm no regressions**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -v
```

Expected: all tests in the package PASS, including the new one.

- [ ] **Step 5: Commit**

```bash
cd .worktrees/collaborators-pagination
git add daemon/internal/github/client.go
git commit -m "fix(github): paginate FetchCollaborators via Link header"
```

---

## Task 3: Test single-page behavior (no `Link` header)

**Files:**
- Modify: `daemon/internal/github/collaborators_test.go`

- [ ] **Step 1: Append the test**

Append to `collaborators_test.go`:

```go
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
```

This requires `sync/atomic` in the imports. Update the import block in `collaborators_test.go`:

```go
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
```

- [ ] **Step 2: Run the test**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -run TestFetchCollaborators_SinglePage -v
```

Expected: **PASS**.

- [ ] **Step 3: Commit**

```bash
cd .worktrees/collaborators-pagination
git add daemon/internal/github/collaborators_test.go
git commit -m "test: cover single-page FetchCollaborators path"
```

---

## Task 4: Test runaway pagination cap

**Files:**
- Modify: `daemon/internal/github/collaborators_test.go`

- [ ] **Step 1: Append the test**

```go
// TestFetchCollaborators_RunawayCap verifies that an infinite Link: rel="next"
// chain returns an error mentioning "pagination" rather than hanging.
func TestFetchCollaborators_RunawayCap(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Link",
			fmt.Sprintf(`<%s/repos/org/repo/collaborators?per_page=100&page=2>; rel="next"`, srv.URL))
		_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "alice"}})
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	_, err := client.FetchCollaborators("org/repo")
	if err == nil {
		t.Fatal("expected error from runaway pagination, got nil")
	}
	if !strings.Contains(err.Error(), "pagination") {
		t.Errorf("expected error mentioning 'pagination', got: %v", err)
	}
}
```

This requires `strings` in the imports. Update the import block:

```go
import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	gh "github.com/heimdallm/daemon/internal/github"
)
```

- [ ] **Step 2: Run the test (expect it to complete in seconds, not hang)**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -run TestFetchCollaborators_RunawayCap -timeout 30s -v
```

Expected: **PASS**, completing well under the 30-second timeout. (The cap is 100 pages; against an in-process httptest server this finishes in tens of milliseconds.)

- [ ] **Step 3: Commit**

```bash
cd .worktrees/collaborators-pagination
git add daemon/internal/github/collaborators_test.go
git commit -m "test: cover FetchCollaborators runaway pagination cap"
```

---

## Task 5: Test mid-pagination HTTP error

**Files:**
- Modify: `daemon/internal/github/collaborators_test.go`

- [ ] **Step 1: Append the test**

```go
// TestFetchCollaborators_MidPaginationError verifies that an HTTP 500 on a
// later page causes FetchCollaborators to return an error and not a partial
// (silently truncated) slice.
func TestFetchCollaborators_MidPaginationError(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := 1
		if p := r.URL.Query().Get("page"); p != "" {
			fmt.Sscanf(p, "%d", &page)
		}
		if page == 1 {
			w.Header().Set("Link",
				fmt.Sprintf(`<%s/repos/org/repo/collaborators?per_page=100&page=2>; rel="next"`, srv.URL))
			_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "alice"}})
			return
		}
		http.Error(w, `{"message":"boom"}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	got, err := client.FetchCollaborators("org/repo")
	if err == nil {
		t.Fatalf("expected error from mid-pagination 500, got nil and logins=%v", got)
	}
	if got != nil {
		t.Errorf("expected nil logins on error, got %v", got)
	}
}
```

- [ ] **Step 2: Run the test**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -run TestFetchCollaborators_MidPaginationError -v
```

Expected: **PASS**.

- [ ] **Step 3: Commit**

```bash
cd .worktrees/collaborators-pagination
git add daemon/internal/github/collaborators_test.go
git commit -m "test: cover FetchCollaborators mid-pagination error"
```

---

## Task 6: Test `Link` header parsing variants (through public function)

This task replaces the spec's planned standalone `parseNextLink` unit tests with black-box coverage through `FetchCollaborators`, matching the package's testing convention (no white-box test files exist anywhere in `daemon/internal/github/`).

**Files:**
- Modify: `daemon/internal/github/collaborators_test.go`

- [ ] **Step 1: Append the test**

```go
// TestFetchCollaborators_LinkHeaderVariants exercises parseNextLink through
// FetchCollaborators by stubbing several Link header shapes and verifying
// pagination terminates correctly when "next" is absent.
func TestFetchCollaborators_LinkHeaderVariants(t *testing.T) {
	cases := []struct {
		name       string
		linkHeader string
		wantPages  int // how many pages we expect FetchCollaborators to fetch
	}{
		{
			name:       "only_last_no_next",
			linkHeader: `<https://example/api?page=5>; rel="last"`,
			wantPages:  1,
		},
		{
			name:       "empty_header",
			linkHeader: ``,
			wantPages:  1,
		},
		{
			name:       "malformed_no_brackets",
			linkHeader: `https://example/api?page=2; rel="next"`,
			wantPages:  1,
		},
		{
			name:       "malformed_no_rel",
			linkHeader: `<https://example/api?page=2>`,
			wantPages:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				atomic.AddInt32(&calls, 1)
				if tc.linkHeader != "" {
					w.Header().Set("Link", tc.linkHeader)
				}
				_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "alice"}})
			}))
			defer srv.Close()

			client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
			if _, err := client.FetchCollaborators("org/repo"); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := atomic.LoadInt32(&calls); int(got) != tc.wantPages {
				t.Errorf("expected %d HTTP calls, got %d", tc.wantPages, got)
			}
		})
	}

	// Sanity case: a Link header containing both "next" and "last" must be
	// followed for the "next" leg.
	t.Run("next_and_last", func(t *testing.T) {
		var srv *httptest.Server
		var calls int32
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&calls, 1)
			page := 1
			if p := r.URL.Query().Get("page"); p != "" {
				fmt.Sscanf(p, "%d", &page)
			}
			if page == 1 {
				link := fmt.Sprintf(
					`<%s/repos/org/repo/collaborators?per_page=100&page=2>; rel="next", `+
						`<%s/repos/org/repo/collaborators?per_page=100&page=2>; rel="last"`,
					srv.URL, srv.URL)
				w.Header().Set("Link", link)
				_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "alice"}})
				return
			}
			_ = json.NewEncoder(w).Encode([]map[string]string{{"login": "bob"}})
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
		if c := atomic.LoadInt32(&calls); c != 2 {
			t.Errorf("expected 2 HTTP calls, got %d", c)
		}
	})
}
```

- [ ] **Step 2: Run the test**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/internal/github/ -run TestFetchCollaborators_LinkHeaderVariants -v
```

Expected: **PASS** for all subtests (`only_last_no_next`, `empty_header`, `malformed_no_brackets`, `malformed_no_rel`, `next_and_last`).

- [ ] **Step 3: Commit**

```bash
cd .worktrees/collaborators-pagination
git add daemon/internal/github/collaborators_test.go
git commit -m "test: cover Link header parsing variants in FetchCollaborators"
```

---

## Task 7: Final verification

**Files:** none (verification only).

- [ ] **Step 1: Run the full daemon test suite**

```bash
cd .worktrees/collaborators-pagination && go test ./daemon/...
```

Expected: all tests PASS. If anything outside `daemon/internal/github/` fails, investigate — `FetchCollaborators` is called by handlers (`daemon/internal/server/handlers.go`), so a regression would most likely surface there.

- [ ] **Step 2: Run `go vet`**

```bash
cd .worktrees/collaborators-pagination && go vet ./daemon/...
```

Expected: no output (no vet warnings).

- [ ] **Step 3: Run the project's standard verification target if available**

```bash
cd .worktrees/collaborators-pagination && make verify-linux
```

If `make verify-linux` is not the right target for this repo, fall back to:

```bash
cd .worktrees/collaborators-pagination && make test
```

Expected: green.

(Per repo convention, `make verify-linux` must run from the worktree directory, not from the main checkout.)

- [ ] **Step 4: Confirm no leftover changes**

```bash
cd .worktrees/collaborators-pagination && git status
```

Expected: `working tree clean`. All changes from Tasks 1-6 should be committed.

- [ ] **Step 5: View the diff against `main` for a final read-through**

```bash
cd .worktrees/collaborators-pagination && git log --oneline origin/main..HEAD && echo '---' && git diff origin/main..HEAD -- daemon/internal/github/
```

Expected: 6 commits (one failing test, one fix, four follow-up tests). The diff in `client.go` should be the small `parseNextLink` helper plus the rewritten `FetchCollaborators` body — nothing else.

---

## Self-Review Notes (for the plan author)

- **Spec coverage:**
  - Goal (paginate via Link header) → Task 2.
  - Safety bound (`maxPages = 100`) → Task 2 + Task 4.
  - Error handling (HTTP non-200, malformed link, runaway) → Tasks 4, 5, 6.
  - Tests 1-4 from spec → Tasks 1, 3, 4, 5.
  - Spec test 5 (`parseNextLink` unit cases) → Task 6, replanned as black-box subtests through `FetchCollaborators` because the package has no white-box test files; this is documented in the task header.
  - Non-goals (no caching, no UI changes, no other endpoints) → respected; nothing in the plan touches `flutter_app`, caching, or other GitHub endpoints.
- **Placeholder scan:** none.
- **Type consistency:** `parseNextLink(string) string` and `FetchCollaborators(repo string) ([]string, error)` are referenced consistently. The error message string `"github: fetch collaborators %s: pagination exceeded %d pages"` in Task 2 is matched by the assertion `strings.Contains(err.Error(), "pagination")` in Task 4. Aligned.
