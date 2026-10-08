package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultBaseURL is the OpenRouter API. The client never takes a URL from
// configuration or a request: only tests swap it.
const DefaultBaseURL = "https://openrouter.ai/api/v1"

// Response size limits for each endpoint.
const (
	maxChatResponseBytes   = 8 << 20
	maxModelsResponseBytes = 16 << 20
	maxSmallResponseBytes  = 1 << 20
	maxErrorBodyLen        = 300
)

// ErrNoKey is returned when no API key is configured.
var ErrNoKey = errors.New("openrouter: no API key configured")

// Client talks to the OpenRouter API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	Keys    *KeyStore

	modelsMu   sync.Mutex
	models     []Model
	modelsAt   time.Time
	modelsTTL  time.Duration
	nowForTest func() time.Time
}

// NewClient returns a client for the real API.
func NewClient(keys *KeyStore) *Client {
	return &Client{
		BaseURL:   DefaultBaseURL,
		HTTP:      &http.Client{Timeout: 10 * time.Minute},
		Keys:      keys,
		modelsTTL: time.Hour,
	}
}

func (c *Client) now() time.Time {
	if c.nowForTest != nil {
		return c.nowForTest()
	}
	return time.Now()
}

// APIError is a non-2xx answer from OpenRouter.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openrouter: HTTP %d: %s", e.Status, e.Message)
}

// RateLimited reports whether the error is a quota or rate limit, which the
// review flow treats as "try another agent".
func (e *APIError) RateLimited() bool {
	return e.Status == http.StatusTooManyRequests || e.Status == http.StatusPaymentRequired
}

func (c *Client) do(ctx context.Context, method, path string, body any, limit int64, out any) error {
	key, _ := c.Keys.Get()
	if key == "" {
		return ErrNoKey
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("openrouter: encode request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.BaseURL, "/")+path, reader)
	if err != nil {
		return fmt.Errorf("openrouter: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	// Attribution headers OpenRouter documents for app rankings.
	req.Header.Set("HTTP-Referer", "https://github.com/theburrowhub/heimdallm")
	req.Header.Set("X-Title", "Heimdallm")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("openrouter: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return fmt.Errorf("openrouter: read response: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Message: errorMessage(data)}
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("openrouter: decode response: %w", err)
	}
	return nil
}

// errorMessage extracts OpenRouter's {"error":{"message"}} or a trimmed body.
// The API key is never part of a response, so this is safe to log.
func errorMessage(data []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &env) == nil && env.Error.Message != "" {
		return truncate(env.Error.Message, maxErrorBodyLen)
	}
	return truncate(strings.TrimSpace(string(data)), maxErrorBodyLen)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ── Chat completions ─────────────────────────────────────────────────────

// Message is one chat message. Content is a pointer because an assistant
// turn that only calls tools has a null content, and must be echoed back as
// such.
type Message struct {
	Role       string     `json:"role"`
	Content    *string    `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall is a function call requested by the model.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ToolSpec declares a tool to the model.
type ToolSpec struct {
	Type     string       `json:"type"`
	Function FunctionSpec `json:"function"`
}

// FunctionSpec is a tool's name, purpose and JSON-schema parameters.
type FunctionSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type chatRequest struct {
	Model      string         `json:"model"`
	Messages   []Message      `json:"messages"`
	Tools      []ToolSpec     `json:"tools,omitempty"`
	ToolChoice string         `json:"tool_choice,omitempty"`
	MaxTokens  int            `json:"max_tokens,omitempty"`
	Usage      map[string]any `json:"usage"`
	Reasoning  map[string]any `json:"reasoning,omitempty"`
}

// ChatUsage is OpenRouter's accounting for one completion.
type ChatUsage struct {
	PromptTokens        int64   `json:"prompt_tokens"`
	CompletionTokens    int64   `json:"completion_tokens"`
	Cost                float64 `json:"cost"`
	PromptTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage ChatUsage `json:"usage"`
	Error *struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

// ChatParams are the per-call options of a completion.
type ChatParams struct {
	Model      string
	Messages   []Message
	Tools      []ToolSpec
	ToolChoice string
	MaxTokens  int
	Effort     string
}

// Chat runs one completion and returns the assistant message and usage.
func (c *Client) Chat(ctx context.Context, p ChatParams) (Message, ChatUsage, error) {
	req := chatRequest{
		Model:      p.Model,
		Messages:   p.Messages,
		Tools:      p.Tools,
		ToolChoice: p.ToolChoice,
		MaxTokens:  p.MaxTokens,
		Usage:      map[string]any{"include": true},
	}
	if p.Effort != "" {
		effort := p.Effort
		if effort == "max" {
			effort = "high" // OpenRouter's scale stops at high
		}
		req.Reasoning = map[string]any{"effort": effort}
	}
	var resp chatResponse
	if err := c.do(ctx, http.MethodPost, "/chat/completions", req, maxChatResponseBytes, &resp); err != nil {
		return Message{}, ChatUsage{}, err
	}
	if resp.Error != nil {
		return Message{}, resp.Usage, &APIError{Status: http.StatusBadGateway, Message: truncate(resp.Error.Message, maxErrorBodyLen)}
	}
	if len(resp.Choices) == 0 {
		return Message{}, resp.Usage, errors.New("openrouter: completion has no choices")
	}
	return resp.Choices[0].Message, resp.Usage, nil
}

// ── Key info and models ──────────────────────────────────────────────────

// KeyInfo is the usage and limit of the configured key (GET /key).
type KeyInfo struct {
	Label          string   `json:"label"`
	Limit          *float64 `json:"limit"`
	LimitRemaining *float64 `json:"limit_remaining"`
	Usage          float64  `json:"usage"`
	UsageDaily     float64  `json:"usage_daily"`
	UsageMonthly   float64  `json:"usage_monthly"`
	IsFreeTier     bool     `json:"is_free_tier"`
}

// KeyInfo returns the key's spend and limit.
func (c *Client) KeyInfo(ctx context.Context) (KeyInfo, error) {
	var env struct {
		Data KeyInfo `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/key", nil, maxSmallResponseBytes, &env); err != nil {
		return KeyInfo{}, err
	}
	return env.Data, nil
}

// Model is one entry of OpenRouter's model list.
type Model struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	ContextLength int64  `json:"context_length"`
	Pricing       struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	SupportedParameters []string `json:"supported_parameters"`
}

// SupportsTools reports whether the model accepts tool calls.
func (m Model) SupportsTools() bool {
	for _, p := range m.SupportedParameters {
		if p == "tools" {
			return true
		}
	}
	return false
}

// Models returns the model list, cached for an hour.
func (c *Client) Models(ctx context.Context) ([]Model, error) {
	c.modelsMu.Lock()
	defer c.modelsMu.Unlock()
	if c.models != nil && c.now().Sub(c.modelsAt) < c.modelsTTL {
		return c.models, nil
	}
	var env struct {
		Data []Model `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/models", nil, maxModelsResponseBytes, &env); err != nil {
		return nil, err
	}
	c.models, c.modelsAt = env.Data, c.now()
	return c.models, nil
}
