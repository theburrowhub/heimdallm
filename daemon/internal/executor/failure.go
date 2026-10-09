package executor

import (
	"errors"
	"strings"
)

// rateLimitedError is implemented by in-process agent errors (OpenRouter's
// APIError) that know a quota or rate limit stopped them.
type rateLimitedError interface {
	RateLimited() bool
}

// quotaPhrases are what agent CLIs print when an account's allowance, not the
// review itself, is the problem. Matched case-insensitively against the
// failed run's error and output.
var quotaPhrases = []string{
	"usage limit",
	"rate limit",
	"rate_limit",
	"ratelimit",
	"too many requests",
	"429",
	"quota",
	"resource_exhausted",
	"limit reached",
	"limit exceeded",
	"credit balance is too low",
	"insufficient credits",
	"out of credits",
	"premium requests",
	"402 payment required",
}

// IsQuotaError reports whether a failed run failed because the agent's quota
// or rate limit is spent. A review flow then retries on its next agent
// instead of failing the review.
func IsQuotaError(err error) bool {
	if err == nil || errors.Is(err, ErrExecutionCancelled) {
		return false
	}
	var rl rateLimitedError
	if errors.As(err, &rl) && rl.RateLimited() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, p := range quotaPhrases {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// readOnlyAgents run reviews without write access by construction (Copilot
// denies writes, Cursor runs in ask mode, OpenRouter is review-only), so
// they cannot resolve merge conflicts.
var readOnlyAgents = map[string]bool{"copilot": true, "cursor_cli": true, "openrouter": true}

// WriteCapable reports whether cli can edit files (merge conflict resolution).
func WriteCapable(cli string) bool {
	return ValidateCLIName(cli) == nil && !readOnlyAgents[cli]
}
