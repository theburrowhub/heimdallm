package github_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

// ── ParseRateLimitHeaders unit tests ─────────────────────────────────────────

func makeResp(status int, headers map[string]string) *http.Response {
	resp := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}
	for k, v := range headers {
		resp.Header.Set(k, v)
	}
	return resp
}

func TestParseRateLimitHeaders_CoreResource(t *testing.T) {
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Remaining": "4321",
		"X-RateLimit-Reset":     "1700000000",
		"X-RateLimit-Resource":  "core",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true for core resource response")
	}
	if parsed.Resource != "core" {
		t.Errorf("Resource = %q, want %q", parsed.Resource, "core")
	}
	if parsed.Remaining != 4321 {
		t.Errorf("Remaining = %d, want 4321", parsed.Remaining)
	}
	want := time.Unix(1700000000, 0)
	if !parsed.Reset.Equal(want) {
		t.Errorf("Reset = %v, want %v", parsed.Reset, want)
	}
	if parsed.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 (not a 403/429)", parsed.RetryAfter)
	}
}

func TestParseRateLimitHeaders_SearchResource(t *testing.T) {
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Remaining": "25",
		"X-RateLimit-Reset":     "1700001234",
		"X-RateLimit-Resource":  "search",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Resource != "search" {
		t.Errorf("Resource = %q, want %q", parsed.Resource, "search")
	}
	if parsed.Remaining != 25 {
		t.Errorf("Remaining = %d, want 25", parsed.Remaining)
	}
}

func TestParseRateLimitHeaders_DefaultsToCore(t *testing.T) {
	// X-RateLimit-Resource absent → default to "core".
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Remaining": "100",
		"X-RateLimit-Reset":     "1700000000",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Resource != "core" {
		t.Errorf("Resource = %q, want %q (default)", parsed.Resource, "core")
	}
}

func TestParseRateLimitHeaders_RetryAfterOn403(t *testing.T) {
	resp := makeResp(http.StatusForbidden, map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     "1700000000",
		"X-RateLimit-Resource":  "core",
		"Retry-After":           "60",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true on 403 with headers")
	}
	if parsed.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", parsed.RetryAfter)
	}
}

func TestParseRateLimitHeaders_RetryAfterOn429(t *testing.T) {
	resp := makeResp(http.StatusTooManyRequests, map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     "1700000000",
		"Retry-After":           "30",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true on 429 with headers")
	}
	if parsed.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", parsed.RetryAfter)
	}
}

func TestParseRateLimitHeaders_No403RetryAfterOn200(t *testing.T) {
	// Retry-After on a 200 should NOT be set (it's not a rate-limit signal on 200).
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Remaining": "4000",
		"X-RateLimit-Reset":     "1700000000",
		"Retry-After":           "60", // unusual — should be ignored on non-403/429
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.RetryAfter != 0 {
		t.Errorf("RetryAfter = %v, want 0 on 200", parsed.RetryAfter)
	}
}

func TestParseRateLimitHeaders_LimitAndUsedParsed(t *testing.T) {
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Limit":     "5000",
		"X-RateLimit-Remaining": "4321",
		"X-RateLimit-Used":      "679",
		"X-RateLimit-Reset":     "1700000000",
		"X-RateLimit-Resource":  "core",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Limit != 5000 {
		t.Errorf("Limit = %d, want 5000", parsed.Limit)
	}
	if parsed.Used != 679 {
		t.Errorf("Used = %d, want 679", parsed.Used)
	}
}

func TestParseRateLimitHeaders_UsedDerivedWhenAbsent(t *testing.T) {
	// GitHub always sends X-RateLimit-Used, but derive it defensively from
	// Limit - Remaining when a proxy strips the header.
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Limit":     "5000",
		"X-RateLimit-Remaining": "4321",
		"X-RateLimit-Reset":     "1700000000",
		"X-RateLimit-Resource":  "core",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if parsed.Used != 679 {
		t.Errorf("Used = %d, want 679 (derived from Limit-Remaining)", parsed.Used)
	}
}

