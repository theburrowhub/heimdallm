package issues_test

import (
	"testing"
	"time"

	"github.com/heimdallm/daemon/internal/github"
	"github.com/heimdallm/daemon/internal/issues"
)

func comment(body string, minutesAgo int) github.Comment {
	return github.Comment{
		Author:    "someone",
		Body:      body,
		CreatedAt: time.Now().Add(-time.Duration(minutesAgo) * time.Minute),
	}
}

func TestScanMarkers_NoMarkers(t *testing.T) {
	comments := []github.Comment{
		comment("Just a regular comment", 10),
		comment("Another one", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerNone {
		t.Errorf("ScanMarkers = %d, want MarkerNone", got)
	}
}

func TestScanMarkers_EmptyComments(t *testing.T) {
	if got := issues.ScanMarkers(nil); got != issues.MarkerNone {
		t.Errorf("ScanMarkers(nil) = %d, want MarkerNone", got)
	}
}

func TestScanMarkers_DoneMarker(t *testing.T) {
	comments := []github.Comment{
		comment("some discussion", 10),
		comment("<!-- heimdallm:done -->\n✅ Implementation complete", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultDone {
		t.Errorf("ScanMarkers = %d, want MarkerResultDone", got)
	}
}

func TestScanMarkers_SkipMarker(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:skip -->", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultSkip {
		t.Errorf("ScanMarkers = %d, want MarkerResultSkip", got)
	}
}

func TestScanMarkers_RetryMarker(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:retry -->", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultRetry {
		t.Errorf("ScanMarkers = %d, want MarkerResultRetry", got)
	}
}

func TestScanMarkers_RetryOverridesDone(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:done -->\n✅ done", 10),
		comment("<!-- heimdallm:retry -->", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultRetry {
		t.Errorf("ScanMarkers = %d, want MarkerResultRetry (overrides done)", got)
	}
}

func TestScanMarkers_RetryOverridesSkip(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:skip -->", 10),
		comment("<!-- heimdallm:retry -->", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultRetry {
		t.Errorf("ScanMarkers = %d, want MarkerResultRetry (overrides skip)", got)
	}
}

func TestScanMarkers_DoneAfterRetrySkips(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:retry -->", 10),
		comment("<!-- heimdallm:done -->\n✅ new implementation", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultDone {
		t.Errorf("ScanMarkers = %d, want MarkerResultDone (latest wins)", got)
	}
}

func TestScanMarkers_SkipAfterDone(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:done -->", 10),
		comment("<!-- heimdallm:skip -->", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultSkip {
		t.Errorf("ScanMarkers = %d, want MarkerResultSkip (latest wins)", got)
	}
}

func TestScanMarkers_MarkerEmbeddedInLargerComment(t *testing.T) {
	comments := []github.Comment{
		comment("Hey, please skip this.\n<!-- heimdallm:skip -->\nThanks!", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultSkip {
		t.Errorf("ScanMarkers = %d, want MarkerResultSkip (embedded marker)", got)
	}
}

func TestScanMarkers_RetryPriorityWithinSingleComment(t *testing.T) {
	comments := []github.Comment{
		comment("<!-- heimdallm:done -->\n<!-- heimdallm:retry -->", 5),
	}
	if got := issues.ScanMarkers(comments); got != issues.MarkerResultRetry {
		t.Errorf("ScanMarkers = %d, want MarkerResultRetry (retry wins within comment)", got)
	}
}
