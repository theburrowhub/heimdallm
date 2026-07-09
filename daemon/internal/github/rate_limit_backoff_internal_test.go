package github

import (
	"net/http"
	"testing"
	"time"
)

func respWith(status int, headers map[string]string) *http.Response {
	h := http.Header{}
	for k, v := range headers {
		h.Set(k, v)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func TestRateLimitDelay(t *testing.T) {
	// Non-rate-limit responses.
	for _, status := range []int{200, 404, 422, 500} {
		if _, ok := rateLimitDelay(respWith(status, nil)); ok {
			t.Errorf("status %d should not be a rate limit", status)
		}
	}
	// A bare 403 (permission denied) or a 403 with budget left is not a limit.
	if _, ok := rateLimitDelay(respWith(403, nil)); ok {
		t.Error("bare 403 should not be a rate limit")
	}
	if _, ok := rateLimitDelay(respWith(403, map[string]string{"X-RateLimit-Remaining": "12"})); ok {
		t.Error("403 with remaining>0 should not be a rate limit")
	}

	// Numeric Retry-After honored, +1s cushion, on 403 and 429.
	if d, ok := rateLimitDelay(respWith(403, map[string]string{"Retry-After": "30"})); !ok || d != 31*time.Second {
		t.Errorf("403 Retry-After=30 → (%v,%v), want (31s,true)", d, ok)
	}
	if d, ok := rateLimitDelay(respWith(429, map[string]string{"Retry-After": "5"})); !ok || d != 6*time.Second {
		t.Errorf("429 Retry-After=5 → (%v,%v), want (6s,true)", d, ok)
	}

	// Exhausted (remaining==0) without a usable Retry-After → default cooldown.
	if d, ok := rateLimitDelay(respWith(403, map[string]string{"X-RateLimit-Remaining": "0"})); !ok || d != rateLimitCooldown {
		t.Errorf("remaining=0 → (%v,%v), want (%v,true)", d, ok, rateLimitCooldown)
	}
}

func TestPauseRateLimit(t *testing.T) {
	c := &Client{}
	if _, paused := c.rateLimitPaused(); paused {
		t.Fatal("fresh client should not be paused")
	}
	c.pauseRateLimit(30 * time.Second)
	until, paused := c.rateLimitPaused()
	if !paused {
		t.Fatal("client should be paused after pauseRateLimit")
	}
	if d := time.Until(until); d < 25*time.Second || d > 31*time.Second {
		t.Errorf("pause window ≈ %v, want ~30s", d)
	}
	// An over-large delay is clamped to the default cooldown, not honored raw.
	c2 := &Client{}
	c2.pauseRateLimit(2 * time.Hour)
	until2, _ := c2.rateLimitPaused()
	if d := time.Until(until2); d > maxRateLimitCooldown+time.Second {
		t.Errorf("over-large pause not clamped: %v", d)
	}
}