func TestParseRateLimitHeaders_LimitAbsentDoesNotBreakOK(t *testing.T) {
	resp := makeResp(http.StatusOK, map[string]string{
		"X-RateLimit-Remaining": "100",
		"X-RateLimit-Reset":     "1700000000",
	})

	parsed, ok := gh.ParseRateLimitHeaders(resp)
	if !ok {
		t.Fatal("expected ok=true even without X-RateLimit-Limit")
	}
	if parsed.Limit != 0 {
		t.Errorf("Limit = %d, want 0 (absent)", parsed.Limit)
	}
	if parsed.Used != 0 {
		t.Errorf("Used = %d, want 0 (no Limit to derive from)", parsed.Used)
	}
}

func TestParseRateLimitHeaders_AbsentHeadersReturnsFalse(t *testing.T) {
	resp := makeResp(http.StatusOK, map[string]string{})
	_, ok := gh.ParseRateLimitHeaders(resp)
	if ok {
		t.Error("expected ok=false when no X-RateLimit-Remaining header")
	}
}

func TestParseRateLimitHeaders_NilResponseReturnsFalse(t *testing.T) {
	_, ok := gh.ParseRateLimitHeaders(nil)
	if ok {
		t.Error("expected ok=false for nil response")
	}
}

func TestParseRateLimitHeaders_TableDriven(t *testing.T) {
	epoch := int64(1700000000)
	cases := []struct {
		name      string
		status    int
		headers   map[string]string
		wantOK    bool
		wantRes   string
		wantRem   int
		wantRetry time.Duration
	}{
		{
			name:   "core 200",
			status: 200,
			headers: map[string]string{
				"X-RateLimit-Remaining": "5000",
				"X-RateLimit-Reset":     fmt.Sprintf("%d", epoch),
				"X-RateLimit-Resource":  "core",
			},
			wantOK: true, wantRes: "core", wantRem: 5000, wantRetry: 0,
		},
		{
			name:   "search 200",
			status: 200,
			headers: map[string]string{
				"X-RateLimit-Remaining": "10",
				"X-RateLimit-Reset":     fmt.Sprintf("%d", epoch),
				"X-RateLimit-Resource":  "search",
			},
			wantOK: true, wantRes: "search", wantRem: 10, wantRetry: 0,
		},
		{
			name:   "403 secondary limit",
			status: 403,
			headers: map[string]string{
				"X-RateLimit-Remaining": "0",
				"X-RateLimit-Reset":     fmt.Sprintf("%d", epoch),
				"X-RateLimit-Resource":  "core",
				"Retry-After":           "120",
			},
			wantOK: true, wantRes: "core", wantRem: 0, wantRetry: 120 * time.Second,
		},
		{
			name:    "no headers → false",
			status:  200,
			headers: map[string]string{},
			wantOK:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := makeResp(tc.status, tc.headers)
			parsed, ok := gh.ParseRateLimitHeaders(resp)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if parsed.Resource != tc.wantRes {
				t.Errorf("Resource = %q, want %q", parsed.Resource, tc.wantRes)
			}
			if parsed.Remaining != tc.wantRem {
				t.Errorf("Remaining = %d, want %d", parsed.Remaining, tc.wantRem)
			}
			if parsed.RetryAfter != tc.wantRetry {
				t.Errorf("RetryAfter = %v, want %v", parsed.RetryAfter, tc.wantRetry)
			}
		})
	}
}

// ── Observer integration tests via httptest ───────────────────────────────────

// fakeObserver records all calls to ObserveResponse for test assertions.
type fakeObserver struct {
	mu    sync.Mutex
	calls []*http.Response
}

func (f *fakeObserver) ObserveResponse(resp *http.Response) {
	f.mu.Lock()
	f.calls = append(f.calls, resp)
	f.mu.Unlock()
}

func (f *fakeObserver) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeObserver) last() *http.Response {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1]
}

