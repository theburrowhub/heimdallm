package github

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

// RateLimitObserver is implemented by callers that want to receive parsed
// rate-limit signals after every GitHub API response.
type RateLimitObserver interface {
	// ObserveResponse is called with the raw HTTP response after every call
	// made through do() or doWithBody(). The response body is NOT consumed
	// by ObserveResponse; only headers are inspected.
	ObserveResponse(resp *http.Response)
}

// RateLimitObserverFunc is a function type that implements RateLimitObserver.
// It lets callers register a plain function as an observer without defining
// a new named type.
type RateLimitObserverFunc func(resp *http.Response)

// ObserveResponse implements RateLimitObserver.
func (f RateLimitObserverFunc) ObserveResponse(resp *http.Response) { f(resp) }

// ParsedRateLimit holds the result of parseRateLimitHeaders.
// ok is false when the response carries no X-RateLimit-* headers.
type ParsedRateLimit struct {
	Resource   string        // e.g. "core", "search"
	Limit      int           // X-RateLimit-Limit (0 when absent)
	Remaining  int           // X-RateLimit-Remaining
	Used       int           // X-RateLimit-Used (derived from Limit-Remaining when absent)
	Reset      time.Time     // X-RateLimit-Reset (unix epoch → time.Time)
	RetryAfter time.Duration // Retry-After if present (0 when absent)
}

// ParseRateLimitHeaders extracts GitHub rate-limit signals from a response.
// It is a pure function with no side effects, placed here for easy testing.
//
// Returns (result, true) when at least X-RateLimit-Remaining is present.
// Returns (ParsedRateLimit{}, false) when the headers are absent.
//
// Secondary rate limit detection:
//   - 403 or 429 with a Retry-After header → RetryAfter is set.
//   - 403 with body containing "secondary rate limit" is handled upstream by
//     callers that read the body; the header-only path here uses Retry-After.
func ParseRateLimitHeaders(resp *http.Response) (ParsedRateLimit, bool) {
	if resp == nil {
		return ParsedRateLimit{}, false
	}

	remainingStr := resp.Header.Get("X-RateLimit-Remaining")
	if remainingStr == "" {
		// No rate-limit headers at all.
		return ParsedRateLimit{}, false
	}

	remaining, err := strconv.Atoi(strings.TrimSpace(remainingStr))
	if err != nil {
		return ParsedRateLimit{}, false
	}

	resource := strings.TrimSpace(resp.Header.Get("X-RateLimit-Resource"))
	if resource == "" {
		resource = "core" // default to "core" when resource header is absent
	}

	var limit int
	if limitStr := strings.TrimSpace(resp.Header.Get("X-RateLimit-Limit")); limitStr != "" {
		if v, err := strconv.Atoi(limitStr); err == nil {
			limit = v
		}
	}

	var used int
	if usedStr := strings.TrimSpace(resp.Header.Get("X-RateLimit-Used")); usedStr != "" {
		if v, err := strconv.Atoi(usedStr); err == nil {
			used = v
		}
	} else if limit > 0 {
		// GitHub always sends X-RateLimit-Used, but derive it defensively so a
		// proxy that strips the header doesn't leave the UI with a blank "used".
		// Clamp at 0: a proxy or API anomaly reporting Remaining > Limit must
		// not surface as a negative "used".
		used = limit - remaining
		if used < 0 {
			used = 0
		}
	}

	var reset time.Time
	if resetStr := resp.Header.Get("X-RateLimit-Reset"); resetStr != "" {
		if epoch, err := strconv.ParseInt(strings.TrimSpace(resetStr), 10, 64); err == nil {
			reset = time.Unix(epoch, 0)
		}
	}

	var retryAfter time.Duration
	if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		if raStr := resp.Header.Get("Retry-After"); raStr != "" {
			raStr = strings.TrimSpace(raStr)
			if secs, err := strconv.ParseInt(raStr, 10, 64); err == nil {
				retryAfter = time.Duration(secs) * time.Second
			}
		}
	}

	return ParsedRateLimit{
		Resource:   resource,
		Limit:      limit,
		Remaining:  remaining,
		Used:       used,
		Reset:      reset,
		RetryAfter: retryAfter,
	}, true
}
