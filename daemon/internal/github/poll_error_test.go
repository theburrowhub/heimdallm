package github_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/heimdallm/daemon/internal/github"
)

func TestClassifyPollError(t *testing.T) {
	cases := map[string]struct {
		err  error
		want string
	}{
		"rate-limit cooldown":     {fmt.Errorf("github: search PR budget: %w", &gh.RateLimitError{RetryAt: time.Now()}), gh.PollErrorRateLimited},
		"401 from a wrapped call": {fmt.Errorf("github: resolve user: %w", &gh.StatusError{Op: "github: get user", StatusCode: 401}), gh.PollErrorAuth},
		"403 is not auth":         {&gh.StatusError{Op: "github: search PRs (review-requested)", StatusCode: 403}, gh.PollErrorOther},
		"untyped error":           {errors.New("dial tcp: connection refused"), gh.PollErrorOther},
	}
	for name, tc := range cases {
		if got := gh.ClassifyPollError(tc.err); got != tc.want {
			t.Errorf("%s: ClassifyPollError = %q, want %q", name, got, tc.want)
		}
	}
}

// Callers match on the text of these errors (e.g. "status 4"), so typing them
// must not change a byte of the message.
func TestStatusErrorKeepsTheHistoricalMessage(t *testing.T) {
	err := &gh.StatusError{Op: "github: search PRs (review-requested)", StatusCode: 401, Body: `{"message":"Bad credentials"}`}
	if got, want := err.Error(), `github: search PRs (review-requested): status 401: {"message":"Bad credentials"}`; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A revoked token surfaces on the poll path as a typed 401 whether GitHub
// rejects it at GET /user (cold start) or at the REST search fallback (the
// login is cached after the first poll).
func TestFetchPRsToReview_RevokedTokenClassifiesAsAuth(t *testing.T) {
	const badCredentials = `{"message":"Bad credentials"}`
	unauthorized := func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(badCredentials))
	}

	t.Run("at GET /user", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { unauthorized(w) }))
		defer srv.Close()
		_, err := gh.NewClient("revoked", gh.WithBaseURL(srv.URL)).FetchPRsToReview()
		if got := gh.ClassifyPollError(err); got != gh.PollErrorAuth {
			t.Fatalf("ClassifyPollError(%v) = %q, want auth", err, got)
		}
		if !strings.Contains(err.Error(), "github: get user: status 401: "+badCredentials) {
			t.Errorf("message changed: %v", err)
		}
	})

	t.Run("at the search fallback", func(t *testing.T) {
		var revoked atomic.Bool
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/user" && !revoked.Load() {
				_ = json.NewEncoder(w).Encode(map[string]string{"login": "heimdallm-bot"})
				return
			}
			unauthorized(w)
		}))
		defer srv.Close()
		client := gh.NewClient("token", gh.WithBaseURL(srv.URL))
		if _, err := client.AuthenticatedUser(); err != nil {
			t.Fatalf("warm the cached login: %v", err)
		}
		revoked.Store(true)
		_, err := client.FetchPRsToReview()
		if got := gh.ClassifyPollError(err); got != gh.PollErrorAuth {
			t.Fatalf("ClassifyPollError(%v) = %q, want auth", err, got)
		}
		if !strings.Contains(err.Error(), "github: search PRs (review-requested): status 401: "+badCredentials) {
			t.Errorf("message changed: %v", err)
		}
	})
}
