package main

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/heimdallm/daemon/internal/store"
)

func TestReviewErrorEventDataLogsPRLookupFailure(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open memory store: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close memory store: %v", err)
	}

	var logs bytes.Buffer
	originalLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	data := reviewErrorEventData(s, 0, "org/repo", 73, "PR title", errors.New("review failed"))
	if _, ok := data["pr_id"]; ok {
		t.Fatalf("event data unexpectedly contains pr_id: %#v", data)
	}

	got := logs.String()
	for _, want := range []string{
		"review error event: PR lookup failed",
		"repo=org/repo",
		"pr_number=73",
		"database is closed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log output %q does not contain %q", got, want)
		}
	}
}
