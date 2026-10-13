package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "creds.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func server(t *testing.T, check func(r *http.Request), status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			check(r)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClaudeSource(t *testing.T) {
	srv := server(t, func(r *http.Request) {
		if r.URL.Path != "/api/oauth/usage" || r.Header.Get("Authorization") != "Bearer tok-kc" || r.Header.Get("anthropic-beta") == "" {
			t.Errorf("request = %s %v", r.URL.Path, r.Header)
		}
	}, 200, `{"five_hour":{"utilization":34.5,"resets_at":"2026-10-08T15:00:00Z"},"seven_day":{"used_percentage":120},"seven_day_opus":null}`)
	s := &ClaudeSource{
		BaseURL:         srv.URL,
		ReadKeychain:    func(context.Context) (string, error) { return `{"claudeAiOauth":{"accessToken":"tok-kc"}}`, nil },
		CredentialsPath: filepath.Join(t.TempDir(), "missing"),
	}
	p := s.Read(context.Background())
	if !p.Available || len(p.Windows) != 2 {
		t.Fatalf("claude = %+v", p)
	}
	if w, _ := p.Window(KindSession, ""); w.UsedPercent != 34.5 || w.ResetsAt.IsZero() {
		t.Errorf("session = %+v", w)
	}
	if w, _ := p.Window(KindWeekly, ""); w.UsedPercent != 100 {
		t.Errorf("weekly must be clamped: %+v", w)
	}
}

// claudeUsageWithLimits is the shape the usage endpoint returns today: the
// legacy five_hour/seven_day objects plus a limits list with per-model weekly
// windows, and extra usage credits.
const claudeUsageWithLimits = `{
 "five_hour":{"utilization":67.0,"resets_at":"2026-10-13T18:29:59Z"},
 "seven_day":{"utilization":47.0,"resets_at":"2026-10-16T22:59:59Z"},
 "extra_usage":{"is_enabled":true,"monthly_limit":75000,"used_credits":3165.0,"utilization":4.22,"currency":"EUR","decimal_places":2},
 "limits":[
  {"kind":"session","group":"session","percent":61,"resets_at":"2026-10-13T18:29:59Z"},
  {"kind":"weekly_all","group":"weekly","percent":46,"resets_at":"2026-10-16T22:59:59Z"},
  {"kind":"weekly_scoped","group":"weekly","percent":3,"resets_at":"2026-10-16T23:00:00Z","scope":{"model":{"id":null,"display_name":"Fable"}}}
 ]}`

func TestClaudeSourceReadsLimitsModelWindowsAndCredits(t *testing.T) {
	srv := server(t, nil, 200, claudeUsageWithLimits)
	s := &ClaudeSource{
		BaseURL:         srv.URL,
		ReadKeychain:    func(context.Context) (string, error) { return `{"claudeAiOauth":{"accessToken":"t"}}`, nil },
		CredentialsPath: filepath.Join(t.TempDir(), "missing"),
	}
	p := s.Read(context.Background())
	if !p.Available || len(p.Windows) != 4 {
		t.Fatalf("claude = %+v", p)
	}
	// The limits list wins over the legacy objects.
	if w, _ := p.Window(KindSession, ""); w.UsedPercent != 61 || w.ResetsAt.IsZero() {
		t.Errorf("session = %+v", w)
	}
	if w, _ := p.Window(KindWeekly, ""); w.UsedPercent != 46 {
		t.Errorf("weekly = %+v", w)
	}
	if w, ok := p.Window(KindModel, "Fable"); !ok || w.UsedPercent != 3 || w.ResetsAt.IsZero() {
		t.Errorf("model window = %+v (%v)", w, ok)
	}
	if w, ok := p.Window(KindCredit, ""); !ok || w.UsedPercent != 4.22 || w.Detail != "31.65 / 750.00 EUR" {
		t.Errorf("credit = %+v (%v)", w, ok)
	}
}

func TestClaudeSourceSkipsDisabledExtraUsage(t *testing.T) {
	srv := server(t, nil, 200, `{"five_hour":{"utilization":5},"extra_usage":{"is_enabled":false,"monthly_limit":0,"used_credits":0,"utilization":null}}`)
	s := &ClaudeSource{
		BaseURL:         srv.URL,
		ReadKeychain:    func(context.Context) (string, error) { return `{"claudeAiOauth":{"accessToken":"t"}}`, nil },
		CredentialsPath: filepath.Join(t.TempDir(), "missing"),
	}
	p := s.Read(context.Background())
	if _, ok := p.Window(KindCredit, ""); ok || len(p.Windows) != 1 {
		t.Fatalf("claude = %+v", p)
	}
}

