package quota

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// maxQuotaResponseBytes bounds a usage response.
const maxQuotaResponseBytes = 1 << 20

// httpGetJSON performs an authenticated GET/POST and decodes JSON into out.
// It returns the HTTP status for error classification.
func httpJSON(ctx context.Context, client *http.Client, method, url string, headers map[string]string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return 0, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = &http.Client{Timeout: readTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaResponseBytes))
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode/100 != 2 {
		return resp.StatusCode, errors.New("quota: non-2xx response")
	}
	return resp.StatusCode, json.Unmarshal(data, out)
}

// failure maps a failed request to a Provider error kind.
func failure(agent string, status int) Provider {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return Provider{Agent: agent, Error: ErrExpired}
	}
	return Provider{Agent: agent, Error: ErrUnavailable}
}

// ── Claude Code ──────────────────────────────────────────────────────────

// ClaudeSource reads the 5-hour and weekly windows from Anthropic's OAuth
// usage endpoint with the token Claude Code stored after `claude login`:
// the macOS Keychain item "Claude Code-credentials", or
// ~/.claude/.credentials.json (Linux, Docker, or a CLAUDE_CONFIG_DIR).
type ClaudeSource struct {
	BaseURL string
	HTTP    *http.Client
	// ReadKeychain returns the Keychain item's JSON; nil skips the Keychain.
	ReadKeychain func(ctx context.Context) (string, error)
	// CredentialsPath is the credentials file.
	CredentialsPath string
}

// NewClaudeSource returns a source wired to the real Keychain and files.
func NewClaudeSource() *ClaudeSource {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	s := &ClaudeSource{BaseURL: "https://api.anthropic.com", CredentialsPath: filepath.Join(dir, ".credentials.json")}
	if runtime.GOOS == "darwin" {
		s.ReadKeychain = func(ctx context.Context) (string, error) {
			out, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w").Output()
			return string(out), err
		}
	}
	return s
}

func (s *ClaudeSource) Agent() string { return "claude" }

type claudeCredentials struct {
	ClaudeAiOauth *struct {
		AccessToken string `json:"accessToken"`
	} `json:"claudeAiOauth"`
}

func parseClaudeToken(raw string) string {
	var c claudeCredentials
	if json.Unmarshal([]byte(strings.TrimSpace(raw)), &c) != nil || c.ClaudeAiOauth == nil {
		return ""
	}
	return c.ClaudeAiOauth.AccessToken
}

func (s *ClaudeSource) token(ctx context.Context) string {
	if s.ReadKeychain != nil {
		if raw, err := s.ReadKeychain(ctx); err == nil {
			if tok := parseClaudeToken(raw); tok != "" {
				return tok
			}
		}
	}
	if raw, err := os.ReadFile(s.CredentialsPath); err == nil {
		return parseClaudeToken(string(raw))
	}
	return ""
}

type claudeWindow struct {
	Utilization    *float64 `json:"utilization"`
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *string  `json:"resets_at"`
}

func (w *claudeWindow) toWindow(kind string) (Window, bool) {
	if w == nil {
		return Window{}, false
	}
	var used float64
	switch {
	case w.Utilization != nil:
		used = *w.Utilization
	case w.UsedPercentage != nil:
		used = *w.UsedPercentage
	default:
		return Window{}, false
	}
	out := Window{Kind: kind, UsedPercent: clampPercent(used)}
	if w.ResetsAt != nil {
		if t, err := time.Parse(time.RFC3339Nano, *w.ResetsAt); err == nil {
			out.ResetsAt = t
		}
	}
	return out, true
}

func (s *ClaudeSource) Read(ctx context.Context) Provider {
	tok := s.token(ctx)
	if tok == "" {
		return Provider{Agent: s.Agent(), Error: ErrNotConfigured}
	}
	var body struct {
		FiveHour *claudeWindow `json:"five_hour"`
		SevenDay *claudeWindow `json:"seven_day"`
	}
	status, err := httpJSON(ctx, s.HTTP, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/api/oauth/usage", map[string]string{
		"Authorization":  "Bearer " + tok,
		"anthropic-beta": "oauth-2025-04-20",
		"User-Agent":     "heimdallm",
	}, nil, &body)
	if err != nil {
		return failure(s.Agent(), status)
	}
	p := Provider{Agent: s.Agent(), Available: true}
	if w, ok := body.FiveHour.toWindow(KindSession); ok {
		p.Windows = append(p.Windows, w)
	}
	if w, ok := body.SevenDay.toWindow(KindWeekly); ok {
		p.Windows = append(p.Windows, w)
	}
	return p
}

// ── Codex ────────────────────────────────────────────────────────────────

// CodexSource reads Codex's rate-limit windows from the ChatGPT backend with
// the token `codex login` stored in $CODEX_HOME/auth.json.
type CodexSource struct {
	BaseURL  string
	HTTP     *http.Client
	AuthPath string
}

