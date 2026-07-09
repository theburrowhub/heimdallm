package github

import (
	"net/http"
	"strconv"
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

func TestRateLimitBackoff(t *testing.T) {
	// Non-rate-limit responses never back off.
	for _, status := range []int{200, 404, 422, 500} {
		if _, ok := rateLimitBackoff(respWith(status, nil)); ok {
			t.Errorf("status %d should not back off", status)
		}
	}
	// A bare 403 (permission denied, no rate-limit signal) is not a backoff.
	if _, ok := rateLimitBackoff(respWith(403, nil)); ok {
		t.Error("403 without rate-limit headers should not back off")
	}

	// Retry-After (secondary/abuse limit) — honored on 403 and 429, +1s cushion.
	if w, ok := rateLimitBackoff(respWith(403, map[string]string{"Retry-After": "30"})); !ok || w != 31*time.Second {
		t.Errorf("403 Retry-After=30 → (%v,%v), want (31s,true)", w, ok)
	}
	if w, ok := rateLimitBackoff(respWith(429, map[string]string{"Retry-After": "5"})); !ok || w != 6*time.Second {
		t.Errorf("429 Retry-After=5 → (%v,%v), want (6s,true)", w, ok)
	}

	// Primary exhaustion: remaining=0 + reset in the future → wait ~until reset.
	reset := time.Now().Add(40 * time.Second).Unix()
	if w, ok := rateLimitBackoff(respWith(403, map[string]string{
		"X-RateLimit-Remaining": "0",
		"X-RateLimit-Reset":     strconv.FormatInt(reset, 10),
	})); !ok || w < 30*time.Second || w > 45*time.Second {
		t.Errorf("remaining=0 reset+40s → (%v,%v), want ~41s true", w, ok)
	}

	// remaining=0 without a reset header → small default backoff.
	if w, ok := rateLimitBackoff(respWith(403, map[string]string{"X-RateLimit-Remaining": "0"})); !ok || w != 2*time.Second {
		t.Errorf("remaining=0 no reset → (%v,%v), want (2s,true)", w, ok)
	}

	// remaining>0 on a 403 → not a rate limit.
	if _, ok := rateLimitBackoff(respWith(403, map[string]string{"X-RateLimit-Remaining": "12"})); ok {
		t.Error("403 with remaining>0 should not back off")
	}
}