// TestSetRateObserver_CalledAfterGetRequest verifies that a registered observer
// receives a call with the response after a GET request through do().
func TestSetRateObserver_CalledAfterGetRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"login":"bot"}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	resp, err := client.DoGETForTest("/user", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("DoGETForTest: %v", err)
	}
	resp.Body.Close()

	if obs.count() != 1 {
		t.Errorf("observer called %d times, want 1", obs.count())
	}
	last := obs.last()
	if last == nil {
		t.Fatal("last observed response is nil")
	}
	if last.Header.Get("X-RateLimit-Remaining") != "4999" {
		t.Errorf("observer saw Remaining=%q, want %q",
			last.Header.Get("X-RateLimit-Remaining"), "4999")
	}
	if last.Header.Get("X-RateLimit-Resource") != "core" {
		t.Errorf("observer saw Resource=%q, want %q",
			last.Header.Get("X-RateLimit-Resource"), "core")
	}
}

// TestSetRateObserver_CalledAfterNonGetRequest verifies that the observer is
// also invoked for POST/PUT requests through doWithBody().
func TestSetRateObserver_CalledAfterNonGetRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "2000")
		w.Header().Set("X-RateLimit-Reset", "1700000001")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	// Trigger doWithBody via MergePR (PUT).
	_ = client.MergePR("org/repo", 1, "squash")

	if obs.count() < 1 {
		t.Errorf("observer called %d times, want >= 1 (from PUT via doWithBody)", obs.count())
	}
	last := obs.last()
	if last == nil {
		t.Fatal("last observed response is nil")
	}
	if last.Header.Get("X-RateLimit-Remaining") != "2000" {
		t.Errorf("observer saw Remaining=%q, want %q",
			last.Header.Get("X-RateLimit-Remaining"), "2000")
	}
}

// TestSetRateObserver_NotCalledWhenNil verifies that no panic occurs when no
// observer is registered and a request is made.
func TestSetRateObserver_NotCalledWhenNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"login":"bot"}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	// No observer registered — must not panic.
	resp, err := client.DoGETForTest("/user", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("DoGETForTest: %v", err)
	}
	resp.Body.Close()
}