func TestClaudeSourceLimitsEdgeCases(t *testing.T) {
	// Unknown kinds, missing percents and unnamed scopes are skipped; a model
	// scope without a name falls back to its id; a bad reset time is dropped;
	// credits without utilization are computed from the amounts, and
	// currency/decimal places default sensibly.
	srv := server(t, nil, 200, `{
	 "extra_usage":{"is_enabled":true,"monthly_limit":1000,"used_credits":250},
	 "limits":[
	  {"kind":"session","percent":null},
	  {"kind":"session","percent":10,"resets_at":"not a time"},
	  {"kind":"weekly_scoped","percent":7,"scope":{"model":{"id":"m-1","display_name":""}}},
	  {"kind":"weekly_scoped","percent":8,"scope":{"model":{"id":null,"display_name":""}}},
	  {"kind":"daily_something","percent":9}
	 ]}`)
	s := &ClaudeSource{
		BaseURL:         srv.URL,
		ReadKeychain:    func(context.Context) (string, error) { return `{"claudeAiOauth":{"accessToken":"t"}}`, nil },
		CredentialsPath: filepath.Join(t.TempDir(), "missing"),
	}
	p := s.Read(context.Background())
	if len(p.Windows) != 3 {
		t.Fatalf("windows = %+v", p.Windows)
	}
	if w, _ := p.Window(KindSession, ""); w.UsedPercent != 10 || !w.ResetsAt.IsZero() {
		t.Errorf("session = %+v", w)
	}
	if w, ok := p.Window(KindModel, "m-1"); !ok || w.UsedPercent != 7 {
		t.Errorf("model by id = %+v (%v)", w, ok)
	}
	if w, ok := p.Window(KindCredit, ""); !ok || w.UsedPercent != 25 || w.Detail != "2.50 / 10.00" {
		t.Errorf("credit = %+v (%v)", w, ok)
	}
}

func TestClaudeSourceMergesLegacyDedupesAndPrefersIDs(t *testing.T) {
	// limits gives only per-model windows: session and weekly come from the
	// legacy objects. A scoped model with an id is labelled by the id, a
	// repeated (kind, label) is kept once, and other scoped kinds are
	// skipped. Uncapped extra usage shows its spend as a detail.
	srv := server(t, nil, 200, `{
	 "five_hour":{"utilization":20},
	 "seven_day":{"utilization":30},
	 "extra_usage":{"is_enabled":true,"monthly_limit":0,"used_credits":1234,"currency":"EUR","decimal_places":2},
	 "limits":[
	  {"kind":"weekly_all","percent":null},
	  {"kind":"weekly_scoped","percent":5,"scope":{"model":{"id":"claude-opus","display_name":"Opus"}}},
	  {"kind":"weekly_scoped","percent":6,"scope":{"model":{"id":"claude-opus","display_name":"Opus"}}},
	  {"kind":"session_scoped","percent":9,"scope":{"model":{"id":"claude-opus","display_name":"Opus"}}}
	 ]}`)
	s := &ClaudeSource{
		BaseURL:         srv.URL,
		ReadKeychain:    func(context.Context) (string, error) { return `{"claudeAiOauth":{"accessToken":"t"}}`, nil },
		CredentialsPath: filepath.Join(t.TempDir(), "missing"),
	}
	p := s.Read(context.Background())
	if len(p.Windows) != 3 {
		t.Fatalf("windows = %+v", p.Windows)
	}
	if w, _ := p.Window(KindSession, ""); w.UsedPercent != 20 {
		t.Errorf("session from legacy = %+v", w)
	}
	if w, _ := p.Window(KindWeekly, ""); w.UsedPercent != 30 {
		t.Errorf("weekly from legacy = %+v", w)
	}
	if w, ok := p.Window(KindModel, "claude-opus"); !ok || w.UsedPercent != 5 {
		t.Errorf("model by id = %+v (%v)", w, ok)
	}
	if p.Detail != "12.34 EUR extra usage spent · no limit" {
		t.Errorf("detail = %q", p.Detail)
	}
}

func TestMaxUsedIgnoresModelWindowsUnlessTheyAreAll(t *testing.T) {
	claude := Provider{Windows: []Window{
		{Kind: KindSession, UsedPercent: 20},
		{Kind: KindModel, Label: "Opus", UsedPercent: 100},
	}}
	if w, _ := claude.MaxUsed(); w.Kind != KindSession {
		t.Errorf("claude MaxUsed = %+v, want the session window", w)
	}
	gemini := Provider{Windows: []Window{
		{Kind: KindModel, Label: "a", UsedPercent: 10},
		{Kind: KindModel, Label: "b", UsedPercent: 70},
	}}
	if w, _ := gemini.MaxUsed(); w.Label != "b" {
		t.Errorf("gemini MaxUsed = %+v, want the fullest bucket", w)
	}
}