// NewCodexSource returns a source over the real auth file.
func NewCodexSource() *CodexSource {
	dir := os.Getenv("CODEX_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".codex")
	}
	return &CodexSource{BaseURL: "https://chatgpt.com", AuthPath: filepath.Join(dir, "auth.json")}
}

func (s *CodexSource) Agent() string { return "codex" }

type codexWindow struct {
	UsedPercent        *float64 `json:"used_percent"`
	LimitWindowSeconds int64    `json:"limit_window_seconds"`
	ResetAt            int64    `json:"reset_at"`
}

// sessionWindowMax separates Codex's short (session) window from its weekly
// one by duration, as Orca does.
const sessionWindowMax = 6 * time.Hour

func (s *CodexSource) Read(ctx context.Context) Provider {
	raw, err := os.ReadFile(s.AuthPath)
	if err != nil {
		return Provider{Agent: s.Agent(), Error: ErrNotConfigured}
	}
	var auth struct {
		Tokens *struct {
			AccessToken string `json:"access_token"`
			AccountID   string `json:"account_id"`
		} `json:"tokens"`
	}
	if json.Unmarshal(raw, &auth) != nil || auth.Tokens == nil || auth.Tokens.AccessToken == "" {
		// API-key logins have no ChatGPT plan windows.
		return Provider{Agent: s.Agent(), Error: ErrNotConfigured}
	}
	headers := map[string]string{
		"Authorization": "Bearer " + auth.Tokens.AccessToken,
		"User-Agent":    "codex-cli",
		"OpenAI-Beta":   "codex-1",
	}
	if auth.Tokens.AccountID != "" {
		headers["ChatGPT-Account-Id"] = auth.Tokens.AccountID
	}
	var body struct {
		RateLimit *struct {
			Primary   *codexWindow `json:"primary_window"`
			Secondary *codexWindow `json:"secondary_window"`
		} `json:"rate_limit"`
	}
	status, err := httpJSON(ctx, s.HTTP, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/backend-api/wham/usage", headers, nil, &body)
	if err != nil {
		return failure(s.Agent(), status)
	}
	p := Provider{Agent: s.Agent(), Available: true}
	if body.RateLimit == nil {
		return p
	}
	for _, w := range []*codexWindow{body.RateLimit.Primary, body.RateLimit.Secondary} {
		if w == nil || w.UsedPercent == nil {
			continue
		}
		kind := KindWeekly
		if w.LimitWindowSeconds > 0 && time.Duration(w.LimitWindowSeconds)*time.Second <= sessionWindowMax {
			kind = KindSession
		}
		win := Window{Kind: kind, UsedPercent: clampPercent(*w.UsedPercent)}
		if w.ResetAt > 0 {
			win.ResetsAt = time.Unix(w.ResetAt, 0).UTC()
		}
		p.Windows = append(p.Windows, win)
	}
	return p
}

// ── GitHub Copilot ───────────────────────────────────────────────────────

// CopilotSource reads the monthly premium-request allowance from GitHub's
// Copilot user endpoint with the OAuth token Copilot stored in
// ~/.config/github-copilot/apps.json.
type CopilotSource struct {
	BaseURL  string
	HTTP     *http.Client
	AppsPath string
}

// NewCopilotSource returns a source over the real apps.json.
func NewCopilotSource() *CopilotSource {
	home, _ := os.UserHomeDir()
	return &CopilotSource{BaseURL: "https://api.github.com", AppsPath: filepath.Join(home, ".config", "github-copilot", "apps.json")}
}

func (s *CopilotSource) Agent() string { return "copilot" }

func (s *CopilotSource) token() string {
	raw, err := os.ReadFile(s.AppsPath)
	if err != nil {
		return ""
	}
	var apps map[string]struct {
		OAuthToken string `json:"oauth_token"`
	}
	if json.Unmarshal(raw, &apps) != nil {
		return ""
	}
	var keys []string
	for k, v := range apps {
		if v.OAuthToken != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == "github.com" || strings.HasPrefix(k, "github.com:") {
			return apps[k].OAuthToken
		}
	}
	if len(keys) > 0 {
		return apps[keys[0]].OAuthToken
	}
	return ""
}

func (s *CopilotSource) Read(ctx context.Context) Provider {
	tok := s.token()
	if tok == "" {
		return Provider{Agent: s.Agent(), Error: ErrNotConfigured}
	}
	var body struct {
		QuotaResetDateUTC string `json:"quota_reset_date_utc"`
		QuotaSnapshots    struct {
			Premium *struct {
				Unlimited        bool    `json:"unlimited"`
				PercentRemaining float64 `json:"percent_remaining"`
			} `json:"premium_interactions"`
		} `json:"quota_snapshots"`
	}
	status, err := httpJSON(ctx, s.HTTP, http.MethodGet, strings.TrimRight(s.BaseURL, "/")+"/copilot_internal/user", map[string]string{
		"Authorization":  "token " + tok,
		"Accept":         "application/json",
		"Editor-Version": "heimdallm/1.0",
	}, nil, &body)
	if err != nil {
		return failure(s.Agent(), status)
	}
	p := Provider{Agent: s.Agent(), Available: true}
	if snap := body.QuotaSnapshots.Premium; snap != nil && !snap.Unlimited {
		w := Window{Kind: KindMonthly, UsedPercent: clampPercent(100 - snap.PercentRemaining)}
		if t, err := time.Parse(time.RFC3339, body.QuotaResetDateUTC); err == nil {
			w.ResetsAt = t
		}
		p.Windows = append(p.Windows, w)
	}
	return p
}

// ── Gemini CLI ───────────────────────────────────────────────────────────

// GeminiSource reads per-model quota buckets from Google's Code Assist API
// with the access token in ~/.gemini/oauth_creds.json. It does not refresh
// expired tokens (that needs the Gemini CLI's OAuth client secret, which
// does not belong in this repository); the CLI refreshes it whenever it
// runs, and until then the quota reads as expired.
type GeminiSource struct {
	BaseURL   string
	HTTP      *http.Client
	CredsPath string
	ProjectID string
	now       func() time.Time
}

// NewGeminiSource returns a source over the real credentials file.
func NewGeminiSource() *GeminiSource {
	home := os.Getenv("GEMINI_CLI_HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	return &GeminiSource{
		BaseURL:   "https://cloudcode-pa.googleapis.com",
		CredsPath: filepath.Join(home, ".gemini", "oauth_creds.json"),
		ProjectID: os.Getenv("GOOGLE_CLOUD_PROJECT"),
		now:       time.Now,
	}
}

func (s *GeminiSource) Agent() string { return "gemini" }

func (s *GeminiSource) Read(ctx context.Context) Provider {
	raw, err := os.ReadFile(s.CredsPath)
	if err != nil {
		return Provider{Agent: s.Agent(), Error: ErrNotConfigured}
	}
	var creds struct {
		AccessToken string `json:"access_token"`
		ExpiryMs    int64  `json:"expiry_date"`
	}
	if json.Unmarshal(raw, &creds) != nil || creds.AccessToken == "" {
		return Provider{Agent: s.Agent(), Error: ErrNotConfigured}
	}
	now := time.Now
	if s.now != nil {
		now = s.now
	}
	if creds.ExpiryMs > 0 && now().After(time.UnixMilli(creds.ExpiryMs)) {
		return Provider{Agent: s.Agent(), Error: ErrExpired}
	}
	auth := map[string]string{"Authorization": "Bearer " + creds.AccessToken}
	base := strings.TrimRight(s.BaseURL, "/")
	project := s.ProjectID
	if project == "" {
		var load struct {
			Project string `json:"cloudaicompanionProject"`
		}
		status, err := httpJSON(ctx, s.HTTP, http.MethodPost, base+"/v1internal:loadCodeAssist", auth,
			map[string]any{"cloudaicompanionProject": "", "metadata": map[string]any{}}, &load)
		if err != nil {
			return failure(s.Agent(), status)
		}
		project = load.Project
	}
	if project == "" {
		return Provider{Agent: s.Agent(), Error: ErrUnavailable}
	}
	var quota struct {
		Buckets []struct {
			ModelID           string   `json:"modelId"`
			RemainingFraction *float64 `json:"remainingFraction"`
			ResetTime         string   `json:"resetTime"`
		} `json:"buckets"`
	}
	status, err := httpJSON(ctx, s.HTTP, http.MethodPost, base+"/v1internal:retrieveUserQuota", auth,
		map[string]any{"project": project}, &quota)
	if err != nil {
		return failure(s.Agent(), status)
	}
	p := Provider{Agent: s.Agent(), Available: true}
	for _, b := range quota.Buckets {
		if b.RemainingFraction == nil || b.ModelID == "" || len(b.ModelID) > 120 {
			continue
		}
		w := Window{Kind: KindModel, Label: b.ModelID, UsedPercent: clampPercent(100 * (1 - *b.RemainingFraction))}
		if t, err := time.Parse(time.RFC3339Nano, b.ResetTime); err == nil {
			w.ResetsAt = t
		}
		p.Windows = append(p.Windows, w)
	}
	return p
}

// ── In-process providers ─────────────────────────────────────────────────

// FuncSource adapts a reader function (OpenRouter's key info) to a Source.
type FuncSource struct {
	ID string
	Fn func(ctx context.Context) Provider
}

func (s FuncSource) Agent() string                     { return s.ID }
func (s FuncSource) Read(ctx context.Context) Provider { return s.Fn(ctx) }

// CreditProvider builds a credit window from a spend and an optional limit
// (OpenRouter keys). An unlimited key has no window.
func CreditProvider(agent string, used float64, limit *float64) Provider {
	p := Provider{Agent: agent, Available: true}
	if limit != nil && *limit > 0 {
		p.Windows = []Window{{Kind: KindCredit, UsedPercent: clampPercent(100 * used / *limit)}}
	}
	return p
}
