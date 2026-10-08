package openrouter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/executor"
)

// fakeOpenRouter scripts the completions endpoint: each call pops the next
// reply. It records every request body.
type fakeOpenRouter struct {
	mu       sync.Mutex
	replies  []string
	requests []map[string]any
	headers  []http.Header
	status   int
}

func (f *fakeOpenRouter) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.headers = append(f.headers, r.Header.Clone())
		switch r.URL.Path {
		case "/key":
			_, _ = w.Write([]byte(`{"data":{"label":"heimdallm","limit":10,"limit_remaining":7.5,"usage":2.5,"usage_daily":0.5}}`))
			return
		case "/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"anthropic/claude-sonnet-4.5","name":"Sonnet","supported_parameters":["tools"]},{"id":"x/plain","supported_parameters":[]}]}`))
			return
		case "/chat/completions":
		default:
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		f.requests = append(f.requests, req)
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
			return
		}
		if len(f.replies) == 0 {
			t.Errorf("unexpected extra completion request")
			w.WriteHeader(500)
			return
		}
		reply := f.replies[0]
		f.replies = f.replies[1:]
		_, _ = w.Write([]byte(reply))
	}
}

func completion(content string, toolCalls string) string {
	c := "null"
	if content != "" {
		b, _ := json.Marshal(content)
		c = string(b)
	}
	tc := ""
	if toolCalls != "" {
		tc = `,"tool_calls":` + toolCalls
	}
	return `{"choices":[{"message":{"role":"assistant","content":` + c + tc + `}}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"cost":0.001,"prompt_tokens_details":{"cached_tokens":40}}}`
}

const reviewJSON = `{"summary":"looks fine","issues":[{"file":"main.go","line":3,"description":"admin check","severity":"high"}],"severity":"high"}`

func newTestRunner(t *testing.T, f *fakeOpenRouter) *Runner {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	keys := &KeyStore{Path: filepath.Join(t.TempDir(), "k")}
	if err := keys.Set("sk-or-test"); err != nil {
		t.Fatal(err)
	}
	c := NewClient(keys)
	c.BaseURL = srv.URL
	return NewRunner(c)
}

func TestReview_UsesToolsThenAnswers(t *testing.T) {
	ws, _ := testRepo(t)
	f := &fakeOpenRouter{replies: []string{
		completion("", `[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}},{"id":"c2","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"Login\"}"}}]`),
		completion(reviewJSON, ""),
	}}
	r := newTestRunner(t, f)
	answer, usage, err := r.Review(context.Background(), "review the diff", executor.ExecOptions{
		Model: "x/model", WorkDir: ws.root, Effort: "max",
	})
	if err != nil || answer != reviewJSON {
		t.Fatalf("Review = %q, %v", answer, err)
	}
	if usage.InputTokens != 200 || usage.OutputTokens != 40 || usage.CacheReadTokens != 80 || usage.CostUSD != 0.002 || usage.Estimated {
		t.Errorf("usage = %+v", usage)
	}
	if len(f.requests) != 2 {
		t.Fatalf("requests = %d", len(f.requests))
	}
	first := f.requests[0]
	if first["model"] != "x/model" || first["tool_choice"] != "auto" || len(first["tools"].([]any)) != 3 {
		t.Errorf("first request = %v", first)
	}
	if first["reasoning"].(map[string]any)["effort"] != "high" {
		t.Errorf("max effort must map to high: %v", first["reasoning"])
	}
	if first["usage"].(map[string]any)["include"] != true {
		t.Error("usage accounting must be requested")
	}
	msgs := f.requests[1]["messages"].([]any)
	var toolResults []string
	for _, m := range msgs {
		mm := m.(map[string]any)
		if mm["role"] == "tool" {
			toolResults = append(toolResults, mm["tool_call_id"].(string)+"="+mm["content"].(string))
		}
	}
	if len(toolResults) != 2 || !strings.Contains(toolResults[0], "c1=") || !strings.Contains(toolResults[0], "func Login") ||
		!strings.Contains(toolResults[1], "main.go:3") {
		t.Errorf("tool results = %v", toolResults)
	}
	sys := msgs[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "senior engineer reviewing a pull request") {
		t.Error("the review-specialist system prompt must lead the conversation")
	}
	h := f.headers[0]
	if h.Get("Authorization") != "Bearer sk-or-test" || h.Get("X-Title") != "Heimdallm" {
		t.Errorf("headers = %v", h)
	}
}

func TestReview_ToolBudgetForcesAnAnswer(t *testing.T) {
	ws, _ := testRepo(t)
	call := `[{"id":"c","type":"function","function":{"name":"list_dir","arguments":"{\"path\":\".\"}"}}]`
	f := &fakeOpenRouter{replies: []string{completion("", call), completion("", call), completion(reviewJSON, "")}}
	r := newTestRunner(t, f)
	answer, _, err := r.Review(context.Background(), "p", executor.ExecOptions{WorkDir: ws.root, MaxTurns: 2})
	if err != nil || answer != reviewJSON {
		t.Fatalf("Review = %q, %v", answer, err)
	}
	if f.requests[2]["tool_choice"] != "none" {
		t.Errorf("after the budget the model must be told not to call tools: %v", f.requests[2]["tool_choice"])
	}
	if f.requests[0]["model"] != DefaultModel {
		t.Errorf("default model = %v", f.requests[0]["model"])
	}
	last := f.requests[2]["messages"].([]any)
	if note := last[len(last)-1].(map[string]any)["content"].(string); !strings.Contains(note, "used all 2 tool rounds") {
		t.Errorf("budget note = %q", note)
	}
}