func TestCreditProviderDetail(t *testing.T) {
	limit := 250.0
	p := CreditProvider("openrouter", 12.5, &limit)
	if w, ok := p.Window(KindCredit, ""); !ok || w.UsedPercent != 5 || w.Detail != "12.50 / 250.00 USD" {
		t.Fatalf("limited = %+v", p)
	}
	p = CreditProvider("openrouter", 3, nil)
	if len(p.Windows) != 0 || p.Detail != "3.00 USD spent · no limit" {
		t.Fatalf("unlimited = %+v", p)
	}
	zero := 0.0
	p = CreditProvider("openrouter", 1, &zero)
	if w, ok := p.Window(KindCredit, ""); !ok || w.UsedPercent != 100 || p.Detail != "" {
		t.Fatalf("zero limit must be exhausted, not unlimited: %+v", p)
	}
}

func TestClaudeSourceCredentialsFileAndErrors(t *testing.T) {
	ok := server(t, nil, 200, `{"five_hour":{"utilization":1}}`)
	s := &ClaudeSource{BaseURL: ok.URL, CredentialsPath: writeFile(t, `{"claudeAiOauth":{"accessToken":"tok-file"}}`)}
	if p := s.Read(context.Background()); !p.Available {
		t.Errorf("credentials file = %+v", p)
	}
	s.ReadKeychain = func(context.Context) (string, error) { return "not json", nil }
	if p := s.Read(context.Background()); !p.Available {
		t.Error("a bad keychain item must fall back to the file")
	}
	if p := (&ClaudeSource{CredentialsPath: writeFile(t, `{}`)}).Read(context.Background()); p.Error != ErrNotConfigured {
		t.Errorf("no token = %+v", p)
	}
	expired := server(t, nil, 401, `{}`)
	if p := (&ClaudeSource{BaseURL: expired.URL, CredentialsPath: s.CredentialsPath}).Read(context.Background()); p.Error != ErrExpired {
		t.Errorf("401 = %+v", p)
	}
	down := server(t, nil, 500, `{}`)
	if p := (&ClaudeSource{BaseURL: down.URL, CredentialsPath: s.CredentialsPath}).Read(context.Background()); p.Error != ErrUnavailable {
		t.Errorf("500 = %+v", p)
	}
	if p := (&ClaudeSource{BaseURL: "http://127.0.0.1:1", CredentialsPath: s.CredentialsPath}).Read(context.Background()); p.Error != ErrUnavailable {
		t.Errorf("unreachable = %+v", p)
	}
	if NewClaudeSource().CredentialsPath == "" {
		t.Error("NewClaudeSource needs a credentials path")
	}
}

func TestCodexSource(t *testing.T) {
	srv := server(t, func(r *http.Request) {
		if r.URL.Path != "/backend-api/wham/usage" || r.Header.Get("ChatGPT-Account-Id") != "acct" {
			t.Errorf("request = %s %v", r.URL.Path, r.Header)
		}
	}, 200, `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":80,"limit_window_seconds":18000,"reset_at":1790000000},"secondary_window":{"used_percent":20,"limit_window_seconds":604800}}}`)
	s := &CodexSource{BaseURL: srv.URL, AuthPath: writeFile(t, `{"tokens":{"access_token":"t","account_id":"acct"}}`)}
	p := s.Read(context.Background())
	if w, ok := p.Window(KindSession, ""); !ok || w.UsedPercent != 80 || w.ResetsAt.IsZero() {
		t.Errorf("session = %+v (%+v)", w, p)
	}
	if w, ok := p.Window(KindWeekly, ""); !ok || w.UsedPercent != 20 {
		t.Errorf("weekly = %+v", w)
	}
	if p := (&CodexSource{AuthPath: writeFile(t, `{"OPENAI_API_KEY":"sk"}`)}).Read(context.Background()); p.Error != ErrNotConfigured {
		t.Errorf("api-key login = %+v", p)
	}
	if p := (&CodexSource{AuthPath: filepath.Join(t.TempDir(), "x")}).Read(context.Background()); p.Error != ErrNotConfigured {
		t.Errorf("missing auth = %+v", p)
	}
	empty := server(t, nil, 200, `{"plan_type":"free"}`)
	if p := (&CodexSource{BaseURL: empty.URL, AuthPath: s.AuthPath}).Read(context.Background()); !p.Available || len(p.Windows) != 0 {
		t.Errorf("no windows = %+v", p)
	}
	if NewCodexSource().AuthPath == "" {
		t.Error("NewCodexSource needs an auth path")
	}
}