// TestSetRateObserver_ObserverSeesRealHeaders verifies end-to-end: the observer
// sees the headers the httptest server returned, and ParseRateLimitHeaders
// correctly extracts them.
func TestSetRateObserver_ObserverSeesRealHeaders(t *testing.T) {
	resetEpoch := int64(1800000000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "42")
		w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", resetEpoch))
		w.Header().Set("X-RateLimit-Resource", "search")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"login":"bot"}`)
	}))
	defer srv.Close()

	var gotParsed gh.ParsedRateLimit
	var gotOK bool

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	client.SetRateObserver(gh.RateLimitObserverFunc(func(resp *http.Response) {
		gotParsed, gotOK = gh.ParseRateLimitHeaders(resp)
	}))

	resp, err := client.DoGETForTest("/user", "application/vnd.github+json")
	if err != nil {
		t.Fatalf("DoGETForTest: %v", err)
	}
	resp.Body.Close()

	if !gotOK {
		t.Fatal("ParseRateLimitHeaders returned ok=false inside observer")
	}
	if gotParsed.Resource != "search" {
		t.Errorf("Resource = %q, want %q", gotParsed.Resource, "search")
	}
	if gotParsed.Remaining != 42 {
		t.Errorf("Remaining = %d, want 42", gotParsed.Remaining)
	}
	if !gotParsed.Reset.Equal(time.Unix(resetEpoch, 0)) {
		t.Errorf("Reset = %v, want %v", gotParsed.Reset, time.Unix(resetEpoch, 0))
	}
}

// TestSetRateObserver_MultipleRequestsCallObserverEachTime verifies the observer
// is called once per request, not once per client lifetime.
func TestSetRateObserver_MultipleRequestsCallObserverEachTime(t *testing.T) {
	call := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call++
		w.Header().Set("X-RateLimit-Remaining", fmt.Sprintf("%d", 5000-call))
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"login":"bot"}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	const n = 3
	for i := 0; i < n; i++ {
		resp, err := client.DoGETForTest("/user", "application/vnd.github+json")
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		resp.Body.Close()
	}

	if obs.count() != n {
		t.Errorf("observer called %d times, want %d", obs.count(), n)
	}
}

// TestSetRateObserver_CalledAfterSubmitReview verifies that a registered
// observer is called when SubmitReview is invoked, so 403 secondary-rate-limit
// responses on the write path update the limiter's cooldown.
func TestSetRateObserver_CalledAfterSubmitReview(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "3000")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"id":42,"state":"APPROVED"}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	_, _, err := client.SubmitReview("org/repo", 1, "looks good", "APPROVE")
	if err != nil {
		t.Fatalf("SubmitReview: %v", err)
	}

	if obs.count() != 1 {
		t.Errorf("observer called %d times after SubmitReview, want 1", obs.count())
	}
	last := obs.last()
	if last == nil {
		t.Fatal("last observed response is nil")
	}
	if last.Header.Get("X-RateLimit-Remaining") != "3000" {
		t.Errorf("observer saw Remaining=%q, want %q",
			last.Header.Get("X-RateLimit-Remaining"), "3000")
	}
}

// TestSetRateObserver_CalledAfterPostComment verifies that a registered
// observer is called when PostComment is invoked, so 403 secondary-rate-limit
// responses on the write path update the limiter's cooldown.
func TestSetRateObserver_CalledAfterPostComment(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "1500")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"created_at":"2024-01-01T00:00:00Z"}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	_, err := client.PostComment("org/repo", 1, "hello from bot")
	if err != nil {
		t.Fatalf("PostComment: %v", err)
	}

	if obs.count() != 1 {
		t.Errorf("observer called %d times after PostComment, want 1", obs.count())
	}
	last := obs.last()
	if last == nil {
		t.Fatal("last observed response is nil")
	}
	if last.Header.Get("X-RateLimit-Remaining") != "1500" {
		t.Errorf("observer saw Remaining=%q, want %q",
			last.Header.Get("X-RateLimit-Remaining"), "1500")
	}
}

// TestSetRateObserver_SubmitReview403SecondaryRateLimit verifies that a 403
// with Retry-After (GitHub's secondary rate-limit signal) on SubmitReview
// reaches the observer so the limiter can enter cooldown.
func TestSetRateObserver_SubmitReview403SecondaryRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	// SubmitReview will return an error (403), but the observer must still fire.
	_, _, _ = client.SubmitReview("org/repo", 1, "body", "APPROVE")

	if obs.count() != 1 {
		t.Errorf("observer called %d times on 403 from SubmitReview, want 1", obs.count())
	}
	last := obs.last()
	if last == nil {
		t.Fatal("last observed response is nil")
	}
	parsed, ok := gh.ParseRateLimitHeaders(last)
	if !ok {
		t.Fatal("ParseRateLimitHeaders returned ok=false on 403 response")
	}
	if parsed.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %v, want 60s", parsed.RetryAfter)
	}
}

// TestSetRateObserver_PostComment403SecondaryRateLimit verifies that a 403
// with Retry-After on PostComment reaches the observer.
func TestSetRateObserver_PostComment403SecondaryRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", "1700000000")
		w.Header().Set("X-RateLimit-Resource", "core")
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, `{"message":"You have exceeded a secondary rate limit."}`)
	}))
	defer srv.Close()

	client := gh.NewClient("fake", gh.WithBaseURL(srv.URL))
	obs := &fakeObserver{}
	client.SetRateObserver(obs)

	// PostComment will return an error (403), but the observer must still fire.
	_, _ = client.PostComment("org/repo", 1, "body")

	if obs.count() != 1 {
		t.Errorf("observer called %d times on 403 from PostComment, want 1", obs.count())
	}
	last := obs.last()
	if last == nil {
		t.Fatal("last observed response is nil")
	}
	parsed, ok := gh.ParseRateLimitHeaders(last)
	if !ok {
		t.Fatal("ParseRateLimitHeaders returned ok=false on 403 response")
	}
	if parsed.RetryAfter != 30*time.Second {
		t.Errorf("RetryAfter = %v, want 30s", parsed.RetryAfter)
	}
}
