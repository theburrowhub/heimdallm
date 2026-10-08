package api_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/theburrowhub/heimdallm/cli/internal/api"
)

func TestGetReviewLimits(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/review-limits" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"kind":"org","key":"acme","windows":[{"window":"hour","used":4,"limit":4,"reset_at":"2026-10-08T12:00:00Z"}]}]`))
	}))
	defer srv.Close()

	got, err := api.New(srv.URL, "").GetReviewLimits()
	if err != nil {
		t.Fatalf("GetReviewLimits: %v", err)
	}
	if len(got) != 1 || got[0].Label() != "org acme" || !got[0].Windows[0].Exhausted() {
		t.Fatalf("got %+v", got)
	}
}

func TestReviewLimitStatusLabel(t *testing.T) {
	cases := map[string]api.ReviewLimitStatus{
		"all reviews":  {Kind: "global"},
		"acme/api":     {Kind: "repo", Key: "acme/api"},
		"agent claude": {Kind: "agent", Key: "claude"},
		"future":       {Kind: "future"},
		"future x":     {Kind: "future", Key: "x"},
	}
	for want, s := range cases {
		if got := s.Label(); got != want {
			t.Errorf("Label(%+v) = %q, want %q", s, got, want)
		}
	}
	if (api.ReviewLimitWindow{Used: 3, Limit: 0}).Exhausted() {
		t.Error("an unlimited window is never exhausted")
	}
}

func TestGetReviewLimitsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := api.New(srv.URL, "").GetReviewLimits(); err == nil {
		t.Fatal("a 503 must surface as an error")
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer bad.Close()
	if _, err := api.New(bad.URL, "").GetReviewLimits(); err == nil {
		t.Fatal("malformed JSON must surface as an error")
	}
}