func TestCopilotSource(t *testing.T) {
	srv := server(t, func(r *http.Request) {
		if r.Header.Get("Authorization") != "token gh-tok" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
	}, 200, `{"quota_reset_date_utc":"2026-11-01T00:00:00Z","quota_snapshots":{"premium_interactions":{"percent_remaining":25}}}`)
	apps := writeFile(t, `{"ghe.example:x":{"oauth_token":"ghe-tok"},"github.com:Iv1":{"oauth_token":"gh-tok"}}`)
	p := (&CopilotSource{BaseURL: srv.URL, AppsPath: apps}).Read(context.Background())
	if w, ok := p.Window(KindMonthly, ""); !ok || w.UsedPercent != 75 || w.ResetsAt.IsZero() {
		t.Errorf("monthly = %+v (%+v)", w, p)
	}
	unlimited := server(t, nil, 200, `{"quota_snapshots":{"premium_interactions":{"unlimited":true}}}`)
	if p := (&CopilotSource{BaseURL: unlimited.URL, AppsPath: writeFile(t, `{"ghe:x":{"oauth_token":"t"}}`)}).Read(context.Background()); !p.Available || len(p.Windows) != 0 {
		t.Errorf("unlimited = %+v", p)
	}
	for _, body := range []string{`not json`, `{}`, `{"x":{"oauth_token":""}}`} {
		if p := (&CopilotSource{AppsPath: writeFile(t, body)}).Read(context.Background()); p.Error != ErrNotConfigured {
			t.Errorf("apps %q = %+v", body, p)
		}
	}
	if NewCopilotSource().AppsPath == "" {
		t.Error("NewCopilotSource needs a path")
	}
}

func TestGeminiSource(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"proj-1"}`))
		case "/v1internal:retrieveUserQuota":
			_, _ = w.Write([]byte(`{"buckets":[{"modelId":"gemini-2.5-pro","remainingFraction":0.4,"resetTime":"2026-10-09T00:00:00Z"},{"modelId":"","remainingFraction":1},{"modelId":"gemini-2.5-flash"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	now := time.UnixMilli(1_000_000)
	creds := writeFile(t, `{"access_token":"g","expiry_date":2000000}`)
	s := &GeminiSource{BaseURL: srv.URL, CredsPath: creds, now: func() time.Time { return now }}
	p := s.Read(context.Background())
	if w, ok := p.Window(KindModel, "gemini-2.5-pro"); !ok || w.UsedPercent != 60 || len(p.Windows) != 1 {
		t.Errorf("gemini = %+v", p)
	}
	s.ProjectID = "explicit"
	before := calls.Load()
	s.Read(context.Background())
	if calls.Load()-before != 1 {
		t.Error("an explicit project skips loadCodeAssist")
	}
	s.now = func() time.Time { return time.UnixMilli(3_000_000) }
	if p := s.Read(context.Background()); p.Error != ErrExpired {
		t.Errorf("expired token = %+v", p)
	}
	noProject := server(t, nil, 200, `{}`)
	if p := (&GeminiSource{BaseURL: noProject.URL, CredsPath: creds, now: func() time.Time { return now }}).Read(context.Background()); p.Error != ErrUnavailable {
		t.Errorf("no project = %+v", p)
	}
	if p := (&GeminiSource{CredsPath: writeFile(t, `{}`)}).Read(context.Background()); p.Error != ErrNotConfigured {
		t.Errorf("no token = %+v", p)
	}
	if NewGeminiSource().CredsPath == "" {
		t.Error("NewGeminiSource needs a path")
	}
}

type countingSource struct {
	calls atomic.Int32
	delay time.Duration
}

func (c *countingSource) Agent() string { return "x" }
func (c *countingSource) Read(ctx context.Context) Provider {
	c.calls.Add(1)
	time.Sleep(c.delay)
	return Provider{Available: true, Windows: []Window{{Kind: KindSession, UsedPercent: 10}, {Kind: KindWeekly, UsedPercent: 70}}}
}

func TestServiceCachesAndCoalesces(t *testing.T) {
	src := &countingSource{delay: 20 * time.Millisecond}
	svc := NewService(src, FuncSource{ID: "y", Fn: func(context.Context) Provider { return CreditProvider("y", 2, ptr(8)) }})
	now := time.Now()
	svc.now = func() time.Time { return now }
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); svc.Get(context.Background(), "x") }()
	}
	wg.Wait()
	if n := src.calls.Load(); n != 1 {
		t.Errorf("concurrent reads = %d, want 1", n)
	}
	p := svc.Get(context.Background(), "x")
	if p.Agent != "x" || p.FetchedAt.IsZero() {
		t.Errorf("provider = %+v", p)
	}
	if w, ok := p.MaxUsed(); !ok || w.Kind != KindWeekly {
		t.Errorf("MaxUsed = %+v", w)
	}
	now = now.Add(3 * time.Minute)
	if stale := svc.Get(context.Background(), "x"); stale.FetchedAt != p.FetchedAt {
		t.Errorf("past the TTL the last reading is returned while it refreshes, got %+v", stale)
	}
	waitForCalls(t, src, 2)
	snap := svc.Snapshot(context.Background())
	if len(snap) != 2 || snap[1].Windows[0].Kind != KindCredit || snap[1].Windows[0].UsedPercent != 25 {
		t.Errorf("snapshot = %+v", snap)
	}
	if p := svc.Get(context.Background(), "nope"); p.Error != ErrUnsupported {
		t.Errorf("unknown agent = %+v", p)
	}
	if _, ok := (Provider{}).MaxUsed(); ok {
		t.Error("no windows has no max")
	}
	if p := CreditProvider("y", 1, nil); len(p.Windows) != 0 {
		t.Error("an unlimited key has no window")
	}
	if clampPercent(-5) != 0 {
		t.Error("clamp low")
	}
}

