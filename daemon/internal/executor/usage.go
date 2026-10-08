package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Usage is the token accounting of one agent run.
type Usage struct {
	// InputTokens counts every input token billed at the input rate or
	// above, so it includes CacheWriteTokens; do not add the two together.
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	CostUSD          float64
	// Estimated is set when the agent does not report usage and the counts
	// were approximated from text sizes (see EstimateUsage).
	Estimated bool
}

// estimatedBytesPerToken is the rough bytes-per-token ratio for English
// prose and code that EstimateUsage assumes.
const estimatedBytesPerToken = 4

// EstimateUsage approximates usage for agents that do not report it. It is
// only meant for trend lines (did a token-saving measure shrink prompts?),
// not billing; agent-side exploration of a checkout is invisible to it.
func EstimateUsage(prompt string, result *ReviewResult) *Usage {
	out := 0
	if result != nil {
		if b, err := json.Marshal(result); err == nil {
			out = len(b)
		}
	}
	return &Usage{
		InputTokens:  int64((len(prompt) + estimatedBytesPerToken - 1) / estimatedBytesPerToken),
		OutputTokens: int64((out + estimatedBytesPerToken - 1) / estimatedBytesPerToken),
		Estimated:    true,
	}
}

// claudeEnvelope is the object `claude -p --output-format json` prints: the
// model's final answer in Result plus the run's accounting.
type claudeEnvelope struct {
	Type         string  `json:"type"`
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       *string `json:"result"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// errNotEnvelope means the output is not Claude's JSON envelope (an older CLI,
// or extra_flags chose another output format); the caller parses it as-is.
var errNotEnvelope = errors.New("executor: output is not a claude result envelope")

// ErrClaudeMaxTurns is returned when Claude stopped at --max-turns without an
// answer.
var ErrClaudeMaxTurns = errors.New("executor: claude run ended with error_max_turns")

// unwrapClaudeEnvelope extracts the answer and usage from Claude's JSON
// envelope. An envelope that reports an error (e.g. error_max_turns) is
// returned as an error so the review fails instead of parsing a non-answer.
func unwrapClaudeEnvelope(raw []byte) ([]byte, *Usage, error) {
	var env claudeEnvelope
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(raw))), &env); err != nil || env.Type != "result" {
		return nil, nil, errNotEnvelope
	}
	if env.IsError || env.Result == nil {
		subtype := env.Subtype
		if subtype == "error_max_turns" {
			return nil, nil, ErrClaudeMaxTurns
		}
		if subtype == "" {
			subtype = "error"
		}
		// Keep Claude's own message ("Claude AI usage limit reached…"): it is
		// what tells a quota failure apart from a broken review.
		if env.Result != nil && strings.TrimSpace(*env.Result) != "" {
			msg := strings.TrimSpace(*env.Result)
			if len(msg) > 300 {
				msg = msg[:300]
			}
			return nil, nil, fmt.Errorf("executor: claude run ended with %s: %s", subtype, msg)
		}
		return nil, nil, fmt.Errorf("executor: claude run ended with %s", subtype)
	}
	u := &Usage{CostUSD: env.TotalCostUSD}
	if env.Usage != nil {
		u.InputTokens = env.Usage.InputTokens + env.Usage.CacheCreationInputTokens
		u.OutputTokens = env.Usage.OutputTokens
		u.CacheReadTokens = env.Usage.CacheReadInputTokens
		u.CacheWriteTokens = env.Usage.CacheCreationInputTokens
	}
	return []byte(*env.Result), u, nil
}

// reportsUsage reports whether buildArgs asks cli for a machine-readable
// envelope that carries usage. Only Claude does today, and only when the
// operator has not chosen an output format through extra_flags.
func reportsUsage(cli string, opts ExecOptions) bool {
	return opts.ReportUsage && cli == "claude" && !strings.Contains(opts.ExtraFlags, "--output-format")
}
