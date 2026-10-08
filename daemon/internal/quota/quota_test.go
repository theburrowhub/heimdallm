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
	svc.Get(context.Background(), "x")
	if n := src.calls.Load(); n != 2 {
		t.Errorf("after the TTL reads = %d, want 2", n)
	}
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