func waitForCalls(t *testing.T, src *countingSource, want int32) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for src.calls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("reads = %d, want %d", src.calls.Load(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A slow provider must not hold up reviews: a recent-enough reading is served
// while it refreshes, and a caller with nothing usable waits only as long as
// its ctx allows.
func TestServiceDoesNotWaitOnASlowProvider(t *testing.T) {
	src := &countingSource{delay: 300 * time.Millisecond}
	svc := NewService(src)
	var mu sync.Mutex
	now := time.Now()
	svc.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }

	// Nothing cached and the caller gives up first: no reading, fail closed.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if p := svc.Get(ctx, "x"); p.Available || p.Error != ErrUnavailable {
		t.Fatalf("abandoned wait = %+v, want unavailable", p)
	}
	// The read it started still lands, for the next caller.
	first := svc.Get(context.Background(), "x")
	if !first.Available || src.calls.Load() != 1 {
		t.Fatalf("first reading = %+v after %d reads", first, src.calls.Load())
	}

	// Stale but recent: served at once, one background refresh.
	advance(cacheTTL + time.Second)
	start := time.Now()
	for i := 0; i < 3; i++ {
		if p := svc.Get(context.Background(), "x"); p.FetchedAt != first.FetchedAt {
			t.Fatalf("stale reading = %+v", p)
		}
	}
	if waited := time.Since(start); waited > 100*time.Millisecond {
		t.Errorf("serving a stale reading took %s", waited)
	}
	waitForCalls(t, src, 2)
	deadline := time.Now().Add(5 * time.Second)
	for svc.Get(context.Background(), "x").FetchedAt == first.FetchedAt {
		if time.Now().After(deadline) {
			t.Fatal("the background refresh never landed")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if n := src.calls.Load(); n != 2 {
		t.Fatalf("three stale reads started %d refreshes, want 1", n-1)
	}

	// Too old to trust: the caller waits for the fresh read.
	advance(maxStale + time.Minute)
	if p := svc.Get(context.Background(), "x"); !p.Available || src.calls.Load() != 3 {
		t.Fatalf("after maxStale = %+v with %d reads, want a fresh read", p, src.calls.Load())
	}
}

func ptr(f float64) *float64 { return &f }

func TestHTTPJSONEncodesBodies(t *testing.T) {
	srv := server(t, func(r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("JSON bodies need a content type")
		}
	}, 200, `{"ok":true}`)
	var out struct{ OK bool }
	if _, err := httpJSON(context.Background(), nil, http.MethodPost, srv.URL, nil, map[string]any{"a": 1}, &out); err != nil || !out.OK {
		t.Errorf("post = %v %+v", err, out)
	}
	if _, err := httpJSON(context.Background(), nil, http.MethodPost, srv.URL, nil, func() {}, &out); err == nil {
		t.Error("an unencodable body must fail")
	}
	if _, err := httpJSON(context.Background(), nil, "BAD METHOD", "::", nil, nil, &out); err == nil || !strings.Contains(err.Error(), "") {
		t.Error("a bad request must fail")
	}
}