func TestReview_DiffOnlyWithoutCheckout(t *testing.T) {
	f := &fakeOpenRouter{replies: []string{completion(reviewJSON, "")}}
	r := newTestRunner(t, f)
	if _, _, err := r.Review(context.Background(), "p", executor.ExecOptions{WorkDir: filepath.Join(t.TempDir(), "gone")}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.requests[0]["tools"]; ok {
		t.Error("no checkout means no tools")
	}
}

func TestReview_CorrectsANonJSONAnswerOnce(t *testing.T) {
	f := &fakeOpenRouter{replies: []string{completion("Here is my review: it looks fine.", ""), completion(reviewJSON, "")}}
	r := newTestRunner(t, f)
	answer, usage, err := r.Review(context.Background(), "p", executor.ExecOptions{})
	if err != nil || answer != reviewJSON || usage.InputTokens != 200 {
		t.Fatalf("Review = %q, %+v, %v", answer, usage, err)
	}
	msgs := f.requests[1]["messages"].([]any)
	if !strings.Contains(msgs[len(msgs)-1].(map[string]any)["content"].(string), "ONLY the JSON object") {
		t.Error("the corrective turn must ask for the JSON")
	}

	f.replies = []string{completion("still prose", ""), completion("", "")}
	if _, _, err := r.Review(context.Background(), "p", executor.ExecOptions{}); err == nil || !strings.Contains(err.Error(), "no review") {
		t.Errorf("an empty corrective answer must fail: %v", err)
	}
}

func TestReview_ErrorsAndRateLimits(t *testing.T) {
	f := &fakeOpenRouter{status: http.StatusTooManyRequests}
	r := newTestRunner(t, f)
	_, _, err := r.Review(context.Background(), "p", executor.ExecOptions{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.RateLimited() || !strings.Contains(apiErr.Error(), "Rate limit exceeded") {
		t.Fatalf("429 = %v", err)
	}
	if strings.Contains(err.Error(), "sk-or-test") {
		t.Fatal("the key must never appear in an error")
	}
	f.status = 0
	f.replies = []string{`{"choices":[]}`}
	if _, _, err := r.Review(context.Background(), "p", executor.ExecOptions{}); err == nil {
		t.Error("no choices must fail")
	}
	f.replies = []string{`{"error":{"message":"upstream down"}}`}
	if _, _, err := r.Review(context.Background(), "p", executor.ExecOptions{}); err == nil || !strings.Contains(err.Error(), "upstream down") {
		t.Errorf("in-body error = %v", err)
	}
	f.replies = []string{`not json`}
	if _, _, err := r.Review(context.Background(), "p", executor.ExecOptions{}); err == nil {
		t.Error("malformed response must fail")
	}

	none := NewRunner(NewClient(&KeyStore{Path: filepath.Join(t.TempDir(), "none")}))
	if none.Configured() {
		t.Error("no key means not configured")
	}
	if _, _, err := none.Review(context.Background(), "p", executor.ExecOptions{}); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key = %v", err)
	}
	var nilRunner *Runner
	if nilRunner.Configured() {
		t.Error("nil runner must not be configured")
	}
}

func TestKeyInfoAndModels(t *testing.T) {
	f := &fakeOpenRouter{}
	r := newTestRunner(t, f)
	info, err := r.Client.KeyInfo(context.Background())
	if err != nil || info.Label != "heimdallm" || *info.Limit != 10 || *info.LimitRemaining != 7.5 || info.Usage != 2.5 {
		t.Fatalf("KeyInfo = %+v, %v", info, err)
	}
	now := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	r.Client.nowForTest = func() time.Time { return now }
	models, err := r.Client.Models(context.Background())
	if err != nil || len(models) != 2 || !models[0].SupportsTools() || models[1].SupportsTools() {
		t.Fatalf("Models = %+v, %v", models, err)
	}
	before := len(f.headers)
	if _, err := r.Client.Models(context.Background()); err != nil || len(f.headers) != before {
		t.Error("models must be served from the cache within the TTL")
	}
	now = now.Add(2 * time.Hour)
	if _, err := r.Client.Models(context.Background()); err != nil || len(f.headers) != before+1 {
		t.Error("models must be refetched after the TTL")
	}
}

func TestClientTransportErrors(t *testing.T) {
	keys := &KeyStore{Path: filepath.Join(t.TempDir(), "k")}
	_ = keys.Set("sk-or-x")
	c := NewClient(keys)
	c.BaseURL = "http://127.0.0.1:1"
	if _, err := c.KeyInfo(context.Background()); err == nil {
		t.Error("unreachable API must fail")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(strings.Repeat("x", 1000)))
	}))
	defer srv.Close()
	c.BaseURL = srv.URL
	_, err := c.KeyInfo(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.RateLimited() || len(apiErr.Message) > maxErrorBodyLen+5 {
		t.Errorf("500 = %v", err)
	}
	if _, err := c.Models(context.Background()); err == nil {
		t.Error("models 500 must fail")
	}
	_ = os.Remove(keys.Path)
	if _, err := c.KeyInfo(context.Background()); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key = %v", err)
	}
}

func TestValidReview(t *testing.T) {
	for in, want := range map[string]bool{
		reviewJSON:                         true,
		"```json\n" + reviewJSON + "\n```": true,
		`{"issues":[],"severity":"low"}`:   false,
		"plain text":                       false,
	} {
		if got := validReview(in); got != want {
			t.Errorf("validReview(%q) = %v", in, got)
		}
	}
}
